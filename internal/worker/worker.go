// Package worker runs a plugin's backend as a supervised child process.
//
// This is the tier a Python or Node author lands on, and the honest thing to
// say about it comes first: a worker is NOT sandboxed. It runs as the server's
// OS user with the server's filesystem access. The plugins page says so in the
// same words already used for the subprocess sandbox backend, and an author who
// needs isolation has the image tier instead — cold and isolated rather than
// warm and not.
//
// It is deliberately not built on sandbox.Runner, and the codebase says why in
// its own comments: the Docker backend copies payloads in rather than
// bind-mounting, precisely so it works against a remote or rootless daemon, and
// the Kubernetes backend mounts one subPath by deliberate design. Bolting a
// runtimes mount onto both would break the first's stated rationale and weaken
// the second's isolation. A long-lived request-serving interpreter is also the
// wrong shape for a one-shot Runner.
//
// The transport is length-prefixed JSON over the child's own stdin and stdout.
// Not a socket: a socket needs a path, a path needs a directory that is
// writable and reachable on six platforms, and AF_UNIX on Windows is a
// different answer again. A pipe is the one thing every platform already
// agrees about, and one request at a time makes framing the whole protocol.
package worker

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"sync"
	"time"

	"github.com/orkcom-tech/cogitorium/internal/abi"
	"github.com/orkcom-tech/cogitorium/internal/procgroup"
)

// maxFrame bounds one message in either direction. A plugin answering with a
// gigabyte is a plugin that fills the server's memory with its answer, and a
// length prefix somebody controls is the classic way to ask for that.
const maxFrame = 32 << 20

// stderrKept is how much of a worker's stderr is remembered. The last words
// before a crash are what an operator needs; everything earlier is what the
// log already has.
const stderrKept = 8 << 10

// Spec is how to start one worker.
type Spec struct {
	Plugin string
	// Path and Args are the interpreter and what to hand it. The tier decides
	// these — a provisioned CPython, a native binary — and the worker does not
	// care which it got.
	Path string
	Args []string
	Dir  string
	// Env is the child's environment. It carries no credential: a secret
	// reaches a plugin as a stand-in the gate substitutes at the edge, which
	// is the same rule a gear already lives under.
	Env []string
	// Host answers cog.* calls this child makes mid-exchange. Nil means the
	// child may not make any — which is a refusal it can read, not a silent
	// hang.
	Host abi.Host
	// Start bounds the handshake. A worker that has not said hello by then is
	// one that never will.
	Start time.Duration
	// Call bounds one request.
	Call time.Duration
}

func (s Spec) withDefaults() Spec {
	if s.Start <= 0 {
		s.Start = 30 * time.Second
	}
	if s.Call <= 0 {
		s.Call = 60 * time.Second
	}
	return s
}

// Worker is one supervised child.
//
// One request at a time, on purpose. Concurrency inside a plugin is the
// author's business and interleaving requests down one pipe would make it the
// host's; a plugin that needs parallelism gets more workers, which is a number
// an operator can see rather than a property they cannot.
type Worker struct {
	spec Spec

	mu  sync.Mutex
	cmd *exec.Cmd
	// life is cancelled to stop the child. procgroup.Isolate installs a Cancel
	// hook that kills the whole process group, and os/exec only honours that
	// on a command made with CommandContext — so the worker owns a context
	// that lasts as long as the child, not as long as one request.
	life    context.CancelFunc
	in      io.WriteCloser
	out     *bufio.Reader
	release func()
	errs    *ring
	started bool
	// failures counts consecutive start failures, which is what the backoff
	// is computed from. Reset by a successful handshake, not by a successful
	// start: a process that starts and immediately dies has not succeeded.
	failures int
	nextTry  time.Time
}

// New prepares a worker. Nothing is started until the first call, so a plugin
// that is enabled and never used costs a row in a table and nothing else.
func New(spec Spec) *Worker {
	return &Worker{spec: spec.withDefaults(), errs: newRing(stderrKept)}
}

// Call sends one request and waits for the reply.
func (w *Worker) Call(ctx context.Context, req abi.Request) (abi.Response, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if err := w.ensure(ctx); err != nil {
		return abi.Response{}, err
	}

	req.Contract = abi.Version
	ctx, cancel := context.WithTimeout(ctx, w.spec.Call)
	defer cancel()

	resp, err := w.exchange(ctx, req)
	if err != nil {
		// A worker that failed mid-exchange is in an unknown state: it may
		// have written half a frame, and the next request would read that as
		// its own reply. It is stopped rather than reused.
		w.stopLocked()
		return abi.Response{}, w.explain(err)
	}
	if err := resp.Validate(); err != nil {
		return abi.Response{}, fmt.Errorf("plugin %q: %w", w.spec.Plugin, err)
	}
	return resp, nil
}

