package imagert

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/orkcom-tech/cogitorium/internal/abi"
	"github.com/orkcom-tech/cogitorium/internal/sandbox"
)

// fakeSandbox stands in for whichever backend an operator configured. It is a
// stand-in for the BACKEND, not for the tier: everything this package does —
// the envelope, the reusability rule, reading stdout — is the real thing.
type fakeSandbox struct {
	spec sandbox.Spec
	res  sandbox.Result
	err  error
	// stdin is what the plugin was actually handed.
	stdin string
}

func (f *fakeSandbox) Name() string   { return "fake" }
func (f *fakeSandbox) Isolated() bool { return true }

func (f *fakeSandbox) Run(_ context.Context, spec sandbox.Spec) (sandbox.Result, error) {
	f.spec = spec
	if spec.Stdin != nil {
		b, _ := io.ReadAll(spec.Stdin)
		f.stdin = string(b)
	}
	return f.res, f.err
}

func okResult(resp abi.Response) sandbox.Result {
	b, _ := json.Marshal(resp)
	return sandbox.Result{Stdout: string(b)}
}

func spec() Spec {
	return Spec{
		Plugin: "radar", Image: "ghcr.io/acme/radar@sha256:abc",
		Dir: "/data/plugins/radar/1.0.0", Command: "/entrypoint",
	}
}

// Availability follows the LIVE backend, never the channel's name.
func TestWithoutABackendTheTierIsUnavailable(t *testing.T) {
	if New(nil) != nil {
		t.Fatal("no backend means no runner")
	}
	var r *Runner
	if r.Available() {
		t.Error("a nil runner is not available")
	}
	if _, err := r.Call(context.Background(), spec(), abi.Request{}); err == nil {
		t.Error("calling without a backend must refuse rather than panic")
	}
}

// The same envelope every other tier carries, over stdin and stdout. Nothing
// about the tier is visible in it.
func TestTheEnvelopeCrossesTheContainer(t *testing.T) {
	sb := &fakeSandbox{res: okResult(abi.Response{
		Template: "radar.stage.panel", Model: map[string]any{"Count": 2},
	})}
	r := New(sb)

	resp, err := r.Call(context.Background(), spec(), abi.Request{Export: "panel"})
	if err != nil {
		t.Fatalf("calling: %v", err)
	}
	if resp.Template != "radar.stage.panel" {
		t.Errorf("template = %q", resp.Template)
	}

	var sent abi.Request
	if err := json.Unmarshal([]byte(sb.stdin), &sent); err != nil {
		t.Fatalf("the request was not delivered as an envelope: %v", err)
	}
	if sent.Export != "panel" {
		t.Errorf("export = %q", sent.Export)
	}
	if sent.Contract != abi.Version {
		t.Errorf("the host must state its contract, got %d", sent.Contract)
	}
}

// A warm container shares /tmp with whatever ran in it before, and a plugin
// invocation is exactly the kind of run that may have been handed a credential
// stand-in.
func TestAnInvocationIsNeverGivenAWarmContainer(t *testing.T) {
	sb := &fakeSandbox{res: okResult(abi.Response{Status: 204})}
	if _, err := New(sb).Call(context.Background(), spec(), abi.Request{}); err != nil {
		t.Fatal(err)
	}
	if sb.spec.Reusable {
		t.Error("a plugin invocation must never be given a container that already ran something")
	}
	if sb.spec.Writable {
		t.Error("the payload is the plugin's to read and run, and nothing more")
	}
}

// The gate decides destinations; this decides whether there is a gate to talk
// to at all.
func TestNetworkFollowsTheGrant(t *testing.T) {
	for _, want := range []bool{false, true} {
		sb := &fakeSandbox{res: okResult(abi.Response{Status: 204})}
		s := spec()
		s.Network = want
		if _, err := New(sb).Call(context.Background(), s, abi.Request{}); err != nil {
			t.Fatal(err)
		}
		if sb.spec.Network != want {
			t.Errorf("network = %v, want %v", sb.spec.Network, want)
		}
	}
}

// The image reference is passed through digest-pinned. A moving tag would
// change what an operator approved without the approval changing.
func TestTheImageIsPassedThrough(t *testing.T) {
	sb := &fakeSandbox{res: okResult(abi.Response{Status: 204})}
	if _, err := New(sb).Call(context.Background(), spec(), abi.Request{}); err != nil {
		t.Fatal(err)
	}
	if sb.spec.Image != "ghcr.io/acme/radar@sha256:abc" {
		t.Errorf("image = %q", sb.spec.Image)
	}
}

// "exit status 1" tells an operator nothing they can act on.
func TestAFailedRunCarriesTheContainersLastWords(t *testing.T) {
	sb := &fakeSandbox{res: sandbox.Result{
		ExitCode: 1,
		Stderr:   "Traceback (most recent call last):\n  ImportError: no module named acme",
	}}
	_, err := New(sb).Call(context.Background(), spec(), abi.Request{})
	if err == nil {
		t.Fatal("a non-zero exit must be an error")
	}
	for _, want := range []string{"radar", "exited 1", "ImportError"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the failure omits %q: %v", want, err)
		}
	}
}

func TestATimeoutSaysSo(t *testing.T) {
	sb := &fakeSandbox{res: sandbox.Result{TimedOut: true}}
	_, err := New(sb).Call(context.Background(), spec(), abi.Request{})
	if err == nil || !strings.Contains(err.Error(), "did not finish") {
		t.Errorf("a timeout should say what happened: %v", err)
	}
}

func TestGarbageOnStdoutIsRefusedWithTheStderrBeside(t *testing.T) {
	sb := &fakeSandbox{res: sandbox.Result{
		Stdout: "Hello from my plugin!\n",
		Stderr: "warning: printing to stdout breaks the protocol",
	}}
	_, err := New(sb).Call(context.Background(), spec(), abi.Request{})
	if err == nil {
		t.Fatal("stdout that is not an envelope must be refused")
	}
	if !strings.Contains(err.Error(), "not a response envelope") {
		t.Errorf("the message should name the problem: %v", err)
	}
	if !strings.Contains(err.Error(), "breaks the protocol") {
		t.Errorf("the container's stderr is the diagnosis and must be included: %v", err)
	}
}

func TestASandboxFailureIsReported(t *testing.T) {
	sb := &fakeSandbox{err: errors.New("the daemon does not answer")}
	_, err := New(sb).Call(context.Background(), spec(), abi.Request{})
	if err == nil || !strings.Contains(err.Error(), "daemon") {
		t.Errorf("a backend failure should reach the operator: %v", err)
	}
}

// A plugin that writes a megabyte of warnings should not turn one failure into
// a megabyte of log.
func TestTheStderrTailIsBounded(t *testing.T) {
	sb := &fakeSandbox{res: sandbox.Result{ExitCode: 1, Stderr: strings.Repeat("x", 100<<10)}}
	_, err := New(sb).Call(context.Background(), spec(), abi.Request{})
	if err == nil {
		t.Fatal("expected an error")
	}
	if len(err.Error()) > 16<<10 {
		t.Errorf("the failure is %d bytes; the tail is supposed to be bounded", len(err.Error()))
	}
}

func TestTheBackendIsNamedForAScreen(t *testing.T) {
	if got := New(&fakeSandbox{}).Backend(); got != "fake" {
		t.Errorf("backend = %q", got)
	}
	var none *Runner
	if got := none.Backend(); got != "" {
		t.Errorf("a missing backend has no name, got %q", got)
	}
}
