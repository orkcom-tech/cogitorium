package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// The usage parsers live inside Chat, behind a real response body, so the only
// honest way to exercise them is to hand the adapter a real stream. The server
// is a loopback httptest server replaying a canned payload: no provider, no
// key, no packet leaves the machine.
type fakeProvider struct {
	srv *httptest.Server

	mu   sync.Mutex
	body []byte // the request the adapter sent, for tests that assert on it
}

func newFakeProvider(t *testing.T, stream string) *fakeProvider {
	t.Helper()
	p := &fakeProvider{}
	p.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		p.mu.Lock()
		p.body = b
		p.mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, stream)
	}))
	t.Cleanup(p.srv.Close)
	return p
}

func (p *fakeProvider) sentRequest(t *testing.T) map[string]any {
	t.Helper()
	p.mu.Lock()
	defer p.mu.Unlock()
	var m map[string]any
	if err := json.Unmarshal(p.body, &m); err != nil {
		t.Fatalf("the adapter sent a body that is not JSON: %v (%s)", err, p.body)
	}
	return m
}

func chatWith(t *testing.T, providerType, baseSuffix, stream string) (Result, *fakeProvider) {
	t.Helper()
	p := newFakeProvider(t, stream)
	c, err := New(providerType, p.srv.URL+baseSuffix, "test-key")
	if err != nil {
		t.Fatalf("New(%q): %v", providerType, err)
	}
	res, err := c.Chat(context.Background(), Request{
		Model:    "a-model",
		Messages: []Turn{{Role: "user", Text: "how much did that cost?"}},
	}, nil)
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	return res, p
}

// assertUsage states the numbers a user would see on the per-agent spend
// display. Reported is checked first because it is the one that decides
// whether the other two mean anything at all.
func assertUsage(t *testing.T, got Usage, wantIn, wantOut int, wantReported bool) {
	t.Helper()
	if got.Reported != wantReported {
		if wantReported {
			t.Errorf("usage reported=false, but the provider did send figures (%d in / %d out): the turn is booked as an unknown and its real cost never reaches the operator",
				got.InputTokens, got.OutputTokens)
		} else {
			t.Errorf("usage reported=true (%d in / %d out) for a provider that sent no usage at all: the spend display shows a confident number the operator can only disprove from a bill",
				got.InputTokens, got.OutputTokens)
		}
	}
	if got.InputTokens != wantIn {
		t.Errorf("input tokens = %d, want %d: the agent is billed for prompt tokens it did not send, or not billed for ones it did", got.InputTokens, wantIn)
	}
	if got.OutputTokens != wantOut {
		t.Errorf("output tokens = %d, want %d: the agent's completion spend is misstated", got.OutputTokens, wantOut)
	}
}

// --- Anthropic ------------------------------------------------------------

// A real Anthropic stream states the prompt cost once in message_start and the
// completion cost twice: a tiny running figure in message_start and the true
// cumulative total in message_delta. Reading only the first leaves every agent
// looking like it emitted a couple of tokens per turn.
func TestAnthropicUsageCombinesMessageStartAndMessageDelta(t *testing.T) {
	t.Parallel()

	const stream = `event: message_start
data: {"type":"message_start","message":{"id":"msg_01Abc","type":"message","role":"assistant","model":"claude-sonnet-4-5","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":1200,"output_tokens":2}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: ping
data: {"type":"ping"}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Ledger"}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":" balanced"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":250}}

event: message_stop
data: {"type":"message_stop"}

`

	res, _ := chatWith(t, TypeAnthropic, "", stream)

	// 1200 comes only from message_start; 250 comes only from message_delta.
	// message_delta carries no input_tokens at all in this shape, so keeping
	// the prompt count means not overwriting it with the missing field.
	assertUsage(t, res.Usage, 1200, 250, true)
	if res.Usage.Total() != 1450 {
		t.Errorf("Total() = %d, want 1450: the headline spend figure does not equal what was charged", res.Usage.Total())
	}
	if res.Text != "Ledger balanced" {
		t.Errorf("text = %q, want %q: the turn that was billed is not the turn that was assembled", res.Text, "Ledger balanced")
	}
}

// Newer Anthropic responses restate the prompt cost in message_delta, and that
// restatement is the authoritative one. Ignoring it undercounts every turn
// whose input figure was revised after the stream opened.
func TestAnthropicMessageDeltaRestatingInputTokensWins(t *testing.T) {
	t.Parallel()

	const stream = `event: message_start
data: {"type":"message_start","message":{"id":"msg_01Def","type":"message","role":"assistant","usage":{"input_tokens":1200,"output_tokens":2}}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"input_tokens":1310,"output_tokens":88}}

event: message_stop
data: {"type":"message_stop"}

`

	res, _ := chatWith(t, TypeAnthropic, "", stream)
	assertUsage(t, res.Usage, 1310, 88, true)
}