// ensure starts the child if it is not running, respecting the backoff.
func (w *Worker) ensure(ctx context.Context) error {
	if w.started && w.cmd != nil && w.cmd.ProcessState == nil {
		return nil
	}
	if !w.nextTry.IsZero() && time.Now().Before(w.nextTry) {
		return fmt.Errorf("plugin %q is not running and is waiting %s before trying again%s",
			w.spec.Plugin, time.Until(w.nextTry).Round(time.Second), w.tail())
	}
	if err := w.start(ctx); err != nil {
		w.failures++
		// Backoff doubles and stops doubling. A plugin that will never start
		// should not be retried every request, and should not be given up on
		// either — an operator who fixes the cause wants the next request to
		// work, not to have to restart the server.
		wait := time.Duration(1<<min(w.failures, 6)) * time.Second
		w.nextTry = time.Now().Add(wait)
		slog.Error("a plugin worker would not start",
			"plugin", w.spec.Plugin, "attempt", w.failures, "retry_in", wait, "err", err)
		return err
	}
	w.failures, w.nextTry = 0, time.Time{}
	return nil
}

func (w *Worker) start(ctx context.Context) error {
	// Deliberately NOT the caller's context: that one ends with this request,
	// and the child outlives it. Cancelling this is how the child is stopped.
	life, cancel := context.WithCancel(context.WithoutCancel(ctx))
	cmd := exec.CommandContext(life, w.spec.Path, w.spec.Args...)
	cmd.Dir = w.spec.Dir
	cmd.Env = w.spec.Env

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}

	// The whole group, not just the process. A plugin that spawns a helper and
	// then hangs would otherwise leave the helper holding the pipes open, and
	// the timeout would stop nothing.
	afterStart, release := procgroup.Isolate(cmd)

	if err := cmd.Start(); err != nil {
		release()
		cancel()
		return fmt.Errorf("plugin %q: %s would not start: %w", w.spec.Plugin, w.spec.Path, err)
	}
	afterStart()

	w.cmd, w.in, w.out, w.release, w.life = cmd, stdin, bufio.NewReaderSize(stdout, 64<<10), release, cancel
	go w.drainStderr(stderr)

	hctx, cancel := context.WithTimeout(ctx, w.spec.Start)
	defer cancel()
	if err := w.handshake(hctx); err != nil {
		w.stopLocked()
		return err
	}
	w.started = true
	return nil
}

// handshake reads the worker's hello, which states the contract it speaks.
//
// The same rule as the WebAssembly tier: a manifest can claim a contract its
// code does not speak, and the code cannot.
func (w *Worker) handshake(ctx context.Context) error {
	var hello struct {
		Contract int    `json:"contract"`
		Plugin   string `json:"plugin,omitempty"`
	}
	b, err := w.readFrame(ctx)
	if err != nil {
		return fmt.Errorf("plugin %q did not say hello: %w%s", w.spec.Plugin, err, w.tail())
	}
	if err := json.Unmarshal(b, &hello); err != nil {
		return fmt.Errorf("plugin %q said something that is not a hello: %w%s",
			w.spec.Plugin, err, w.tail())
	}
	if hello.Contract != abi.Version {
		return fmt.Errorf("plugin %q speaks contract %d and this build speaks %d",
			w.spec.Plugin, hello.Contract, abi.Version)
	}
	return nil
}

func (w *Worker) exchange(ctx context.Context, req abi.Request) (abi.Response, error) {
	b, err := json.Marshal(req)
	if err != nil {
		return abi.Response{}, err
	}
	if err := w.writeFrame(b); err != nil {
		return abi.Response{}, err
	}

	// A guest may ask the host for things before it answers, as many times as
	// it needs, so this reads frames until one of them is the response.
	//
	// Bounded, because a guest that only ever asks is a guest this loop would
	// serve forever. The ceiling is high enough that no honest plugin reaches
	// it and low enough that a runaway one is stopped while somebody can still
	// read the log.
	for i := 0; ; i++ {
		if i >= maxHostCalls {
			return abi.Response{}, fmt.Errorf("it made %d host calls without answering", i)
		}
		out, err := w.readFrame(ctx)
		if err != nil {
			return abi.Response{}, err
		}
		var frame abi.Frame
		if err := json.Unmarshal(out, &frame); err != nil {
			return abi.Response{}, fmt.Errorf("that is not a frame: %w", err)
		}
		switch {
		case frame.Response != nil:
			return *frame.Response, nil
		case frame.Host != nil:
			if w.spec.Host == nil {
				return abi.Response{}, fmt.Errorf("it called cog.%s and this worker has no host attached",
					frame.Host.Call)
			}
			// A refusal is a value the guest receives and handles, never an
			// error that ends the exchange: "you may not reach that" is an
			// ordinary answer.
			reply := w.spec.Host.Call(w.spec.Plugin, *frame.Host)
			rb, err := json.Marshal(reply)
			if err != nil {
				return abi.Response{}, err
			}
			if err := w.writeFrame(rb); err != nil {
				return abi.Response{}, err
			}
		default:
			return abi.Response{}, fmt.Errorf("it sent a frame that is neither a host call nor a response")
		}
	}
}

// maxHostCalls bounds one exchange. Not a budget an author should ever think
// about — it is a stop for a loop that has gone wrong.
const maxHostCalls = 10_000

