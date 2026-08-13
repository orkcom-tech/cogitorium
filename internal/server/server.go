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
	"mime"
	"net"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/orkcom-tech/cogitorium/internal/catalog"
	"github.com/orkcom-tech/cogitorium/internal/config"
	"github.com/orkcom-tech/cogitorium/internal/contextstore"
	"github.com/orkcom-tech/cogitorium/internal/egress"
	"github.com/orkcom-tech/cogitorium/internal/engine"
	"github.com/orkcom-tech/cogitorium/internal/gear"
	"github.com/orkcom-tech/cogitorium/internal/gearnet"
	"github.com/orkcom-tech/cogitorium/internal/identity"
	"github.com/orkcom-tech/cogitorium/internal/inlet"
	"github.com/orkcom-tech/cogitorium/internal/library"
	"github.com/orkcom-tech/cogitorium/internal/sandbox"
	"github.com/orkcom-tech/cogitorium/internal/secrets"
	"github.com/orkcom-tech/cogitorium/internal/version"
	"github.com/orkcom-tech/cogitorium/internal/websearch"
	"github.com/orkcom-tech/cogitorium/internal/work"
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
	// env is the named values a gear may be given: the store the operator sets
	// them in, and the resolver a run reads them through.
	env *secrets.Resolver
	// gearNet is the outward gate for gears — the other half of the same
	// approval decision, and the log of what a granted gear actually reached.
	gearNet  *gearnet.Gate
	library  *library.Store
	identity *identity.Store
	inlets   *inlet.Store
	engine   *engine.Engine
	// queue is where a delivery waits when its workspace is busy, and pool is
	// what runs it. queueMax bounds the waiting, because a queue with no bound
	// is a way to run a server out of disk politely.
	queue    *work.Store
	pool     *work.Pool
	stopPool context.CancelFunc
	queueMax int
	http     *http.Server
	// trustLoopback lets an unauthenticated local request act as the admin,
	// which is what makes a single-operator install feel accountless while
	// running the same model as a team install.
	trustLoopback bool
	// interactive is the sandbox backend able to host a terminal; nil means
	// no terminal is possible, and that is a refusal rather than a fallback.
	interactive     sandbox.Interactive
	terminalEnabled bool
	dataDir         string

	// The internet gate. searcher is nil unless the master switch is on and a
	// credential was supplied; broker holds the single pending approval;
	// egressOff is a runtime kill that can only ever close.
	searcher       *websearch.Searcher
	broker         *egress.Broker
	egressOff      atomic.Bool
	egressBearer   bool
	egressKilledBy string
	egressKilledAt string

	// adminToken seeds the first admin instead of generating one. Empty is the
	// normal case; see config.Config.AdminToken for why it is environment-only.
	adminToken string
}

