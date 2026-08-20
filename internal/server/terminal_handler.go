package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"github.com/orkcom-tech/cogitorium/internal/sandbox"
	"github.com/orkcom-tech/cogitorium/internal/workdir"
)

// The terminal is on by default, and is the shell of the machine this server
// runs on, as the account it runs as — the terminal a desktop application would
// have given you. That is the same reach the operator already has by sitting at
// the machine. Where it is NOT that is one case, and onThisMachine is where it
// is decided.
//
// Two gates remain, and they are the ones that matter: `terminal: false`
// refuses it outright, for an install other people can reach, and the
// server-wide shell is an administrator's. Everything else about a terminal —
// where it starts, what it remembers — belongs to the person using it.
var upgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
	// Same-origin only: the UI is served by this server, so a request from
	// anywhere else has no business opening a shell.
	CheckOrigin: func(r *http.Request) bool {
		origin := r.Header.Get("Origin")
		if origin == "" {
			return true // non-browser client, already past bearer auth
		}
		return sameHost(origin, r.Host)
	},
}

func sameHost(origin, host string) bool {
	for _, prefix := range []string{"http://", "https://"} {
		if len(origin) > len(prefix) && origin[:len(prefix)] == prefix {
			return origin[len(prefix):] == host
		}
	}
	return false
}

// handleTerminalStatus tells the UI what it may offer: the server-wide
// shell is an administrator's, while a workspace shell belongs to whoever
// may use that workspace.
func (s *Server) handleTerminalStatus(w http.ResponseWriter, r *http.Request) {
	caller := callerFrom(r.Context())
	// Per scope, because the two can differ: a workspace terminal needs a
	// sandbox on an install other people can reach, and the server-wide one is
	// an administrator's and is always this machine.
	reason := s.terminalRefusalFor(r.Context(), true)
	globally := s.terminalRefusalFor(r.Context(), false)
	writeJSON(w, http.StatusOK, map[string]any{
		// A workspace terminal is open to whoever may use the workspace; that
		// is checked when one is opened, not here.
		"available":        reason == "",
		"global_available": globally == "" && caller.IsAdmin(),
		"reason":           reason,
		"global_reason":    globalReason(globally, caller.IsAdmin()),
		// Two answers, because the two shells can land on different machines:
		// see onThisMachine. One field would have to pick a scope and be wrong
		// on the other screen.
		"backend":        s.terminalBackend(r.Context(), true),
		"global_backend": s.terminalBackend(r.Context(), false),
	})
}

// onThisMachine decides which machine a terminal opens on.
//
// The answer people expect, and the one asked for, is THIS machine — the
// server's own shell, as the account it runs as, the way the terminal in an
// editor works. A terminal that silently lands in a container because Docker
// happens to be installed is a terminal whose `ls` shows somebody else's
// filesystem.
//
// The one exception is a workspace terminal on an install other people can
// reach. A workspace is open to its members, and a member is not the operator:
// handing them the server's own shell would hand them its database and the
// provider keys in it. So on a shared install they get the sandbox, which is
// what they had before and what a workspace terminal was always for. On a
// local install — one person, their own machine — that distinction is between
// somebody and themselves, and it buys nothing.
//
// This used to short-circuit to the host shell whenever there was no sandbox at
// all, which quietly undid the exception in exactly the deployments where it
// mattered most: an install listening on a routable address with no Docker, and
// the Kubernetes backend, which runs a gear as a Job and is NOT Interactive. On
// both, a workspace member opening a terminal was handed a shell as this
// server's user — past the approval gate that makes a gear safe to grant. That
// case is refused now rather than downgraded; see terminalRefusalFor.
func (s *Server) onThisMachine(ctx context.Context, workspaceScoped bool) bool {
	switch {
	case s.hostShell == nil:
		return false // nothing of this machine's to open
	case !workspaceScoped:
		return true // the server-wide shell is an admin's, and IS this machine
	case s.onePerson(ctx):
		return true // nobody else is here: the distinction buys nothing
	default:
		// Somebody else can be here: the sandbox, or nothing at all.
		return false
	}
}

// onePerson reports whether anybody other than the operator can be on this
// install.
//
// The listen address alone was the first answer and it is wrong in a container:
// a Docker install listens on 0.0.0.0 because it must, and that says nothing
// about who can reach the published port — the starter compose file publishes
// it on loopback. So an install with exactly ONE account is one person's too,
// whatever it binds. Add a second account and the question has a different
// answer, which is exactly when it should.
func (s *Server) onePerson(ctx context.Context) bool {
	return s.localInstall || s.identity.OnlyOne(ctx)
}