// A gateway that closes the connection after some content — no message_delta,
// no message_stop — leaves a partial answer that Chat still returns. The
// prompt was evaluated and charged all the same, so the figures message_start
// already gave must survive as a measurement rather than decay into "unknown":
// on a large context the prompt is most of the bill.
func TestAnthropicTruncatedStreamKeepsTheUsageItAlreadyHeard(t *testing.T) {
	t.Parallel()

	const stream = `event: message_start
data: {"type":"message_start","message":{"id":"msg_01Ghi","type":"message","role":"assistant","usage":{"input_tokens":9400,"output_tokens":2}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Half an ans"}}

`

	res, _ := chatWith(t, TypeAnthropic, "", stream)

	// 2 is what the provider last said, and a partial figure the operator can
	// see beats a blank one they cannot reason about.
	assertUsage(t, res.Usage, 9400, 2, true)
	if res.Text != "Half an ans" {
		t.Errorf("text = %q, want %q: the truncated stream did not parse as this case assumes", res.Text, "Half an ans")
	}
}

// A relay in front of the API can forward content without ever forwarding a
// usage figure. The adapter must say "nothing was reported" rather than let a
// zeroed struct pass for a measurement — an agent that looks free is one
// nobody investigates.
func TestAnthropicStreamWithNoUsageEventsReportsAbsence(t *testing.T) {
	t.Parallel()

	const stream = `event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Ledger"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null}}

event: message_stop
data: {"type":"message_stop"}

`

	res, _ := chatWith(t, TypeAnthropic, "", stream)
	assertUsage(t, res.Usage, 0, 0, false)
	if res.Text != "Ledger" {
		t.Errorf("text = %q, want %q: the stream itself did not parse, so the usage assertion above proves nothing", res.Text, "Ledger")
	}
}

// --- OpenAI-compatible ----------------------------------------------------

// Four servers a user can actually point Cogitorium at. They differ only in
// where — or whether — the usage figures appear, which is exactly the axis the
// spend display depends on.
func TestOpenAICompatibleUsageAcrossServerShapes(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name         string
		stream       string
		wantIn       int
		wantOut      int
		wantReported bool
		wantText     string
	}{
		{
			// OpenAI and OpenRouter honour stream_options.include_usage by
			// appending one extra chunk that has no choices at all. Bailing
			// out on empty choices before reading usage throws the only
			// billing figure of the whole turn away.
			name: "usage in a trailing chunk that carries no choices",
			stream: `data: {"id":"chatcmpl-a","object":"chat.completion.chunk","created":1735689600,"model":"gpt-4o-mini","choices":[{"index":0,"delta":{"role":"assistant","content":""},"finish_reason":null}],"usage":null}

data: {"id":"chatcmpl-a","object":"chat.completion.chunk","created":1735689600,"model":"gpt-4o-mini","choices":[{"index":0,"delta":{"content":"Ledger"},"finish_reason":null}],"usage":null}

data: {"id":"chatcmpl-a","object":"chat.completion.chunk","created":1735689600,"model":"gpt-4o-mini","choices":[{"index":0,"delta":{"content":" balanced"},"finish_reason":null}],"usage":null}

data: {"id":"chatcmpl-a","object":"chat.completion.chunk","created":1735689600,"model":"gpt-4o-mini","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":null}

data: {"id":"chatcmpl-a","object":"chat.completion.chunk","created":1735689600,"model":"gpt-4o-mini","choices":[],"usage":{"prompt_tokens":57,"completion_tokens":17,"total_tokens":74}}

data: [DONE]

`,
			wantIn: 57, wantOut: 17, wantReported: true, wantText: "Ledger balanced",
		},
		{
			// vLLM and llama.cpp-server hang the totals off the same chunk
			// that carries the last content and the finish reason.
			name: "usage attached to the final content chunk",
			stream: `data: {"choices":[{"index":0,"delta":{"role":"assistant","content":"Ledger"},"finish_reason":null}]}

data: {"choices":[{"index":0,"delta":{"content":" balanced"},"finish_reason":"stop"}],"usage":{"prompt_tokens":31,"completion_tokens":4,"total_tokens":35}}

data: [DONE]

`,
			wantIn: 31, wantOut: 4, wantReported: true, wantText: "Ledger balanced",
		},
		{
			// Plenty of local servers accept stream_options and then ignore
			// it. This is the case the whole Reported flag exists for: the
			// answer is real, the cost is unknown, and saying "0" would be
			// a number the operator trusts and should not.
			name: "server ignores stream_options and never reports usage",
			stream: `data: {"choices":[{"index":0,"delta":{"role":"assistant","content":"Ledger"},"finish_reason":null}],"usage":null}

data: {"choices":[{"index":0,"delta":{"content":" balanced"},"finish_reason":"stop"}],"usage":null}

data: [DONE]

`,
			wantIn: 0, wantOut: 0, wantReported: false, wantText: "Ledger balanced",
		},
		{
			// The mirror of the case above: a server that measured the turn
			// and measured zero. The adapter must not second-guess it — a
			// reported zero is a fact, and downgrading it to "unknown" makes
			// a genuinely free turn look like a blind spot forever.
			name: "server reports a genuine zero",
			stream: `data: {"choices":[{"index":0,"delta":{"role":"assistant","content":""},"finish_reason":"stop"}]}

data: {"choices":[],"usage":{"prompt_tokens":0,"completion_tokens":0,"total_tokens":0}}

data: [DONE]

`,
			wantIn: 0, wantOut: 0, wantReported: true, wantText: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			res, _ := chatWith(t, TypeOpenAICompatible, "/v1", tc.stream)
			assertUsage(t, res.Usage, tc.wantIn, tc.wantOut, tc.wantReported)
			if res.Text != tc.wantText {
				t.Errorf("text = %q, want %q: the stream did not parse the way this case claims", res.Text, tc.wantText)
			}
		})
	}
}

