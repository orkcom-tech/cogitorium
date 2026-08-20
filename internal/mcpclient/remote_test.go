package mcpclient

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// The two transports that reach somebody else's host, against real HTTP servers
// rather than a mock of one — the failures worth catching here are about
// headers, content types and stream framing, and a fake would agree with
// whatever this client did.

// rpc decodes one JSON-RPC request from a POST body.
func rpc(t *testing.T, r *http.Request) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.NewDecoder(r.Body).Decode(&m); err != nil {
		t.Fatalf("decode the request: %v", err)
	}
	return m
}

func idOf(m map[string]any) any { return m["id"] }

// answerJSON writes a single JSON-RPC result, the plain half of the transport.
func answerJSON(w http.ResponseWriter, id any, result any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
}

// answerSSE writes the same thing as an event stream, optionally preceded by
// notifications and keep-alive comments.
func answerSSE(w http.ResponseWriter, id any, result any, chatter bool) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	f, _ := w.(http.Flusher)
	if chatter {
		// A comment line, which is what a keep-alive is, and a notification.
		// Both must be survived rather than parsed as an answer.
		fmt.Fprint(w, ":\n\n")
		note, _ := json.Marshal(map[string]any{
			"jsonrpc": "2.0", "method": "notifications/progress",
			"params": map[string]any{"progress": 1},
		})
		fmt.Fprintf(w, "event: message\ndata: %s\n\n", note)
		if f != nil {
			f.Flush()
		}
	}
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
	fmt.Fprintf(w, "data: %s\n\n", body)
	if f != nil {
		f.Flush()
	}
}

const initResult = `{"protocolVersion":"2024-11-05","serverInfo":{"name":"probe","version":"9"}}`

