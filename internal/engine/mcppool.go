package engine

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/orkcom-tech/cogitorium/internal/mcpclient"
	"github.com/orkcom-tech/cogitorium/internal/mcpstore"
)

// Keeping an MCP connection between calls.
//
// # What this replaced, and why it was right until it was not
//
// Every tool call dialled afresh and closed afterwards. On stdio that is a
// process per call — `npx` resolving a package, a node runtime starting, a
// handshake — for a tool that then answers in ten milliseconds. That was a
// deliberate choice and the comment said so: a connection kept between calls is
// a process kept alive between turns, which is a lifetime question rather than
// an optimisation, and it was not worth answering while stdio was the only
// transport.
//
// Remote transports changed the arithmetic rather than the argument. A hosted
// server means a TCP connection, a TLS handshake and an `initialize` round trip
// to somebody else's host BEFORE the call that was actually wanted — three
// round trips of latency, per tool call, on every turn. An agent that calls
// four tools pays it four times.
//
// # The lifetime rules, which are the whole of the risk
//
//   - A pooled connection is a PROCESS THAT OUTLIVES THE TURN. It is capped by
//     an idle timeout, and the sweeper closes it whether or not anybody asks
//     again — otherwise a workspace that used a server once holds a node
//     process until the server restarts.
//   - A connection is keyed by server id AND by the fingerprint it was opened
//     under. A server edited since is a different server: reusing the old
//     connection would run code the operator has already unapproved, which is
//     exactly what Spawnable exists to prevent.
//   - A dead connection is never handed out. Alive() is checked on the way out,
//     because the far end can go away between calls and a caller that got a
//     corpse would fail a tool call that would have worked.
type mcpPool struct {
	mu   sync.Mutex
	idle map[string]*pooled
	// stop closes when the engine is done, so the sweeper does not outlive it.
	stop chan struct{}
	once sync.Once
}

type pooled struct {
	conn *mcpclient.Conn
	// last is when it was returned. The sweeper reads it; nothing else does.
	last time.Time
	// busy means a caller has it. A pooled connection is handed to ONE caller
	// at a time even though the protocol multiplexes: two agents sharing a
	// connection would share its session, and a server that keyed anything to
	// that session would mix them.
	busy bool
}

const (
	// mcpIdle is how long an unused connection is kept. Long enough that a
	// turn calling four tools pays one handshake, short enough that a
	// workspace which used a server once is not holding a process an hour
	// later.
	mcpIdle = 2 * time.Minute
	// mcpSweep is how often that is checked.
	mcpSweep = 30 * time.Second
)

func newMCPPool() *mcpPool {
	return &mcpPool{idle: map[string]*pooled{}, stop: make(chan struct{})}
}

// key is what makes a reuse safe. The fingerprint is in it deliberately: it
// changes the moment an operator edits the command, the URL or the names, so an
// edited server cannot be answered by a connection opened under what it used to
// be.
func poolKey(srv mcpstore.Server) string {
	return srv.Name + "\x00" + srv.Fingerprint
}

// take returns a live connection for this server, opening one if there is none.
//
// The returned release function MUST be called. It returns the connection to
// the pool, or closes it if the pool has shut down.
func (p *mcpPool) take(ctx context.Context, srv mcpstore.Server, spec mcpclient.Spec) (*mcpclient.Conn, func(), error) {
	key := poolKey(srv)

	p.mu.Lock()
	if held, ok := p.idle[key]; ok && !held.busy {
		if held.conn.Alive() {
			held.busy = true
			p.mu.Unlock()
			return held.conn, func() { p.put(key) }, nil
		}
		// The far end went away between calls. Drop it rather than hand out a
		// corpse, and let the dial below make a new one.
		delete(p.idle, key)
		go held.conn.Close()
	}
	p.mu.Unlock()

	conn, err := mcpclient.Dial(ctx, spec)
	if err != nil {
		return nil, func() {}, err
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	shuttingDown := false
	select {
	case <-p.stop:
		shuttingDown = true
	default:
	}
	// Two cases, one answer. The engine shut down while this was dialling; or a
	// second caller dialled the same server while this one waited, and the pool
	// already holds a live connection somebody is using. Either way the caller
	// gets the connection it asked for — it works, and they are waiting on it —
	// and it is CLOSED on release rather than pooled into something that is
	// going away or displacing a connection already in use.
	_, taken := p.idle[key]
	if shuttingDown || taken {
		return conn, func() { conn.Close() }, nil
	}
	p.idle[key] = &pooled{conn: conn, last: time.Now(), busy: true}
	return conn, func() { p.put(key) }, nil
}

func (p *mcpPool) put(key string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	held, ok := p.idle[key]
	if !ok {
		return
	}
	held.busy = false
	held.last = time.Now()
	select {
	case <-p.stop:
		delete(p.idle, key)
		go held.conn.Close()
	default:
	}
}

// sweep closes connections nobody has asked for. Started once, with the
// engine's lifetime.
func (p *mcpPool) sweep(ctx context.Context) {
	go func() {
		t := time.NewTicker(mcpSweep)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				p.closeAll()
				return
			case <-p.stop:
				return
			case <-t.C:
				p.expire()
			}
		}
	}()
}

func (p *mcpPool) expire() {
	now := time.Now()
	var closing []*mcpclient.Conn
	p.mu.Lock()
	for key, held := range p.idle {
		if held.busy {
			continue
		}
		// The idle window first, and Alive() only when there is something to
		// ask. A pooled entry always has a connection; asking a nil one is a
		// panic on a background goroutine, which takes the process down rather
		// than failing a request — the same reason the close loop below checks.
		if now.Sub(held.last) > mcpIdle || held.conn == nil || !held.conn.Alive() {
			delete(p.idle, key)
			closing = append(closing, held.conn)
		}
	}
	p.mu.Unlock()
	for _, c := range closing {
		if c == nil {
			// Cannot happen, and is checked anyway: this runs on a background
			// goroutine, where a nil dereference takes the whole process down
			// rather than failing one request.
			continue
		}
		// Outside the lock: closing a stdio connection waits for the child to
		// leave, and holding the pool for that would stall every other call.
		c.Close()
		slog.Debug("an idle MCP connection was closed")
	}
}

func (p *mcpPool) closeAll() {
	p.once.Do(func() { close(p.stop) })
	p.mu.Lock()
	held := make([]*pooled, 0, len(p.idle))
	for key, h := range p.idle {
		held = append(held, h)
		delete(p.idle, key)
	}
	p.mu.Unlock()
	for _, h := range held {
		if h.conn != nil {
			h.conn.Close()
		}
	}
}
