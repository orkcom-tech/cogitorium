package sandbox

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"runtime"
	"sync"

	"github.com/creack/pty"
)

// Host is a terminal on THIS machine, as the user this server runs as.
//
// Not a sandbox, and it does not pretend to be one: a shell here can read this
// server's files, its database and the provider keys in it. That is what makes
// it useful — it is the operator's own machine, and the operator asked for a
// terminal on it, the way a desktop application gives you one.
//
// It is a separate type from the sandbox backends for exactly that reason.
// Nothing that runs a GEAR may reach this: gears run somebody else's code and
// go through Runner, which this deliberately does not implement.
type Host struct {
	// Shell is what to run. Empty means the login shell of the account this
	// process belongs to, which is what "a terminal on this machine" means to
	// the person who asked for one.
	Shell string
}

// Session opens a shell with a pseudo-terminal attached.
//
// A real pty rather than pipes, because everything that makes a terminal a
// terminal needs one: job control, line editing, resizing, and anything
// full-screen. A shell on pipes is a shell that cannot run its own prompt
// properly, and calling that a terminal would be a lie the first time somebody
// pressed the up arrow.
//
// The context is deliberately unused. It matches the sandbox backends' shape so
// the caller can hold either, but the shell must NOT die with whatever asked
// for it: it outlives the connection that opened it, which is the whole point
// of being able to walk away from a terminal and come back to it.
func (h *Host) Session(_ context.Context, rows, cols uint16, dir string) (Session, error) {
	if !HostTerminals() {
		return nil, ErrNoHostTerminal
	}
	shell := h.Shell
	if shell == "" {
		shell = loginShell()
	}
	// -l so it is the shell the operator actually has: their profile, their
	// PATH, their prompt. A terminal that does not know about the tools on the
	// machine it is running on is a worse terminal than the one in the corner
	// of their editor.
	cmd := exec.Command(shell, "-l")
	cmd.Dir = dir
	if dir == "" {
		if home, err := os.UserHomeDir(); err == nil {
			cmd.Dir = home
		}
	}
	cmd.Env = append(os.Environ(), "TERM=xterm-256color")

	f, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: rows, Cols: cols})
	if err != nil {
		return nil, fmt.Errorf("open a terminal on this machine: %w", err)
	}
	slog.Info("host terminal opened", "shell", shell, "dir", cmd.Dir)
	return &hostSession{pty: f, cmd: cmd}, nil
}

type hostSession struct {
	pty  *os.File
	cmd  *exec.Cmd
	once sync.Once
}

func (s *hostSession) Read(p []byte) (int, error)  { return s.pty.Read(p) }
func (s *hostSession) Write(p []byte) (int, error) { return s.pty.Write(p) }

func (s *hostSession) Resize(rows, cols uint16) error {
	return pty.Setsize(s.pty, &pty.Winsize{Rows: rows, Cols: cols})
}

func (s *hostSession) Wait() error { return s.cmd.Wait() }

func (s *hostSession) Close() error {
	var err error
	s.once.Do(func() {
		// The pty first: closing it sends the shell a hangup, which is what
		// closing a terminal window does and what a shell knows how to handle.
		err = s.pty.Close()
		if s.cmd.Process != nil {
			_ = s.cmd.Process.Kill()
			_, _ = s.cmd.Process.Wait()
		}
	})
	return err
}

// loginShell is the shell of the account this process belongs to.
//
// $SHELL first, because that is the one the person chose and the one their
// profile is written for. The rest is for a process started with no
// environment — a service under systemd or launchd has none — and /bin/sh is
// the last resort, because a shell that exists everywhere beats a nicer one
// that might not.
func loginShell() string {
	if s := os.Getenv("SHELL"); s != "" {
		return s
	}
	for _, candidate := range []string{"/bin/zsh", "/bin/bash"} {
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	return "/bin/sh"
}

// ErrNoHostTerminal is returned where a host terminal was asked for on a
// platform that has none to give.
var ErrNoHostTerminal = errors.New("this platform has no terminal to open")

// HostTerminals reports whether this machine can hand out a shell.
//
// Unix can. Windows has ConPTY and this does not speak it, so a Windows
// install without Docker has no terminal — which is a thing to SAY at start
// rather than a socket that opens and immediately dies. If that changes, this
// is the one place that has to know.
func HostTerminals() bool { return runtime.GOOS != "windows" }
