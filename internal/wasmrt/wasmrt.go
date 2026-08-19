// Package wasmrt runs a plugin's WebAssembly module.
//
// This is the tier that makes the parity claim true. A module is data with an
// entry point: it is byte-identical on every target, it needs nothing fetched
// and nothing probed, and the engine that executes it is compiled into the
// binary the operator already installed. There is no "first install a runtime"
// step here, on any channel, ever — which is the one thing Jenkins cannot say,
// because a JVM still has to exist on every machine it reaches.
//
// The ABI is core WebAssembly plus wasi_snapshot_preview1, and it is frozen.
// Not the Component Model and not WASI Preview 2: wazero declines those until
// they reach W3C Recommendation, and chasing them would cost the six-target
// CGO_ENABLED=0 build that this whole tier exists to protect.
package wasmrt

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"

	"github.com/orkcom-tech/cogitorium/internal/abi"
)

// The guest contract, in four functions.
//
// A guest exports cog_alloc so the host can hand it bytes, cog_abi so a module
// states the contract it speaks, and cog_call to do the work. The host exports
// cog_host so a guest can ask for something back. That is the whole surface,
// and it is small on purpose: every name here is a permanent promise, and a
// promise in a language binding is much harder to withdraw than a template.
const (
	guestAlloc = "cog_alloc"
	guestABI   = "cog_abi"
	guestCall  = "cog_call"
	hostCall   = "cog_host"
	hostModule = "cog"
)

// Limits bound one call. A plugin that exceeds them is stopped and said so
// about, rather than being allowed to take the server with it.
type Limits struct {
	// Memory is the guest's linear memory ceiling, in 64KiB pages.
	MemoryPages uint32
	// Timeout kills a call that will not finish. Enforced by cancelling the
	// context wazero is running under, which unwinds the guest rather than
	// leaving a goroutine wedged.
	Timeout time.Duration
	// MaxOutput bounds a reply. A guest that returns a gigabyte is a guest
	// that fills the server's memory with its answer.
	MaxOutput uint32
}

// DefaultLimits are generous for a plugin and cheap for a server.
//
// 256 pages is 16MiB, which is roomy for a Rust or TinyGo module and tight for
// a stock-Go one — the Go runtime alone takes about 3MiB of linear memory per
// instance, and an author who picks that toolchain should learn the cost from
// the documentation rather than from an out-of-memory.
func DefaultLimits() Limits {
	return Limits{MemoryPages: 256, Timeout: 30 * time.Second, MaxOutput: 8 << 20}
}

// Runtime compiles and runs modules. One per server.
type Runtime struct {
	rt     wazero.Runtime
	host   abi.Host
	limits Limits

	mu       sync.Mutex
	compiled map[string]wazero.CompiledModule
}

// New prepares the engine.
//
// Compilation is cached across instantiations, because compiling is the
// expensive part — roughly three hundred milliseconds for a module against
// about eleven to instantiate one that is already compiled. A plugin called
// twice should pay that once.
func New(ctx context.Context, host abi.Host, limits Limits) (*Runtime, error) {
	cfg := wazero.NewRuntimeConfig().
		WithMemoryLimitPages(limits.MemoryPages).
		// Closing on context done is what makes Timeout real: without it a
		// guest in a tight loop ignores cancellation entirely.
		WithCloseOnContextDone(true)

	rt := wazero.NewRuntimeWithConfig(ctx, cfg)
	if _, err := wasi_snapshot_preview1.Instantiate(ctx, rt); err != nil {
		rt.Close(ctx)
		return nil, fmt.Errorf("wasmrt: preparing wasi: %w", err)
	}

	r := &Runtime{rt: rt, host: host, limits: limits, compiled: map[string]wazero.CompiledModule{}}
	if err := r.exportHost(ctx); err != nil {
		rt.Close(ctx)
		return nil, err
	}
	return r, nil
}

// exportHost publishes the one function a guest calls back through.
//
// One function rather than nine, because a boundary like this already has
// exactly one way to pass bytes, and nine entry points would be nine places to
// keep that in step. Which of the nine is being asked for is in the payload.
func (r *Runtime) exportHost(ctx context.Context) error {
	_, err := r.rt.NewHostModuleBuilder(hostModule).
		NewFunctionBuilder().
		WithFunc(func(ctx context.Context, m api.Module, ptr, size, outPtr uint32) uint32 {
			return r.serveHostCall(ctx, m, ptr, size, outPtr)
		}).
		Export(hostCall).
		Instantiate(ctx)
	if err != nil {
		return fmt.Errorf("wasmrt: exporting the host gateway: %w", err)
	}
	return nil
}

