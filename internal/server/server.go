// Package server is the HTTP layer: API routes, the embedded web UI, and
// request logging.
package server

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/orkcom-tech/cogitorium/internal/catalog"
	"github.com/orkcom-tech/cogitorium/internal/contextstore"
	"github.com/orkcom-tech/cogitorium/internal/engine"
	"github.com/orkcom-tech/cogitorium/internal/gear"
	"github.com/orkcom-tech/cogitorium/internal/identity"
	"github.com/orkcom-tech/cogitorium/internal/library"
	"github.com/orkcom-tech/cogitorium/internal/sandbox"
	"github.com/orkcom-tech/cogitorium/internal/version"
	"github.com/orkcom-tech/cogitorium/internal/workspace"
	"github.com/orkcom-tech/cogitorium/web"
)

type Server struct {
	db         *sql.DB
	catalog    *catalog.Store
	workspaces *workspace.Store
	context    *contextstore.Store
	gears      *gear.Store
	gearExec   *gear.Executor
	library    *library.Store
	identity   *identity.Store
	engine     *engine.Engine
	http       *http.Server
	// trustLoopback lets an unauthenticated local request act as the admin,
	// which is what makes a single-operator install feel accountless while
	// running the same model as a team install.
	trustLoopback bool
	// interactive is the sandbox backend able to host a terminal; nil means
	// no terminal is possible, and that is a refusal rather than a fallback.
	interactive     sandbox.Interactive
	terminalEnabled bool
	dataDir         string
}

