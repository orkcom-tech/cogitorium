package llm

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Retrying a model call, and the one place it is safe to.
//
// Every non-200 from every provider used to become the same sentence — one
// fmt.Errorf over the status and two kilobytes of body — with no branch on what
// the status meant. So a rate limit from the provider, which is the single most
// ordinary thing that happens to an agent platform, aborted the whole run: at
// delegation depth three, twelve minutes in, everything the run had done was
// discarded because somebody else's quota window had not rolled over yet.
//
// The retry sits HERE, around the request, and nowhere else. Once the response
// headers are in and the body has started streaming, deltas have reached the
// operator's screen and the caller's SSE connection — replaying the call would
// print a second copy of a half-written sentence. A non-2xx is decided before a
// single byte of body is read, which is exactly the window where a retry is
// invisible to everyone above it.
//
// Nothing here retries a connection that failed to open. That looks like the
// obvious companion and it is not: a dial that fails after the request was
// written may have been received, and a model call is not free to repeat — the
// tokens are spent whether or not the answer came back. A status code is proof
// the provider decided; a broken pipe is not.

// maxAttempts counts the first try. Three is the useful shape of a rate limit —
// most clear within a few seconds — and past that the run is better off failing
// with the record of what it did than sitting in a workspace's one lane.
const maxAttempts = 3

// backoff is the wait before attempts two and three. Short, because a run holds
// the workspace's lane while it waits and there is a person or a pipeline on the
// other end of it. A provider that says Retry-After overrides this.
var backoff = [...]time.Duration{2 * time.Second, 6 * time.Second}

// maxRetryAfter bounds what a provider can make this install wait. A header
// asking for ten minutes is not honoured — the run's caller is holding a
// connection, and the honest answer at that point is the failure.
const maxRetryAfter = 30 * time.Second

// ProviderError is a non-2xx answer from a model provider, with the status kept
// rather than flattened into prose. The status is the whole reason this type
// exists: it is what tells a caller whether asking again could work.
type ProviderError struct {
	Status int
	Body   string
	// What the provider is called in this install's catalog, so an error in a
	// workspace with four models says which one refused.
	Provider string
}

func (e *ProviderError) Error() string {
	if e.Provider != "" {
		return fmt.Sprintf("provider %s returned %s: %s", e.Provider, statusText(e.Status), e.Body)
	}
	return fmt.Sprintf("provider returned %s: %s", statusText(e.Status), e.Body)
}

func statusText(code int) string {
	return strconv.Itoa(code) + " " + http.StatusText(code)
}

// Retryable reports whether asking the same question again could plausibly get
// a different answer.
//
// 429 is the rate limit. 500, 502, 503 and 504 are the provider having a
// moment. Everything else is this install's problem — a wrong key, a model that
// does not exist, a request the provider will not accept — and repeating it
// would spend the caller's time proving something already known.
//
// 408 and 409 are deliberately absent: no provider here returns them, and a
// list that includes codes nobody has seen is a list nobody trusts.
func (e *ProviderError) Retryable() bool {
	switch e.Status {
	case http.StatusTooManyRequests,
		http.StatusInternalServerError,
		http.StatusBadGateway,
		http.StatusServiceUnavailable,
		http.StatusGatewayTimeout:
		return true
	}
	return false
}

// httpErrorOf reads a non-2xx response into a ProviderError, including a
// bounded slice of the body — provider error bodies are the only way to debug a
// misconfigured key or URL.
func httpErrorOf(resp *http.Response, provider string) *ProviderError {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
	return &ProviderError{
		Status:   resp.StatusCode,
		Body:     strings.TrimSpace(string(body)),
		Provider: provider,
	}
}

// retryAfter reads the header the provider sent, bounded, and reports whether
// it said anything usable at all.
//
// The two answers are separate because "wait no time" and "said nothing" are
// different instructions and collapsing them loses the first: a provider that
// answers Retry-After: 0 is saying try again now, and a single duration cannot
// distinguish that from an absent header without inventing a sentinel.
func retryAfter(h http.Header) (time.Duration, bool) {
	v := strings.TrimSpace(h.Get("Retry-After"))
	if v == "" {
		return 0, false
	}
	if secs, err := strconv.Atoi(v); err == nil {
		if secs < 0 {
			return 0, false
		}
		return min(time.Duration(secs)*time.Second, maxRetryAfter), true
	}
	// The HTTP-date form. A time already past means now.
	if when, err := http.ParseTime(v); err == nil {
		return min(max(time.Until(when), 0), maxRetryAfter), true
	}
	return 0, false
}

// send performs one model request, retrying only a retryable status and only
// before any of the body has been read.
//
// The caller supplies a function that builds a FRESH request each time: an
// http.Request's body can only be read once, and a retry that reused it would
// send an empty one and be refused for a reason that has nothing to do with the
// first refusal.
func send(ctx context.Context, do func() (*http.Response, error), provider string) (*http.Response, error) {
	for attempt := 1; ; attempt++ {
		resp, err := do()
		if err != nil {
			return nil, err
		}
		if resp.StatusCode == http.StatusOK {
			return resp, nil
		}

		perr := httpErrorOf(resp, provider)
		resp.Body.Close()
		if !perr.Retryable() || attempt >= maxAttempts {
			return nil, perr
		}

		wait, stated := retryAfter(resp.Header)
		if !stated {
			wait = backoff[attempt-1]
		}
		slog.Warn("model provider refused, retrying",
			"provider", provider, "status", perr.Status, "attempt", attempt,
			"of", maxAttempts, "wait_ms", wait.Milliseconds())

		select {
		case <-ctx.Done():
			// The caller has gone. Report what the provider said rather than
			// the cancellation: the provider's refusal is the reason this run
			// was still here to be cancelled.
			return nil, errors.Join(perr, ctx.Err())
		case <-time.After(wait):
		}
	}
}
