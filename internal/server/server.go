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
	"strconv"
	"strings"
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
	"github.com/orkcom-tech/cogitorium/internal/mcpcatalog"
	"github.com/orkcom-tech/cogitorium/internal/mcpstore"
	"github.com/orkcom-tech/cogitorium/internal/metrics"
	"github.com/orkcom-tech/cogitorium/internal/sandbox"
	"github.com/orkcom-tech/cogitorium/internal/schedule"
	"github.com/orkcom-tech/cogitorium/internal/secrets"
	"github.com/orkcom-tech/cogitorium/internal/settings"
	"github.com/orkcom-tech/cogitorium/internal/update"
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
	gearNet *gearnet.Gate
	// mcp is external MCP servers — somebody else's tools, granted to an agent.
	// Nil unless the operator switched it on, because it is the one thing this
	// product runs that it never saw the source of.
	mcp *mcpstore.Store
	// mcpLibrary reads the published MCP registry. Never nil; whether it may
	// actually fetch is decided per request by the update-check consent.
	mcpLibrary *mcpcatalog.Registry
	library    *library.Store
	identity   *identity.Store
	inlets     *inlet.Store
	engine     *engine.Engine
	// queue is where a delivery waits when its workspace is busy, and pool is
	// what runs it. queueMax bounds the waiting, because a queue with no bound
	// is a way to run a server out of disk politely.
	schedules *schedule.Store
	queue     *work.Store
	pool      *work.Pool
	stopPool  context.CancelFunc
	queueMax  int
	// callbackHosts is who this install may tell that a run finished. EMPTY
	// MEANS OFF: a callback URL arrives in a task, and a task is editable by
	// anyone who can reach the workspace, so defaulting to open would turn
	// "edit a task" into "make this server call an address of my choosing".
	callbackHosts []string
	// publicURL is how this install is reachable from outside, used to put
	// fetchable links to a run's files in its callback. Empty simply leaves
	// them out.
	publicURL string
	http      *http.Server
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

	// metrics is what an operator can alert on. Never nil; whether anything
	// scrapes it is decided by metrics_listen.
	metrics *metrics.Set

	// updates answers "is there a newer release", and holds the setting that
	// decides whether it may ask. Never nil: an install with the check off
	// still has to be able to say that it is off.
	updates *update.Checker

	// adminToken seeds the first admin instead of generating one. Empty is the
	// normal case; see config.Config.AdminToken for why it is environment-only.
	adminToken string

	// routes is the inventory every registration adds itself to; see routes.go.
	routes []Route
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
	gearExec.SetBrowserImage(cfg.BrowserImage)

	// External MCP servers, only if asked for. The engine's path stays
	// unreachable otherwise rather than merely unused.
	var mcpStore *mcpstore.Store
	if cfg.MCPClients {
		mcpStore = mcpstore.NewStore(db)
		slog.Warn("external MCP servers are ON: an approved one runs on this host as this server's user, "+
			"outside the sandbox, with this server's file access — including the database and the provider "+
			"keys in it. Every install, approval and grant is admin-only and no agent can reach any of them",
			"note", "set mcp_clients: false to switch it off")
	}
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
		mcp:        mcpStore,
		mcpLibrary: mcpcatalog.NewRegistry(),
		library:    lib,
		identity:   identity.NewStore(db),
		inlets:     inlet.NewStore(db),
		engine: engine.New(ws, cat, cs, gears, gearExec, lib, searcher, broker, queue,
			engine.Budgets{Run: cfg.BudgetRunTokens}, cfg.DataDir),
		queue:         queue,
		schedules:     schedule.NewStore(db),
		queueMax:      queueMax,
		callbackHosts: cfg.CallbackHosts,
		publicURL:     strings.TrimSuffix(cfg.PublicURL, "/"),
		// A server reachable beyond this machine must not hand out admin
		// to anyone who can open a socket to it.
		trustLoopback:   isLoopbackListen(cfg.Listen),
		terminalEnabled: cfg.Terminal,
		dataDir:         cfg.DataDir,
		searcher:        searcher,
		broker:          broker,
		egressBearer:    cfg.EgressApprovalBearer,
		adminToken:      cfg.AdminToken,
		// The contextd version is fetched at check time, not here: an install
		// with no contextd should not pay a subprocess on every boot to
		// discover that, and the check runs at most once a day.
		metrics: metrics.NewSet(version.Version),
		updates: update.New(cfg.UpdateCheck, version.Version, func(ctx context.Context) string {
			return cs.CheckStatus(ctx).Version
		}),
	}
	// The workers, started here rather than in Serve.
	//
	// A pool that only ran while the HTTP listener did would leave queued work
	// unrunnable everywhere the handlers are driven directly — every test, and
	// any embedding — and the symptom is not an error but a delivery that waits
	// forever for a worker that does not exist. The server owns the pool for as
	// long as the server exists; Close stops it.
	s.pool = work.NewPool(queue, s.runWork, work.PoolOptions{Workers: cfg.QueueWorkers})
	poolCtx, stopPool := context.WithCancel(context.Background())
	s.stopPool = stopPool
	s.pool.Start(poolCtx)

	// The clock, on the same lifetime as the workers and for the same reason:
	// a scheduler that only ran while the HTTP listener did is one no test and
	// no embedding ever sees.
	// The engine is told last, and only when the operator asked for it: nil
	// leaves the whole MCP path unreachable rather than merely unused.
	if mcpStore != nil {
		s.engine.SetMCP(mcpStore, env)
	}
	s.engine.SetMetrics(s.metrics)
	// The sweeper that closes idle MCP connections. On the pool's lifetime,
	// because a pooled connection can be a child process and one that outlived
	// the server would be a process nobody owns.
	s.engine.StartMCPPool(poolCtx)

	s.startScheduler(poolCtx)

	// The answer an operator gave last time, before the timer is started —
	// otherwise an install whose operator said "on" months ago starts under
	// "ask" and asks again, and one whose operator said "no" is asked forever.
	// Load enforces the ceiling on the way in: a stored answer can never lift
	// a configured off.
	s.updates.Load(poolCtx, settings.NewStore(db))

	// The update check, on the same lifetime and deliberately not on the
	// startup path: it returns immediately and defers its first request, so a
	// slow or unreachable GitHub is never part of this server's boot. Under
	// "ask" and "off" it schedules nothing at all.
	s.updates.Start(poolCtx)

	// The metrics listener, on the same lifetime as everything else the server
	// owns. Empty address is off, which is the default.
	s.metrics.Serve(poolCtx, cfg.MetricsListen)

	// A terminal is only offered when the sandbox can host one: without it
	// the shell would hold the server's own file access.
	if i, ok := sb.(sandbox.Interactive); ok && sb != nil {
		s.interactive = i
	}
	if cfg.Terminal && s.interactive == nil {
		slog.Warn("terminal requested but no sandbox can host one; it stays disabled")
	}

	mux := http.NewServeMux()
	s.route(mux, "GET /health", s.handleHealth)

	s.route(mux, "GET /api/v1/providers", s.handleListProviders)
	s.route(mux, "POST /api/v1/providers", s.handleCreateProvider)
	s.route(mux, "PATCH /api/v1/providers/{id}", s.handleUpdateProvider)
	s.route(mux, "DELETE /api/v1/providers/{id}", s.handleDeleteProvider)
	s.route(mux, "POST /api/v1/providers/{id}/test", s.handleTestProvider)

	s.route(mux, "GET /api/v1/models", s.handleListModels)
	s.route(mux, "POST /api/v1/models", s.handleCreateModel)
	s.route(mux, "PATCH /api/v1/models/{id}", s.handleSetModelAccepts)
	s.route(mux, "DELETE /api/v1/models/{id}", s.handleDeleteModel)

	s.route(mux, "GET /api/v1/workspaces", s.handleListWorkspaces)
	s.routeIn(mux, "POST /api/v1/workspaces", s.handleCreateWorkspace, CreateWorkspaceBody{})
	s.route(mux, "GET /api/v1/workspaces/{id}", s.handleGetWorkspace)
	s.route(mux, "PATCH /api/v1/workspaces/{id}", s.handlePatchWorkspace)
	s.route(mux, "DELETE /api/v1/workspaces/{id}", s.handleDeleteWorkspace)
	s.route(mux, "POST /api/v1/workspaces/{id}/clone", s.handleCloneWorkspace)
	s.route(mux, "GET /api/v1/workspaces/{id}/export", s.handleExportWorkspace)
	s.route(mux, "POST /api/v1/workspaces/import", s.handleImportWorkspace)
	s.route(mux, "POST /api/v1/workspaces/{id}/teams", s.handleShareWorkspace)
	s.route(mux, "DELETE /api/v1/workspaces/{id}/teams/{teamId}", s.handleUnshareWorkspace)
	s.route(mux, "GET /api/v1/workspaces/{id}/agents", s.handleListAgents)
	s.routeIn(mux, "POST /api/v1/workspaces/{id}/agents", s.handleCreateAgent, CreateAgentBody{})
	s.route(mux, "PATCH /api/v1/agents/{id}", s.handleUpdateAgent)
	s.route(mux, "DELETE /api/v1/agents/{id}", s.handleDeleteAgent)
	s.route(mux, "GET /api/v1/workspaces/{id}/wires", s.handleListWires)
	s.route(mux, "POST /api/v1/workspaces/{id}/wires", s.handleCreateWire)
	s.route(mux, "DELETE /api/v1/wires/{id}", s.handleDeleteWire)
	s.route(mux, "GET /api/v1/workspaces/{id}/messages", s.handleListWSMessages)
	s.route(mux, "GET /api/v1/workspaces/{id}/status", s.handleWorkspaceStatus)
	s.route(mux, "POST /api/v1/workspaces/{id}/chat", s.handleWorkspaceChat)
	s.route(mux, "POST /api/v1/workspaces/{id}/attachments", s.handleAttachFile)

	s.route(mux, "GET /api/v1/context/status", s.handleContextStatus)
	s.route(mux, "GET /api/v1/context/files", s.handleContextList)
	s.route(mux, "GET /api/v1/context/file", s.handleContextGet)
	s.route(mux, "PUT /api/v1/context/file", s.handleContextPut)
	s.route(mux, "GET /api/v1/context/search", s.handleContextSearch)
	s.route(mux, "DELETE /api/v1/context/file", s.handleContextDelete)
	s.route(mux, "GET /api/v1/workspaces/{id}/context", s.handleListContextBindings)
	s.route(mux, "POST /api/v1/workspaces/{id}/context", s.handleCreateContextBinding)
	s.route(mux, "DELETE /api/v1/context-bindings/{id}", s.handleDeleteContextBinding)
	s.route(mux, "GET /api/v1/agents/{id}/prompt", s.handleAgentPrompt)
	s.route(mux, "GET /api/v1/agents/{id}/memory", s.handleAgentMemory)
	s.route(mux, "DELETE /api/v1/messages/{id}", s.handleForgetMessage)

	s.route(mux, "GET /api/v1/terminal/status", s.handleTerminalStatus)
	s.route(mux, "GET /api/v1/terminal", s.handleTerminal)
	s.route(mux, "GET /api/v1/workspaces/{id}/terminal", s.handleWorkspaceTerminal)

	s.route(mux, "GET /api/v1/workspaces/{id}/files", s.handleListFiles)
	s.route(mux, "GET /api/v1/workspaces/{id}/file", s.handleReadFile)
	s.route(mux, "PUT /api/v1/workspaces/{id}/file", s.handleWriteFile)

	s.route(mux, "GET /api/v1/workspaces/{id}/usage", s.handleWorkspaceUsage)
	s.route(mux, "GET /api/v1/agents/{id}/usage", s.handleAgentUsage)

	s.route(mux, "GET /api/v1/workspaces/{id}/graph", s.handleWorkspaceGraph)
	s.route(mux, "GET /api/v1/map", s.handleAccessMap)

	// Reading is for anybody signed in — a version is a fact about the install,
	// like its health. Asking, and deciding whether this server may ask at all,
	// are an administrator's: both are outbound requests on behalf of everybody
	// on the install rather than a preference one person holds.
	s.route(mux, "GET /api/v1/updates", s.handleUpdateStatus)
	s.route(mux, "POST /api/v1/updates/check", s.handleUpdateCheckNow)
	s.routeIn(mux, "PUT /api/v1/updates/mode", s.handleSetUpdateMode, UpdateModeBody{})

	s.route(mux, "GET /api/v1/egress/status", s.handleEgressStatus)
	s.route(mux, "POST /api/v1/egress/off", s.handleEgressKill)
	s.route(mux, "GET /api/v1/workspaces/{id}/egress", s.handleListEgressGrants)
	s.route(mux, "POST /api/v1/workspaces/{id}/egress", s.handleGrantEgress)
	s.route(mux, "DELETE /api/v1/egress-grants/{id}", s.handleRevokeEgress)
	s.route(mux, "GET /api/v1/workspaces/{id}/egress/pending", s.handleEgressPending)
	s.route(mux, "POST /api/v1/egress/approvals/{token}", s.handleEgressApproval)
	s.route(mux, "GET /api/v1/workspaces/{id}/egress/log", s.handleEgressLog)

	s.route(mux, "GET /api/v1/instructions", s.handleListInstructions)
	s.route(mux, "POST /api/v1/instructions", s.handleSaveInstruction)
	s.route(mux, "GET /api/v1/instructions/{id}", s.handleGetInstruction)
	s.route(mux, "DELETE /api/v1/instructions/{id}", s.handleDeleteInstruction)

	s.route(mux, "GET /api/v1/env", s.handleListEnv)
	s.route(mux, "PUT /api/v1/env/{name}", s.handleSetEnv)
	s.route(mux, "DELETE /api/v1/env/{name}", s.handleDeleteEnv)
	s.route(mux, "GET /api/v1/workspaces/{id}/env", s.handleListWorkspaceEnv)
	s.route(mux, "PUT /api/v1/workspaces/{id}/env/{name}", s.handleSetWorkspaceEnv)
	s.route(mux, "DELETE /api/v1/workspaces/{id}/env/{name}", s.handleDeleteWorkspaceEnv)

	s.route(mux, "GET /api/v1/gears", s.handleListGears)
	s.routeIn(mux, "POST /api/v1/gears", s.handleCreateGear, CreateGearBody{})
	s.route(mux, "GET /api/v1/gears/{id}", s.handleGetGear)
	s.routeIn(mux, "POST /api/v1/gears/{id}/run", s.handleRunGear, RunGearBody{})
	// Distinct from the dry run above on purpose: /run bypasses approval and
	// /invoke enforces it. Two verbs because they are two different promises,
	// and collapsing them into one flag is how the safe one becomes optional.
	s.routeIn(mux, "POST /api/v1/gears/{id}/invoke", s.handleInvokeGear, InvokeGearBody{})
	s.route(mux, "GET /api/v1/gears/{id}/runs", s.handleListGearRuns)
	s.route(mux, "GET /api/v1/gears/{id}/connections", s.handleListGearConnections)
	s.route(mux, "GET /api/v1/gears/{id}/approvals", s.handleListGearApprovals)
	s.routeIn(mux, "PATCH /api/v1/gears/{id}", s.handleSetGearStatus, SetGearStatusBody{})
	s.route(mux, "DELETE /api/v1/gears/{id}", s.handleDeleteGear)
	s.route(mux, "GET /api/v1/workspaces/{id}/gears", s.handleListGearBindings)
	s.route(mux, "POST /api/v1/workspaces/{id}/gears", s.handleCreateGearBinding)
	s.route(mux, "DELETE /api/v1/gear-bindings/{id}", s.handleDeleteGearBinding)

	// External MCP servers. Reading is open to any authenticated caller; every
	// write is admin-only, and there is deliberately no agent-reachable path to
	// any of them.
	s.route(mux, "GET /api/v1/mcp-catalog", s.handleMCPCatalog)
	s.route(mux, "GET /api/v1/mcp-servers", s.handleListMCPServers)
	s.routeIn(mux, "POST /api/v1/mcp-servers", s.handleInstallMCPServer, CreateMCPServerBody{})
	s.routeIn(mux, "PATCH /api/v1/mcp-servers/{id}", s.handleUpdateMCPServer, UpdateMCPServerBody{})
	s.route(mux, "DELETE /api/v1/mcp-servers/{id}", s.handleDeleteMCPServer)
	s.route(mux, "POST /api/v1/mcp-servers/{id}/probe", s.handleProbeMCPServer)
	s.route(mux, "GET /api/v1/mcp-servers/{id}/tools", s.handleListMCPTools)
	s.routeIn(mux, "PATCH /api/v1/mcp-tools/{id}", s.handleApproveMCPTool, ApproveMCPToolBody{})
	s.route(mux, "GET /api/v1/workspaces/{id}/mcp-bindings", s.handleListMCPBindings)
	s.routeIn(mux, "POST /api/v1/workspaces/{id}/mcp-bindings", s.handleCreateMCPBinding, CreateMCPBindingBody{})
	s.route(mux, "DELETE /api/v1/mcp-bindings/{id}", s.handleDeleteMCPBinding)

	// Inlet MANAGEMENT. It is under /api/ on purpose: creating a door, issuing
	// its key and adding tasks to it are workspace administration, gated by the
	// same access rule as everything else in that workspace. Only delivery
	// lives outside the authentication middleware, and it lives at /i/.
	s.route(mux, "GET /api/v1/workspaces/{id}/inlets", s.handleListInlets)
	s.routeIn(mux, "POST /api/v1/workspaces/{id}/inlets", s.handleCreateInlet, CreateInletBody{})
	s.route(mux, "GET /api/v1/inlets/{id}", s.handleGetInlet)
	s.route(mux, "DELETE /api/v1/inlets/{id}", s.handleDeleteInlet)
	s.route(mux, "POST /api/v1/inlets/{id}/key", s.handleRotateInletKey)
	s.routeIn(mux, "POST /api/v1/inlets/{id}/tasks", s.handleAddInletTask, InletTaskBody{})
	// PUT, not PATCH: the body is the whole task. A merge would have to decide
	// what an absent schema means, and "accept anything" is not a thing that
	// may happen because a field was left out.
	s.routeIn(mux, "PUT /api/v1/inlet-tasks/{id}", s.handleUpdateInletTask, InletTaskBody{})
	s.route(mux, "DELETE /api/v1/inlet-tasks/{id}", s.handleDeleteInletTask)
	s.route(mux, "GET /api/v1/workspaces/{id}/inlet-runs", s.handleListInletRuns)
	s.route(mux, "GET /api/v1/inlet-runs/{id}", s.handleGetInletRun)
	s.route(mux, "GET /api/v1/workspaces/{id}/queue", s.handleWorkspaceQueue)
	s.route(mux, "GET /api/v1/workspaces/{id}/spend", s.handleWorkspaceSpend)
	s.route(mux, "GET /api/v1/workspaces/{id}/schedules", s.handleListSchedules)
	s.routeIn(mux, "POST /api/v1/workspaces/{id}/schedules", s.handleCreateSchedule, CreateScheduleBody{})
	s.route(mux, "PATCH /api/v1/schedules/{id}", s.handleSetScheduleEnabled)
	// PUT rather than another PATCH on the same path: PATCH already means
	// "turn this off", and the shortest route to that stays the shortest.
	s.routeIn(mux, "PUT /api/v1/schedules/{id}", s.handleEditSchedule, EditScheduleBody{})
	s.route(mux, "DELETE /api/v1/schedules/{id}", s.handleDeleteSchedule)
	s.route(mux, "POST /api/v1/schedules/{id}/run", s.handleRunScheduleNow)
	s.route(mux, "DELETE /api/v1/queue/{id}", s.handleCancelQueued)

	// Unmatched /api/* must answer JSON, not fall through to the SPA —
	// otherwise wrong-method or typo'd API calls get 200 + index.html.
	s.route(mux, "/api/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "no such API endpoint: "+r.Method+" "+r.URL.Path)
	})

	s.route(mux, "POST /api/v1/login", s.handleLogin)
	s.route(mux, "POST /api/v1/logout", s.handleLogout)
	s.route(mux, "GET /api/v1/whoami", s.handleWhoami)
	s.route(mux, "PUT /api/v1/users/{id}/password", s.handleSetPassword)
	s.route(mux, "GET /api/v1/users", s.handleListUsers)
	s.route(mux, "POST /api/v1/users", s.handleCreateUser)
	s.route(mux, "DELETE /api/v1/users/{id}", s.handleDeleteUser)
	s.route(mux, "GET /api/v1/teams", s.handleListTeams)
	s.route(mux, "POST /api/v1/teams", s.handleCreateTeam)
	s.route(mux, "DELETE /api/v1/teams/{id}", s.handleDeleteTeam)
	s.route(mux, "POST /api/v1/teams/{id}/members", s.handleAddTeamMember)
	s.route(mux, "DELETE /api/v1/teams/{id}/members/{userId}", s.handleRemoveTeamMember)

	// Inlet DELIVERY. The only route in this server that authenticates against
	// something other than a user token, which is why it is not under /api/ —
	// see inlet_handlers.go. Anything else under /i/ is answered with the rule
	// rather than falling through to the SPA, which would serve index.html with
	// a 200 to a pipeline that got the URL slightly wrong.
	s.route(mux, "POST /i/{address}/{task}", s.handleInletDelivery)
	s.route(mux, "GET /i/{address}/runs/{id}", s.handleInletRunStatus)
	s.route(mux, "GET /i/{address}/runs/{id}/file", s.handleInletRunFile)
	s.route(mux, inletDeliveryPrefix, handleInletDeliveryPath)

	mux.Handle("/", uiHandler())

	s.http = &http.Server{
		Addr:              cfg.Listen,
		Handler:           s.logRequests(mux, s.authenticate(mux)),
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
	// Pooled MCP connections can be child processes, and one that outlived the
	// server would be a process with nobody left to own it. The sweeper would
	// get there on its own tick; this makes shutdown deterministic, which is
	// what a test closing a server needs.
	s.engine.CloseMCP()
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

func (s *Server) logRequests(mux *http.ServeMux, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		took := time.Since(start)
		slog.Info("http request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.status,
			"duration_ms", took.Milliseconds(),
			"remote", r.RemoteAddr,
		)
		// THE TEMPLATE, NEVER THE PATH. `/api/v1/workspaces/41` as a label is
		// one time series per workspace forever, which is the textbook way to
		// make a metrics database run out of memory — and it publishes how many
		// workspaces exist and which ids are live to whoever can scrape.
		route := s.templateFor(mux, r)
		labels := map[string]string{
			"method": r.Method,
			"route":  route,
			"status": strconv.Itoa(rec.status),
		}
		s.metrics.HTTPRequests.Inc(labels)
		// Without the route, because a latency histogram per route per method
		// per status is the cardinality problem again in a shape that looks
		// reasonable.
		s.metrics.HTTPSeconds.Observe(map[string]string{"method": r.Method}, took.Seconds())
	})
}

// templateFor turns a request into the route pattern it matched, so a label is
// bounded by the number of endpoints rather than by the number of rows.
//
// THE MUX IS ASKED, rather than reading r.Pattern. The obvious version of this
// reads that field and gets an empty string every time: the mux sets it on the
// request it passes DOWN to the handler, and this middleware wraps the mux from
// the outside, so the request it holds never has it. The symptom is quiet —
// every route labelled "other", which looks like a working metric.
//
// Handler() does the routing without serving, which is exactly the question
// being asked. A path that matched nothing is bucketed rather than passed
// through: an unmatched path is usually somebody probing, and one series per
// probe is what a label must never allow.
func (s *Server) templateFor(mux *http.ServeMux, r *http.Request) string {
	_, pattern := mux.Handler(r)
	if pattern == "" {
		return "other"
	}
	if _, path, ok := strings.Cut(pattern, " "); ok {
		return path
	}
	return pattern
}
