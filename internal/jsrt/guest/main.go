// The JavaScript engine, as a WebAssembly guest.
//
// `needs: js` promised an engine inside the binary and there was none: the
// technology table resolved it to the WebAssembly tier and an author got a
// tier with no engine behind it, discovering that only when their plugin was
// asked for a .wasm they never compiled.
//
// This is that engine. It runs INSIDE wazero rather than in the server
// process, which is the whole reason it is built this way: `needs: js` says
// the WebAssembly tier, and the isolation a plugin gets has to be the one the
// label claims. An interpreter embedded in the host would run a stranger's
// JavaScript on the server's own heap under a label that says sandbox.
//
// Built from source by the toolchain this project already uses — GOOS=wasip1
// go build — rather than vendored as a binary nobody can reproduce. A
// QuickJS build would be roughly ten times smaller and faster, and it would
// also be an opaque artifact in a repository whose whole argument is that you
// read what you approve. If a release pipeline ever grows wasi-sdk, swapping
// this module is a drop-in: the contract belongs to the module, not to the
// engine inside it.
package main

import (
	"encoding/json"
	"fmt"
	"unsafe"

	"github.com/dop251/goja"
)

// The contract this speaks. Stated by the module rather than claimed by a
// manifest, because a manifest can be wrong about its own code.
const contract = 1

// vm and source survive between the load and the call, which is safe here for
// a reason that does not generalise: the host instantiates a fresh module per
// call, so this global is one call's state and nothing else can ever see it.
var (
	vm     *goja.Runtime
	loaded bool
)

//go:wasmimport cog cog_host
func hostCall(ptr, size, outPtr uint32) uint32

//go:wasmexport cog_abi
func cogABI() uint32 { return contract }

// cog_alloc hands the host a place to write.
//
// The slice is kept alive in a package-level map rather than returned to the
// collector, because the host writes into it after this returns and a freed
// buffer is a use-after-free the guest cannot see.
var live = map[uint32][]byte{}

//go:wasmexport cog_alloc
func cogAlloc(size uint32) uint32 {
	buf := make([]byte, size)
	ptr := uint32(uintptr(unsafe.Pointer(unsafe.SliceData(buf))))
	live[ptr] = buf
	return ptr
}

// cog_js_load receives the plugin's own JavaScript.
//
// An export of the ENGINE, not of the plugin contract: a JavaScript plugin
// ships plugin.js and exports nothing, which is the point of the tier. The
// host writes the source here before the first call of each instance.
//
//go:wasmexport cog_js_load
func cogJSLoad(ptr, size uint32) uint32 {
	src := string(readMemory(ptr, size))
	vm = goja.New()
	if err := installHost(vm); err != nil {
		return fail("the host bridge could not be installed: " + err.Error())
	}
	if _, err := vm.RunString(src); err != nil {
		return fail("plugin.js: " + err.Error())
	}
	loaded = true
	return 0
}

//go:wasmexport cog_call
func cogCall(ptr, size uint32) uint64 {
	if !loaded {
		return reply([]byte(`{"error":"no JavaScript was loaded into this instance"}`))
	}

	var req struct {
		Export string          `json:"export"`
		Role   string          `json:"role"`
		Input  json.RawMessage `json:"input"`
		Ctx    json.RawMessage `json:"ctx"`
		HTTP   json.RawMessage `json:"http"`
	}
	if err := json.Unmarshal(readMemory(ptr, size), &req); err != nil {
		return reply(errorEnvelope("the request envelope is not readable: " + err.Error()))
	}

	fn, ok := goja.AssertFunction(vm.Get(req.Export))
	if !ok {
		// Named, with what the file does define. An author whose export is
		// never called otherwise cannot tell a typo from a host that never
		// asked for it.
		return reply(errorEnvelope(fmt.Sprintf(
			"plugin.js defines no function %q; it defines: %s", req.Export, definedFunctions(vm))))
	}

	arg := vm.NewObject()
	setJSON(arg, "input", req.Input)
	setJSON(arg, "ctx", req.Ctx)
	setJSON(arg, "http", req.HTTP)
	_ = arg.Set("export", req.Export)
	_ = arg.Set("role", req.Role)

	out, err := fn(goja.Undefined(), arg)
	if err != nil {
		return reply(errorEnvelope(err.Error()))
	}
	if out == nil || goja.IsUndefined(out) || goja.IsNull(out) {
		return reply([]byte(`{"data":{}}`))
	}

	data, err := json.Marshal(out.Export())
	if err != nil {
		return reply(errorEnvelope("what it returned is not JSON: " + err.Error()))
	}
	env, err := json.Marshal(map[string]json.RawMessage{"data": data})
	if err != nil {
		return reply(errorEnvelope(err.Error()))
	}
	return reply(env)
}

