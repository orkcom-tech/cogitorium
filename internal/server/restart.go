package server

import (
	"log/slog"
	"net/http"
	"os"
	"runtime"
	"time"
)

// Restarting this install, because restart-to-activate is the model.
//
// Every screen that changes the plugin set ends by saying "restart Cogitorium
// to apply", and until now there was nothing to press. On a laptop that meant
// finding the terminal it was started from; through the desktop app it meant
// quitting and reopening; and either way the last step of installing a plugin
// was a thing the product told you to do and could not do.
//
// It re-execs rather than exiting. Exiting is only a restart where something is
// watching — compose, systemd, the kubelet — and on the channel where this is
// most useful, somebody's own machine, nothing is. Replacing the process image
// works in both: a supervisor does not even see it happen, because the pid is
// the same one it started.

// handleRestart replaces this process with a fresh copy of itself.
func (s *Server) handleRestart(w http.ResponseWriter, r *http.Request) {
	caller, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	if !canRestart() {
		writeError(w, http.StatusNotImplemented,
			"this build cannot restart itself — close Cogitorium and start it again")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"restarting": true,
		"message":    "Restarting. This page will reconnect on its own.",
	})
	s.restartSoon(caller.Name)
}

// restartSoon replaces this process a moment from now.
//
// Answered first, then done: a caller whose connection is replaced by the exec
// before the response is written sees a network error and cannot tell "it is
// restarting" from "it died", which is exactly the distinction somebody who
// asked for a restart needs. So every caller writes its page or its JSON and
// then calls this.
func (s *Server) restartSoon(by string) {
	slog.Warn("restarting on request", "by", by)
	go func() {
		// Long enough for the response to be flushed and short enough that
		// nobody wonders whether the button worked. Not a guess about how long
		// the write takes — the write has already happened; this is about the
		// client reading it.
		time.Sleep(restartGrace)
		if err := reexec(); err != nil {
			// Reached only if exec failed, because a successful one never
			// returns. Worth being loud about: the operator asked for a restart
			// and is now looking at a server that did not.
			slog.Error("the restart failed and this process is still the old one", "err", err)
		}
	}()
}

const restartGrace = 250 * time.Millisecond

// canRestart reports whether replacing the process image is possible here.
//
// Windows has no exec: a process cannot become a different program in place,
// and the alternatives — spawn a child and exit, or ask a service manager —
// are each a different thing with different failure modes on a channel that
// mostly runs this as a desktop app anyway. It is refused with a sentence
// somebody can act on rather than approximated.
func canRestart() bool { return runtime.GOOS != "windows" }

// executable is os.Executable, replaceable in a test that must not exec.
var executable = os.Executable