// terminalBackend names where a shell would run, because "a terminal" means
// two different things here and somebody about to type into one deserves to
// know which they have: a container they can throw away, or their computer.
func (s *Server) terminalBackend(ctx context.Context, workspaceScoped bool) string {
	if s.onThisMachine(ctx, workspaceScoped) {
		return "host"
	}
	return s.gearExec.Backend()
}

func globalReason(reason string, admin bool) string {
	if reason != "" {
		return reason
	}
	if !admin {
		return "the server-wide terminal is admin-only; open one inside a workspace instead"
	}
	return ""
}

// handleTerminal is the server-wide shell: it belongs to nobody's workspace
// and therefore to nobody but an administrator.
func (s *Server) handleTerminal(w http.ResponseWriter, r *http.Request) {
	if !s.terminalReady(w, r, false) {
		return
	}
	if _, ok := requireAdmin(w, r); !ok {
		return
	}
	s.serveTerminal(w, r, "", "cogitorium", false)
}

// handleWorkspaceTerminal is the shell for one workspace, open to whoever may
// use that workspace.
//
// With a sandbox it grants nothing new: anyone who can reach a workspace can
// already run code in that same sandbox by writing a gear and dry-running it,
// and scoping the shell to the workspace makes that capability visible and
// bounded instead of tacit. On a one-person install it is that person's own
// machine, in that workspace's directory. On a shared install with no sandbox
// it is refused — see terminalRefusalFor and onThisMachine.
func (s *Server) handleWorkspaceTerminal(w http.ResponseWriter, r *http.Request) {
	if !s.terminalReady(w, r, true) {
		return
	}
	id, ok := s.workspaceScoped(w, r)
	if !ok {
		return
	}
	ws, err := s.workspaces.GetWorkspace(r.Context(), id)
	if err != nil {
		fail(w, r, err)
		return
	}
	s.serveTerminal(w, r, s.workspaceDir(id), ws.Name, true)
}

// workspaceDir is the per-workspace scratch directory copied into the
// session, so work done in a workspace's terminal is that workspace's. The
// same directory is the Files page's, the inlet's landing ground, and what
// agents and gears now read and write — so where it is lives in workdir, and
// this is the server's spelling of it.
func (s *Server) workspaceDir(wsID int64) string {
	return workdir.Dir(s.dataDir, wsID)
}

func (s *Server) terminalReady(w http.ResponseWriter, r *http.Request, workspaceScoped bool) bool {
	if reason := s.terminalRefusalFor(r.Context(), workspaceScoped); reason != "" {
		writeError(w, http.StatusForbidden, "there is no terminal here: "+reason)
		return false
	}
	return true
}

// terminalRefusal is why there is no terminal, or "" when there is one.
//
// One sentence in one place, because three surfaces ask this question — the
// status the app reads, the panel the workspace drawer renders, and the socket
// itself — and three separate answers is how an install ends up telling
// somebody two different stories about why their shell will not open.
func (s *Server) terminalRefusal() string {
	switch {
	case !s.terminalEnabled:
		return "switched off in this server's configuration (terminal: false)"
	case s.interactive == nil && s.hostShell == nil:
		return "there is no sandbox to host a shell, and this platform has none of its own to offer"
	}
	return ""
}

// terminalRefusalFor is terminalRefusal for one scope.
//
// A workspace terminal on an install other people can reach needs a sandbox,
// and there is no sandbox on every backend: the Kubernetes one runs a gear as a
// Job and cannot attach to one, and an install with no Docker has none at all.
// Where that meets a shared install, the answer is no — not "here is the host
// shell instead", which is what it used to be and which handed a workspace
// member a shell as this server's user, past the approval gate that is the
// whole reason a gear is safe to grant.
func (s *Server) terminalRefusalFor(ctx context.Context, workspaceScoped bool) string {
	if reason := s.terminalRefusal(); reason != "" {
		return reason
	}
	if workspaceScoped && !s.onePerson(ctx) && s.interactive == nil {
		return "a workspace terminal needs a sandbox on an install other people can reach, and " +
			"this one has none — a member is not the operator, and a shell as this server's user " +
			"would be one. The server-wide terminal is still open to an administrator"
	}
	return ""
}

