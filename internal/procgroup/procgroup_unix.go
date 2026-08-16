//go:build !windows

package procgroup

import (
	"os/exec"
	"syscall"
)

// Package procgroup runs a child in its own process group so a timeout kills
// everything it started, not just the process that was started.
//
// It lives here rather than in internal/gear because there are two callers now:
// a gear with no sandbox, and an external MCP server, which is a host process
// by construction. An MCP server launched through npx or uvx is a wrapper that
// execs the real thing, so killing the wrapper alone leaves the server running
// and holding the pipes.

// Isolate puts a child in its own process group, and the timeout kills the group.
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
//
// The returned function is called once the process has started; on Unix the
// group is established by the kernel at fork, so there is nothing left to do.
func Isolate(cmd *exec.Cmd) (afterStart func(), release func()) {
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
	return func() {}, func() {}
}
