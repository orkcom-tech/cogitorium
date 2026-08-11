// Package sandbox runs untrusted code — gears an agent forged, and the
// operator's terminal — away from the server's own files.
//
// The problem it exists to solve is concrete and was demonstrated, not
// theorised: a subprocess started by the server runs as the server's user,
// so file permissions do not separate them. A gear could open the server's
// SQLite database and read every provider API key in plaintext. No amount
// of restricting paths or allowed executables inside a shell fixes that —
// a restricted shell is escapable through any interpreter, and an allowlist
// is meaningless while the process holds the server's own credentials to
// the filesystem. Only the operating system can draw that line.
package sandbox

import (
	"context"
	"io"
)

// Spec describes one unit of work to run in isolation.
type Spec struct {
	// Dir is the host directory holding the code to run; it is the only
	// host path made available, and it is the process's working directory.
	Dir string
	// Command and Args are executed inside the sandbox.
	Command string
	Args    []string
	// Stdin is delivered to the process; nil means an empty stdin.
	Stdin io.Reader
	// Env is the complete environment. The server's own environment is
	// never inherited — it may hold credentials.
	Env map[string]string
	// TimeoutSeconds bounds the run; 0 means the backend's default.
	TimeoutSeconds int
	// Network says whether the process may reach the network at all.
	// Off by default, because most gears do not need it and the ones that
	// do should say so.
	Network bool
	// Writable hands the whole payload to the process to do as it likes with.
	// A terminal needs it — a shell that cannot create a file is not a shell —
	// and so does a plain gear run, which has always had it in fact if not in
	// name (see payload.go).
	//
	// False means the payload is the process's to read and run, and nothing
	// more. Only Out is writable then.
	Writable bool
	// Out names a subdirectory of Dir that stays writable when Writable is
	// false, and whose contents are copied back into Dir on the host when the
	// process finishes — so the caller reads the results at the same path the
	// process wrote them to.
	//
	// Empty means the process produces no files and nothing is copied back,
	// which is every call that existed before gears could be handed one.
	Out string
	// OnOutput, when set, is called with each chunk as it arrives rather than
	// only at the end. The buffered Result is unaffected — this is an extra
	// tap on the same stream, not a replacement — so a caller that does not
	// care can leave it nil and nothing about the run changes.
	//
	// It is called from the goroutine draining the pipe, so it must not block:
	// a slow consumer stalls the gear it is watching.
	OnOutput func(stream, chunk string)
}

type Result struct {
	Stdout   string
	Stderr   string
	ExitCode int
	TimedOut bool
}

// Runner executes a Spec to completion.
type Runner interface {
	// Name identifies the backend in logs and in the UI, so an operator can
	// tell at a glance whether their gears are actually isolated.
	Name() string
	// Isolated reports whether this backend separates the process from the
	// server's files. A backend that answers false must say so loudly.
	Isolated() bool
	Run(ctx context.Context, spec Spec) (Result, error)
}

// Session is an interactive attachment — a terminal. Writes go to the
// process's stdin, reads come from its combined output.
type Session interface {
	io.ReadWriteCloser
	// Resize tells the pseudo-terminal how big the client's view is.
	Resize(rows, cols uint16) error
	// Wait blocks until the process exits.
	Wait() error
}

// Interactive is implemented by backends that can host a terminal.
type Interactive interface {
	Runner
	Start(ctx context.Context, spec Spec) (Session, error)
}
