package wasmrt

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/orkcom-tech/cogitorium/internal/abi"
)

// The guest is COMPILED by this test from testdata/guest, and that is the
// point of it.
//
// A module checked in as bytes would agree with the host by construction and
// prove nothing about whether the contract is writable. Building it here
// proves an author can write one with a stock toolchain — and building it
// rather than skipping when it is absent means these tests, which are the only
// end-to-end proof this tier works, cannot quietly not run in CI.
func buildGuest(t *testing.T) []byte {
	t.Helper()
	const out = "testdata/guest.wasm"

	if b, err := os.ReadFile(out); err == nil {
		return b
	}
	cmd := exec.Command("go", "build", "-buildmode=c-shared", "-o", "../guest.wasm", ".")
	cmd.Dir = "testdata/guest"
	cmd.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm")
	if outBytes, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("the test guest would not build, so this tier is unproven: %v\n%s", err, outBytes)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("the test guest built but could not be read: %v", err)
	}
	return b
}

type recordingHost struct {
	calls []abi.HostRequest
	reply abi.HostReply
	// seen is the plugin id the host was told, which the guest cannot set.
	seen string
}

func (h *recordingHost) Call(plugin string, req abi.HostRequest) abi.HostReply {
	h.seen = plugin
	h.calls = append(h.calls, req)
	return h.reply
}

func newRT(t *testing.T, host abi.Host, limits Limits) *Runtime {
	t.Helper()
	module := buildGuest(t)
	ctx := context.Background()
	rt, err := New(ctx, host, limits)
	if err != nil {
		t.Fatalf("preparing the engine: %v", err)
	}
	t.Cleanup(func() { rt.Close(context.Background()) })
	if err := rt.Compile(ctx, "guest", module); err != nil {
		t.Fatalf("compiling: %v", err)
	}
	return rt
}

func call(t *testing.T, rt *Runtime, export string, input any) abi.Response {
	t.Helper()
	var raw json.RawMessage
	if input != nil {
		b, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		raw = b
	}
	resp, err := rt.Call(context.Background(), "guest", abi.Request{Export: export, Input: raw})
	if err != nil {
		t.Fatalf("calling %s: %v", export, err)
	}
	return resp
}

func TestAGuestRoundTripsAnEnvelope(t *testing.T) {
	rt := newRT(t, &recordingHost{}, DefaultLimits())
	resp := call(t, rt, "echo", map[string]any{"hello": "world"})

	var got map[string]string
	if err := json.Unmarshal(resp.Data, &got); err != nil {
		t.Fatalf("decoding the guest's answer: %v", err)
	}
	if got["hello"] != "world" {
		t.Errorf("the payload did not survive the boundary: %v", got)
	}
}

// A guest is told which contract the host speaks so it can refuse rather than
// guess.
func TestTheHostStatesItsContract(t *testing.T) {
	rt := newRT(t, &recordingHost{}, DefaultLimits())
	resp := call(t, rt, "contract", nil)

	var got int
	if err := json.Unmarshal(resp.Data, &got); err != nil {
		t.Fatal(err)
	}
	if got != abi.Version {
		t.Errorf("the guest saw contract %d, this build speaks %d", got, abi.Version)
	}
}

// THE branch. A backend answering with a template name and a model renders
// through the layer stack, so its output can itself be overridden by a plugin
// layered above it.
func TestTheTemplateBranchCrossesTheBoundary(t *testing.T) {
	rt := newRT(t, &recordingHost{}, DefaultLimits())
	resp := call(t, rt, "render", nil)

	if resp.Template != "guest.stage.panel" {
		t.Fatalf("template = %q", resp.Template)
	}
	model, ok := resp.Model.(map[string]any)
	if !ok {
		t.Fatalf("model came back as %T", resp.Model)
	}
	if model["Count"] != float64(3) {
		t.Errorf("the model did not survive: %v", model)
	}
	if err := resp.Validate(); err != nil {
		t.Errorf("a template answer should be valid: %v", err)
	}
}