// serveTerminal upgrades to a WebSocket and joins it to a shell. Messages are
// text frames: keystrokes as-is, and a resize as a JSON object, which is the
// only structured message the protocol needs.
func (s *Server) serveTerminal(w http.ResponseWriter, r *http.Request, dir, label string, workspaceScoped bool) {
	caller := callerFrom(r.Context())

	rows := uint16(parseUint(r.URL.Query().Get("rows"), 24))
	cols := uint16(parseUint(r.URL.Query().Get("cols"), 80))

	// Echo back the subprotocol the browser used to carry its token, or the
	// handshake is rejected by the client.
	var header http.Header
	if p := r.Header.Get("Sec-WebSocket-Protocol"); strings.HasPrefix(p, "bearer") {
		header = http.Header{"Sec-WebSocket-Protocol": []string{"bearer"}}
	}
	conn, err := upgrader.Upgrade(w, r, header)
	if err != nil {
		slog.Warn("terminal upgrade failed", "err", err)
		return
	}
	defer conn.Close()

	// THE session for this person and this place, not A session.
	//
	// Every upgrade used to start a fresh shell and kill it on disconnect, so
	// walking to another screen and coming back lost the directory you were in
	// and everything you had run. That is not a terminal; a terminal is a place
	// you leave and come back to. So the shell outlives the connection, and
	// reattaching replays what it printed while nobody was watching.
	key := caller.Name + "\x00" + label
	onHost := s.onThisMachine(r.Context(), workspaceScoped)
	session, fresh, err := s.terminals.attach(key, func() (sandbox.Session, error) {
		// WithoutCancel in both: the request ends when this browser goes away,
		// and the shell must not end with it.
		if onHost {
			return s.hostShell.Session(context.WithoutCancel(r.Context()), rows, cols, dir)
		}
		return s.interactive.Start(context.WithoutCancel(r.Context()), sandbox.ShellSpec(rows, cols, dir, label))
	})
	if err != nil {
		slog.Error("terminal could not start", "user", caller.Name, "scope", label, "err", err)
		conn.WriteMessage(websocket.TextMessage, []byte("could not start a terminal: "+err.Error()+"\r\n"))
		return
	}
	_ = session.resize(rows, cols)
	slog.Info("terminal attached", "user", caller.Name, "scope", label,
		"backend", s.terminalBackend(r.Context(), workspaceScoped), "new", fresh, "rows", rows, "cols", cols)
	defer slog.Info("terminal detached", "user", caller.Name, "scope", label)

	out, history := session.take()
	defer session.release(out)
	if len(history) > 0 {
		// What happened while nobody was here, before anything new — so coming
		// back looks like returning to a window rather than opening one.
		if err := conn.WriteMessage(websocket.TextMessage, history); err != nil {
			return
		}
	}

	// The shell's output to this browser.
	go func() {
		for chunk := range out {
			if err := conn.WriteMessage(websocket.TextMessage, chunk); err != nil {
				return
			}
		}
		// The channel closes when the shell ends OR when another connection
		// took the session over. Only the first is worth saying out loud.
		if session.done() {
			conn.WriteMessage(websocket.TextMessage, []byte("\r\n[session ended]\r\n"))
			conn.WriteControl(websocket.CloseMessage,
				websocket.FormatCloseMessage(websocket.CloseNormalClosure, "session ended"),
				time.Now().Add(time.Second))
		}
	}()

	// The browser's keystrokes to the shell.
	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			return
		}
		if resize, ok := asResize(data); ok {
			if err := session.resize(resize.Rows, resize.Cols); err != nil {
				slog.Debug("terminal resize failed", "err", err)
			}
			continue
		}
		if err := session.write(data); err != nil {
			return
		}
	}
}

type resizeMessage struct {
	Type string `json:"type"`
	Rows uint16 `json:"rows"`
	Cols uint16 `json:"cols"`
}

// asResize recognises the one structured message the client sends. Anything
// else is keystrokes and must reach the shell untouched — including text
// that happens to look like JSON.
func asResize(data []byte) (resizeMessage, bool) {
	if len(data) == 0 || data[0] != '{' {
		return resizeMessage{}, false
	}
	var m resizeMessage
	if err := json.Unmarshal(data, &m); err != nil || m.Type != "resize" {
		return resizeMessage{}, false
	}
	return m, true
}

func parseUint(s string, fallback int) int {
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 || n > 1000 {
		return fallback
	}
	return n
}

// workspaceShellNote is the second half of the startup warning: the server-wide
// shell is always this machine, and a workspace's may not be. Saying which
// costs one line at start and saves an operator working it out from behaviour.
func workspaceShellNote(s *Server) string {
	if s.onThisMachine(context.Background(), true) {
		return "also this machine"
	}
	return "sandboxed — this install is reachable by other people"
}