// installHost puts the nine calls on a `cog` object.
//
// The same nine every other tier offers, named the same way, so a plugin that
// outgrows JavaScript is rewritten and not redesigned. A refusal is thrown
// rather than returned: a plugin that ignores "you may not reach that" and
// carries on is a plugin producing an answer built on a call that did not
// happen.
func installHost(vm *goja.Runtime) error {
	cog := vm.NewObject()

	call := func(name string, payload any) (goja.Value, error) {
		body, err := json.Marshal(map[string]any{"call": name, "input": payload})
		if err != nil {
			return nil, err
		}
		out, err := ask(body)
		if err != nil {
			return nil, err
		}
		var reply struct {
			Output json.RawMessage `json:"output"`
			Err    string          `json:"error"`
		}
		if err := json.Unmarshal(out, &reply); err != nil {
			return nil, err
		}
		if reply.Err != "" {
			// The host's own sentence, which already names both what was
			// asked for and what was granted.
			return nil, fmt.Errorf("%s", reply.Err)
		}
		if len(reply.Output) == 0 {
			return goja.Undefined(), nil
		}
		var v any
		if err := json.Unmarshal(reply.Output, &v); err != nil {
			return nil, err
		}
		return vm.ToValue(v), nil
	}

	bind := func(js, host string, shape func(goja.FunctionCall) any) error {
		return cog.Set(js, func(fc goja.FunctionCall) goja.Value {
			v, err := call(host, shape(fc))
			if err != nil {
				// Thrown into the plugin's own JavaScript rather than
				// returned: a plugin that ignores "you may not reach that"
				// and carries on produces an answer built on a call that did
				// not happen.
				panic(vm.NewGoError(err))
			}
			return v
		})
	}

	arg := func(fc goja.FunctionCall, i int) any {
		if i >= len(fc.Arguments) {
			return nil
		}
		return fc.Argument(i).Export()
	}

	for _, b := range []struct {
		js, host string
		shape    func(goja.FunctionCall) any
	}{
		{"log", "log", func(fc goja.FunctionCall) any { return fmt.Sprint(arg(fc, 0)) }},
		{"now", "now", func(goja.FunctionCall) any { return map[string]any{} }},
		{"rand", "rand", func(fc goja.FunctionCall) any { return map[string]any{"max": arg(fc, 0)} }},
		{"config", "config", func(goja.FunctionCall) any { return map[string]any{} }},
		{"render", "render", func(fc goja.FunctionCall) any {
			return map[string]any{"template": arg(fc, 0), "data": arg(fc, 1)}
		}},
		{"http", "http", func(fc goja.FunctionCall) any {
			opts, _ := arg(fc, 1).(map[string]any)
			if opts == nil {
				opts = map[string]any{}
			}
			opts["url"] = arg(fc, 0)
			return opts
		}},
		{"api", "api", func(fc goja.FunctionCall) any {
			opts, _ := arg(fc, 1).(map[string]any)
			if opts == nil {
				opts = map[string]any{}
			}
			opts["path"] = arg(fc, 0)
			if _, ok := opts["method"]; !ok {
				opts["method"] = "GET"
			}
			return opts
		}},
		{"enqueue", "enqueue", func(fc goja.FunctionCall) any {
			opts, _ := arg(fc, 1).(map[string]any)
			if opts == nil {
				opts = map[string]any{}
			}
			opts["export"] = arg(fc, 0)
			return opts
		}},
		{"get", "kv", func(fc goja.FunctionCall) any {
			return map[string]any{"op": "get", "key": arg(fc, 0)}
		}},
		{"set", "kv", func(fc goja.FunctionCall) any {
			return map[string]any{"op": "set", "key": arg(fc, 0), "value": arg(fc, 1)}
		}},
		{"del", "kv", func(fc goja.FunctionCall) any {
			return map[string]any{"op": "delete", "key": arg(fc, 0)}
		}},
		{"incr", "kv", func(fc goja.FunctionCall) any {
			by := arg(fc, 1)
			if by == nil {
				by = 1
			}
			return map[string]any{"op": "incr", "key": arg(fc, 0), "by": by}
		}},
		{"keys", "kv", func(fc goja.FunctionCall) any {
			return map[string]any{"op": "list", "prefix": arg(fc, 0)}
		}},
	} {
		if err := bind(b.js, b.host, b.shape); err != nil {
			return err
		}
	}
	return vm.Set("cog", cog)
}

// ask carries one host call across the boundary.
func ask(body []byte) ([]byte, error) {
	in := cogAlloc(uint32(len(body)))
	copy(live[in], body)

	// One word for the reply's packed pointer and length, written by the host.
	//
	// Non-zero means "there is a reply where I said", and a REFUSAL is a
	// reply: the host answers "you may not reach that" as a value, because a
	// refusal is an ordinary thing a plugin handles. Zero means the host could
	// not write at all, which is the only case that is not a conversation.
	outSlot := cogAlloc(8)
	if code := hostCall(in, uint32(len(body)), outSlot); code == 0 {
		return nil, fmt.Errorf("the host could not answer at all")
	}
	packed := *(*uint64)(unsafe.Pointer(uintptr(outSlot)))
	ptr, size := uint32(packed>>32), uint32(packed)
	if size == 0 {
		return []byte("{}"), nil
	}
	return readMemory(ptr, size), nil
}

func readMemory(ptr, size uint32) []byte {
	return unsafe.Slice((*byte)(unsafe.Pointer(uintptr(ptr))), size)
}

// reply packs a pointer and a length into one word, because a wasm function
// returns one number and the alternative — a second call asking "how long was
// that" — is a second place for the two to disagree.
func reply(b []byte) uint64 {
	ptr := cogAlloc(uint32(len(b)))
	copy(live[ptr], b)
	return uint64(ptr)<<32 | uint64(len(b))
}

func fail(msg string) uint32 {
	// Kept where cog_call will find it, so the refusal reaches the operator as
	// a response rather than as a module that started and did nothing.
	loadError = msg
	return 1
}

var loadError string

func errorEnvelope(msg string) []byte {
	b, _ := json.Marshal(map[string]string{"error": msg})
	return b
}

func setJSON(o *goja.Object, name string, raw json.RawMessage) {
	if len(raw) == 0 {
		return
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return
	}
	_ = o.Set(name, v)
}

func definedFunctions(vm *goja.Runtime) string {
	var out []string
	for _, k := range vm.GlobalObject().Keys() {
		if _, ok := goja.AssertFunction(vm.Get(k)); ok {
			out = append(out, k)
		}
	}
	if len(out) == 0 {
		return "none"
	}
	return fmt.Sprint(out)
}

func main() {}
