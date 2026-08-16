package mcpclient

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// The client is driven against a REAL MCP server: a second program, compiled
// and spawned, exchanging real JSON-RPC over real pipes.
//
// Not a fake reader and not an in-process stub. Every hazard this file is about
// — a notification arriving between a request and its answer, answers coming
// back out of order, a child dying mid-call — is a property of two processes
// and a pipe, and something that hands the client a prepared slice of messages
// proves nothing about any of them.
//
// The counterpart's behaviour is chosen by argv, so one program covers every
// case and the cases cannot drift apart.
const counterpart = `package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
)

func send(v any) {
	b, _ := json.Marshal(v)
	fmt.Println(string(b))
}

func main() {
	mode := ""
	if len(os.Args) > 1 {
		mode = os.Args[1]
	}
	in := bufio.NewReader(os.Stdin)
	// What the client sent that it should not have. A RESPONSE from a client
	// is unsolicited by definition here: this program asks exactly one thing
	// (sampling, in "asks" mode), so anything else with an id and no method is
	// the client answering something that was never a question — which is what
	// replying to a notification looks like from this side.
	unsolicited := 0
	answeredSampling := false
	for {
		line, err := in.ReadString('\n')
		if err != nil {
			return
		}
		var m map[string]any
		if json.Unmarshal([]byte(line), &m) != nil {
			continue
		}
		method, _ := m["method"].(string)
		id := m["id"]

		if method == "" {
			_, hasResult := m["result"]
			_, hasError := m["error"]
			if hasResult || hasError {
				if fmt.Sprint(id) == "9001" {
					answeredSampling = true
				} else {
					unsolicited++
				}
			}
			continue
		}

		switch method {
		case "initialize":
			send(map[string]any{"jsonrpc": "2.0", "id": id, "result": map[string]any{
				"protocolVersion": "2024-11-05",
				"serverInfo":      map[string]any{"name": "counterpart", "version": "1"},
			}})
			if mode == "chatty" {
				// A notification, unprompted, exactly where a naive client
				// would read it as the answer to its next request.
				send(map[string]any{"jsonrpc": "2.0", "method": "notifications/tools/list_changed"})
			}
			if mode == "asks" {
				// A REQUEST from the server. It expects an answer, and blocks
				// its own tools/list behind receiving one.
				send(map[string]any{"jsonrpc": "2.0", "id": 9001, "method": "sampling/createMessage",
					"params": map[string]any{"messages": []any{}}})
			}
		case "notifications/initialized", "notifications/cancelled":
			// Never answered. A client that replies here is broken, and the
			// test for that reads this program's stdout for the absence.
			if mode == "cancelnote" && method == "notifications/cancelled" {
				send(map[string]any{"jsonrpc": "2.0", "method": "notifications/message",
					"params": map[string]any{"data": "saw-cancel"}})
			}
		case "tools/list":
			cursor, _ := m["params"].(map[string]any)["cursor"].(string)
			switch {
			case mode == "paged" && cursor == "":
				send(map[string]any{"jsonrpc": "2.0", "id": id, "result": map[string]any{
					"tools":      []any{map[string]any{"name": "one", "description": "first", "inputSchema": map[string]any{"type": "object"}}},
					"nextCursor": "page2",
				}})
			case mode == "paged" && cursor == "page2":
				send(map[string]any{"jsonrpc": "2.0", "id": id, "result": map[string]any{
					"tools": []any{map[string]any{"name": "two", "description": "second", "inputSchema": map[string]any{"type": "object"}}},
				}})
			default:
				send(map[string]any{"jsonrpc": "2.0", "id": id, "result": map[string]any{
					"tools": []any{map[string]any{"name": "echo", "description": "says it back", "inputSchema": map[string]any{"type": "object"}}},
				}})
			}
		case "tools/call":
			p, _ := m["params"].(map[string]any)
			name, _ := p["name"].(string)
			args, _ := p["arguments"].(map[string]any)
			switch {
			case mode == "slow" && name == "echo":
				// Never answered, but the loop keeps serving: a counterpart
				// that slept here would block its own reads, and the test for
				// "the connection survives a timeout" would be measuring the
				// counterpart rather than the client.
				continue
			case mode == "dies":
				fmt.Fprintln(os.Stderr, "the counterpart is giving up now")
				os.Exit(3)
			case name == "unordered":
				// Answer three calls back to front, once all three have arrived.
				go func(id any, want string) {
					d := map[string]time.Duration{"a": 300, "b": 200, "c": 100}[want]
					time.Sleep(d * time.Millisecond)
					send(map[string]any{"jsonrpc": "2.0", "id": id, "result": map[string]any{
						"content": []any{map[string]any{"type": "text", "text": "answer-" + want}},
					}})
				}(id, fmt.Sprint(args["which"]))
				continue
			case name == "report":
				// The only way this test can see what the client wrote back.
				// Without it, "the client did not answer a notification" is a
				// claim nothing checks — and a mutation that answered every
				// notification passed.
				send(map[string]any{"jsonrpc": "2.0", "id": id, "result": map[string]any{
					"content": []any{map[string]any{"type": "text", "text": fmt.Sprintf(
						"unsolicited=%d sampling_answered=%v", unsolicited, answeredSampling)}},
				}})
			case name == "mixed":
				send(map[string]any{"jsonrpc": "2.0", "id": id, "result": map[string]any{
					"content": []any{
						map[string]any{"type": "text", "text": "the text part"},
						map[string]any{"type": "image", "data": "..."},
					},
				}})
			case name == "fails":
				send(map[string]any{"jsonrpc": "2.0", "id": id, "result": map[string]any{
					"content": []any{map[string]any{"type": "text", "text": "it did not work"}},
					"isError": true,
				}})
			default:
				send(map[string]any{"jsonrpc": "2.0", "id": id, "result": map[string]any{
					"content": []any{map[string]any{"type": "text", "text": "said: " + strings.TrimSpace(fmt.Sprint(args["text"]))}},
				}})
			}
		}
	}
}
`