// New takes the whole config rather than trailing booleans. That is a
// deliberate refusal of the obvious alternative: with New(..., terminal,
// egress bool) a caller that swaps two arguments compiles cleanly, and the
// result is a security gate silently switched on.
func New(cfg config.Config, db *sql.DB, sb sandbox.Runner, searcher *websearch.Searcher, env *secrets.Resolver, gate *gearnet.Gate) *Server {
	cat := catalog.NewStore(db)
	ws := workspace.NewStore(db)
	cs := contextstore.New(cfg.ContextdPath)
	gears := gear.NewStore(db)
	gearExec := gear.NewExecutor(gears, cfg.DataDir, sb, env, gate)
	lib := library.NewStore(db)
	broker := egress.New()
	queue := work.NewStore(db)
	// Zero means unset, not "refuse everything". A Config built in code rather
	// than read from a file — which is what every test and every embedding
	// does — would otherwise turn the queue's bound into a door that is shut,
	// and the failure would read as a queue that is full while it is empty.
	queueMax := cfg.QueueMaxPerWorkspace
	if queueMax <= 0 {
		queueMax = config.Defaults().QueueMaxPerWorkspace
	}
	s := &Server{
		db:         db,
		catalog:    cat,
		workspaces: ws,
		context:    cs,
		gears:      gears,
		gearExec:   gearExec,
		env:        env,
		gearNet:    gate,
		library:    lib,
		identity:   identity.NewStore(db),
		inlets:     inlet.NewStore(db),
		engine:     engine.New(ws, cat, cs, gears, gearExec, lib, searcher, broker, queue, cfg.DataDir),
		queue:      queue,
		queueMax:   queueMax,
		// A server reachable beyond this machine must not hand out admin
		// to anyone who can open a socket to it.
		trustLoopback:   isLoopbackListen(cfg.Listen),
		terminalEnabled: cfg.Terminal,
		dataDir:         cfg.DataDir,
		searcher:        searcher,
		broker:          broker,
		egressBearer:    cfg.EgressApprovalBearer,
		adminToken:      cfg.AdminToken,
	}
	// The workers, started here rather than in Serve.
	//
	// A pool that only ran while the HTTP listener did would leave queued work
	// unrunnable everywhere the handlers are driven directly — every test, and
	// any embedding — and the symptom is not an error but a delivery that waits
	// forever for a worker that does not exist. The server owns the pool for as
	// long as the server exists; Close stops it.
	s.pool = work.NewPool(queue, s.runDelivery, work.PoolOptions{Workers: cfg.QueueWorkers})
	poolCtx, stopPool := context.WithCancel(context.Background())
	s.stopPool = stopPool
	s.pool.Start(poolCtx)

	// A terminal is only offered when the sandbox can host one: without it
	// the shell would hold the server's own file access.
	if i, ok := sb.(sandbox.Interactive); ok && sb != nil {
		s.interactive = i
	}
	if cfg.Terminal && s.interactive == nil {
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
	mux.HandleFunc("PATCH /api/v1/models/{id}", s.handleSetModelAccepts)
	mux.HandleFunc("DELETE /api/v1/models/{id}", s.handleDeleteModel)

	mux.HandleFunc("GET /api/v1/workspaces", s.handleListWorkspaces)
	mux.HandleFunc("POST /api/v1/workspaces", s.handleCreateWorkspace)
	mux.HandleFunc("GET /api/v1/workspaces/{id}", s.handleGetWorkspace)
	mux.HandleFunc("DELETE /api/v1/workspaces/{id}", s.handleDeleteWorkspace)
	mux.HandleFunc("POST /api/v1/workspaces/{id}/clone", s.handleCloneWorkspace)
	mux.HandleFunc("GET /api/v1/workspaces/{id}/export", s.handleExportWorkspace)
	mux.HandleFunc("POST /api/v1/workspaces/import", s.handleImportWorkspace)
	mux.HandleFunc("POST /api/v1/workspaces/{id}/teams", s.handleShareWorkspace)
	mux.HandleFunc("DELETE /api/v1/workspaces/{id}/teams/{teamId}", s.handleUnshareWorkspace)
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
	mux.HandleFunc("POST /api/v1/workspaces/{id}/attachments", s.handleAttachFile)

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

	mux.HandleFunc("GET /api/v1/workspaces/{id}/files", s.handleListFiles)
	mux.HandleFunc("GET /api/v1/workspaces/{id}/file", s.handleReadFile)
	mux.HandleFunc("PUT /api/v1/workspaces/{id}/file", s.handleWriteFile)

	mux.HandleFunc("GET /api/v1/workspaces/{id}/usage", s.handleWorkspaceUsage)
	mux.HandleFunc("GET /api/v1/agents/{id}/usage", s.handleAgentUsage)

	mux.HandleFunc("GET /api/v1/workspaces/{id}/graph", s.handleWorkspaceGraph)
	mux.HandleFunc("GET /api/v1/map", s.handleAccessMap)

	mux.HandleFunc("GET /api/v1/egress/status", s.handleEgressStatus)
	mux.HandleFunc("POST /api/v1/egress/off", s.handleEgressKill)
	mux.HandleFunc("GET /api/v1/workspaces/{id}/egress", s.handleListEgressGrants)
	mux.HandleFunc("POST /api/v1/workspaces/{id}/egress", s.handleGrantEgress)
	mux.HandleFunc("DELETE /api/v1/egress-grants/{id}", s.handleRevokeEgress)
	mux.HandleFunc("GET /api/v1/workspaces/{id}/egress/pending", s.handleEgressPending)
	mux.HandleFunc("POST /api/v1/egress/approvals/{token}", s.handleEgressApproval)
	mux.HandleFunc("GET /api/v1/workspaces/{id}/egress/log", s.handleEgressLog)

	mux.HandleFunc("GET /api/v1/instructions", s.handleListInstructions)
	mux.HandleFunc("POST /api/v1/instructions", s.handleSaveInstruction)
	mux.HandleFunc("GET /api/v1/instructions/{id}", s.handleGetInstruction)
	mux.HandleFunc("DELETE /api/v1/instructions/{id}", s.handleDeleteInstruction)

	mux.HandleFunc("GET /api/v1/env", s.handleListEnv)
	mux.HandleFunc("PUT /api/v1/env/{name}", s.handleSetEnv)
	mux.HandleFunc("DELETE /api/v1/env/{name}", s.handleDeleteEnv)
	mux.HandleFunc("GET /api/v1/workspaces/{id}/env", s.handleListWorkspaceEnv)
	mux.HandleFunc("PUT /api/v1/workspaces/{id}/env/{name}", s.handleSetWorkspaceEnv)
	mux.HandleFunc("DELETE /api/v1/workspaces/{id}/env/{name}", s.handleDeleteWorkspaceEnv)

	mux.HandleFunc("GET /api/v1/gears", s.handleListGears)
	mux.HandleFunc("POST /api/v1/gears", s.handleCreateGear)
	mux.HandleFunc("GET /api/v1/gears/{id}", s.handleGetGear)
	mux.HandleFunc("POST /api/v1/gears/{id}/run", s.handleRunGear)
	mux.HandleFunc("GET /api/v1/gears/{id}/runs", s.handleListGearRuns)
	mux.HandleFunc("GET /api/v1/gears/{id}/connections", s.handleListGearConnections)
	mux.HandleFunc("PATCH /api/v1/gears/{id}", s.handleSetGearStatus)
	mux.HandleFunc("DELETE /api/v1/gears/{id}", s.handleDeleteGear)
	mux.HandleFunc("GET /api/v1/workspaces/{id}/gears", s.handleListGearBindings)
	mux.HandleFunc("POST /api/v1/workspaces/{id}/gears", s.handleCreateGearBinding)
	mux.HandleFunc("DELETE /api/v1/gear-bindings/{id}", s.handleDeleteGearBinding)

	// Inlet MANAGEMENT. It is under /api/ on purpose: creating a door, issuing
	// its key and adding tasks to it are workspace administration, gated by the
	// same access rule as everything else in that workspace. Only delivery
	// lives outside the authentication middleware, and it lives at /i/.
	mux.HandleFunc("GET /api/v1/workspaces/{id}/inlets", s.handleListInlets)
	mux.HandleFunc("POST /api/v1/workspaces/{id}/inlets", s.handleCreateInlet)
	mux.HandleFunc("GET /api/v1/inlets/{id}", s.handleGetInlet)
	mux.HandleFunc("DELETE /api/v1/inlets/{id}", s.handleDeleteInlet)
	mux.HandleFunc("POST /api/v1/inlets/{id}/key", s.handleRotateInletKey)
	mux.HandleFunc("POST /api/v1/inlets/{id}/tasks", s.handleAddInletTask)
	mux.HandleFunc("DELETE /api/v1/inlet-tasks/{id}", s.handleDeleteInletTask)
	mux.HandleFunc("GET /api/v1/workspaces/{id}/inlet-runs", s.handleListInletRuns)
	mux.HandleFunc("GET /api/v1/inlet-runs/{id}", s.handleGetInletRun)
	mux.HandleFunc("GET /api/v1/workspaces/{id}/queue", s.handleWorkspaceQueue)
	mux.HandleFunc("DELETE /api/v1/queue/{id}", s.handleCancelQueued)

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

	// Inlet DELIVERY. The only route in this server that authenticates against
	// something other than a user token, which is why it is not under /api/ —
	// see inlet_handlers.go. Anything else under /i/ is answered with the rule
	// rather than falling through to the SPA, which would serve index.html with
	// a 200 to a pipeline that got the URL slightly wrong.
	mux.HandleFunc("POST /i/{address}/{task}", s.handleInletDelivery)
	mux.HandleFunc(inletDeliveryPrefix, handleInletDeliveryPath)

	mux.Handle("/", uiHandler())

	s.http = &http.Server{
		Addr:              cfg.Listen,
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
	// The runtime kill switch is handed to the engine here rather than at
	// construction, so the engine never imports the server.
	s.engine.SetEgressKill(s.egressOff.Load)

	// A pause lives in one process's memory, so nothing left awaiting can
	// ever be answered after a restart. Saying so in the row is what keeps
	// the log from implying a request is still open.
	if n, err := s.workspaces.ReconcileSearches(ctx); err != nil {
		return err
	} else if n > 0 {
		slog.Warn("search requests were awaiting approval when the server stopped; they are recorded as interrupted", "count", n)
	}

	// And the same reasoning for a gear's connections: a socket lives in one
	// process, so a row still saying open is a connection that ended when the
	// process did.
	if n, err := s.gearNet.Store().Reconcile(ctx); err != nil {
		return err
	} else if n > 0 {
		slog.Warn("gear connections were open when the server stopped; they are recorded as interrupted", "count", n)
	}

	// The same reasoning for inlet runs: a run lives in one process, so a row
	// still saying accepted or running is a job that stopped when the process
	// did. Leaving it would make the ledger claim work is in flight.
	if n, err := s.inlets.ReconcileRuns(ctx); err != nil {
		return err
	} else if n > 0 {
		slog.Warn("inlet runs were in flight when the server stopped; they are recorded as interrupted", "count", n)
	}

	_, token, err := s.identity.Bootstrap(ctx, s.adminToken)
	if err != nil {
		return err
	}
	// Empty means there is nothing to show: either the admin already existed,
	// or its token was seeded by the operator and printing it would only put a
	// credential somewhere it was not before — which on Kubernetes is the pod
	// log, readable by anyone with access to the namespace.
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
	ln, err := net.Listen("tcp", s.http.Addr)
	if err != nil {
		return err
	}
	return s.Serve(ctx, ln)
}

// Serve runs on a listener the caller already opened.
//
// This exists for the desktop shell, which has to bind port 0 and then learn
// which port the kernel gave it before it can point a window at itself. Asking
// for a fixed port instead would mean a second copy of the application, or a
// machine where somebody else already holds 8688, deciding whether the app can
// start at all.
// Close stops the workers and waits for whatever they are running.
//
// Waits rather than abandons: a unit mid-model-call has spent money and may
// have written files, and dropping it would leave a claimed row whose only
// account of itself is that the server restarted. Safe to call twice, because
// a caller that has both a Serve and a defer should not have to know which of
// them got there first.
func (s *Server) Close() {
	if s.stopPool == nil {
		return
	}
	s.stopPool()
	s.stopPool = nil
	s.pool.Stop()
}

func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	baseCtx, cancelBase := context.WithCancel(context.Background())
	defer cancelBase()
	s.http.BaseContext = func(net.Listener) context.Context { return baseCtx }
	s.http.RegisterOnShutdown(cancelBase)

	// The workers were started with the server and stop with it. They run on a
	// context of their own rather than on the request base context: a queued
	// delivery is not a request, and the one thing that must not happen to it
	// is being cancelled because the listener stopped accepting.
	defer s.Close()

	errCh := make(chan error, 1)
	go func() {
		slog.Info("http server listening", "addr", ln.Addr().String(), "version", version.Version)
		errCh <- s.http.Serve(ln)
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
	// Go's table has no entry for .webmanifest, so the file went out as
	// text/plain and a browser is entitled to ignore it. Registering it here
	// rather than special-casing the path keeps the file server the only thing
	// that decides content types.
	if err := mime.AddExtensionType(".webmanifest", "application/manifest+json"); err != nil {
		panic("registering the webmanifest media type: " + err.Error())
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