// Close releases the engine.
func (r *Runtime) Close(ctx context.Context) error { return r.rt.Close(ctx) }

// Compile prepares a module and remembers it under an id.
func (r *Runtime) Compile(ctx context.Context, id string, module []byte) error {
	code, err := r.rt.CompileModule(ctx, module)
	if err != nil {
		return fmt.Errorf("wasmrt: plugin %q: this is not a module this build can run: %w", id, err)
	}

	// The contract the module states is checked against the one this build
	// speaks, before anything of its is ever called. A manifest can claim a
	// contract its code does not speak; the code cannot.
	if err := checkExports(code, id); err != nil {
		code.Close(ctx)
		return err
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if old, ok := r.compiled[id]; ok {
		old.Close(ctx)
	}
	r.compiled[id] = code
	return nil
}

func checkExports(code wazero.CompiledModule, id string) error {
	exports := code.ExportedFunctions()
	for _, want := range []string{guestAlloc, guestABI, guestCall} {
		if _, ok := exports[want]; !ok {
			return fmt.Errorf("wasmrt: plugin %q does not export %s, so it does not speak this "+
				"host's contract. A plugin built with one of the published SDKs exports it "+
				"for you", id, want)
		}
	}
	return nil
}

// pluginContext carries which plugin a call belongs to, so the host gateway
// knows whose grants to check without the guest being able to say.
type pluginKey struct{}

// Call runs one export.
//
// The whole conversation is two byte slices: the request envelope in, the
// response envelope out. Nothing about the tier is visible in it, which is
// what lets the same plugin move to a subprocess or a container later without
// its author changing a line.
func (r *Runtime) Call(ctx context.Context, id string, req abi.Request) (abi.Response, error) {
	r.mu.Lock()
	code, ok := r.compiled[id]
	r.mu.Unlock()
	if !ok {
		return abi.Response{}, fmt.Errorf("wasmrt: plugin %q has no compiled module", id)
	}

	req.Contract = abi.Version
	in, err := json.Marshal(req)
	if err != nil {
		return abi.Response{}, fmt.Errorf("wasmrt: encoding the request: %w", err)
	}

	ctx, cancel := context.WithTimeout(ctx, r.limits.Timeout)
	defer cancel()
	ctx = context.WithValue(ctx, pluginKey{}, id)

	// A fresh instance per call. Deliberately not a pooled one yet: module
	// state that survives between calls is state two callers can see, and
	// making that safe is a decision to take with a reason rather than as a
	// side effect of caching.
	mod, err := r.rt.InstantiateModule(ctx, code, wazero.NewModuleConfig().
		WithName("").
		WithStartFunctions("_initialize"))
	if err != nil {
		return abi.Response{}, fmt.Errorf("wasmrt: plugin %q could not start: %w", id, err)
	}
	defer mod.Close(ctx)

	if err := checkContract(ctx, mod, id); err != nil {
		return abi.Response{}, err
	}

	out, err := r.invoke(ctx, mod, in)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return abi.Response{}, fmt.Errorf("wasmrt: plugin %q did not finish within %s and was stopped",
				id, r.limits.Timeout)
		}
		return abi.Response{}, fmt.Errorf("wasmrt: plugin %q: %w", id, err)
	}

	var resp abi.Response
	if err := json.Unmarshal(out, &resp); err != nil {
		return abi.Response{}, fmt.Errorf("wasmrt: plugin %q returned something that is not a "+
			"response envelope: %w", id, err)
	}
	if err := resp.Validate(); err != nil {
		return abi.Response{}, fmt.Errorf("wasmrt: plugin %q: %w", id, err)
	}
	return resp, nil
}

func checkContract(ctx context.Context, mod api.Module, id string) error {
	fn := mod.ExportedFunction(guestABI)
	if fn == nil {
		return fmt.Errorf("wasmrt: plugin %q does not export %s", id, guestABI)
	}
	res, err := fn.Call(ctx)
	if err != nil || len(res) == 0 {
		return fmt.Errorf("wasmrt: plugin %q would not state its contract: %w", id, err)
	}
	if got := uint32(res[0]); got != abi.Version {
		return fmt.Errorf("wasmrt: plugin %q speaks contract %d and this build speaks %d",
			id, got, abi.Version)
	}
	return nil
}

