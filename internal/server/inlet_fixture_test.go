package server

// The fixture the inlet tests share: a real server, a real database, and a
// provider that answers over a real socket.
//
// The provider here is not a stand-in for the llm package — that package is
// exercised in full. It is an HTTP server speaking the openai-compatible
// chat-completions protocol, registered in the catalog like any other, and the
// engine reaches it through the ordinary client it builds for a provider row.
// What a test controls is only what the model SAYS, which is the one thing
// that cannot be had from a real one: a test that needs the model to ask for
// save_instruction cannot ask a model nicely.
//
// It records every request, so the tools an agent was offered and the tool
// results it was handed back are observable facts rather than assertions about
// a flag.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/orkcom-tech/cogitorium/internal/identity"
	"github.com/orkcom-tech/cogitorium/internal/inlet"
	"github.com/orkcom-tech/cogitorium/internal/websearch"
	"github.com/orkcom-tech/cogitorium/internal/workspace"
)

// modelCall is one request the engine actually sent: the system prompt, the
// conversation as the provider received it, and the names of the tools the
// agent was offered on this call.
type modelCall struct {
	System   string
	Messages []map[string]any
	Tools    []string
	// Raw is the request body exactly as it arrived, before anything decoded
	// it. A test that has to prove something is ABSENT — that a later turn
	// carries no file bytes under any part name — cannot ask a decoded shape
	// it already knows the names of; it has to search what was actually sent.
	Raw string
}

// toolResults returns what the engine handed back for the tool calls of the
// previous turn, in order. This is how a test sees a real dispatch's answer
// without reaching into the engine.
func (c modelCall) toolResults() []string {
	var out []string
	for _, m := range c.Messages {
		if m["role"] == "tool" {
			text, _ := m["content"].(string)
			out = append(out, text)
		}
	}
	return out
}

// userText is the opening turn — for an inlet run, the instruction and the
// payload the agent was actually given.
func (c modelCall) userText() string {
	for _, m := range c.Messages {
		if m["role"] == "user" {
			text, _ := m["content"].(string)
			return text
		}
	}
	return ""
}

func (c modelCall) offers(tool string) bool {
	for _, name := range c.Tools {
		if name == tool {
			return true
		}
	}
	return false
}

type modelToolCall struct{ ID, Name, Args string }

type modelReply struct {
	Text      string
	ToolCalls []modelToolCall
}

// says is the ordinary reply: the model answers and stops.
func says(text string) modelReply { return modelReply{Text: text} }

// asksFor is a reply that calls tools. The engine executes them and comes back
// with the results, which is what makes a taint test a test of the dispatch
// rather than of a map literal.
func asksFor(calls ...modelToolCall) modelReply { return modelReply{ToolCalls: calls} }

type provider struct {
	server *httptest.Server

	mu    sync.Mutex
	calls []modelCall
	notes []string
	// reply decides what the model says on call n (1-based). It runs on the
	// HTTP server's goroutine, so it must never touch *testing.T: anything it
	// learns goes into notes and is asserted on from the test's own goroutine.
	reply func(n int, call modelCall) modelReply
}

func newProvider(t *testing.T) *provider {
	t.Helper()
	p := &provider{}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /chat/completions", p.chat)
	p.server = httptest.NewServer(mux)
	t.Cleanup(p.server.Close)
	return p
}

func (p *provider) answers(reply func(n int, call modelCall) modelReply) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.reply = reply
}

func (p *provider) note(format string, args ...any) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.notes = append(p.notes, fmt.Sprintf(format, args...))
}

func (p *provider) called() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.calls)
}

func (p *provider) call(t *testing.T, n int) modelCall {
	t.Helper()
	p.mu.Lock()
	defer p.mu.Unlock()
	if n < 1 || n > len(p.calls) {
		t.Fatalf("the model was called %d times; there is no call %d", len(p.calls), n)
	}
	return p.calls[n-1]
}

// complaints returns anything the reply function saw that the test needs to
// fail on. It is checked explicitly rather than logged, so a note nobody looks
// at cannot turn into a silently passing test.
func (p *provider) complaints() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.notes...)
}

