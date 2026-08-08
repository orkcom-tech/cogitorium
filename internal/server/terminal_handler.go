package server

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"github.com/orkcom-tech/cogitorium/internal/sandbox"
)

// The terminal is deliberately hard to switch on. It is interactive code
// execution reachable over HTTP, so it requires all three of: the operator
// turning it on in configuration, an admin caller, and a working sandbox.
// Without the sandbox it would be a shell with the server's own file
// access — which is exactly the hole that made sandboxing necessary.
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

// handleTerminalStatus tells the UI whether to offer a terminal at all, and
// why not when it does not.
func (s *Server) handleTerminalStatus(w http.ResponseWriter, r *http.Request) {
	caller := callerFrom(r.Context())
	reason := ""
	switch {
	case !s.terminalEnabled:
		reason = "disabled in configuration (set terminal: true to enable)"
	case s.interactive == nil:
		reason = "no sandbox available — a terminal would run with this server's file access, so it stays off"
	case !caller.IsAdmin():
		reason = "the terminal is admin-only"
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"available": reason == "",
		"reason":    reason,
		"backend":   s.gearExec.Backend(),
	})
}

// handleTerminal upgrades to a WebSocket and joins it to a shell running in
// the sandbox. Messages are text frames: keystrokes as-is, and a resize as
// a JSON object, which is the only structured message the protocol needs.
func (s *Server) handleTerminal(w http.ResponseWriter, r *http.Request) {
	if !s.terminalEnabled {
		writeError(w, http.StatusForbidden, "the terminal is disabled in this server's configuration")
		return
	}
	if s.interactive == nil {
		writeError(w, http.StatusForbidden,
			"no sandbox available: a terminal would run with this server's file access, so it is refused")
		return
	}
	caller, ok := requireAdmin(w, r)
	if !ok {
		return
	}

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

	session, err := s.interactive.Start(r.Context(), sandbox.ShellSpec(rows, cols))
	if err != nil {
		slog.Error("terminal could not start", "user", caller.Name, "err", err)
		conn.WriteMessage(websocket.TextMessage, []byte("could not start a terminal: "+err.Error()+"\r\n"))
		return
	}
	defer session.Close()
	_ = session.Resize(rows, cols)
	slog.Info("terminal opened", "user", caller.Name, "rows", rows, "cols", cols)
	defer slog.Info("terminal closed", "user", caller.Name)

	// Container output to the browser.
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := session.Read(buf)
			if n > 0 {
				if err := conn.WriteMessage(websocket.TextMessage, buf[:n]); err != nil {
					return
				}
			}
			if err != nil {
				conn.WriteMessage(websocket.TextMessage, []byte("\r\n[session ended]\r\n"))
				conn.WriteControl(websocket.CloseMessage,
					websocket.FormatCloseMessage(websocket.CloseNormalClosure, "session ended"),
					time.Now().Add(time.Second))
				return
			}
		}
	}()

	// Browser input to the container.
	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			return
		}
		if resize, ok := asResize(data); ok {
			if err := session.Resize(resize.Rows, resize.Cols); err != nil {
				slog.Debug("terminal resize failed", "err", err)
			}
			continue
		}
		if _, err := session.Write(data); err != nil {
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