// invoke hands the guest its request and reads the reply.
//
// The reply comes back as a packed pointer and length in one 64-bit value,
// because a wasm function returns one number and the alternative — a second
// call to ask "how long was that" — is a second place for the two to disagree.
func (r *Runtime) invoke(ctx context.Context, mod api.Module, in []byte) ([]byte, error) {
	ptr, err := guestWrite(ctx, mod, in)
	if err != nil {
		return nil, err
	}

	fn := mod.ExportedFunction(guestCall)
	res, err := fn.Call(ctx, uint64(ptr), uint64(len(in)))
	if err != nil {
		return nil, err
	}
	if len(res) == 0 {
		return nil, errors.New("the guest returned nothing where a response was expected")
	}

	outPtr, outLen := unpack(res[0])
	if outLen == 0 {
		return nil, errors.New("the guest returned an empty response")
	}
	if outLen > r.limits.MaxOutput {
		return nil, fmt.Errorf("the guest returned %d bytes, past the %d byte limit",
			outLen, r.limits.MaxOutput)
	}
	out, ok := mod.Memory().Read(outPtr, outLen)
	if !ok {
		return nil, errors.New("the guest pointed at memory it does not have")
	}
	// Copied out before the instance is closed underneath it.
	return append([]byte(nil), out...), nil
}

// guestWrite asks the guest for room and copies bytes into it. The host never
// picks an address: the guest's allocator owns its own memory, and writing
// wherever looked free is how a host corrupts a heap it does not understand.
func guestWrite(ctx context.Context, mod api.Module, b []byte) (uint32, error) {
	alloc := mod.ExportedFunction(guestAlloc)
	if alloc == nil {
		return 0, fmt.Errorf("the guest does not export %s", guestAlloc)
	}
	res, err := alloc.Call(ctx, uint64(len(b)))
	if err != nil {
		return 0, fmt.Errorf("the guest could not allocate %d bytes: %w", len(b), err)
	}
	if len(res) == 0 {
		return 0, errors.New("the guest's allocator returned nothing")
	}
	ptr := uint32(res[0])
	if ptr == 0 {
		return 0, fmt.Errorf("the guest refused to allocate %d bytes", len(b))
	}
	if !mod.Memory().Write(ptr, b) {
		return 0, errors.New("the guest allocated memory the host cannot reach")
	}
	return ptr, nil
}

// serveHostCall answers a guest asking the host for something.
//
// The guest passes a request and a place to put the answer's packed
// pointer-and-length. Every refusal comes back as a value in the reply rather
// than as a trap: a denied host or an ungranted scope is an ordinary thing a
// plugin has to handle, and trapping would turn "you may not reach that" into
// a crash with no message.
func (r *Runtime) serveHostCall(ctx context.Context, m api.Module, ptr, size, outPtr uint32) uint32 {
	id, _ := ctx.Value(pluginKey{}).(string)

	fail := func(msg string) uint32 {
		b, _ := json.Marshal(abi.HostReply{Err: msg})
		p, err := guestWrite(ctx, m, b)
		if err != nil {
			return 0
		}
		m.Memory().WriteUint64Le(outPtr, pack(p, uint32(len(b))))
		return 1
	}

	raw, ok := m.Memory().Read(ptr, size)
	if !ok {
		return fail("the host could not read the request you pointed at")
	}
	var req abi.HostRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return fail("that is not a host request: " + err.Error())
	}
	if !abi.ValidCall(req.Call) {
		return fail(fmt.Sprintf("%q is not a call this host offers", req.Call))
	}

	reply := r.host.Call(id, req)
	b, err := json.Marshal(reply)
	if err != nil {
		return fail("the host could not encode its answer")
	}
	p, err := guestWrite(ctx, m, b)
	if err != nil {
		return 0
	}
	if !m.Memory().WriteUint64Le(outPtr, pack(p, uint32(len(b)))) {
		return 0
	}
	return 1
}

// pack and unpack put a pointer and a length in one 64-bit value.
func pack(ptr, size uint32) uint64 { return uint64(ptr)<<32 | uint64(size) }

func unpack(v uint64) (ptr, size uint32) {
	return uint32(v >> 32), uint32(v)
}
