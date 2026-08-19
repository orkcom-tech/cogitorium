package worker

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/orkcom-tech/cogitorium/internal/abi"
)

// The workers here are real child processes, built from Go source at test
// time. A fake that spoke the protocol from inside this package would agree
// with the host by construction; a child that has to be started, handshaken
// with, and killed is the thing being claimed.
func buildChild(t *testing.T, name, src string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the child fixtures are built for the host platform; the protocol is the same")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module child\n\ngo 1.25\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, name)
	cmd := exec.Command("go", "build", "-o", bin, ".")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("building the %s fixture: %v\n%s", name, err, out)
	}
	return bin
}

const frameHelpers = `
func writeFrame(b []byte) {
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], uint32(len(b)))
	os.Stdout.Write(hdr[:])
	os.Stdout.Write(b)
}

func readFrame() ([]byte, error) {
	var hdr [4]byte
	if _, err := io.ReadFull(os.Stdin, hdr[:]); err != nil {
		return nil, err
	}
	b := make([]byte, binary.BigEndian.Uint32(hdr[:]))
	_, err := io.ReadFull(os.Stdin, b)
	return b, err
}
`

// An ordinary worker: says hello, then answers.
const goodChild = `package main

import (
	"encoding/binary"
	"encoding/json"
	"io"
	"os"
	"time"
)
` + frameHelpers + `
func main() {
	writeFrame([]byte(` + "`" + `{"contract":1}` + "`" + `))
	for {
		in, err := readFrame()
		if err != nil {
			return
		}
		var req struct {
			Export string          ` + "`json:\"export\"`" + `
			Input  json.RawMessage ` + "`json:\"input\"`" + `
		}
		json.Unmarshal(in, &req)

		var out []byte
		switch req.Export {
		case "echo":
			out, _ = json.Marshal(map[string]any{"data": req.Input})
		case "render":
			out, _ = json.Marshal(map[string]any{
				"template": "child.stage.panel",
				"model":    map[string]any{"Count": 7},
			})
		case "die":
			os.Stderr.WriteString("Traceback: something went wrong on line 42\n")
			os.Exit(3)
		case "hang":
			// Sleeping rather than select{}: Go's deadlock detector fires on a
			// process where every goroutine is blocked forever, so select{}
			// would crash and this would be testing a crash rather than a hang.
			time.Sleep(time.Hour)
		default:
			out, _ = json.Marshal(map[string]any{"error": "no such export"})
		}
		writeFrame(out)
	}
}
`

func run(t *testing.T, src string, tune func(*Spec)) *Worker {
	t.Helper()
	bin := buildChild(t, "child", src)
	spec := Spec{Plugin: "radar", Path: bin, Start: 20 * time.Second, Call: 20 * time.Second}
	if tune != nil {
		tune(&spec)
	}
	w := New(spec)
	t.Cleanup(w.Stop)
	return w
}

func TestAWorkerAnswers(t *testing.T) {
	w := run(t, goodChild, nil)
	resp, err := w.Call(context.Background(), abi.Request{
		Export: "echo", Input: json.RawMessage(`{"hello":"world"}`),
	})
	if err != nil {
		t.Fatalf("calling: %v", err)
	}
	var got map[string]string
	if err := json.Unmarshal(resp.Data, &got); err != nil {
		t.Fatal(err)
	}
	if got["hello"] != "world" {
		t.Errorf("the payload did not survive the pipe: %v", got)
	}
}