func New(listen string, db *sql.DB, contextdPath, dataDir string, sb sandbox.Runner, terminal bool) *Server {
	cat := catalog.NewStore(db)
	ws := workspace.NewStore(db)
	cs := contextstore.New(contextdPath)
	gears := gear.NewStore(db)
	gearExec := gear.NewExecutor(gears, dataDir, sb)
	lib := library.NewStore(db)
	s := &Server{
		db:         db,
		catalog:    cat,
		workspaces: ws,
		context:    cs,
		gears:      gears,
		gearExec:   gearExec,
		library:    lib,
		identity:   identity.NewStore(db),
		engine:     engine.New(ws, cat, cs, gears, gearExec, lib),
		// A server reachable beyond this machine must not hand out admin
		// to anyone who can open a socket to it.
		trustLoopback:   isLoopbackListen(listen),
		terminalEnabled: terminal,
		dataDir:         dataDir,
	}
	// A terminal is only offered when the sandbox can host one: without it
	// the shell would hold the server's own file access.
	if i, ok := sb.(sandbox.Interactive); ok && sb != nil {
		s.interactive = i
	}
	if terminal && s.interactive == nil {
		slog.Warn("terminal requested but no sandbox can host one; it stays disabled")
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", s.handleHealth)

	mux.HandleFunc("GET /api/v1/providers", s.handleListProviders)
	mux.HandleFunc("POST /api/v1/providers", s.handleCreateProvider)
	mux.HandleFunc("PATCH /api/v1/providers/{id}", s.handleUpdateProvider)
	mux.HandleFunc("DELETE /api/v1/providers/{id}", s.handleDeleteProvider)
	mux.HandleFunc("POST /api/v1/providers/{id}/test", s.handleTestProvider)

	mux.HandleFunc("GET /api/v1/models", s.handleListModels)
	mux.HandleFunc("POST /api/v1/models", s.handleCreateModel)
	mux.HandleFunc("DELETE /api/v1/models/{id}", s.handleDeleteModel)

	mux.HandleFunc("POST /api/v1/chat", s.handleChat)

	mux.HandleFunc("GET /api/v1/workspaces", s.handleListWorkspaces)
	mux.HandleFunc("POST /api/v1/workspaces", s.handleCreateWorkspace)
	mux.HandleFunc("GET /api/v1/workspaces/{id}", s.handleGetWorkspace)
	mux.HandleFunc("DELETE /api/v1/workspaces/{id}", s.handleDeleteWorkspace)
	mux.HandleFunc("POST /api/v1/workspaces/{id}/clone", s.handleCloneWorkspace)
	mux.HandleFunc("PUT /api/v1/workspaces/{id}/team", s.handleSetWorkspaceTeam)
	mux.HandleFunc("GET /api/v1/workspaces/{id}/agents", s.handleListAgents)
	mux.HandleFunc("POST /api/v1/workspaces/{id}/agents", s.handleCreateAgent)
	mux.HandleFunc("PATCH /api/v1/agents/{id}", s.handleUpdateAgent)
	mux.HandleFunc("DELETE /api/v1/agents/{id}", s.handleDeleteAgent)
	mux.HandleFunc("GET /api/v1/workspaces/{id}/wires", s.handleListWires)
	mux.HandleFunc("POST /api/v1/workspaces/{id}/wires", s.handleCreateWire)
	mux.HandleFunc("DELETE /api/v1/wires/{id}", s.handleDeleteWire)
	mux.HandleFunc("GET /api/v1/workspaces/{id}/messages", s.handleListWSMessages)
	mux.HandleFunc("GET /api/v1/workspaces/{id}/status", s.handleWorkspaceStatus)
	mux.HandleFunc("POST /api/v1/workspaces/{id}/chat", s.handleWorkspaceChat)

	mux.HandleFunc("GET /api/v1/context/status", s.handleContextStatus)
	mux.HandleFunc("GET /api/v1/context/files", s.handleContextList)
	mux.HandleFunc("GET /api/v1/context/file", s.handleContextGet)
	mux.HandleFunc("PUT /api/v1/context/file", s.handleContextPut)
	mux.HandleFunc("GET /api/v1/workspaces/{id}/context", s.handleListContextBindings)
	mux.HandleFunc("POST /api/v1/workspaces/{id}/context", s.handleCreateContextBinding)
	mux.HandleFunc("DELETE /api/v1/context-bindings/{id}", s.handleDeleteContextBinding)
	mux.HandleFunc("GET /api/v1/agents/{id}/prompt", s.handleAgentPrompt)
	mux.HandleFunc("GET /api/v1/agents/{id}/memory", s.handleAgentMemory)
	mux.HandleFunc("DELETE /api/v1/messages/{id}", s.handleForgetMessage)

	mux.HandleFunc("GET /api/v1/terminal/status", s.handleTerminalStatus)
	mux.HandleFunc("GET /api/v1/terminal", s.handleTerminal)
	mux.HandleFunc("GET /api/v1/workspaces/{id}/terminal", s.handleWorkspaceTerminal)

	mux.HandleFunc("GET /api/v1/workspaces/{id}/usage", s.handleWorkspaceUsage)
	mux.HandleFunc("GET /api/v1/agents/{id}/usage", s.handleAgentUsage)

	mux.HandleFunc("GET /api/v1/workspaces/{id}/graph", s.handleWorkspaceGraph)
	mux.HandleFunc("GET /api/v1/map", s.handleAccessMap)

	mux.HandleFunc("GET /api/v1/instructions", s.handleListInstructions)
	mux.HandleFunc("POST /api/v1/instructions", s.handleSaveInstruction)
	mux.HandleFunc("GET /api/v1/instructions/{id}", s.handleGetInstruction)
	mux.HandleFunc("DELETE /api/v1/instructions/{id}", s.handleDeleteInstruction)

	mux.HandleFunc("GET /api/v1/gears", s.handleListGears)
	mux.HandleFunc("POST /api/v1/gears", s.handleCreateGear)
	mux.HandleFunc("GET /api/v1/gears/{id}", s.handleGetGear)
	mux.HandleFunc("POST /api/v1/gears/{id}/run", s.handleRunGear)
	mux.HandleFunc("GET /api/v1/gears/{id}/runs", s.handleListGearRuns)
	mux.HandleFunc("PATCH /api/v1/gears/{id}", s.handleSetGearStatus)
	mux.HandleFunc("DELETE /api/v1/gears/{id}", s.handleDeleteGear)
	mux.HandleFunc("GET /api/v1/workspaces/{id}/gears", s.handleListGearBindings)
	mux.HandleFunc("POST /api/v1/workspaces/{id}/gears", s.handleCreateGearBinding)
	mux.HandleFunc("DELETE /api/v1/gear-bindings/{id}", s.handleDeleteGearBinding)

	// Unmatched /api/* must answer JSON, not fall through to the SPA —
	// otherwise wrong-method or typo'd API calls get 200 + index.html.
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "no such API endpoint: "+r.Method+" "+r.URL.Path)
	})

	mux.HandleFunc("POST /api/v1/login", s.handleLogin)
	mux.HandleFunc("POST /api/v1/logout", s.handleLogout)
	mux.HandleFunc("GET /api/v1/whoami", s.handleWhoami)
	mux.HandleFunc("PUT /api/v1/users/{id}/password", s.handleSetPassword)
	mux.HandleFunc("GET /api/v1/users", s.handleListUsers)
	mux.HandleFunc("POST /api/v1/users", s.handleCreateUser)
	mux.HandleFunc("DELETE /api/v1/users/{id}", s.handleDeleteUser)
	mux.HandleFunc("GET /api/v1/teams", s.handleListTeams)
	mux.HandleFunc("POST /api/v1/teams", s.handleCreateTeam)
	mux.HandleFunc("DELETE /api/v1/teams/{id}", s.handleDeleteTeam)
	mux.HandleFunc("POST /api/v1/teams/{id}/members", s.handleAddTeamMember)
	mux.HandleFunc("DELETE /api/v1/teams/{id}/members/{userId}", s.handleRemoveTeamMember)

	mux.Handle("/", uiHandler())

	s.http = &http.Server{
		Addr:              listen,
		Handler:           logRequests(s.authenticate(mux)),
		ReadHeaderTimeout: 10 * time.Second,
	}
	return s
}

