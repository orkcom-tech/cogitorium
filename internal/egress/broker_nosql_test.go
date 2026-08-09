package egress

import (
	"context"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// This is the deadlock argument, enforced rather than commented.
//
// SQLite is opened with MaxOpenConns(1). A turn that waited for a human while
// holding that connection would leave the approval endpoint unable to write,
// so the operator could never answer and the only exit would be a restart.
// The structural guarantee is that this package cannot touch the database at
// all; this test is what keeps the guarantee true after someone "just needs
// one query here".
func TestPackageCannotTouchTheDatabase(t *testing.T) {
	banned := []string{"database/sql", "internal/store", "internal/workspace", "modernc.org/sqlite"}

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("reading the package directory: %v", err)
	}
	fset := token.NewFileSet()
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".go" || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, e.Name(), nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parsing %s: %v", e.Name(), err)
		}
		for _, imp := range f.Imports {
			path := strings.Trim(imp.Path.Value, `"`)
			for _, b := range banned {
				if path == b || strings.Contains(path, b) {
					t.Errorf("%s imports %q. A paused turn must hold no database connection while it "+
						"waits for a human, or the single SQLite connection deadlocks the approval "+
						"endpoint that is supposed to release it.", e.Name(), path)
				}
			}
		}
	}
}

func req(t *testing.T, deadline time.Duration) *Request {
	t.Helper()
	tok, err := NewToken()
	if err != nil {
		t.Fatalf("token: %v", err)
	}
	return &Request{Token: tok, Deadline: time.Now().Add(deadline)}
}

func TestSecondPauseAnywhereIsRefused(t *testing.T) {
	b := New()
	first := req(t, time.Minute)
	if err := b.Open(first); err != nil {
		t.Fatalf("the first pause was refused: %v", err)
	}
	if err := b.Open(req(t, time.Minute)); err != ErrBusy {
		t.Fatalf("a second pause was accepted (%v); one modal at a time is what keeps the "+
			"browser connection pool from starving the answer", err)
	}
	b.Close(first.Token)
	if err := b.Open(req(t, time.Minute)); err != nil {
		t.Fatalf("the slot was not released: %v", err)
	}
}

func TestAnsweringTwiceIsRefusedRatherThanSilentlyAccepted(t *testing.T) {
	b := New()
	r := req(t, time.Minute)
	if err := b.Open(r); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Resolve(r.Token, Answer{Allow: true}); err != nil {
		t.Fatalf("the first answer failed: %v", err)
	}
	if _, err := b.Resolve(r.Token, Answer{Allow: false}); err != ErrAlreadyClosed {
		t.Fatalf("a second decision was accepted (%v); a late click must never overwrite a "+
			"decision already acted on", err)
	}
}

func TestAnUnknownOrForeignTokenIsRefused(t *testing.T) {
	b := New()
	r := req(t, time.Minute)
	if err := b.Open(r); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Resolve("deadbeef", Answer{Allow: true}); err != ErrUnknown {
		t.Fatalf("a foreign token resolved the pending request: %v", err)
	}
}

func TestResolveNeverBlocksTheHandlerGoroutine(t *testing.T) {
	// Resolve runs on an HTTP handler that is about to want the database. If
	// it could block on an unread channel this is exactly where the deadlock
	// would reappear, so it must return with nobody waiting.
	b := New()
	r := req(t, time.Minute)
	if err := b.Open(r); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		_, _ = b.Resolve(r.Token, Answer{Allow: true})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Resolve blocked with no waiter; the approval handler would hang holding nothing")
	}
}

func TestWaitFailsClosedOnDeadline(t *testing.T) {
	b := New()
	r := req(t, 50*time.Millisecond)
	if err := b.Open(r); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Wait(context.Background(), r); err != ErrTimedOut {
		t.Fatalf("an unanswered request did not time out: %v", err)
	}
	// A click landing after the deadline must not resurrect it.
	if _, err := b.Resolve(r.Token, Answer{Allow: true}); err == nil {
		t.Fatal("a decision was accepted after the request expired")
	}
}

func TestWaitFailsClosedWhenTheStreamGoesAway(t *testing.T) {
	b := New()
	r := req(t, time.Minute)
	if err := b.Open(r); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // the browser tab carrying the SSE stream closed
	if _, err := b.Wait(ctx, r); err == nil {
		t.Fatal("the turn kept waiting after its stream died; it must abort rather than hang")
	}
}

func TestAllowIsDeliveredWithTheDecidersIdentity(t *testing.T) {
	b := New()
	r := req(t, time.Minute)
	if err := b.Open(r); err != nil {
		t.Fatal(err)
	}
	go func() {
		time.Sleep(20 * time.Millisecond)
		_, _ = b.Resolve(r.Token, Answer{Allow: true, UserID: 7, UserName: "the-operator", Auth: "bearer"})
	}()
	a, err := b.Wait(context.Background(), r)
	if err != nil {
		t.Fatalf("wait failed: %v", err)
	}
	if !a.Allow || a.UserID != 7 || a.UserName != "the-operator" || a.Auth != "bearer" {
		t.Fatalf("the waiter cannot write an honest audit row from %+v", a)
	}
}

func TestTokensAreUnguessableAndDistinct(t *testing.T) {
	seen := map[Token]bool{}
	for range 512 {
		tok, err := NewToken()
		if err != nil {
			t.Fatalf("token: %v", err)
		}
		if len(tok) != 32 {
			t.Fatalf("token %q is not 128 bits of hex", tok)
		}
		if seen[tok] {
			t.Fatalf("token %q repeated", tok)
		}
		seen[tok] = true
	}
}