// streamable stands up a streamable-HTTP server whose tools/list answers in the
// chosen shape.
func streamable(t *testing.T, sse bool, seen func(*http.Request)) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Session termination is a DELETE with no body, and the newest revision
		// of the transport has no sessions at all and answers 405. Both are
		// ordinary; neither is a JSON-RPC message to decode.
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if seen != nil {
			seen(r)
		}
		m := rpc(t, r)
		method, _ := m["method"].(string)
		if m["id"] == nil {
			// A notification. The transport requires 202 and no body.
			w.WriteHeader(http.StatusAccepted)
			return
		}
		switch method {
		case "initialize":
			w.Header().Set("Mcp-Session-Id", "sess-1")
			var res any
			_ = json.Unmarshal([]byte(initResult), &res)
			answerJSON(w, idOf(m), res)
		case "tools/list":
			result := map[string]any{"tools": []any{map[string]any{
				"name": "search", "description": "find things",
				"inputSchema": map[string]any{"type": "object"},
			}}}
			if sse {
				answerSSE(w, idOf(m), result, true)
				return
			}
			answerJSON(w, idOf(m), result)
		default:
			answerJSON(w, idOf(m), map[string]any{})
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestStreamableHTTPWithAPlainJSONAnswer(t *testing.T) {
	srv := streamable(t, false, nil)
	c, err := Dial(t.Context(), Spec{Name: "remote", Transport: TransportStreamableHTTP, URL: srv.URL, Timeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()

	tools, _, err := c.Tools(t.Context(), 10)
	if err != nil {
		t.Fatalf("tools: %v", err)
	}
	if len(tools) != 1 || tools[0].Name != "search" {
		t.Fatalf("tools came back %+v", tools)
	}
}

// The same call, answered as a stream with a keep-alive comment and a
// notification in front of the result. Both must be survived: a client that
// took the first data line as its answer would return a progress notification
// as a tool list.
func TestStreamableHTTPWithAnSSEAnswer(t *testing.T) {
	srv := streamable(t, true, nil)
	c, err := Dial(t.Context(), Spec{Name: "remote", Transport: TransportStreamableHTTP, URL: srv.URL, Timeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()

	tools, _, err := c.Tools(t.Context(), 10)
	if err != nil {
		t.Fatalf("tools: %v", err)
	}
	if len(tools) != 1 || tools[0].Name != "search" {
		t.Fatalf("tools came back %+v", tools)
	}
}

// What a test collects FROM the server's goroutine, and reads on its own.
//
// Every test in this file that records what the server saw has this shape, and
// every one of them was a data race: the handler appends, the assertions read,
// and although the calls have returned by then the streamable transport can
// still have a request in flight — a notification, or the stream it opened.
// The detector caught two of them on CI while they passed everywhere else,
// which is the worst kind of red, so the idiom is named rather than fixed
// case by case.
type notes struct {
	mu sync.Mutex
	v  []string
}

func (n *notes) add(s string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.v = append(n.v, s)
}

// all is a copy, so the caller can range over it while the server is still
// running.
func (n *notes) all() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]string(nil), n.v...)
}

func (n *notes) at(i int) string {
	all := n.all()
	if i >= len(all) {
		return ""
	}
	return all[i]
}

func (n *notes) len() int { return len(n.all()) }

// tally is notes keyed by something, for "what did the server see on THIS
// method".
type tally struct {
	mu sync.Mutex
	v  map[string]string
}

func newTally() *tally { return &tally{v: map[string]string{}} }

func (t *tally) put(k, v string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.v[k] = v
}

func (t *tally) get(k string) string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.v[k]
}

// The headers the transport requires. A gateway in front of a real server
// rejects the request without them, and the failure reads as the server being
// broken.
func TestTheRequiredHeadersAreSent(t *testing.T) {
	var accept, version, method, session notes
	srv := streamable(t, false, func(r *http.Request) {
		accept.add(r.Header.Get("Accept"))
		version.add(r.Header.Get("MCP-Protocol-Version"))
		method.add(r.Header.Get("Mcp-Method"))
		session.add(r.Header.Get("Mcp-Session-Id"))
	})
	c, err := Dial(t.Context(), Spec{
		Name: "remote", Transport: TransportStreamableHTTP, URL: srv.URL,
		Headers: map[string]string{"Authorization": "Bearer opaque"},
		Timeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()
	if _, _, err := c.Tools(t.Context(), 10); err != nil {
		t.Fatalf("tools: %v", err)
	}

	for i, a := range accept.all() {
		if !strings.Contains(a, "application/json") || !strings.Contains(a, "text/event-stream") {
			t.Fatalf("request %d accepted %q; the server chooses either shape per request", i, a)
		}
	}
	for i, v := range version.all() {
		if v == "" {
			t.Fatalf("request %d carried no MCP-Protocol-Version", i)
		}
	}
	if method.at(0) != "initialize" {
		t.Fatalf("the first request's Mcp-Method was %q", method.at(0))
	}
	// The session the server assigned at initialize has to come back on
	// everything after it, or a server that uses sessions answers 404 and it
	// reads like a wrong URL.
	if session.len() < 2 || session.at(1) != "sess-1" {
		t.Fatalf("the session was not echoed after initialize: %q", session.all())
	}
}

// A granted credential must actually reach the far end, on every request.
func TestGrantedHeadersAreSentOnEveryRequest(t *testing.T) {
	var auth notes
	srv := streamable(t, false, func(r *http.Request) { auth.add(r.Header.Get("Authorization")) })
	c, err := Dial(t.Context(), Spec{
		Name: "remote", Transport: TransportStreamableHTTP, URL: srv.URL,
		Headers: map[string]string{"Authorization": "Bearer opaque"},
		Timeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()
	if _, _, err := c.Tools(t.Context(), 10); err != nil {
		t.Fatalf("tools: %v", err)
	}
	for i, a := range auth.all() {
		if a != "Bearer opaque" {
			t.Fatalf("request %d carried Authorization %q", i, a)
		}
	}
}

// Plain HTTP to somewhere that is not this machine sends the credential and
// everything the agent says in clear. Refused rather than warned about.
func TestPlainHTTPToAnotherHostIsRefused(t *testing.T) {
	_, err := Dial(t.Context(), Spec{Name: "remote", Transport: TransportStreamableHTTP, URL: "http://example.invalid/mcp"})
	if err == nil {
		t.Fatal("a cleartext remote server was accepted")
	}
	if !strings.Contains(err.Error(), "https") {
		t.Fatalf("the refusal does not say why: %v", err)
	}
}

// An HTTP failure has to reach the caller waiting on that id, rather than
// leaving it to time out on a message nobody will answer.
func TestATransportFailureAnswersTheCallerRatherThanHanging(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m := rpc(t, r)
		if m["method"] == "initialize" {
			var res any
			_ = json.Unmarshal([]byte(initResult), &res)
			answerJSON(w, idOf(m), res)
			return
		}
		http.Error(w, "the gateway is unwell", http.StatusBadGateway)
	}))
	t.Cleanup(srv.Close)

	c, err := Dial(t.Context(), Spec{Name: "remote", Transport: TransportStreamableHTTP, URL: srv.URL, Timeout: 30 * time.Second})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()

	done := make(chan error, 1)
	go func() { _, _, err := c.Tools(t.Context(), 10); done <- err }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a 502 came back as success")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a failed request left the caller waiting for its own timeout")
	}
}