// The guest asks the host for something, and the host knows whose grants to
// check — from the call, not from anything the guest said.
func TestTheGatewayWorksAndTheGuestCannotNameItself(t *testing.T) {
	host := &recordingHost{reply: abi.HostReply{Output: json.RawMessage(`{"status":200}`)}}
	rt := newRT(t, host, DefaultLimits())

	resp := call(t, rt, "ask", nil)
	if len(host.calls) != 1 {
		t.Fatalf("expected one host call, got %d", len(host.calls))
	}
	if host.calls[0].Call != abi.CallHTTP {
		t.Errorf("call = %q", host.calls[0].Call)
	}
	if host.seen != "guest" {
		t.Errorf("the host was told the plugin is %q; the identity must come from the "+
			"call, never from the guest", host.seen)
	}
	if !strings.Contains(string(resp.Data), "200") {
		t.Errorf("the host's answer did not reach the guest: %s", resp.Data)
	}
}

// A denied host is an ordinary thing a plugin handles, so it arrives as a
// value rather than as a trap with no message.
func TestARefusalFromTheHostIsAValueNotACrash(t *testing.T) {
	host := &recordingHost{reply: abi.HostReply{
		Err: `plugin "guest" asked to reach api.example.com, but it was not granted any network`,
	}}
	rt := newRT(t, host, DefaultLimits())

	resp := call(t, rt, "ask", nil)
	if !strings.Contains(string(resp.Data), "not granted any network") {
		t.Errorf("the refusal did not reach the guest intact: %s", resp.Data)
	}
}

// A plugin refusing on purpose is distinct from one crashing.
func TestAGuestMayRefuse(t *testing.T) {
	rt := newRT(t, &recordingHost{}, DefaultLimits())
	resp := call(t, rt, "refuse", nil)
	if resp.Error != "this plugin declines" {
		t.Errorf("error = %q", resp.Error)
	}
}

// Without WithCloseOnContextDone a guest in a tight loop ignores cancellation
// entirely, and the server has a core pinned until it is restarted.
func TestAGuestThatWillNotFinishIsStopped(t *testing.T) {
	limits := DefaultLimits()
	limits.Timeout = 300 * time.Millisecond
	rt := newRT(t, &recordingHost{}, limits)

	start := time.Now()
	_, err := rt.Call(context.Background(), "guest", abi.Request{Export: "spin"})
	if err == nil {
		t.Fatal("an endless guest must be stopped")
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Errorf("it took %s to stop, so cancellation is not reaching the guest", took)
	}
	if !strings.Contains(err.Error(), "guest") {
		t.Errorf("the failure should name the plugin: %v", err)
	}
}

// A module that does not speak the contract is refused at compile, before
// anything of its is ever run.
func TestAModuleWithoutTheContractIsRefused(t *testing.T) {
	ctx := context.Background()
	rt, err := New(ctx, &recordingHost{}, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close(ctx)

	// The smallest valid module: it exports nothing.
	empty := []byte{0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00}
	err = rt.Compile(ctx, "hollow", empty)
	if err == nil {
		t.Fatal("a module that exports nothing does not speak this contract")
	}
	if !strings.Contains(err.Error(), "cog_alloc") {
		t.Errorf("the refusal should name what is missing: %v", err)
	}
}

func TestBytesThatAreNotAModuleAreRefused(t *testing.T) {
	ctx := context.Background()
	rt, err := New(ctx, &recordingHost{}, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close(ctx)

	if err := rt.Compile(ctx, "junk", []byte("this is not a wasm module")); err == nil {
		t.Fatal("junk must be refused")
	}
}

func TestPackAndUnpackAgree(t *testing.T) {
	for _, c := range []struct{ ptr, size uint32 }{{0, 0}, {1, 1}, {1 << 20, 4096}, {^uint32(0), ^uint32(0)}} {
		ptr, size := unpack(pack(c.ptr, c.size))
		if ptr != c.ptr || size != c.size {
			t.Errorf("pack/unpack(%d,%d) = (%d,%d)", c.ptr, c.size, ptr, size)
		}
	}
}
