package llm

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// A rate limit is the most ordinary thing that happens to an agent platform, and
// it used to end the run.
//
// Every non-200 from every provider became one fmt.Errorf over the status, with
// no branch on what the status meant, and the engine has no retry of its own. So
// a 429 at delegation depth three, twelve minutes into a run, discarded
// everything the run had done — because somebody else's quota window had not
// rolled over yet.
//
// These drive a real HTTP server speaking each provider's wire format. Nothing
// is stubbed: the client is the shipped client, the transport is the shipped
// transport, and the server answers the way a rate-limited provider does. The
// server sends Retry-After: 0 so the test is fast, which is not a test-only
// affordance — a provider saying "try again now" is a thing to honour.
func TestARateLimitedCallIsRetried(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		name     string
		provider string
		body     func() string
	}{
		{"anthropic", "anthropic", anthropicOK},
		{"openai-compatible", "openai-compatible", openAIOK},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			var calls atomic.Int64
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if calls.Add(1) == 1 {
					w.Header().Set("Retry-After", "0")
					w.WriteHeader(http.StatusTooManyRequests)
					fmt.Fprint(w, `{"error":{"message":"rate limit"}}`)
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, c.body())
			}))
			defer srv.Close()

			client, err := New(c.provider, srv.URL, "sk-test")
			if err != nil {
				t.Fatalf("build client: %v", err)
			}
			res, err := client.Chat(context.Background(), Request{
				Model:    "test-model",
				Messages: []Turn{{Role: "user", Text: "hello"}},
			}, nil)
			if err != nil {
				t.Fatalf("a call that was rate limited once did not survive it: %v", err)
			}
			if n := calls.Load(); n != 2 {
				t.Fatalf("the provider was called %d time(s), want 2 — the refusal was not retried", n)
			}
			if !strings.Contains(res.Text, "answered") {
				t.Fatalf("the retried call returned %q", res.Text)
			}
		})
	}
}

// And a refusal that repeating cannot fix is not repeated. A wrong key is the
// case that matters: retrying it spends the caller's time proving something
// already known, and on a rate-limited provider it is also how a client gets
// itself blocked.
func TestARefusalTheCallerCausedIsNotRetried(t *testing.T) {
	t.Parallel()

	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"error":{"message":"invalid api key"}}`)
	}))
	defer srv.Close()

	client, err := New("anthropic", srv.URL, "sk-wrong")
	if err != nil {
		t.Fatalf("build client: %v", err)
	}
	_, err = client.Chat(context.Background(), Request{
		Model:    "test-model",
		Messages: []Turn{{Role: "user", Text: "hello"}},
	}, nil)
	if err == nil {
		t.Fatal("a 401 was not reported as an error")
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("a 401 was sent %d times; a credential does not become valid by asking again", n)
	}
	// The message still carries what the provider said, which is the only way
	// to debug a misconfigured key.
	if !strings.Contains(err.Error(), "invalid api key") || !strings.Contains(err.Error(), "401") {
		t.Fatalf("the error lost what the provider said: %v", err)
	}
}

// A provider that keeps refusing stops being asked. Three attempts total: past
// that the run is better off failing with the record of what it did than
// holding a workspace's one lane.
func TestAPersistentRateLimitGivesUp(t *testing.T) {
	t.Parallel()

	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Retry-After", "0")
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprint(w, `{"error":{"message":"upstream is down"}}`)
	}))
	defer srv.Close()

	client, err := New("openai-compatible", srv.URL, "sk-test")
	if err != nil {
		t.Fatalf("build client: %v", err)
	}
	if _, err = client.Chat(context.Background(), Request{
		Model:    "test-model",
		Messages: []Turn{{Role: "user", Text: "hello"}},
	}, nil); err == nil {
		t.Fatal("a provider that never answered was reported as success")
	}
	if n := calls.Load(); n != maxAttempts {
		t.Fatalf("the provider was called %d times, want %d", n, maxAttempts)
	}
}

// Retry-After is obeyed and bounded. A provider cannot make an install sit for
// ten minutes while a caller holds a connection.
func TestRetryAfterIsReadAndBounded(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		header string
		want   string
	}{
		{"", "absent"},
		{"0", "0s"},
		{"3", "3s"},
		{"600", maxRetryAfter.String()},
		{"-5", "absent"},
		{"not a number", "absent"},
	} {
		h := http.Header{}
		if c.header != "" {
			h.Set("Retry-After", c.header)
		}
		got, stated := retryAfter(h)
		if !stated {
			if c.want != "absent" {
				t.Fatalf("Retry-After %q was ignored, want %s", c.header, c.want)
			}
			continue
		}
		if c.want == "absent" {
			t.Fatalf("Retry-After %q was honoured as %s, and should not have been", c.header, got)
		}
		if got.String() != c.want {
			t.Fatalf("Retry-After %q became %s, want %s", c.header, got, c.want)
		}
	}
}

// Each provider's smallest well-formed stream that says one word. Written out
// rather than generated so that what the shipped parser is being fed is visible
// in the test that depends on it.
func anthropicOK() string {
	return "event: content_block_delta\n" +
		`data: {"index":0,"delta":{"type":"text_delta","text":"answered"}}` + "\n\n" +
		"event: message_delta\n" +
		`data: {"delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}` + "\n\n" +
		"event: message_stop\ndata: {}\n\n"
}

func openAIOK() string {
	return "data: " + `{"choices":[{"index":0,"delta":{"content":"answered"}}]}` + "\n\n" +
		"data: " + `{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2}}` + "\n\n" +
		"data: [DONE]\n\n"
}
