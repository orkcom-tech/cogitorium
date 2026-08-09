//go:build !windows

package gear

import (
	"os/exec"
	"syscall"
)

// A gear runs in its own process group, and the timeout kills the group.
//
// Without this, a gear that backgrounds anything is not stopped by its timeout
// and the call never returns at all. Measured: a bash gear that runs
// `sleep 300 &` against a two-second timeout left cmd.Run blocked twelve
// seconds later with two processes still alive. exec.CommandContext kills only
// the process it started, and Wait then blocks on output pipes the orphan
// inherited — so the timeout stops neither the work nor the waiting.
//
// This matters only where there is no sandbox: with Docker the container is
// the process group, and removing it takes everything inside. It is exactly
// the configuration where every other protection is already absent, which is
// the worst place to also hang the server.
func isolateProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		// The negative pid addresses the group. Fall back to the single
		// process if the group is already gone, so a race cannot leave the
		// direct child running.
		if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
			return cmd.Process.Kill()
		}
		return nil
	}
}
