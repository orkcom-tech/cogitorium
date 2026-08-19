//go:build windows

package server

import "errors"

// reexec is not available here. See canRestart for why this is refused rather
// than approximated with a spawn-and-exit.
func reexec() error {
	return errors.New("this platform cannot replace a running process")
}
