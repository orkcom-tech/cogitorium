package server

import (
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/orkcom-tech/cogitorium/internal/sandbox"
)

// What makes a terminal a terminal, tested at the layer that decides it.
//
// The shell itself has its own test in internal/sandbox. What is here is the
// promise made to the person using one: leave the screen, come back, and it is
// the same shell with the same history — not a fresh prompt in a directory you
// have to walk back to.

func TestATerminalIsStillThereWhenYouComeBack(t *testing.T) {
	t.Parallel()
	reg := newTerminals()
	t.Cleanup(func() { reg.closeAll(t.Context()) })

	fake := newFakeShell()
	starts := 0
	start := func() (sandbox.Session, error) {
		starts++
		return fake, nil
	}

	session, fresh, err := reg.attach("alice\x00cogitorium", start)
	if err != nil {
		t.Fatalf("open a terminal: %v", err)
	}
	if !fresh {
		t.Error("the first attach did not report a new session")
	}

	// Somebody types something and the shell answers.
	out, _ := session.take()
	fake.say("$ pwd\r\n/home/alice\r\n")
	if got := waitForOutput(t, out, "/home/alice"); got == "" {
		t.Fatal("what the shell printed never reached the connection")
	}

	// They walk away — another screen, a reload, a closed laptop lid.
	session.release(out)
	if fake.closed() {
		t.Fatal("walking away killed the shell: the session is gone and so is everything in it")
	}
	fake.say("background work while nobody was watching\r\n")
	// The pump is a goroutine, so what the shell said lands in the scrollback a
	// moment after it is written. Waiting for it is the test being honest about
	// its own timing; asserting immediately would make this fail at random.
	waitForScrollback(t, session, "background work while nobody was watching")

	// And come back.
	again, fresh, err := reg.attach("alice\x00cogitorium", start)
	if err != nil {
		t.Fatalf("come back to the terminal: %v", err)
	}
	if fresh {
		t.Error("coming back reported a NEW session: the old shell was abandoned")
	}
	if starts != 1 {
		t.Errorf("the shell was started %d times; coming back must not start another", starts)
	}
	if again != session {
		t.Error("coming back landed on a different session")
	}

	_, history := again.take()
	transcript := string(history)
	if !strings.Contains(transcript, "/home/alice") {
		t.Errorf("the scrollback lost what was on screen before.\nGot:\n%s", transcript)
	}
	if !strings.Contains(transcript, "background work while nobody was watching") {
		t.Errorf("what the shell printed while nobody was attached was dropped.\nGot:\n%s", transcript)
	}
}

// Two people, the same workspace, one shell each. A session carries whatever
// was typed into it, and handing Bob Alice's because they opened the same
// place would be handing him her session.
func TestTerminalsAreNotSharedBetweenPeople(t *testing.T) {
	t.Parallel()
	reg := newTerminals()
	t.Cleanup(func() { reg.closeAll(t.Context()) })

	alices, bobs := newFakeShell(), newFakeShell()
	next := []sandbox.Session{alices, bobs}
	start := func() (sandbox.Session, error) {
		s := next[0]
		next = next[1:]
		return s, nil
	}

	a, _, err := reg.attach("alice\x00shared", start)
	if err != nil {
		t.Fatal(err)
	}
	b, _, err := reg.attach("bob\x00shared", start)
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatal("two people in the same workspace were given the same shell")
	}

	outA, _ := a.take()
	defer a.release(outA)
	alices.say("alice's secret\r\n")
	if got := waitForOutput(t, outA, "alice's secret"); got == "" {
		t.Fatal("alice never saw her own shell")
	}

	outB, history := b.take()
	defer b.release(outB)
	if strings.Contains(string(history), "alice's secret") {
		t.Fatal("bob's terminal opened with alice's scrollback in it")
	}
}

