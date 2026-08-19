//go:build wasip1

// A guest, written the way an author would write one with the stock Go
// toolchain. It exists so the host's side of the contract is tested against a
// real module rather than against a fixture this repository invented — a
// hand-written .wasm would agree with the host by construction and prove
// nothing about whether the contract is writable.
package main

import (
	"encoding/json"
	"unsafe"
)

func main() {}

// buf keeps every allocation alive. A guest that let Go's collector move or
// free these would hand the host a pointer into memory that is no longer its
// own, and the failure would be intermittent.
var buf [][]byte

//go:wasmexport cog_alloc
func alloc(size uint32) uint32 {
	b := make([]byte, size)
	buf = append(buf, b)
	return uint32(uintptr(unsafe.Pointer(unsafe.SliceData(b))))
}

//go:wasmexport cog_abi
func abiVersion() uint32 { return 1 }

//go:wasmimport cog cog_host
func hostCall(ptr, size, out uint32) uint32

//go:wasmexport cog_call
func call(ptr, size uint32) uint64 {
	in := unsafe.Slice((*byte)(unsafe.Pointer(uintptr(ptr))), size)

	var req struct {
		Contract int             `json:"contract"`
		Export   string          `json:"export"`
		Input    json.RawMessage `json:"input"`
	}
	if err := json.Unmarshal(in, &req); err != nil {
		return reply(map[string]any{"error": "bad request: " + err.Error()})
	}

	switch req.Export {
	case "echo":
		return reply(map[string]any{"data": req.Input})

	case "contract":
		b, _ := json.Marshal(req.Contract)
		return reply(map[string]any{"data": json.RawMessage(b)})

	case "render":
		// The branch that matters: answer with a template name and a model and
		// the host renders it through the layer stack, so this output can
		// itself be overridden by a plugin layered above.
		return reply(map[string]any{
			"template": "guest.stage.panel",
			"model":    map[string]any{"Count": 3},
		})

	case "ask":
		// Ask the host for something and hand back exactly what it said,
		// including a refusal — a denied host is an ordinary answer.
		out := askHost(`{"call":"http","input":{"url":"https://api.example.com/"}}`)
		return reply(map[string]any{"data": json.RawMessage(out)})

	case "refuse":
		return reply(map[string]any{"error": "this plugin declines"})

	case "spin":
		for {
		}
	}
	return reply(map[string]any{"error": "no such export: " + req.Export})
}

func askHost(request string) []byte {
	req := []byte(request)
	p := alloc(uint32(len(req)))
	copy(unsafe.Slice((*byte)(unsafe.Pointer(uintptr(p))), len(req)), req)

	var packed uint64
	outSlot := alloc(8)
	if hostCall(p, uint32(len(req)), outSlot) == 0 {
		return []byte(`{"error":"the host did not answer"}`)
	}
	packed = *(*uint64)(unsafe.Pointer(uintptr(outSlot)))
	ptr, size := uint32(packed>>32), uint32(packed)
	return unsafe.Slice((*byte)(unsafe.Pointer(uintptr(ptr))), size)
}

func reply(v map[string]any) uint64 {
	b, err := json.Marshal(v)
	if err != nil {
		b = []byte(`{"error":"the guest could not encode its reply"}`)
	}
	p := alloc(uint32(len(b)))
	copy(unsafe.Slice((*byte)(unsafe.Pointer(uintptr(p))), len(b)), b)
	return uint64(p)<<32 | uint64(len(b))
}