// Nothing about the tier is visible in the conversation — the same envelope
// the WebAssembly tier carries crosses a pipe unchanged.
func TestTheTemplateBranchCrossesThePipe(t *testing.T) {
	w := run(t, goodChild, nil)
	resp, err := w.Call(context.Background(), abi.Request{Export: "render"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Template != "child.stage.panel" {
		t.Errorf("template = %q", resp.Template)
	}
	if err := resp.Validate(); err != nil {
		t.Errorf("a template answer should be valid: %v", err)
	}
}

// Nothing starts until the first call, so a plugin that is enabled and never
// used costs a row in a table and nothing else.
func TestNothingStartsUntilTheFirstCall(t *testing.T) {
	w := run(t, goodChild, nil)
	if w.Running() {
		t.Fatal("a registered worker must not be running before it is needed")
	}
	if _, err := w.Call(context.Background(), abi.Request{Export: "echo"}); err != nil {
		t.Fatal(err)
	}
	if !w.Running() {
		t.Error("it should be running after a call")
	}
}

// The most common real failure: the interpreter died. Its last words are the
// whole diagnosis, and without them the operator gets "EOF".
func TestACrashReportsItsLastWords(t *testing.T) {
	w := run(t, goodChild, nil)
	_, err := w.Call(context.Background(), abi.Request{Export: "die"})
	if err == nil {
		t.Fatal("a worker that exits mid-request must be an error")
	}
	if !strings.Contains(err.Error(), "Traceback") || !strings.Contains(err.Error(), "line 42") {
		t.Errorf("the failure must carry the worker's last output: %v", err)
	}
	if !strings.Contains(err.Error(), "radar") {
		t.Errorf("it must name the plugin: %v", err)
	}
}

// A worker that failed mid-exchange may have written half a frame, and the
// next request would read that as its own reply.
func TestAFailedWorkerIsNotReused(t *testing.T) {
	w := run(t, goodChild, nil)
	_, _ = w.Call(context.Background(), abi.Request{Export: "die"})
	if w.Running() {
		t.Error("a worker that failed mid-exchange must be stopped, not reused")
	}
	// And the next call starts a fresh one rather than inheriting the mess.
	if _, err := w.Call(context.Background(), abi.Request{Export: "echo"}); err != nil {
		t.Errorf("a later call should start a new worker: %v", err)
	}
}

func TestAHangingWorkerIsStopped(t *testing.T) {
	w := run(t, goodChild, func(s *Spec) { s.Call = 400 * time.Millisecond })
	start := time.Now()
	_, err := w.Call(context.Background(), abi.Request{Export: "hang"})
	if err == nil {
		t.Fatal("a worker that never answers must be stopped")
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Errorf("it took %s, so the deadline is not reaching the read", took)
	}
	if !strings.Contains(err.Error(), "did not answer") {
		t.Errorf("the message should say what happened: %v", err)
	}
	if w.Running() {
		t.Error("it must not be left running")
	}
}

// A manifest can claim a contract its code does not speak; the code cannot.
func TestAWorkerSpeakingAnotherContractIsRefused(t *testing.T) {
	src := strings.Replace(goodChild, `{"contract":1}`, `{"contract":99}`, 1)
	w := run(t, src, nil)
	_, err := w.Call(context.Background(), abi.Request{Export: "echo"})
	if err == nil {
		t.Fatal("a worker speaking another contract must be refused")
	}
	if !strings.Contains(err.Error(), "99") {
		t.Errorf("the refusal should name both contracts: %v", err)
	}
}

// A worker that never says hello is one that never will.
func TestAWorkerThatWillNotHandshakeIsRefused(t *testing.T) {
	silent := `package main

import "time"

func main() { time.Sleep(time.Hour) }
`
	w := run(t, silent, func(s *Spec) { s.Start = 400 * time.Millisecond })
	_, err := w.Call(context.Background(), abi.Request{Export: "echo"})
	if err == nil {
		t.Fatal("a silent worker must be refused")
	}
	if !strings.Contains(err.Error(), "hello") {
		t.Errorf("the message should say what was missing: %v", err)
	}
}

// A plugin that will never start should not be retried on every request, and
// should not be given up on either.
func TestAFailingStartBacksOff(t *testing.T) {
	w := New(Spec{Plugin: "radar", Path: "/nonexistent/interpreter", Start: time.Second})
	t.Cleanup(w.Stop)

	if _, err := w.Call(context.Background(), abi.Request{Export: "x"}); err == nil {
		t.Fatal("a missing interpreter must fail")
	}
	_, err := w.Call(context.Background(), abi.Request{Export: "x"})
	if err == nil {
		t.Fatal("expected the second call to fail too")
	}
	if !strings.Contains(err.Error(), "waiting") {
		t.Errorf("the second failure should say it is backing off rather than retrying: %v", err)
	}
}

// A server that exits leaving interpreters running has handed a machine's
// memory to nobody.
func TestTheSupervisorStopsEverything(t *testing.T) {
	bin := buildChild(t, "child", goodChild)
	s := NewSupervisor()
	for _, id := range []string{"alfa", "bravo"} {
		s.Register(Spec{Plugin: id, Path: bin, Start: 20 * time.Second, Call: 20 * time.Second})
		if _, err := s.Call(context.Background(), id, abi.Request{Export: "echo"}); err != nil {
			t.Fatalf("%s: %v", id, err)
		}
	}
	s.Close()
	for _, id := range []string{"alfa", "bravo"} {
		if w, ok := s.Get(id); ok && w.Running() {
			t.Errorf("%s survived Close", id)
		}
	}
}

func TestTheSupervisorRefusesAnUnknownPlugin(t *testing.T) {
	s := NewSupervisor()
	if _, err := s.Call(context.Background(), "ghost", abi.Request{}); err == nil {
		t.Fatal("an unregistered plugin has no worker")
	}
}

// The last words are bounded: a worker that writes forever must not become the
// thing that fills memory.
func TestTheStderrTailIsBounded(t *testing.T) {
	r := newRing(16)
	r.write([]byte(strings.Repeat("a", 100)))
	if got := len(r.string()); got != 16 {
		t.Errorf("kept %d bytes, want 16", got)
	}
	if !strings.HasSuffix(r.string(), "a") {
		t.Error("it should keep the END of the output — the last words are the diagnosis")
	}
}