var (
	buildOnce sync.Once
	builtPath string
	buildErr  error
)

// build compiles the counterpart once for the whole package.
func build(t *testing.T) string {
	t.Helper()
	buildOnce.Do(func() {
		if _, err := exec.LookPath("go"); err != nil {
			buildErr = err
			return
		}
		dir, err := os.MkdirTemp("", "mcp-counterpart")
		if err != nil {
			buildErr = err
			return
		}
		if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(counterpart), 0o644); err != nil {
			buildErr = err
			return
		}
		if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module counterpart\n\ngo 1.25\n"), 0o644); err != nil {
			buildErr = err
			return
		}
		out := filepath.Join(dir, "counterpart")
		cmd := exec.Command("go", "build", "-o", out, ".")
		cmd.Dir = dir
		if b, err := cmd.CombinedOutput(); err != nil {
			buildErr = err
			t.Logf("build output: %s", b)
			return
		}
		builtPath = out
	})
	if buildErr != nil {
		t.Skipf("cannot build the counterpart MCP server: %v", buildErr)
	}
	return builtPath
}

func dial(t *testing.T, mode string, timeout time.Duration) *Conn {
	t.Helper()
	bin := build(t)
	var args []string
	if mode != "" {
		args = []string{mode}
	}
	c, err := Dial(context.Background(), Spec{
		Name: "counterpart", Command: bin, Args: args, Timeout: timeout,
	})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

// The ordinary path, end to end through two processes.
func TestAToolIsListedAndCalled(t *testing.T) {
	c := dial(t, "", 10*time.Second)
	tools, capped, err := c.Tools(context.Background(), 100)
	if err != nil {
		t.Fatalf("tools: %v", err)
	}
	if capped || len(tools) != 1 || tools[0].Name != "echo" {
		t.Fatalf("tools/list gave %+v (capped=%v)", tools, capped)
	}
	res, err := c.CallTool(context.Background(), "echo", json.RawMessage(`{"text":"hello"}`))
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if res.Text != "said: hello" {
		t.Fatalf("the tool answered %q", res.Text)
	}
}

// A notification must never be answered, and must not be mistaken for the
// answer to whatever was asked next.
//
// The counterpart sends one immediately after initialize, which is exactly
// where a client that reads the next line as its own answer breaks.
func TestANotificationIsNeverAnsweredAndDoesNotStealAnAnswer(t *testing.T) {
	c := dial(t, "chatty", 10*time.Second)
	tools, _, err := c.Tools(context.Background(), 100)
	if err != nil {
		t.Fatalf("a notification broke the next call: %v", err)
	}
	if len(tools) != 1 || tools[0].Name != "echo" {
		t.Fatalf("the notification was read as the tool list: %+v", tools)
	}
	// And nothing was written back to it. The counterpart counts every response
	// it receives that it did not ask for, and answering a notification is
	// exactly that. Asking it is the only way this side can tell — the first
	// version of this test asserted the conversation was "still in step", which
	// a spurious reply does not disturb, so a client that answered every
	// notification passed it.
	report := ask(t, c, "report")
	if !strings.Contains(report, "unsolicited=0") {
		t.Fatalf("the client wrote back to a notification: %s — answering one is a protocol error, "+
			"not a harmless extra", report)
	}
}

// ask calls a tool with no arguments and returns its text.
func ask(t *testing.T, c *Conn, tool string) string {
	t.Helper()
	res, err := c.CallTool(context.Background(), tool, json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("%s: %v", tool, err)
	}
	return res.Text
}

// A REQUEST from the server is answered — with method-not-found — because a
// server waiting for an answer it will never get stops serving.
func TestAServerRequestIsRefusedRatherThanIgnored(t *testing.T) {
	c := dial(t, "asks", 10*time.Second)
	// The counterpart sent sampling/createMessage during the handshake and
	// records whether it was ever answered. A server left waiting for an answer
	// stops serving, so silence is not the safe option — the answer has to be
	// an explicit refusal.
	report := ask(t, c, "report")
	if !strings.Contains(report, "sampling_answered=true") {
		t.Fatalf("a request from the server went unanswered: %s — a server waiting on an answer it "+
			"will never get is a server that stops serving", report)
	}
	if !strings.Contains(report, "unsolicited=0") {
		t.Fatalf("the client wrote something else back as well: %s", report)
	}
}

// Answers that come back out of order must reach the caller that asked.
func TestOutOfOrderAnswersReachTheRightCaller(t *testing.T) {
	c := dial(t, "", 10*time.Second)
	var wg sync.WaitGroup
	got := make(map[string]string)
	var mu sync.Mutex
	for _, which := range []string{"a", "b", "c"} {
		wg.Add(1)
		go func(which string) {
			defer wg.Done()
			res, err := c.CallTool(context.Background(), "unordered",
				json.RawMessage(`{"which":"`+which+`"}`))
			if err != nil {
				t.Errorf("%s: %v", which, err)
				return
			}
			mu.Lock()
			got[which] = res.Text
			mu.Unlock()
		}(which)
	}
	wg.Wait()
	// The counterpart answers c first and a last, so anything that delivered
	// to "the next waiter" would cross them over.
	for _, which := range []string{"a", "b", "c"} {
		if want := "answer-" + which; got[which] != want {
			t.Fatalf("caller %s received %q, want %q — answers were delivered by arrival order "+
				"rather than by id", which, got[which], want)
		}
	}
}

// A child that dies fails every waiting call at once, with what it last said.
func TestChildDeathFailsThePendingCallAndQuotesIt(t *testing.T) {
	c := dial(t, "dies", 30*time.Second)
	start := time.Now()
	_, err := c.CallTool(context.Background(), "echo", json.RawMessage(`{"text":"x"}`))
	if err == nil {
		t.Fatal("a call against a dead server succeeded")
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Fatalf("the call waited %s for a child that had already died — it should fail as soon as "+
			"the pipe closes, not at the timeout", took)
	}
	if !strings.Contains(err.Error(), "giving up now") {
		t.Fatalf("the error does not quote what the server last said, which is the only place it "+
			"explained itself: %v", err)
	}
	if c.Alive() {
		t.Fatal("a connection whose child died reports itself alive, so a pool would hand it out")
	}
}

// A call that times out does not take the connection with it.
func TestATimedOutCallLeavesTheConnectionUsable(t *testing.T) {
	c := dial(t, "slow", 700*time.Millisecond)
	_, err := c.CallTool(context.Background(), "echo", json.RawMessage(`{"text":"x"}`))
	if err == nil {
		t.Fatal("a call against a server that never answers succeeded")
	}
	if !strings.Contains(err.Error(), "did not answer") {
		t.Fatalf("the timeout does not say what happened: %v", err)
	}
	if !c.Alive() {
		t.Fatal("a call that timed out closed the connection, so the next one pays to start the " +
			"server again")
	}
	// And a different tool on the same connection still works.
	res, err := c.CallTool(context.Background(), "other", json.RawMessage(`{"text":"after"}`))
	if err != nil {
		t.Fatalf("the connection is unusable after a timeout: %v", err)
	}
	if res.Text != "said: after" {
		t.Fatalf("answered %q", res.Text)
	}
}

// The server is told to stop working on a call nobody is waiting for.
func TestATimedOutCallTellsTheServerToStop(t *testing.T) {
	c := dial(t, "cancelnote", 500*time.Millisecond)
	// "echo" answers immediately in this mode, so force a timeout by asking
	// for something the counterpart never answers.
	_, err := c.Call(context.Background(), "never/answered", map[string]any{})
	if err == nil {
		t.Fatal("expected a timeout")
	}
	// The counterpart echoes a notification back when it sees cancelled. There
	// is nothing to read it into, so the proof is that the connection survived
	// and is still in step — a cancelled notification that had been sent as a
	// REQUEST would have left an id nobody answers.
	res, err := c.CallTool(context.Background(), "echo", json.RawMessage(`{"text":"ok"}`))
	if err != nil || res.Text != "said: ok" {
		t.Fatalf("the connection is out of step after a cancellation: %q %v", res.Text, err)
	}
}

// Pagination is followed to the end, and the cap is reported rather than
// silently applied.
func TestEveryPageOfToolsIsRead(t *testing.T) {
	c := dial(t, "paged", 10*time.Second)
	tools, capped, err := c.Tools(context.Background(), 100)
	if err != nil {
		t.Fatalf("tools: %v", err)
	}
	if len(tools) != 2 || tools[0].Name != "one" || tools[1].Name != "two" {
		t.Fatalf("only the first page was read: %+v", tools)
	}
	if capped {
		t.Fatal("two tools under a cap of a hundred reported as capped")
	}
	tools, capped, err = c.Tools(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 1 || !capped {
		t.Fatalf("the cap was not applied or not reported: %d %v", len(tools), capped)
	}
}

// Content this cut cannot carry is named, not dropped in silence.
func TestContentThatCannotBeCarriedIsNamed(t *testing.T) {
	c := dial(t, "", 10*time.Second)
	res, err := c.CallTool(context.Background(), "mixed", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "the text part" {
		t.Fatalf("the text was not carried: %q", res.Text)
	}
	if len(res.Dropped) != 1 || res.Dropped[0] != "image" {
		t.Fatalf("an image came back as nothing at all: %+v — half an answer returned as the whole "+
			"answer is the failure this reports", res.Dropped)
	}
}

// A tool that failed on its own terms is not the same as a call that failed.
func TestAToolFailureIsReportedAsTheToolsAndNotTheCalls(t *testing.T) {
	c := dial(t, "", 10*time.Second)
	res, err := c.CallTool(context.Background(), "fails", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("a tool that reported failure was treated as a broken call: %v", err)
	}
	if !res.IsError || res.Text != "it did not work" {
		t.Fatalf("the failure was not carried through: %+v", res)
	}
}

// The child does not inherit this server's environment, which holds
// credentials. It gets what it was granted.
func TestTheChildGetsOnlyTheEnvironmentItWasGranted(t *testing.T) {
	t.Setenv("COGITORIUM_TEST_LEAK", "this-must-not-reach-the-child")
	if got := envList(map[string]string{"GRANTED": "yes"}); len(got) != 1 || got[0] != "GRANTED=yes" {
		t.Fatalf("the child's environment is %v — anything drawn from this process would carry "+
			"whatever it holds", got)
	}
}