// isLoopbackListen reports whether the server is only reachable from this
// machine. A listener on 0.0.0.0 or a routable address is not, so it must
// demand a token from everyone.
func isLoopbackListen(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// Bootstrap seeds the admin on first start. The returned token is shown to
// the operator once and never recoverable afterwards.
func (s *Server) Bootstrap(ctx context.Context) error {
	_, token, err := s.identity.Bootstrap(ctx)
	if err != nil {
		return err
	}
	if token == "" {
		return nil
	}
	if s.trustLoopback {
		slog.Info("admin token created; local requests are admin automatically", "token", token)
	} else {
		slog.Warn("admin token created — copy it now, it cannot be shown again", "token", token)
	}
	return nil
}

// Run serves until ctx is cancelled, then shuts down gracefully. Request
// contexts derive from a server-wide context that Shutdown cancels, so
// long-lived SSE streams end promptly instead of blocking shutdown for the
// full timeout.
func (s *Server) Run(ctx context.Context) error {
	baseCtx, cancelBase := context.WithCancel(context.Background())
	defer cancelBase()
	s.http.BaseContext = func(net.Listener) context.Context { return baseCtx }
	s.http.RegisterOnShutdown(cancelBase)

	errCh := make(chan error, 1)
	go func() {
		slog.Info("http server listening", "addr", s.http.Addr, "version", version.Version)
		errCh <- s.http.ListenAndServe()
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		slog.Info("shutdown requested")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := s.http.Shutdown(shutdownCtx); err != nil {
			return err
		}
		slog.Info("http server stopped")
		return nil
	}
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	status := "ok"
	code := http.StatusOK
	if err := s.db.PingContext(r.Context()); err != nil {
		slog.Error("health: database ping failed", "err", err)
		status = "degraded"
		code = http.StatusServiceUnavailable
	}
	writeJSON(w, code, map[string]string{"status": status, "version": version.Version})
}

// uiHandler serves the embedded SPA: real files as-is, everything else falls
// back to index.html so client-side routes deep-link correctly.
func uiHandler() http.Handler {
	dist, err := fs.Sub(web.Dist, "dist")
	if err != nil {
		// Impossible with a correct embed; fail loudly, not silently.
		panic("web/dist not embedded: " + err.Error())
	}
	fileServer := http.FileServerFS(dist)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := fs.Stat(dist, "index.html"); errors.Is(err, fs.ErrNotExist) {
			http.Error(w, "web UI is not built into this binary — build with `make build`", http.StatusServiceUnavailable)
			return
		}
		path := r.URL.Path
		if path != "/" {
			// Any Stat failure (ErrNotExist, ErrInvalid for e.g. trailing
			// slashes) means "not a real file" — serve the SPA shell.
			if _, err := fs.Stat(dist, path[1:]); err != nil {
				r = r.Clone(r.Context())
				r.URL.Path = "/"
			}
		}
		fileServer.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Error("write json response", "err", err)
	}
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// Unwrap lets http.ResponseController reach the underlying writer, so
// streaming handlers can flush through this wrapper.
func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

// Hijack lets the WebSocket upgrade take over the connection. Middleware
// that wraps the writer silently breaks protocol upgrades unless it passes
// this through — gorilla asserts on http.Hijacker directly, not through
// ResponseController.
func (r *statusRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h, ok := r.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, fmt.Errorf("connection cannot be hijacked by %T", r.ResponseWriter)
	}
	return h.Hijack()
}

func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		slog.Info("http request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.status,
			"duration_ms", time.Since(start).Milliseconds(),
			"remote", r.RemoteAddr,
		)
	})
}