func (p *provider) reset() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls, p.notes = nil, nil
}

func (p *provider) chat(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "the engine's request body could not be read: "+err.Error(), http.StatusBadRequest)
		return
	}
	var body struct {
		Messages []map[string]any `json:"messages"`
		Tools    []struct {
			Function struct {
				Name string `json:"name"`
			} `json:"function"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		http.Error(w, "the engine sent something that is not a chat request: "+err.Error(), http.StatusBadRequest)
		return
	}
	call := modelCall{Messages: body.Messages, Raw: string(raw)}
	for _, tool := range body.Tools {
		call.Tools = append(call.Tools, tool.Function.Name)
	}
	if len(body.Messages) > 0 && body.Messages[0]["role"] == "system" {
		call.System, _ = body.Messages[0]["content"].(string)
	}

	p.mu.Lock()
	p.calls = append(p.calls, call)
	n, reply := len(p.calls), p.reply
	p.mu.Unlock()

	rep := says("done")
	if reply != nil {
		rep = reply(n, call)
	}
	writeChatStream(w, rep)
}

// writeChatStream emits the reply as the wire protocol really carries it: one
// SSE event per fragment, a finish_reason, the usage chunk the engine asks for
// with stream_options, and [DONE].
func writeChatStream(w http.ResponseWriter, rep modelReply) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)
	send := func(v any) {
		raw, _ := json.Marshal(v)
		fmt.Fprintf(w, "data: %s\n\n", raw)
		if flusher != nil {
			flusher.Flush()
		}
	}

	if rep.Text != "" {
		send(map[string]any{"choices": []any{map[string]any{
			"delta": map[string]any{"content": rep.Text},
		}}})
	}
	for i, tc := range rep.ToolCalls {
		send(map[string]any{"choices": []any{map[string]any{
			"delta": map[string]any{"tool_calls": []any{map[string]any{
				"index": i,
				"id":    tc.ID,
				"type":  "function",
				"function": map[string]any{
					"name":      tc.Name,
					"arguments": tc.Args,
				},
			}}},
		}}})
	}

	finish := "stop"
	if len(rep.ToolCalls) > 0 {
		finish = "tool_calls"
	}
	send(map[string]any{"choices": []any{map[string]any{
		"delta":         map[string]any{},
		"finish_reason": finish,
	}}})
	send(map[string]any{
		"choices": []any{},
		"usage":   map[string]any{"prompt_tokens": 11, "completion_tokens": 7},
	})
	io.WriteString(w, "data: [DONE]\n\n")
}

// --- The install under test -----------------------------------------------

// door is an install with one workspace, one provider that answers, and one
// inlet with a key. It listens on all interfaces, so an unauthenticated caller
// is nobody: every refusal below is the rule being tested and never the
// loopback-admin shortcut.
type door struct {
	*install

	provider *provider
	adminID  int64
	wsID     int64
	modelID  int64
	inletID  int64
	address  string
	key      string
}

const doorListen = "0.0.0.0:8688"

func newDoor(t *testing.T) *door { return newDoorWithSearcher(t, nil) }

// newDoorWithSearcher is newDoor for the one rule that cannot be seen with the
// outward gate off: whether web_search is OFFERED. With a nil searcher it is
// never offered to anybody, and a test asserting its absence would pass on an
// install where the unattended rule had been deleted.
func newDoorWithSearcher(t *testing.T, searcher *websearch.Searcher) *door {
	t.Helper()
	ctx := context.Background()

	p := newProvider(t)
	in := newInstallWithSearcher(t, doorListen, searcher, nil)

	admin, err := in.users.GetUserByName(ctx, identity.AdminName)
	if err != nil {
		t.Fatalf("get admin: %v", err)
	}
	catProvider, err := in.cat.CreateProvider(ctx, "house", "openai-compatible", p.server.URL, "sk-the-house-key")
	if err != nil {
		t.Fatalf("create provider: %v", err)
	}
	model, err := in.cat.CreateModel(ctx, catProvider.ID, "test-model", "house / test-model")
	if err != nil {
		t.Fatalf("create model: %v", err)
	}
	ws, err := in.spaces.CreateWorkspace(ctx, "operations", "", model.ID, admin.ID)
	if err != nil {
		t.Fatalf("create workspace: %v", err)
	}

	d := &door{install: in, provider: p, adminID: admin.ID, wsID: ws.ID, modelID: model.ID, address: "tickets"}
	inl, err := in.srv.inlets.CreateInlet(ctx, ws.ID, d.address, "the ticket door")
	if err != nil {
		t.Fatalf("create inlet: %v", err)
	}
	d.inletID = inl.ID
	if d.key, err = in.srv.inlets.IssueKey(ctx, inl.ID); err != nil {
		t.Fatalf("issue key: %v", err)
	}
	return d
}

// addJSONTask and addFileTask put a job behind the door. They go through the
// store rather than the management API because the management API is what the
// access test drives; here the task is setup, not the subject.
func (d *door) addJSONTask(t *testing.T, name, schema, agent, instruction string) inlet.Task {
	t.Helper()
	task, err := d.srv.inlets.AddTask(context.Background(), d.inletID, inlet.Task{
		Name: name, Accepts: inlet.AcceptsJSON, Schema: schema,
		AgentName: agent, Instruction: instruction,
	})
	if err != nil {
		t.Fatalf("add task %q: %v", name, err)
	}
	return task
}

func (d *door) addFileTask(t *testing.T, name, contentType, agent, instruction string) inlet.Task {
	t.Helper()
	task, err := d.srv.inlets.AddTask(context.Background(), d.inletID, inlet.Task{
		Name: name, Accepts: inlet.AcceptsFile, ContentType: contentType,
		AgentName: agent, Instruction: instruction,
	})
	if err != nil {
		t.Fatalf("add task %q: %v", name, err)
	}
	return task
}

// deliver is a caller outside this install posting to a door.
func (d *door) deliver(t *testing.T, task, key, contentType string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	return d.deliverTo(t, d.address, task, key, contentType, body)
}

func (d *door) deliverTo(t *testing.T, address, task, key, contentType string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader
	if body != nil {
		reader = strings.NewReader(string(body))
	}
	req := httptest.NewRequest(http.MethodPost, "/i/"+address+"/"+task, reader)
	req.RemoteAddr = offBox
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	rec := httptest.NewRecorder()
	d.srv.http.Handler.ServeHTTP(rec, req)
	return rec
}

// delivered is the answer a caller gets once a run number exists.
type delivered struct {
	Run    int64  `json:"run"`
	State  string `json:"state"`
	Result string `json:"result"`
	Error  string `json:"error"`
}

func (d *door) decode(t *testing.T, rec *httptest.ResponseRecorder) delivered {
	t.Helper()
	var out delivered
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("delivery answer is not the delivery shape (%d): %s", rec.Code, rec.Body.String())
	}
	return out
}

func (d *door) run(t *testing.T, id int64) inlet.Run {
	t.Helper()
	r, err := d.srv.inlets.GetRun(context.Background(), id)
	if err != nil {
		t.Fatalf("read the ledger row for run %d: %v", id, err)
	}
	return r
}

func (d *door) runs(t *testing.T) []inlet.Run {
	t.Helper()
	rs, err := d.srv.inlets.ListRuns(context.Background(), d.wsID, 500)
	if err != nil {
		t.Fatalf("read the ledger: %v", err)
	}
	return rs
}

// timelineLength counts the workspace's messages straight out of the table, so
// it cannot be fooled by a filter or a limit in the store.
func (d *door) timelineLength(t *testing.T) int {
	t.Helper()
	var n int
	if err := d.db.QueryRow(`SELECT COUNT(*) FROM ws_messages WHERE workspace_id = ?`, d.wsID).Scan(&n); err != nil {
		t.Fatalf("count the timeline: %v", err)
	}
	return n
}

func (d *door) agent(t *testing.T, name string) workspace.Agent {
	t.Helper()
	a, err := d.spaces.GetAgentByName(context.Background(), d.wsID, name)
	if err != nil {
		t.Fatalf("get agent %q: %v", name, err)
	}
	return a
}