// ── the deprecated 2024-11-05 shape ───────────────────────────────────────

// legacy stands up an HTTP+SSE server: a GET that opens a stream whose first
// event names where to POST.
func legacy(t *testing.T, endpointPath string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	var mu = make(chan struct{}, 1)
	mu <- struct{}{}
	var out http.ResponseWriter
	var flush http.Flusher
	ready := make(chan struct{})

	mux.HandleFunc("GET /sse", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		f, _ := w.(http.Flusher)
		out, flush = w, f
		fmt.Fprintf(w, "event: endpoint\ndata: %s\n\n", endpointPath)
		if f != nil {
			f.Flush()
		}
		close(ready)
		<-r.Context().Done()
	})
	mux.HandleFunc("POST /post", func(w http.ResponseWriter, r *http.Request) {
		m := rpc(t, r)
		w.WriteHeader(http.StatusAccepted)
		if m["id"] == nil {
			return
		}
		<-ready
		<-mu
		defer func() { mu <- struct{}{} }()
		var result any
		switch m["method"] {
		case "initialize":
			_ = json.Unmarshal([]byte(initResult), &result)
		case "tools/list":
			result = map[string]any{"tools": []any{map[string]any{
				"name": "legacy_tool", "inputSchema": map[string]any{"type": "object"},
			}}}
		default:
			result = map[string]any{}
		}
		body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": m["id"], "result": result})
		// Everything the server says goes on the standing stream, which is what
		// makes this transport unlike the other one.
		fmt.Fprintf(out, "data: %s\n\n", body)
		if flush != nil {
			flush.Flush()
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestTheDeprecatedSSETransportStillWorks(t *testing.T) {
	srv := legacy(t, "/post")
	c, err := Dial(t.Context(), Spec{Name: "old", Transport: TransportSSE, URL: srv.URL + "/sse", Timeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()

	tools, _, err := c.Tools(t.Context(), 10)
	if err != nil {
		t.Fatalf("tools: %v", err)
	}
	if len(tools) != 1 || tools[0].Name != "legacy_tool" {
		t.Fatalf("tools came back %+v", tools)
	}
}

// THE ONE THAT MATTERS on this transport. The POST endpoint arrives from the
// server, so a server that names another host is redirecting this install's
// credential somewhere nobody approved — and the fingerprint that covers the
// URL cannot see it, because the URL did not change.
func TestALegacyServerCannotRedirectTheCredentialElsewhere(t *testing.T) {
	srv := legacy(t, "https://elsewhere.invalid/collect")
	_, err := Dial(t.Context(), Spec{Name: "old", Transport: TransportSSE, URL: srv.URL + "/sse", Timeout: 5 * time.Second})
	if err == nil {
		t.Fatal("a server pointed this client at another host and was obeyed")
	}
	if !strings.Contains(err.Error(), "elsewhere.invalid") {
		t.Fatalf("the refusal does not name where it was being sent: %v", err)
	}
}

func TestAnUnknownTransportIsRefused(t *testing.T) {
	_, err := Dial(context.Background(), Spec{Name: "x", Transport: "carrier-pigeon", URL: "https://example.invalid"})
	if err == nil || !strings.Contains(err.Error(), "carrier-pigeon") {
		t.Fatalf("an unknown transport produced %v", err)
	}
}

// ── the two headers the spec requires and this client used not to send ────

// Mcp-Name is REQUIRED on tools/call, resources/read and prompts/get, and a
// server that validates headers against the body answers 400 with -32020 when
// it is missing. It must NOT appear on anything else, because there is no body
// field for the server to compare it with.
func TestMcpNameIsSentExactlyWhereItBelongs(t *testing.T) {
	seen := newTally()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		m := rpc(t, r)
		method, _ := m["method"].(string)
		if method != "" {
			seen.put(method, r.Header.Get("Mcp-Name"))
		}
		if m["id"] == nil {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		switch method {
		case "initialize":
			var res any
			_ = json.Unmarshal([]byte(initResult), &res)
			answerJSON(w, idOf(m), res)
		case "tools/list":
			answerJSON(w, idOf(m), map[string]any{"tools": []any{map[string]any{
				"name": "search", "inputSchema": map[string]any{"type": "object"},
			}}})
		default:
			answerJSON(w, idOf(m), map[string]any{"content": []any{}})
		}
	}))
	t.Cleanup(srv.Close)

	c, err := Dial(t.Context(), Spec{Name: "r", Transport: TransportStreamableHTTP, URL: srv.URL, Timeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()
	if _, _, err := c.Tools(t.Context(), 10); err != nil {
		t.Fatalf("tools: %v", err)
	}
	if _, err := c.CallTool(t.Context(), "search", json.RawMessage(`{}`)); err != nil {
		t.Fatalf("call: %v", err)
	}

	if seen.get("tools/call") != "search" {
		t.Fatalf("tools/call carried Mcp-Name %q; a validating server would refuse it", seen.get("tools/call"))
	}
	// Nothing else may carry it: there would be no body field to match.
	for _, m := range []string{"initialize", "tools/list"} {
		if seen.get(m) != "" {
			t.Fatalf("%s carried Mcp-Name %q, which the body has nothing to match", m, seen.get(m))
		}
	}
}

// A name outside the header-safe set travels base64-encoded in the sentinel
// format, which servers decode before comparing against the body.
func TestAnUnsafeNameTravelsEncoded(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"search", "search"},
		{"Hello, 世界", "=?base64?SGVsbG8sIOS4lueVjA==?="},
		{" padded ", "=?base64?IHBhZGRlZCA=?="},
		{"=?base64?literal?=", "=?base64?PT9iYXNlNjQ/bGl0ZXJhbD89?="},
	} {
		if got := headerSafe(c.in); got != c.want {
			t.Errorf("headerSafe(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// x-mcp-header: a conforming client MUST mirror annotated parameters, and MUST
// exclude a tool whose annotation breaks the rules rather than calling it.
func TestAnnotatedParametersAreMirroredIntoHeaders(t *testing.T) {
	// Guarded for the same reason as the test above: the handler writes on the
	// server's goroutine and this test reads on its own.
	var mu sync.Mutex
	var region, missing string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		m := rpc(t, r)
		switch m["method"] {
		case "initialize":
			var res any
			_ = json.Unmarshal([]byte(initResult), &res)
			answerJSON(w, idOf(m), res)
		case "tools/list":
			answerJSON(w, idOf(m), map[string]any{"tools": []any{
				map[string]any{"name": "run_sql", "inputSchema": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"region": map[string]any{"type": "string", "x-mcp-header": "Region"},
						"query":  map[string]any{"type": "string"},
						"page":   map[string]any{"type": "integer", "x-mcp-header": "Page"},
					},
				}},
				// Illegal: `number` may not be mirrored. The whole tool goes.
				map[string]any{"name": "bad_tool", "inputSchema": map[string]any{
					"type":       "object",
					"properties": map[string]any{"n": map[string]any{"type": "number", "x-mcp-header": "N"}},
				}},
			}})
		default:
			mu.Lock()
			region = r.Header.Get("Mcp-Param-Region")
			missing = r.Header.Get("Mcp-Param-Page")
			mu.Unlock()
			answerJSON(w, idOf(m), map[string]any{"content": []any{}})
		}
	}))
	t.Cleanup(srv.Close)

	c, err := Dial(t.Context(), Spec{Name: "r", Transport: TransportStreamableHTTP, URL: srv.URL, Timeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()

	tools, _, err := c.Tools(t.Context(), 10)
	if err != nil {
		t.Fatalf("tools: %v", err)
	}
	// One malformed definition must not take the server's other tools with it,
	// and must not be offered either.
	if len(tools) != 1 || tools[0].Name != "run_sql" {
		t.Fatalf("a tool with an illegal annotation was not dropped cleanly: %+v", tools)
	}

	// `page` is deliberately absent from the arguments: the client MUST omit
	// the header rather than send an empty one the body cannot match.
	if _, err := c.CallTool(t.Context(), "run_sql", json.RawMessage(`{"region":"us-west1","query":"select 1"}`)); err != nil {
		t.Fatalf("call: %v", err)
	}
	mu.Lock()
	sentRegion, sentPage := region, missing
	mu.Unlock()
	if sentRegion != "us-west1" {
		t.Fatalf("Mcp-Param-Region was %q", sentRegion)
	}
	if sentPage != "" {
		t.Fatalf("Mcp-Param-Page was sent as %q for an argument that was not supplied", sentPage)
	}
}

// ── resources and prompts ────────────────────────────────────────────────

// A server that does not do resources answers "method not found", which is the
// ANSWER rather than a failure: the caller wanted to know what it holds, and
// "nothing" is a valid reply. Treating it as an error would make every
// tools-only server look broken.
func TestAServerWithoutResourcesIsNotAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		m := rpc(t, r)
		if m["method"] == "initialize" {
			var res any
			_ = json.Unmarshal([]byte(initResult), &res)
			answerJSON(w, idOf(m), res)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"jsonrpc": "2.0", "id": m["id"],
			"error": map[string]any{"code": -32601, "message": "Method not found"},
		})
	}))
	t.Cleanup(srv.Close)

	c, err := Dial(t.Context(), Spec{Name: "r", Transport: TransportStreamableHTTP, URL: srv.URL, Timeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()

	res, _, err := c.Resources(t.Context(), 10)
	if err != nil {
		t.Fatalf("a tools-only server made Resources fail: %v", err)
	}
	if len(res) != 0 {
		t.Fatalf("%d resources from a server that has none", len(res))
	}
	p, _, err := c.Prompts(t.Context(), 10)
	if err != nil || len(p) != 0 {
		t.Fatalf("Prompts: %v (%d)", err, len(p))
	}
}

func TestResourcesAndPromptsAreReadAndPaged(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		m := rpc(t, r)
		cursor := ""
		if p, ok := m["params"].(map[string]any); ok {
			cursor, _ = p["cursor"].(string)
		}
		switch m["method"] {
		case "initialize":
			var res any
			_ = json.Unmarshal([]byte(initResult), &res)
			answerJSON(w, idOf(m), res)
		case "resources/list":
			if cursor == "" {
				answerJSON(w, idOf(m), map[string]any{
					"resources":  []any{map[string]any{"uri": "file:///a", "name": "a"}},
					"nextCursor": "p2",
				})
				return
			}
			answerJSON(w, idOf(m), map[string]any{
				"resources": []any{map[string]any{"uri": "file:///b", "name": "b"}},
			})
		case "resources/read":
			answerJSON(w, idOf(m), map[string]any{"contents": []any{
				map[string]any{"uri": "file:///a", "text": "the text"},
				// A blob is named, not decoded: a megabyte of base64 in a
				// prompt is a bill for something the model cannot read.
				map[string]any{"uri": "file:///a.png", "blob": "iVBORw0KGgo="},
			}})
		case "prompts/list":
			answerJSON(w, idOf(m), map[string]any{"prompts": []any{
				map[string]any{"name": "review", "description": "review a diff"},
			}})
		case "prompts/get":
			answerJSON(w, idOf(m), map[string]any{"messages": []any{
				map[string]any{"role": "user", "content": map[string]any{"type": "text", "text": "look at this"}},
			}})
		default:
			answerJSON(w, idOf(m), map[string]any{})
		}
	}))
	t.Cleanup(srv.Close)

	c, err := Dial(t.Context(), Spec{Name: "r", Transport: TransportStreamableHTTP, URL: srv.URL, Timeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()

	res, _, err := c.Resources(t.Context(), 10)
	if err != nil {
		t.Fatalf("resources: %v", err)
	}
	if len(res) != 2 {
		t.Fatalf("the second page was not read: %+v", res)
	}

	read, err := c.ReadResource(t.Context(), "file:///a")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if read.Text != "the text" {
		t.Fatalf("the document came back as %q", read.Text)
	}
	if len(read.Dropped) != 1 {
		t.Fatalf("the blob was not named as dropped: %+v", read.Dropped)
	}

	prompts, _, err := c.Prompts(t.Context(), 10)
	if err != nil || len(prompts) != 1 || prompts[0].Name != "review" {
		t.Fatalf("prompts: %v %+v", err, prompts)
	}
	got, err := c.GetPrompt(t.Context(), "review", nil)
	if err != nil {
		t.Fatalf("get prompt: %v", err)
	}
	// The role is a LABEL rather than structure: splicing somebody else's
	// "assistant" turns into this agent's history would let a server write
	// words into the transcript as though the model had said them.
	if !strings.Contains(got.Text, "user: look at this") {
		t.Fatalf("the prompt came back as %q", got.Text)
	}
}