// A shell that ends is over. `exit` must close the session rather than leave a
// dead one that the next visit reattaches to and finds silent.
func TestAShellThatEndsIsNotReattachedTo(t *testing.T) {
	t.Parallel()
	reg := newTerminals()
	t.Cleanup(func() { reg.closeAll(t.Context()) })

	first, second := newFakeShell(), newFakeShell()
	next := []sandbox.Session{first, second}
	start := func() (sandbox.Session, error) {
		s := next[0]
		next = next[1:]
		return s, nil
	}

	session, _, err := reg.attach("alice\x00cogitorium", start)
	if err != nil {
		t.Fatal(err)
	}
	out, _ := session.take()
	first.end() // somebody typed exit

	// The reader closes, which is how the connection learns to say so.
	select {
	case _, open := <-out:
		if open {
			// Drain whatever was in flight; the close is what matters.
			<-out
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the shell ended and the connection was never told")
	}
	if !session.done() {
		t.Error("the session did not notice its own shell ending")
	}

	fresh, isNew, err := reg.attach("alice\x00cogitorium", start)
	if err != nil {
		t.Fatal(err)
	}
	if !isNew || fresh == session {
		t.Error("opening a terminal after the shell exited reattached to the dead one")
	}
}

// A fake pty: what a shell looks like from the outside, with a hand on when it
// speaks. A real one is tested in internal/sandbox; this is here so the
// reattach rules can be tested without a process per case.
type fakeShell struct {
	mu   sync.Mutex
	buf  chan []byte
	gone bool
	rows uint16
	cols uint16
}

func newFakeShell() *fakeShell { return &fakeShell{buf: make(chan []byte, 64)} }

func (f *fakeShell) say(s string) { f.buf <- []byte(s) }

func (f *fakeShell) end() {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.gone {
		f.gone = true
		close(f.buf)
	}
}

func (f *fakeShell) closed() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.gone
}

func (f *fakeShell) Read(p []byte) (int, error) {
	chunk, open := <-f.buf
	if !open {
		return 0, io.EOF
	}
	return copy(p, chunk), nil
}

func (f *fakeShell) Write(p []byte) (int, error) {
	if f.closed() {
		return 0, errors.New("shell has ended")
	}
	return len(p), nil
}

func (f *fakeShell) Resize(rows, cols uint16) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rows, f.cols = rows, cols
	return nil
}

func (f *fakeShell) Wait() error  { return nil }
func (f *fakeShell) Close() error { f.end(); return nil }

// waitForOutput collects output until want shows up, and returns "" if it never
// does — a timeout rather than a hang, so a broken pump fails the test instead
// of stalling the package.
func waitForOutput(t *testing.T, out <-chan []byte, want string) string {
	t.Helper()
	var seen strings.Builder
	deadline := time.After(5 * time.Second)
	for {
		select {
		case chunk, open := <-out:
			if !open {
				return ""
			}
			seen.Write(chunk)
			if strings.Contains(seen.String(), want) {
				return seen.String()
			}
		case <-deadline:
			return ""
		}
	}
}

// waitForScrollback waits for what a shell printed to reach the memory that a
// reconnecting terminal is replayed from.
func waitForScrollback(t *testing.T, s *terminalSession, want string) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		s.mu.Lock()
		got := string(s.scrollback)
		s.mu.Unlock()
		if strings.Contains(got, want) || time.Now().After(deadline) {
			return got
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// Which machine a terminal opens on.
//
// The row that matters is the last one: a workspace on an install other people
// can reach. A member is not the operator, and the server's own shell would
// hand them its database and every provider key in it. Everything else lands
// on the machine, because that is what somebody who opens a terminal means.
func TestWhichMachineATerminalOpensOn(t *testing.T) {
	t.Parallel()
	host := &sandbox.Host{}
	docker := &sandbox.Docker{Image: "cogitorium-tests-never-run-this"}

	for _, c := range []struct {
		name      string
		srv       *Server
		workspace bool
		want      bool
	}{
		{"no sandbox, the server-wide shell", &Server{hostShell: host}, false, true},
		{"no sandbox, a workspace shell", &Server{hostShell: host}, true, true},
		{
			"a sandbox exists, but the server-wide shell is an admin's and is this machine",
			&Server{hostShell: host, interactive: docker}, false, true,
		},
		{
			"a sandbox exists and this install is one person's",
			&Server{hostShell: host, interactive: docker, localInstall: true}, true, true,
		},
		{
			"a sandbox exists and other people can reach this install",
			&Server{hostShell: host, interactive: docker}, true, false,
		},
		{"nothing of this machine's to open", &Server{interactive: docker}, false, false},
	} {
		if got := c.srv.onThisMachine(c.workspace); got != c.want {
			t.Errorf("%s: opened on %s, wanted %s", c.name, machine(got), machine(c.want))
		}
	}
}

func machine(onHost bool) string {
	if onHost {
		return "this machine"
	}
	return "the sandbox"
}