// The usage chunk arrives after the tool calls are finished and carries no
// choices of its own. Reading it must not cost the turn its tool calls, and
// assembling tool calls must not cost the turn its bill — a tool-heavy
// orchestrator turn is the most expensive kind there is.
func TestOpenAIToolCallTurnKeepsBothCallsAndUsage(t *testing.T) {
	t.Parallel()

	const stream = `data: {"choices":[{"index":0,"delta":{"role":"assistant","content":null,"tool_calls":[{"index":0,"id":"call_abc123","type":"function","function":{"name":"read_file","arguments":""}}]},"finish_reason":null}],"usage":null}

data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"path\":"}}]},"finish_reason":null}],"usage":null}

data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"notes.md\"}"}}]},"finish_reason":null}],"usage":null}

data: {"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}],"usage":null}

data: {"choices":[],"usage":{"prompt_tokens":812,"completion_tokens":40,"total_tokens":852}}

data: [DONE]

`

	res, _ := chatWith(t, TypeOpenAICompatible, "/v1", stream)

	assertUsage(t, res.Usage, 812, 40, true)
	if res.StopReason != "tool_use" {
		t.Errorf("stop reason = %q, want %q: the engine would end the turn instead of running the tool", res.StopReason, "tool_use")
	}
	if len(res.ToolCalls) != 1 {
		t.Fatalf("got %d tool calls, want 1: the trailing usage chunk disturbed tool assembly", len(res.ToolCalls))
	}
	if got := res.ToolCalls[0]; got.ID != "call_abc123" || got.Name != "read_file" || got.InputJSON != `{"path":"notes.md"}` {
		t.Errorf("tool call = %+v, want id call_abc123 name read_file args {\"path\":\"notes.md\"}", got)
	}
}

// An OpenAI-compatible stream reports nothing unless the request asks for it.
// Drop the option and every agent on every such provider silently becomes an
// unmetered one, which no test of the parser alone would ever notice.
func TestOpenAIRequestAsksTheServerToIncludeUsage(t *testing.T) {
	t.Parallel()

	const stream = `data: {"choices":[{"index":0,"delta":{"content":"ok"},"finish_reason":"stop"}]}

data: [DONE]

`

	_, p := chatWith(t, TypeOpenAICompatible, "/v1", stream)
	sent := p.sentRequest(t)

	if sent["stream"] != true {
		t.Errorf("request stream = %v, want true: without a stream there is no usage chunk and no live output", sent["stream"])
	}
	opts, ok := sent["stream_options"].(map[string]any)
	if !ok {
		t.Fatalf("request has no stream_options object (%v): OpenAI-compatible servers report usage only when asked", sent["stream_options"])
	}
	if opts["include_usage"] != true {
		t.Errorf("stream_options.include_usage = %v, want true: the provider will stream the answer and never say what it cost", opts["include_usage"])
	}
}
