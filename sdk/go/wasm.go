//go:build wasm

package cogitorium

import (
	"encoding/json"
	"errors"
	"unsafe"
)

// The WebAssembly tier: no process, no pipe, no loop. The host instantiates a
// module and calls into it, and the module calls back out through one import.
//
// Everything here is memory bookkeeping the author never sees. What they wrote
// — New, Provider, Run — is identical to the native tier, because a plugin
// that had to know which tier it landed on would turn the operator's approval
// decision into the author's build decision.

// served is the plugin this module answers as.
//
// Set by New rather than by Run, because the host never runs main here: it
// loads the module, package-level initialisation happens, and then it calls
// in. A package-level variable is safe for a reason that does not generalise —
// a module instance belongs to exactly one call, so this is one call's state
// and nothing else can ever observe it.
var served *Plugin

func adopt(p *Plugin) { served = p }

// Run does nothing on this tier and says so by returning.
//
// The host drives here; there is no loop to enter. It exists so that one main
// is correct on both tiers, which is the whole promise: the operator decides
// the tier when they approve an install, and that decision must not reach back
// into the author's source.
func (p *Plugin) Run() {}

//go:wasmimport cog cog_host
func cogHost(ptr, size, outPtr uint32) uint32

//go:wasmexport cog_abi
func cogABI() uint32 { return Contract }

// live keeps every buffer the host may still be looking at.
//
// Kept in a map rather than left to the collector because the host writes into
// these after the call that made them has returned — a collected buffer is a
// use-after-free that the guest has no way to see and the host has no way to
// explain.
var live = map[uint32][]byte{}

//go:wasmexport cog_alloc
func cogAlloc(size uint32) uint32 {
	buf := make([]byte, size)
	ptr := uint32(uintptr(unsafe.Pointer(unsafe.SliceData(buf))))
	live[ptr] = buf
	return ptr
}

//go:wasmexport cog_call
func cogCall(ptr, size uint32) uint64 {
	if served == nil {
		return emit([]byte(`{"error":"this module never called Run, so it has no exports"}`))
	}
	host := &Host{ask: ask}
	resp := served.dispatch(read(ptr, size), host)
	body, err := json.Marshal(resp)
	if err != nil {
		return emit([]byte(`{"error":"the response could not be encoded"}`))
	}
	return emit(body)
}

func ask(body []byte) ([]byte, error) {
	in := cogAlloc(uint32(len(body)))
	copy(live[in], body)

	// One word for the reply's packed pointer and length, written by the host.
	//
	// Non-zero means "there is a reply where I said", and a REFUSAL is a
	// reply: being told no is a value a plugin handles. Zero is the only case
	// that is not a conversation — the host could not answer at all.
	slot := cogAlloc(8)
	if code := cogHost(in, uint32(len(body)), slot); code == 0 {
		return nil, errors.New("the host could not answer at all")
	}
	packed := *(*uint64)(unsafe.Pointer(uintptr(slot)))
	rptr, rsize := uint32(packed>>32), uint32(packed)
	if rsize == 0 {
		return []byte("{}"), nil
	}
	return read(rptr, rsize), nil
}

func read(ptr, size uint32) []byte {
	return unsafe.Slice((*byte)(unsafe.Pointer(uintptr(ptr))), size)
}

// emit packs a pointer and a length into the single word a wasm function can
// return.
func emit(b []byte) uint64 {
	ptr := cogAlloc(uint32(len(b)))
	copy(live[ptr], b)
	return uint64(ptr)<<32 | uint64(len(b))
}
