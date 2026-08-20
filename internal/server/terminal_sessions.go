package server

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/orkcom-tech/cogitorium/internal/sandbox"
)

// A terminal that survives the tab being closed.
//
// Every WebSocket upgrade used to start a fresh shell and kill it on
// disconnect, so switching to another screen and coming back lost the
// directory you were in, the command you were halfway through typing, and
// everything you had run. That is not what a terminal is: a terminal is a
// place you leave and come back to.
//
// So a session outlives its connection. Reconnecting reattaches to the same
// shell and is sent the scrollback, which is what makes coming back look like
// nothing happened rather than like a new window.
//
// It ends when the shell does — somebody typing `exit` closes it, as it should
// — or when nobody has been attached for idleFor. A shell nobody has looked at
// in an hour is a process nobody asked to keep.
const (
	// How much output a detached session remembers. Enough to see what
	// happened while you were away; bounded, because this is memory held for a
	// terminal nobody is watching.
	scrollbackBytes = 256 << 10
	idleFor         = time.Hour
)

type terminalSession struct {
	mu sync.Mutex

	shell sandbox.Session
	// scrollback is what to send somebody who reattaches. Trimmed from the
	// front, so it is always the most recent output rather than the first.
	scrollback []byte
	// attached is the connection currently reading, or nil. One at a time: two
	// browsers sharing a shell would each see half the keystrokes.
	attached chan []byte
	lastSeen time.Time
	closed   bool
}

// terminals is every live session, by owner and scope.
//
// Keyed by the person as well as the place: a terminal carries whatever the
// person typed into it, and handing that to the next caller because they
// opened the same workspace would be handing them somebody else's session.
type terminals struct {
	mu   sync.Mutex
	live map[string]*terminalSession
}

func newTerminals() *terminals {
	t := &terminals{live: map[string]*terminalSession{}}
	go t.sweep()
	return t
}

// attach returns the session for this key, starting one if there is none.
//
// The bool says whether it is new, so the caller knows whether to send
// scrollback or a fresh prompt.
func (t *terminals) attach(key string, start func() (sandbox.Session, error)) (*terminalSession, bool, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if s, ok := t.live[key]; ok && !s.done() {
		s.mu.Lock()
		s.lastSeen = time.Now()
		s.mu.Unlock()
		return s, false, nil
	}

	shell, err := start()
	if err != nil {
		return nil, false, err
	}
	s := &terminalSession{shell: shell, lastSeen: time.Now()}
	t.live[key] = s
	go s.pump()
	return s, true, nil
}

// end closes a session and forgets it. For somebody who asked to close their
// terminal rather than merely walking away from it.
func (t *terminals) end(key string) {
	t.mu.Lock()
	s, ok := t.live[key]
	delete(t.live, key)
	t.mu.Unlock()
	if ok {
		s.close()
	}
}

// pump reads the shell forever, keeping the scrollback and forwarding to
// whoever is attached.
//
// One reader, here, rather than one per connection: two readers on a pty share
// the bytes between them, so half the output would go to a connection that had
// already gone away.
func (s *terminalSession) pump() {
	buf := make([]byte, 4096)
	for {
		n, err := s.shell.Read(buf)
		if n > 0 {
			chunk := make([]byte, n)
			copy(chunk, buf[:n])

			s.mu.Lock()
			s.scrollback = append(s.scrollback, chunk...)
			if len(s.scrollback) > scrollbackBytes {
				s.scrollback = s.scrollback[len(s.scrollback)-scrollbackBytes:]
			}
			out := s.attached
			s.mu.Unlock()

			if out != nil {
				// Never block on a connection that has stopped reading: the
				// shell must not stall because a browser was closed badly.
				select {
				case out <- chunk:
				default:
				}
			}
		}
		if err != nil {
			s.close()
			return
		}
	}
}

func (s *terminalSession) done() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

func (s *terminalSession) close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	out := s.attached
	s.attached = nil
	s.mu.Unlock()

	if out != nil {
		close(out)
	}
	_ = s.shell.Close()
}

// take makes this connection the one reading, replacing any before it, and
// returns the scrollback to send first.
func (s *terminalSession) take() (<-chan []byte, []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.attached != nil {
		close(s.attached)
	}
	ch := make(chan []byte, 64)
	s.attached = ch
	s.lastSeen = time.Now()
	history := make([]byte, len(s.scrollback))
	copy(history, s.scrollback)
	return ch, history
}

// release detaches this connection without ending the shell.
func (s *terminalSession) release(ch <-chan []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.attached != nil && (<-chan []byte)(s.attached) == ch {
		close(s.attached)
		s.attached = nil
	}
	s.lastSeen = time.Now()
}

func (s *terminalSession) write(p []byte) error {
	_, err := s.shell.Write(p)
	return err
}

func (s *terminalSession) resize(rows, cols uint16) error { return s.shell.Resize(rows, cols) }

// sweep ends sessions nobody has come back to.
func (t *terminals) sweep() {
	for range time.Tick(5 * time.Minute) {
		now := time.Now()
		t.mu.Lock()
		for key, s := range t.live {
			s.mu.Lock()
			idle := s.attached == nil && now.Sub(s.lastSeen) > idleFor
			dead := s.closed
			s.mu.Unlock()
			if idle || dead {
				delete(t.live, key)
				if !dead {
					slog.Info("terminal closed after an hour with nobody attached", "scope", key)
				}
				s.close()
			}
		}
		t.mu.Unlock()
	}
}

// closeAll ends every session. For shutdown: a shell whose server has gone is
// a process nobody can reach.
func (t *terminals) closeAll(context.Context) {
	t.mu.Lock()
	live := t.live
	t.live = map[string]*terminalSession{}
	t.mu.Unlock()
	for _, s := range live {
		s.close()
	}
}