func (w *Worker) writeFrame(b []byte) error {
	if len(b) > maxFrame {
		return fmt.Errorf("the request is %d bytes, past the %d byte frame limit", len(b), maxFrame)
	}
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], uint32(len(b)))
	if _, err := w.in.Write(hdr[:]); err != nil {
		return err
	}
	_, err := w.in.Write(b)
	return err
}

// readFrame reads one length-prefixed message, honouring the deadline.
//
// The read runs on its own goroutine because a pipe read does not take a
// context. On a timeout the worker is stopped by the caller, which is what
// releases that goroutine — it is not left blocked on a pipe nobody will write
// to again.
func (w *Worker) readFrame(ctx context.Context) ([]byte, error) {
	type result struct {
		b   []byte
		err error
	}
	done := make(chan result, 1)
	go func() {
		var hdr [4]byte
		if _, err := io.ReadFull(w.out, hdr[:]); err != nil {
			done <- result{err: err}
			return
		}
		n := binary.BigEndian.Uint32(hdr[:])
		if n > maxFrame {
			done <- result{err: fmt.Errorf("it announced a %d byte message, past the %d byte limit",
				n, maxFrame)}
			return
		}
		b := make([]byte, n)
		if _, err := io.ReadFull(w.out, b); err != nil {
			done <- result{err: err}
			return
		}
		done <- result{b: b}
	}()

	select {
	case r := <-done:
		return r.b, r.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (w *Worker) drainStderr(r io.Reader) {
	buf := make([]byte, 4<<10)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			w.errs.write(buf[:n])
		}
		if err != nil {
			return
		}
	}
}

// explain turns a transport failure into something an operator can act on.
func (w *Worker) explain(err error) error {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return fmt.Errorf("plugin %q did not answer within %s and was stopped%s",
			w.spec.Plugin, w.spec.Call, w.tail())
	case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
		// The most common real failure: the interpreter died. Its last words
		// are the whole diagnosis, and without them this is "EOF".
		return fmt.Errorf("plugin %q stopped while answering%s", w.spec.Plugin, w.tail())
	}
	return fmt.Errorf("plugin %q: %w%s", w.spec.Plugin, err, w.tail())
}

// tail is the worker's last words, ready to append to a message.
func (w *Worker) tail() string {
	s := w.errs.string()
	if s == "" {
		return ""
	}
	return ". Its last output was:\n" + s
}

// LastOutput is what the plugins page shows on a failed row.
func (w *Worker) LastOutput() string { return w.errs.string() }

// Stop ends the worker.
func (w *Worker) Stop() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.stopLocked()
}

func (w *Worker) stopLocked() {
	if w.cmd == nil {
		return
	}
	if w.in != nil {
		w.in.Close()
	}
	// Cancelling runs procgroup's hook, which kills the whole group — so a
	// helper the plugin started goes with it rather than outliving the timeout
	// holding the pipes open.
	if w.life != nil {
		w.life()
	}
	if w.release != nil {
		w.release()
	}
	_ = w.cmd.Wait()
	w.cmd, w.in, w.out, w.release, w.life, w.started = nil, nil, nil, nil, nil, false
}

// Running reports whether a child is up.
func (w *Worker) Running() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.started && w.cmd != nil
}

// ── the supervisor ────────────────────────────────────────────────────────

// Supervisor holds one worker per plugin.
type Supervisor struct {
	mu      sync.Mutex
	workers map[string]*Worker
}

func NewSupervisor() *Supervisor {
	return &Supervisor{workers: map[string]*Worker{}}
}

// Register prepares a plugin's worker. Starting is deferred to its first call.
func (s *Supervisor) Register(spec Spec) *Worker {
	s.mu.Lock()
	defer s.mu.Unlock()
	if old, ok := s.workers[spec.Plugin]; ok {
		old.Stop()
	}
	w := New(spec)
	s.workers[spec.Plugin] = w
	return w
}

// Get returns a plugin's worker.
func (s *Supervisor) Get(plugin string) (*Worker, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	w, ok := s.workers[plugin]
	return w, ok
}

// Call routes to the right worker.
func (s *Supervisor) Call(ctx context.Context, plugin string, req abi.Request) (abi.Response, error) {
	w, ok := s.Get(plugin)
	if !ok {
		return abi.Response{}, fmt.Errorf("plugin %q has no worker", plugin)
	}
	return w.Call(ctx, req)
}

// Close stops every worker. A server that exits leaving interpreters running
// has handed a machine's memory to nobody.
func (s *Supervisor) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, w := range s.workers {
		w.Stop()
	}
	s.workers = map[string]*Worker{}
}

// ── a bounded tail of stderr ──────────────────────────────────────────────

type ring struct {
	mu   sync.Mutex
	buf  []byte
	size int
}

func newRing(size int) *ring { return &ring{size: size} }

func (r *ring) write(b []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.buf = append(r.buf, b...)
	if len(r.buf) > r.size {
		r.buf = r.buf[len(r.buf)-r.size:]
	}
}

func (r *ring) string() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return string(r.buf)
}
