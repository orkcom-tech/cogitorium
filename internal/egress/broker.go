// Package egress is the rendezvous between a turn that has paused and the
// HTTP request that answers it.
//
// It imports no database/sql and holds no store. That is not tidiness — it is
// the entire deadlock argument. SQLite here is opened with MaxOpenConns(1)
// (internal/store/store.go), so a turn that waited for a human while holding
// the single connection would make the approval endpoint unable to write, and
// the server would deadlock against itself with no way out but a restart.
// broker_nosql_test.go fails the build if anyone adds the import.
//
// The rule this package exists to enforce: the only thing that may happen
// while a database connection is checked out is a query. Waiting on a human,
// a socket or a model happens with the pool empty.
package egress

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sync"
	"time"
)

// Token identifies one pending approval. It is opaque and unguessable so that
// knowing a workspace id is not enough to answer someone else's prompt.
type Token string

func NewToken() (Token, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return Token(hex.EncodeToString(b)), nil
}

// Overlap is one run of characters the query shares with something already in
// the agent's context. It is reported as a fact with its source, never as a
// score: false positives are expected, and a fact an operator can dismiss is
// worth more than a verdict they learn to ignore.
type Overlap struct {
	Chars  int    `json:"chars"`
	Source string `json:"source"`
}

// Facts are everything the approval dialog shows that a model cannot author.
// The only model-written strings in the dialog are the query and the agent
// name, and both are constrained elsewhere.
type Facts struct {
	Runes        int       `json:"runes"`
	Bytes        int       `json:"bytes"`
	NonASCII     int       `json:"non_ascii"`
	BlobShaped   bool      `json:"blob_shaped"`
	Overlap      []Overlap `json:"overlap"`
	UsedThisTurn int       `json:"used_this_turn"`
	MaxPerTurn   int       `json:"max_per_turn"`
	Used24h      int       `json:"used_24h"`
	Max24h       int       `json:"max_24h"`
	GrantedBy    string    `json:"granted_by"`
	GrantedAt    string    `json:"granted_at"`
	PrevQueries  []string  `json:"prev_queries"`
}

// Request is one paused turn waiting to be answered.
type Request struct {
	Token            Token     `json:"token"`
	WorkspaceID      int64     `json:"workspace_id"`
	AgentID          int64     `json:"agent_id"`
	AgentName        string    `json:"agent_name"`
	AgentRoleExcerpt string    `json:"role_excerpt"`
	RoleChangedAt    string    `json:"role_changed_at,omitempty"`
	Chain            []string  `json:"chain"`
	Query            string    `json:"query"`
	Wire             string    `json:"wire"`
	Facts            Facts     `json:"facts"`
	AuditID          int64     `json:"-"`
	Opened           time.Time `json:"-"`
	Deadline         time.Time `json:"-"`
	ExpiresAt        string    `json:"expires_at"`

	answered bool // guarded by Broker.mu
	ch       chan Answer
}

// Answer is the operator's decision, carrying enough for the waiting turn to
// write the whole audit row by itself.
type Answer struct {
	Allow    bool
	UserID   int64
	UserName string
	Auth     string // "bearer" | "loopback-implicit"
	At       time.Time
}

var (
	ErrBusy          = errors.New("another agent's search is already waiting for approval")
	ErrUnknown       = errors.New("no such approval request")
	ErrAlreadyClosed = errors.New("this approval was already answered")
	ErrTimedOut      = errors.New("no operator answered in time")
)

// Broker holds at most ONE pending request for the whole process.
//
// One global slot rather than one per workspace, and that single constraint
// closes four separate problems at once. The server speaks plain HTTP/1.1, so
// a browser allows six connections per origin and every in-flight turn pins
// one for its whole duration — with one pause maximum, answering needs one
// free connection and five remain. Workspace creation is not admin-gated, so
// a per-workspace map would be an unbounded allocation an agent could drive.
// At most one modal can exist, so there is never a queue to skim. And there is
// never a batch of decisions, which is the shape that produces rubber-stamping.
//
// Raising this cap requires TLS/h2c first, so the browser can multiplex.
type Broker struct {
	mu  sync.Mutex
	cur *Request
}

func New() *Broker { return &Broker{} }

// Open claims the single slot. The caller must defer Close on every path.
func (b *Broker) Open(r *Request) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.cur != nil {
		return ErrBusy
	}
	r.ch = make(chan Answer, 1)
	r.answered = false
	b.cur = r
	return nil
}

// Close releases the slot if it still belongs to this token. Idempotent, so
// the deferred call after a normal resolution is harmless.
func (b *Broker) Close(tok Token) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.cur != nil && b.cur.Token == tok {
		b.cur = nil
	}
}

// Pending returns the request awaiting an answer, or nil. The UI polls this
// so a buffering proxy that swallows the event stream costs latency rather
// than function.
func (b *Broker) Pending() *Request {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.cur
}

// Wait blocks the turn until the operator answers, the deadline passes, or
// the request's context is cancelled — which is what happens when the browser
// tab carrying the SSE stream goes away.
//
// No *sql.Tx, no open *sql.Rows and no pooled connection may be held across
// this call. See the package comment.
func (b *Broker) Wait(ctx context.Context, r *Request) (Answer, error) {
	timer := time.NewTimer(time.Until(r.Deadline))
	defer timer.Stop()

	select {
	case a := <-r.ch:
		return a, nil
	case <-timer.C:
		// Mark it answered so a click arriving in the same instant cannot
		// deliver a decision nobody is listening for any more.
		b.mu.Lock()
		if b.cur == r {
			r.answered = true
		}
		b.mu.Unlock()
		return Answer{}, ErrTimedOut
	case <-ctx.Done():
		b.mu.Lock()
		if b.cur == r {
			r.answered = true
		}
		b.mu.Unlock()
		return Answer{}, ctx.Err()
	}
}

// Resolve delivers a decision. It never blocks: the channel is buffered and
// the answered flag makes this the only send. That matters because the caller
// is an HTTP handler goroutine that is about to want the database — if
// Resolve could block, this is precisely where the deadlock would reappear.
func (b *Broker) Resolve(tok Token, a Answer) (*Request, error) {
	b.mu.Lock()
	r := b.cur
	if r == nil || r.Token != tok {
		b.mu.Unlock()
		return nil, ErrUnknown
	}
	if r.answered {
		b.mu.Unlock()
		return nil, ErrAlreadyClosed
	}
	// Checked inside the critical section so a decision cannot be accepted
	// for a request that expired while the handler was authenticating.
	if time.Now().After(r.Deadline) {
		b.mu.Unlock()
		return nil, ErrTimedOut
	}
	r.answered = true
	b.mu.Unlock()

	r.ch <- a
	return r, nil
}
