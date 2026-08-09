//go:build windows

package gear

import "os/exec"

// Windows has no process groups in the POSIX sense; stopping a whole tree
// needs a Job Object, which is a larger piece of work than this file.
//
// What still holds here is the half that keeps the SERVER healthy: WaitDelay,
// set by the caller, makes Wait return even when a surviving grandchild is
// holding the output pipes open. So a runaway gear cannot hang the turn that
// called it — but on Windows, without a sandbox, it can outlive its timeout.
//
// That is stated rather than papered over, and it is one more reason the
// unsandboxed path is a fallback the server warns about at startup rather than
// a supported way to run.
func isolateProcess(cmd *exec.Cmd) {}
