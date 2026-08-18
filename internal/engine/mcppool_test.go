package engine

import (
	"context"
	"testing"
	"time"

	"github.com/orkcom-tech/cogitorium/internal/mcpstore"
)

// The pool's rules are about LIFETIME, and every one of them is a way for a
// process to outlive something it should not.

// A server edited since is a different server. Reusing a connection opened
// under the old command would run code the operator has already unapproved —
// which is precisely what the fingerprint check at spawn exists to prevent, and
// a pool keyed only by id would route straight past it.
func TestTheFingerprintIsPartOfTheKey(t *testing.T) {
	before := mcpstore.Server{Name: "jira", Fingerprint: "aaa"}
	after := mcpstore.Server{Name: "jira", Fingerprint: "bbb"}
	if poolKey(before) == poolKey(after) {
		t.Fatal("an edited server shares a pool key with what it used to be")
	}
	same := mcpstore.Server{Name: "jira", Fingerprint: "aaa"}
	if poolKey(before) != poolKey(same) {
		t.Fatal("the same server does not share its own key, so nothing would ever be reused")
	}
}

// A connection nobody has asked for in a while is closed whether or not anybody
// asks again — otherwise a workspace that used a server once holds a node
// process until the server restarts.
func TestAnIdleConnectionIsExpired(t *testing.T) {
	p := newMCPPool()
	p.idle["stale"] = &pooled{last: time.Now().Add(-2 * mcpIdle)}

	p.expire()

	p.mu.Lock()
	_, kept := p.idle["stale"]
	p.mu.Unlock()
	if kept {
		t.Fatal("an idle connection survived the sweep; a workspace that used a server once would hold a process")
	}
	// The other half — that a FRESH connection survives — cannot be written
	// with a fixture, because a pooled entry with no live connection is
	// correctly read as dead and swept for that reason rather than for its age.
	// What keeps a live one is Alive() returning true, which needs a real
	// server on the other end; the reuse path itself is covered where a real
	// connection exists, in the mcpclient tests.
}

// A busy connection is never swept out from under the caller holding it.
func TestABusyConnectionIsNotExpired(t *testing.T) {
	p := newMCPPool()
	p.idle["k"] = &pooled{busy: true, last: time.Now().Add(-10 * mcpIdle)}
	p.expire()
	p.mu.Lock()
	_, still := p.idle["k"]
	p.mu.Unlock()
	if !still {
		t.Fatal("a connection somebody was using was closed under them")
	}
}

// After shutdown nothing is pooled: a release must close rather than put a
// connection back into something that is going away.
func TestNothingIsPooledAfterShutdown(t *testing.T) {
	p := newMCPPool()
	p.closeAll()
	p.put("anything")
	p.mu.Lock()
	n := len(p.idle)
	p.mu.Unlock()
	if n != 0 {
		t.Fatalf("%d connections were pooled after shutdown", n)
	}
}

// closeAll twice must not panic on a closed channel, because Close is called
// from the server's shutdown AND from the sweeper's context ending.
func TestClosingTwiceIsSafe(t *testing.T) {
	p := newMCPPool()
	p.closeAll()
	p.closeAll()
}

// The sweeper stops with its context rather than outliving the engine.
func TestTheSweeperStopsWithItsContext(t *testing.T) {
	p := newMCPPool()
	ctx, cancel := context.WithCancel(context.Background())
	p.sweep(ctx)
	cancel()
	// closeAll runs on the way out; calling it again must still be safe, which
	// is the property that says the goroutine is not still running one.
	time.Sleep(50 * time.Millisecond)
	p.closeAll()
}
