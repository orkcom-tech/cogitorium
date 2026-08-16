//go:build windows

package procgroup

import (
	"log/slog"
	"os/exec"
	"unsafe"

	"golang.org/x/sys/windows"
)

// The Windows half of "a timeout must stop the whole gear, not just the
// process it started".
//
// Windows has no POSIX process group that can be signalled, so the equivalent
// is a Job Object: a kernel container that processes are assigned to, with
// JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE set so that closing the last handle to it
// terminates everything inside. A grandchild the gear spawned is in the job
// too — that is the whole point — so it dies with the job rather than
// outliving its timeout holding the output pipes open.
//
// golang.org/x/sys is already in this module's graph (modernc.org/sqlite pulls
// it in), so this costs no new dependency, only a promotion to a direct one.
//
// ## The race, stated rather than hidden
//
// The job has to be created before the process starts, but a process can only
// be ASSIGNED to a job once it exists. Between CreateProcess returning and the
// assignment landing there is a window in which the gear could spawn a
// grandchild that is not in the job. Closing it properly means starting the
// process suspended and resuming it after the assignment, and os/exec does not
// hand back the thread handle needed to resume. The window is microseconds
// wide and sits before an interpreter has even booted, so the pragmatic
// assignment is what ships — and it is written down here rather than left for
// someone to discover.
//
// ## Not verified on Windows
//
// This compiles for windows/amd64 and follows the documented Job Object
// contract, but it has not been run on a Windows machine. Everything else in
// this file's Unix twin was measured; this was not, and saying so is worth
// more than implying otherwise.
func Isolate(cmd *exec.Cmd) (afterStart func(), release func()) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		// Without a job the old behaviour is the fallback: the direct child is
		// killed and a grandchild may survive. WaitDelay still keeps the
		// server itself from hanging, which is the part that must not fail.
		slog.Warn("could not create a job object; a gear's grandchildren may outlive its timeout", "err", err)
		return func() {}, func() {}
	}

	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{
		BasicLimitInformation: windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{
			LimitFlags: windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE,
		},
	}
	if _, err := windows.SetInformationJobObject(
		job,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)),
		uint32(unsafe.Sizeof(info)),
	); err != nil {
		slog.Warn("could not configure the job object; a gear's grandchildren may outlive its timeout", "err", err)
		windows.CloseHandle(job)
		return func() {}, func() {}
	}

	// Closing the handle is what kills the job, so the timeout does exactly
	// that. release() closes it again on the normal path; CloseHandle on an
	// already-closed handle is the reason `closed` guards both.
	closed := false
	closeJob := func() {
		if !closed {
			closed = true
			windows.CloseHandle(job)
		}
	}

	cmd.Cancel = func() error {
		closeJob()
		// Belt and braces: if the assignment never landed, the direct child is
		// still ours to kill.
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		return nil
	}

	afterStart = func() {
		if cmd.Process == nil {
			return
		}
		h, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(cmd.Process.Pid))
		if err != nil {
			slog.Warn("could not open the gear process to contain it", "err", err)
			return
		}
		defer windows.CloseHandle(h)
		if err := windows.AssignProcessToJobObject(job, h); err != nil {
			slog.Warn("could not assign the gear to its job object", "err", err)
		}
	}
	return afterStart, closeJob
}
