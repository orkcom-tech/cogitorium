//go:build !windows

package server

import (
	"os"
	"syscall"
)

// reexec replaces this process with a fresh copy of the same binary, keeping
// its arguments, its environment and its pid.
//
// The pid is the part that matters beyond convenience: a supervisor watching
// this process does not observe an exit, so there is no restart policy to
// satisfy, no backoff to wait out and no restart count to climb. From
// systemd's or the kubelet's side, nothing happened.
func reexec() error {
	path, err := executable()
	if err != nil {
		return err
	}
	// Everything open is closed by the exec itself: the listener, the
	// database, the sandbox's connections. Nothing needs draining first
	// because nothing survives — which is also why this is a restart rather
	// than a reload.
	return syscall.Exec(path, os.Args, os.Environ())
}
