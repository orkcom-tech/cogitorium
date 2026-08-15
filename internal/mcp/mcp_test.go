package mcp

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
)

// The protocol, driven the way a client drives it: JSON-RPC lines in, JSON-RPC
// lines out. The backend is a stub here ON PURPOSE — these tests are about the
// protocol being correct, and backend_test.go exercises the real HTTP path
// against a real server.

type stubBackend struct {
	tools []Tool
	calls []string
	text  string
	isErr bool
	err   error
}

func (s *stubBackend) Tools(context.Context) ([]Tool, error) { return s.tools, nil }
func (s *stubBackend) Call(_ context.Context, name string, args json.RawMessage) (string, bool, error) {
	s.calls = append(s.calls, name+" "+string(args))
	return s.text, s.isErr, s.err
}

// converse feeds lines in and returns the responses, decoded.
func converse(t *testing.T, b Backend, lines ...string) []map[string]any {
	t.Helper()
	var out strings.Builder
	srv := NewServer(b, &out)
	if err := srv.Serve(t.Context(), strings.NewReader(strings.Join(lines, "\n")+"\n")); err != nil {
		t.Fatalf("serve: %v", err)
	}
	var got []map[string]any
	dec := json.NewDecoder(strings.NewReader(out.String()))
	for {
		var m map[string]any
		if err := dec.Decode(&m); err == io.EOF {
			break
		} else if err != nil {
			t.Fatalf("the server wrote something that is not JSON: %v\n%s", err, out.String())
		}
		got = append(got, m)
	}
	return got
}

func TestInitializeAnswersWithAProtocolVersionAndToolCapability(t *testing.T) {
	got := converse(t, &stubBackend{}, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`)
	if len(got) != 1 {
		t.Fatalf("expected one response, got %d: %v", len(got), got)
	}
	res, _ := got[0]["result"].(map[string]any)
	if res == nil {
		t.Fatalf("initialize did not return a result: %v", got[0])
	}
	if res["protocolVersion"] != protocolVersion {
		t.Fatalf("protocolVersion is %v, not %q", res["protocolVersion"], protocolVersion)
	}
	caps, _ := res["capabilities"].(map[string]any)
	if _, ok := caps["tools"]; !ok {
		t.Fatalf("a server that offers tools must say so in capabilities: %v", caps)
	}
}

// A notification has no id and must not be answered. Answering it is a protocol
// error that shows up as a client hanging on a reply it cannot match.
func TestANotificationIsNotAnswered(t *testing.T) {
	got := converse(t, &stubBackend{},
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":7,"method":"tools/list"}`)
	if len(got) != 1 {
		t.Fatalf("the notification was answered: %v", got)
	}
	if got[0]["id"] != float64(7) {
		t.Fatalf("the one answer is not to the request that had an id: %v", got[0])
	}
}

func TestToolsListReturnsTheBackendsTools(t *testing.T) {
	b := &stubBackend{tools: []Tool{
		{Name: "gear_unpack", Description: "unpacks", InputSchema: json.RawMessage(`{"type":"object"}`)},
	}}
	got := converse(t, b, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	res := got[0]["result"].(map[string]any)
	tools := res["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("expected one tool: %v", tools)
	}
	first := tools[0].(map[string]any)
	if first["name"] != "gear_unpack" {
		t.Fatalf("the tool is named %v", first["name"])
	}
	// The schema must survive as an object, not as a string containing JSON —
	// a client validates arguments against it and a string validates nothing.
	if _, ok := first["inputSchema"].(map[string]any); !ok {
		t.Fatalf("inputSchema came back as %T, so no client can validate against it", first["inputSchema"])
	}
}

// An empty list is [] and never null.
func TestNoToolsIsAnEmptyListRatherThanNull(t *testing.T) {
	got := converse(t, &stubBackend{tools: nil}, `{"jsonrpc":"2.0","id":3,"method":"tools/list"}`)
	res := got[0]["result"].(map[string]any)
	tools, ok := res["tools"].([]any)
	if !ok {
		t.Fatalf("tools came back as %T (%v) — a client reads null as a broken server, not an empty one",
			res["tools"], res["tools"])
	}
	if len(tools) != 0 {
		t.Fatalf("expected no tools: %v", tools)
	}
}

func TestCallingAToolPassesTheArgumentsThrough(t *testing.T) {
	b := &stubBackend{text: "done"}
	got := converse(t, b,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"gear_unpack","arguments":{"path":"a.zip"}}}`)
	if len(b.calls) != 1 || !strings.Contains(b.calls[0], `"path":"a.zip"`) {
		t.Fatalf("the arguments did not reach the backend: %v", b.calls)
	}
	res := got[0]["result"].(map[string]any)
	content := res["content"].([]any)
	first := content[0].(map[string]any)
	if first["text"] != "done" {
		t.Fatalf("the tool's output is %v", first["text"])
	}
	if res["isError"] != false {
		t.Fatalf("a tool that succeeded was reported as an error: %v", res)
	}
}

// A tool that RAN and failed is a result with isError, not a JSON-RPC error.
// The difference decides whether the model is told what went wrong or the
// client reports a broken server.
func TestAToolThatFailsIsAResultRatherThanARPCError(t *testing.T) {
	b := &stubBackend{text: "exit 1: no such file", isErr: true}
	got := converse(t, b,
		`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"gear_x","arguments":{}}}`)
	if _, isRPCError := got[0]["error"]; isRPCError {
		t.Fatalf("a failing tool was reported as a protocol error: %v", got[0])
	}
	res := got[0]["result"].(map[string]any)
	if res["isError"] != true {
		t.Fatalf("isError was not set on a failing tool: %v", res)
	}
	if !strings.Contains(res["content"].([]any)[0].(map[string]any)["text"].(string), "no such file") {
		t.Fatalf("the failure text did not reach the model: %v", res)
	}
}

// An unknown method gets the specification's code, not a generic failure.
func TestAnUnknownMethodIsMethodNotFound(t *testing.T) {
	got := converse(t, &stubBackend{}, `{"jsonrpc":"2.0","id":6,"method":"resources/list"}`)
	e := got[0]["error"].(map[string]any)
	if int(e["code"].(float64)) != codeMethodNotFound {
		t.Fatalf("code is %v, want %d", e["code"], codeMethodNotFound)
	}
}

func TestMalformedJSONDoesNotStopTheServer(t *testing.T) {
	b := &stubBackend{}
	got := converse(t, b,
		`{not json at all`,
		`{"jsonrpc":"2.0","id":9,"method":"tools/list"}`)
	// The parse failure has no id to answer, so the only response is to the
	// good request that followed it — the point being that one bad line does
	// not end the session.
	if len(got) != 1 || got[0]["id"] != float64(9) {
		t.Fatalf("a malformed line broke the conversation: %v", got)
	}
}
