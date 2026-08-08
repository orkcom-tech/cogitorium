// Package server is the HTTP layer: API routes, the embedded web UI, and
// request logging.
package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/orkcom-tech/cogitorium/internal/catalog"
	"github.com/orkcom-tech/cogitorium/internal/contextstore"
	"github.com/orkcom-tech/cogitorium/internal/engine"
	"github.com/orkcom-tech/cogitorium/internal/version"
	"github.com/orkcom-tech/cogitorium/internal/workspace"
	"github.com/orkcom-tech/cogitorium/web"
)

type Server struct {
	db         *sql.DB
	catalog    *catalog.Store
	workspaces *workspace.Store
	context    *contextstore.Store
	engine     *engine.Engine
	http       *http.Server
}

func New(listen string, db *sql.DB, contextdPath string) *Server {
	cat := catalog.NewStore(db)
	ws := workspace.NewStore(db)
	cs := contextstore.New(contextdPath)
	s := &Server{
		db:         db,
		catalog:    cat,
		workspaces: ws,
		context:    cs,
		engine:     engine.New(ws, cat, cs),
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

	// Unmatched /api/* must answer JSON, not fall through to the SPA —
	// otherwise wrong-method or typo'd API calls get 200 + index.html.
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "no such API endpoint: "+r.Method+" "+r.URL.Path)
	})

	mux.Handle("/", uiHandler())

	s.http = &http.Server{
		Addr:              listen,
		Handler:           logRequests(mux),
		ReadHeaderTimeout: 10 * time.Second,
	}
	return s
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
