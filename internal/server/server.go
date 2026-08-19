// Package server is the HTTP layer: API routes, the embedded web UI, and
// request logging.
package server

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
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
	"github.com/orkcom-tech/cogitorium/internal/mcpoauth"
	"github.com/orkcom-tech/cogitorium/internal/mcpstore"
	"github.com/orkcom-tech/cogitorium/internal/metrics"
	"github.com/orkcom-tech/cogitorium/internal/plugin"
	"github.com/orkcom-tech/cogitorium/internal/sandbox"
	"github.com/orkcom-tech/cogitorium/internal/schedule"
	"github.com/orkcom-tech/cogitorium/internal/secrets"
	"github.com/orkcom-tech/cogitorium/internal/settings"
	"github.com/orkcom-tech/cogitorium/internal/update"
	"github.com/orkcom-tech/cogitorium/internal/version"
	"github.com/orkcom-tech/cogitorium/internal/view"
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
	// mcpOAuth holds the grants for remote MCP servers signed in to. Never nil;
	// whether it can hold anything is decided by COGITORIUM_SECRET_KEY, because
	// a grant is the one live credential this schema stores.
	mcpOAuth *mcpoauth.Store

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
	settings  *settings.Store
	// orchestratorSecrets is what the config file said: "off" is absolute.
	orchestratorSecrets string
	queue               *work.Store
	pool                *work.Pool
	stopPool            context.CancelFunc
	queueMax            int
	// callbackHosts is who this install may tell that a run finished. EMPTY
	// MEANS OFF: a callback URL arrives in a task, and a task is editable by
	// anyone who can reach the workspace, so defaulting to open would turn
	// "edit a task" into "make this server call an address of my choosing".
	callbackHosts []string
	// backends runs plugin code. Nil when nothing enabled has any, which is
	// the ordinary case for an install whose plugins are templates.
	backends *backends
	// plugins is the composed interface: the template set every page renders
	// through, and the pages the enabled plugins declared. Nil only if
	// composition failed, which is fatal at startup rather than survivable.
	plugins *pluginRuntime
	// publicURL is how this install is reachable from outside, used to put
	// fetchable links to a run's files in its callback. Empty simply leaves
	// them out.
	publicURL string
	http      *http.Server
	// localInstall means the listener is reachable only from this machine —
	// somebody's own laptop rather than a server other people connect to.
	//
	// It no longer grants anything. It once did, as trustLoopback, and the
	// name changed with the meaning: what is left is a fact about the address
	// this server answers on, which two decisions still legitimately turn on.
	// An OAuth redirect can point at the loopback address only here, and a
	// browser is told to remember a session only here.
	localInstall bool
	// pageSpaces is every screen this server renders as a template, by its
	// first path segment. What makes a converted screen authenticated — see
	// (*Server).page for why the rule about /api/ is not enough.
	pageSpaces map[string]bool

	// The application's stylesheet links, read from the embedded index once.
	appHeadOnce sync.Once
	appHeadHTML template.HTML
	// catalogClient fetches the shared plugin catalog. Nil in production, which
	// means the default client — it is a field so a test can point the fetch
	// at a server it controls, since the catalog's URL is a compiled-in
	// constant on purpose and must stay one.
	catalogClient *http.Client
	// sandbox is the backend that starts containers, kept because the plugins
	// screen has to answer "can the image tier run here" long after New
	// returned — and the honest answer follows the LIVE backend rather than
	// the channel's name. The shipped compose image is itself a container and
	// cannot start one; a native install with Docker can.
	sandbox sandbox.Runner
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
	egressKilledBy string
	egressKilledAt string

	// metrics is what an operator can alert on. Never nil; whether anything
	// scrapes it is decided by metrics_listen.
	metrics *metrics.Set

	// updates answers "is there a newer release", and holds the setting that
	// decides whether it may ask. Never nil: an install with the check off
	// still has to be able to say that it is off.
	updates *update.Checker

	// adminSeeds are the first admin's credentials when the operator supplied
	// them instead of letting the server generate one and print it. Both empty
	// is the normal case on a laptop; see config.Config.AdminToken and
	// AdminPassword for why they are environment-only.
	adminSeeds identity.Seeds

	// routes is the inventory every registration adds itself to; see routes.go.
	routes []Route
}

// New takes the whole config rather than trailing booleans. That is a
// deliberate refusal of the obvious alternative: with New(..., terminal,
// egress bool) a caller that swaps two arguments compiles cleanly, and the
// result is a security gate silently switched on.
func New(cfg config.Config, db *sql.DB, sb sandbox.Runner, searcher *websearch.Searcher, env *secrets.Resolver, gate *gearnet.Gate) *Server {
	// The interface is composed before anything is served. A plugin that
	// cannot render is dropped by name here rather than discovered by a
	// visitor, and the product's own templates failing is a panic because
	// there would be nothing to serve — a test in internal/view catches that
	// long before a build gets this far.
	plugins, err := loadPlugins(cfg.DataDir)
	if err != nil {
		panic("cogitorium: the interface could not be composed: " + err.Error())
	}
	// The image's own plugin tree, checked as the user this process actually
	// runs as. It ran only inside `plugins seed` and its answer went nowhere,
	// so a tree that is present and unreadable — the ownership case on the
	// cluster channel — came up as an interface that quietly has nothing extra
	// in it, with nothing anywhere to read.
	if err := plugin.CheckRef(""); err != nil {
		slog.Warn("this image carries plugins that cannot be read as this user, so none of them "+
			"are installed", "err", err, "uid", os.Getuid())
	}
	// Compiled at boot rather than on the first request, so a module that will
	// not load is a line in the startup log rather than a page that fails the
	// first time somebody visits it.
	// Before the backends, because a plugin's cog.enqueue goes on this queue
	// and the gateway is built with it.
	queue := work.NewStore(db)
	pluginBackends := startBackends(context.Background(), plugins, plugins.live, cfg.DataDir,
		sb, db, cfg.Plugins, gate, queue)

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
	// Zero means unset, not "refuse everything". A Config built in code rather
	// than read from a file — which is what every test and every embedding
	// does — would otherwise turn the queue's bound into a door that is shut,
	// and the failure would read as a queue that is full while it is empty.
	queueMax := cfg.QueueMaxPerWorkspace
	if queueMax <= 0 {
		queueMax = config.Defaults().QueueMaxPerWorkspace
	}
	s := &Server{
		plugins:    plugins,
		backends:   pluginBackends,
		sandbox:    sb,
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
		// The key is rebuilt from the config rather than reached for through
		// the resolver: the resolver's job is resolving names, and reaching
		// into it for its key would make one type answer two questions.
		// A nil key is an ordinary install — see mcpoauth.Store.Available.
		mcpOAuth: mcpoauth.NewStore(db, oauthKey(cfg)),
		library:  lib,
		identity: identity.NewStore(db),
		inlets:   inlet.NewStore(db),
		engine: engine.New(ws, cat, cs, gears, gearExec, lib, searcher, broker, queue,
			engine.Budgets{Run: cfg.BudgetRunTokens}, cfg.DataDir),
		queue:               queue,
		schedules:           schedule.NewStore(db),
		settings:            settings.NewStore(db),
		orchestratorSecrets: cfg.OrchestratorSecrets,
		queueMax:            queueMax,
		callbackHosts:       cfg.CallbackHosts,
		publicURL:           strings.TrimSuffix(cfg.PublicURL, "/"),
		localInstall:        isLoopbackListen(cfg.Listen),
		terminalEnabled:     cfg.Terminal,
		dataDir:             cfg.DataDir,
		searcher:            searcher,
		broker:              broker,
		adminSeeds:          identity.Seeds{Token: cfg.AdminToken, Password: cfg.AdminPassword},
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
	// The resolver for named values, whether or not MCP was configured: the
	// orchestrator's env tools read the same one a gear's names go through.
	s.engine.SetSecrets(env)
	s.engine.SetMetrics(s.metrics)
	s.engine.SetMCPOAuth(s.mcpOAuth)
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
	s.updates.Load(poolCtx, s.settings)

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

	s.route(mux, "GET /api/v1/workspaces/{id}/metrics", s.handleWorkspaceMetrics)
	s.route(mux, "GET /api/v1/plugins", s.handleListPlugins)
	s.route(mux, "POST /api/v1/plugins", s.handleUploadPlugin)
	// Its own path space rather than under /plugins/. Go's mux refused the
	// nested form at registration — /plugins/catalog/{id} and
	// /plugins/{id}/approve both match /plugins/catalog/approve and neither is
	// more specific — which is exactly the crash that keeping every route in
	// one file exists to surface at boot rather than in production.
	s.route(mux, "GET /api/v1/plugin-catalog", s.handleBrowseCatalog)
	s.route(mux, "POST /api/v1/plugin-catalog/{id}", s.handleInstallFromCatalog)
	s.routeIn(mux, "PUT /api/v1/plugins/order", s.handleOrderPlugins, struct {
		Order []string `json:"order"`
	}{})
	// Not under /plugins/, because restarting is not a plugin operation even
	// though the plugin screen is what mostly asks for it.
	s.route(mux, "POST /api/v1/restart", s.handleRestart)
	s.route(mux, "GET /api/v1/plugins/{id}/preview", s.handlePreviewPlugin)
	s.route(mux, "POST /api/v1/plugins/{id}/approve", s.handleApprovePlugin)
	s.route(mux, "POST /api/v1/plugins/{id}/revoke", s.handleRevokePlugin)
	s.route(mux, "POST /api/v1/plugins/{id}/enable", s.handleEnablePlugin)
	s.route(mux, "POST /api/v1/plugins/{id}/disable", s.handleDisablePlugin)
	s.route(mux, "DELETE /api/v1/plugins/{id}", s.handleRemovePlugin)
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
	s.route(mux, "POST /api/v1/mcp-servers/{id}/oauth", s.handleStartMCPOAuth)
	s.route(mux, "DELETE /api/v1/mcp-servers/{id}/oauth", s.handleForgetMCPOAuth)
	// Unauthenticated of necessity: it arrives on a browser redirect from
	// somebody else's authorization server, and the `state` is what stands in
	// for a credential. See mcpoauth_handlers.go.
	s.route(mux, "GET /api/v1/mcp-oauth/callback", s.handleMCPOAuthCallback)
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

	s.route(mux, "GET /api/v1/setup", s.handleSetupState)
	s.routeIn(mux, "POST /api/v1/setup", s.handleSetup, SetupBody{})
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

	// The hypermedia layer, from this binary.
	//
	// Under /cog/ and NOT /assets/, which is where it was first put — and
	// /assets/ is where Vite writes the application's own bundle. Registering
	// a handler there shadowed the whole interface: every screen answered 404
	// for its own JavaScript, which presents as a blank page rather than as a
	// routing mistake.
	//
	// /cog/ matches the template namespace, so it reads as the host's own, and
	// nothing a plugin serves can land in it.
	mux.Handle(hostAssetPrefix, http.StripPrefix(hostAssetPrefix,
		http.FileServer(http.FS(view.Hypermedia()))))

	// The first screen of the product served as a template rather than by the
	// application. Its own paths rather than /api/v1 ones: these answer with
	// HTML, and the described API is a JSON surface — putting a page in it
	// would make every generated client expect a document.
	// A panel the server renders, swapped into a page the client still owns.
	// The seam the conversion is happening on: the workspace is the
	// application's and the drawers inside it are becoming templates, so
	// something inside a workspace is overridable before the workspace itself
	// has to be.
	s.page(mux, "GET /workspaces/{id}/drawers/{name}", s.handleWorkspaceDrawer)
	s.page(mux, "GET /workspaces/{id}/transcript", s.handleTranscript)
	s.page(mux, "POST /messages/{id}/forget", s.handleForgetMessageForm)
	s.page(mux, "POST /memory/save", s.handleSaveMemoryForm)
	s.page(mux, "POST /memory/forget", s.handleForgetMemoryForm)

	// The library screen, rendered by the system it is about. If the template
	// stack, the layer order or the approval gate were wrong, this is the
	// screen that would fail to draw — and the one an operator would be on
	// when they needed it most.
	// The lists, not the page. The access map shares this screen and stays
	// where it is — it is a drawn graph, and a template renders a thing that
	// exists at a moment rather than a layout somebody drags. So the client
	// keeps the page and the server fills the half made of words.
	s.page(mux, "GET /people/lists", s.handlePeoplePage)
	s.page(mux, "POST /people/users", s.handleCreateUserForm)
	s.page(mux, "POST /people/users/{id}/delete", s.handleDeleteUserForm)
	s.page(mux, "POST /people/teams", s.handleCreateTeamForm)
	s.page(mux, "POST /people/teams/{id}/delete", s.handleDeleteTeamForm)
	s.page(mux, "POST /people/teams/{id}/members", s.handleAddTeamMemberForm)

	s.page(mux, "GET /account", s.handleAccountPage)
	s.page(mux, "POST /account/password", s.handleAccountPasswordForm)
	s.page(mux, "POST /account/signout", s.handleAccountSignOutForm)
	s.page(mux, "GET /plugins", s.handlePluginsPage)
	s.page(mux, "POST /plugins", s.handleUploadPluginForm)
	s.page(mux, "POST /plugins/restart", s.handleRestartFromPluginsForm)
	// Its own path space rather than under /plugins/, for the same reason the
	// API's is: /plugins/catalog/{id} and /plugins/{id}/approve both match
	// /plugins/catalog/approve and neither is more specific. Go's mux refuses
	// that at registration — which is exactly the crash that keeping every
	// route in one file exists to surface at boot rather than in production.
	s.page(mux, "POST /plugin-catalog/{id}", s.handleInstallFromCatalogForm)
	s.page(mux, "POST /plugins/{id}/approve", s.handleApprovePluginForm)
	s.page(mux, "POST /plugins/{id}/revoke", s.handleRevokePluginForm)
	s.page(mux, "POST /plugins/{id}/enable", s.handleEnablePluginForm)
	s.page(mux, "POST /plugins/{id}/disable", s.handleDisablePluginForm)
	s.page(mux, "POST /plugins/{id}/up", s.handlePluginUpForm)
	s.page(mux, "POST /plugins/{id}/down", s.handlePluginDownForm)
	s.page(mux, "POST /plugins/{id}/remove", s.handleRemovePluginForm)

	s.page(mux, "GET /workspaces", s.handleWorkspacesPage)
	s.page(mux, "POST /workspaces", s.handleCreateWorkspaceForm)
	s.page(mux, "POST /workspaces/import", s.handleImportWorkspaceForm)
	s.page(mux, "POST /workspaces/{id}/clone", s.handleCloneWorkspaceForm)
	s.page(mux, "POST /workspaces/{id}/colour", s.handleColourWorkspaceForm)
	s.page(mux, "POST /workspaces/{id}/delete", s.handleDeleteWorkspaceForm)
	s.page(mux, "POST /workspaces/{id}/share", s.handleShareWorkspaceForm)
	s.page(mux, "POST /workspaces/{id}/unshare", s.handleUnshareWorkspaceForm)

	s.page(mux, "GET /gears", s.handleGearsPage)
	s.page(mux, "POST /gears", s.handleWriteGearForm)
	s.page(mux, "POST /gears/{id}/approve", s.handleApproveGearForm)
	// Open to anyone: a dry run is how somebody decides whether to ask for an
	// approval, and requiring the permission to form the opinion would leave
	// only administrators able to have one.
	s.page(mux, "POST /gears/{id}/run", s.handleRunGearForm)
	s.page(mux, "POST /gears/{id}/disable", s.handleDisableGearForm)
	s.page(mux, "POST /gears/{id}/delete", s.handleDeleteGearForm)

	s.page(mux, "GET /env", s.handleVariablesPage)
	s.page(mux, "GET /context", s.handleContextPage)
	s.page(mux, "POST /context/save", s.handleSaveContextForm)
	s.page(mux, "POST /context/delete", s.handleDeleteContextForm)

	s.page(mux, "GET /models", s.handleModelsPage)
	s.page(mux, "POST /models/providers", s.handleCreateProviderForm)
	s.page(mux, "POST /models/providers/{id}/delete", s.handleDeleteProviderForm)
	s.page(mux, "POST /models/providers/{id}/test", s.handleTestProviderForm)
	s.page(mux, "POST /models/providers/{id}/models", s.handleCreateModelForm)
	s.page(mux, "POST /models/{id}/delete", s.handleDeleteModelForm)

	s.page(mux, "GET /instructions", s.handleInstructionsPage)
	s.page(mux, "POST /instructions", s.handleCreateInstructionForm)
	s.page(mux, "POST /instructions/{id}/delete", s.handleDeleteInstructionForm)

	mux.Handle(pluginPagePrefix, s.pluginHandler())
	mux.Handle("/", s.uiHandler())

	// The bare mux, for a plugin's cog.api call.
	//
	// Below authenticate on purpose: that middleware resolves a credential
	// into a user, and a plugin has no credential to resolve — its identity is
	// put on the context directly, which is the one thing a network request
	// can never do. Going through authenticate would mean either minting a
	// token for a call that never leaves this process, or teaching the
	// middleware a second way in.
	if pluginBackends != nil {
		pluginBackends.attachAPI(mux)
	}

	s.http = &http.Server{
		Addr:              cfg.Listen,
		Handler:           s.logRequests(mux, s.authenticate(mux)),
		ReadHeaderTimeout: 10 * time.Second,
	}
	return s
}

// isLoopbackListen reports whether the server is only reachable from this
// machine. A listener on 0.0.0.0 or a routable address is not.
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
	// The orchestrator's clocks. Without this it can build the agent that
	// would run nightly and then has to tell somebody to go and set the timer
	// themselves, which is not what an orchestrator is for.
	s.engine.SetSchedules(s.schedules)
	// Whether the orchestrator may read and write named values. On unless an
	// operator says otherwise: an orchestrator that cannot configure what it
	// builds hands the job back. Read per turn, so switching it off takes
	// effect on the next thing it does.
	s.engine.SetSecretsAccess(func(ctx context.Context) bool {
		// The file first and absolutely: an operator who wrote "off" on the
		// server's own disk has decided, and a row in the database must not be
		// able to lift it. That is the same rule update_check follows, and for
		// the same reason — a decision made on disk is not a suggestion.
		if strings.EqualFold(strings.TrimSpace(s.orchestratorSecrets), "off") {
			return false
		}
		v, err := s.settings.Get(ctx, settings.OrchestratorSecrets)
		if err != nil {
			// A database that cannot answer is not permission to hand out
			// credentials.
			return false
		}
		return v != "off"
	})

	// Before anything else asks for context: an installed contextd with no
	// space is a product where memory silently does nothing, and until now only
	// the container image did anything about it. See EnsureSpace.
	s.context.EnsureSpace(ctx)

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

	_, token, err := s.identity.Bootstrap(ctx, s.adminSeeds)
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
	slog.Warn("admin token created — copy it now, it cannot be shown again", "token", token)
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

// clientRoutes is every path the single-page application answers.
//
// Declared, not guessed. This server knows exactly which screens exist, so a
// path that is not one of them is a mistake and must be answered as one — the
// alternative is a 200 and an HTML document for anything anybody types, which
// makes a typo look like a working page and a missing asset look like a
// corrupt bundle.
//
// One segment of ":" matches any single segment. Kept in step with the Route
// elements in web/src/App.tsx by a test that fails when the two disagree, and
// it shrinks as pages are converted to templates.
var clientRoutes = []string{
	"/",
	"/workspaces",
	"/workspaces/:",
	"/map",
	"/people",
	"/terminal",
	"/context",
	"/gears",
	"/env",
	"/instructions",
	"/models",
	"/plugins",
}

// servesTheApp reports whether a path is one of the application's own screens.
func servesTheApp(path string) bool {
	got := splitPath(path)
	for _, pattern := range clientRoutes {
		if matchRoute(splitPath(pattern), got) {
			return true
		}
	}
	return false
}

func splitPath(p string) []string {
	p = strings.Trim(p, "/")
	if p == "" {
		return nil
	}
	return strings.Split(p, "/")
}

func matchRoute(pattern, got []string) bool {
	if len(pattern) != len(got) {
		return false
	}
	for i := range pattern {
		if pattern[i] == ":" {
			if got[i] == "" {
				return false
			}
			continue
		}
		if pattern[i] != got[i] {
			return false
		}
	}
	return true
}

// uiHandler serves the embedded SPA: real files as-is, everything else falls
// back to index.html so client-side routes deep-link correctly.
func (s *Server) uiHandler() http.Handler {
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
				if !servesTheApp(path) {
					http.NotFound(w, r)
					return
				}
				r = r.Clone(r.Context())
				r.URL.Path = "/"
			}
		}
		// The index carries what the plugins contribute, so the rail is not a
		// destination that briefly has fewer entries than it will have a
		// moment later. Everything else is served as-is.
		if r.URL.Path == "/" {
			if b, err := s.indexWithPlugins(dist); err == nil {
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				w.Header().Set("Cache-Control", "no-store")
				_, _ = w.Write(b)
				return
			}
		}
		fileServer.ServeHTTP(w, r)
	})
}

// appHead is the application's own stylesheet links, for a page the server
// renders itself.
//
// A converted screen builds its document from the template stack rather than
// from index.html, so nothing carries the application's CSS into it unless
// something does it deliberately — and for a while nothing did: every
// converted screen went out as a correct document with no styling at all,
// which reads as a broken product rather than as a missing link tag.
//
// Links only, never the scripts. The application's module in a server-rendered
// page would boot the single-page app on top of the page it is inside.
//
// Computed once and cached, because it is the same answer for the life of the
// process: the file is embedded in the binary.
func (s *Server) appHead() template.HTML {
	s.appHeadOnce.Do(func() {
		dist, err := fs.Sub(web.Dist, "dist")
		if err != nil {
			return
		}
		raw, err := fs.ReadFile(dist, "index.html")
		if err != nil {
			return
		}
		var b strings.Builder
		for _, m := range stylesheetLink.FindAll(raw, -1) {
			// Without the crossorigin attribute the build emits. It makes the
			// stylesheet a CORS request, which is fine from this origin and
			// fails from the preview frame: that frame is sandboxed, so its
			// origin is opaque, it sends Origin: null, and a file server that
			// answers no CORS header leaves the preview unstyled — which is
			// the one thing a preview must not be.
			b.Write(crossorigin.ReplaceAll(m, nil))
		}
		s.appHeadHTML = template.HTML(b.String())

	})
	return s.appHeadHTML
}

// stylesheetLink matches a link element that carries a stylesheet.
//
// A regexp over the built index rather than a parse, and deliberately narrow:
// it matches the one shape Vite emits, and matching nothing is a page with no
// CSS — visibly wrong, and caught by the test that reads the served document
// for it.
var stylesheetLink = regexp.MustCompile(`(?i)<link[^>]+rel="stylesheet"[^>]*>`)

// crossorigin is that attribute, with the space before it.
var crossorigin = regexp.MustCompile(`(?i)\s+crossorigin(="[^"]*")?`)

// contributesNothing is the early out for a document that needs no splicing.
//
// Its own function so that it can be tested, and so that adding a contribution
// kind has one place to be remembered rather than a boolean chain inside a
// bigger function. Mounts were forgotten exactly that way: a plugin whose only
// contribution was a workspace panel matched "nothing", and its button was
// silently missing.
func contributesNothing(c Contribution) bool {
	return len(c.Nav) == 0 && len(c.Mounts) == 0 && len(c.Styles) == 0 && len(c.Scripts) == 0
}

// indexWithPlugins splices the plugin contribution into the application's own
// document.
//
// Injected rather than fetched, because a rail that gains entries a moment
// after it renders is a rail that moves under somebody's cursor. Written into
// the head so it is set before the application's module runs, which is the
// only ordering that lets the first render already be right.
func (s *Server) indexWithPlugins(dist fs.FS) ([]byte, error) {
	raw, err := fs.ReadFile(dist, "index.html")
	if err != nil {
		return nil, err
	}
	c := s.plugins.Contribution()
	// Mounts are counted here too. They were not, and a plugin whose whole
	// contribution is a workspace panel — no rail entry, no stylesheet, no
	// script — took this early return and never reached the document, so its
	// button was missing with nothing anywhere saying why.
	if contributesNothing(c) {
		return raw, nil
	}

	payload, err := json.Marshal(c)
	if err != nil {
		return nil, err
	}
	var b strings.Builder
	b.WriteString("<script>window.__COG_PLUGINS__=")
	// The payload is this server's own JSON, but it lands inside a script
	// element, where the parser ends the script at the first </script> no
	// matter what the JSON meant. A plugin's name is author-controlled text,
	// so the sequence is broken up rather than trusted not to occur.
	b.WriteString(strings.ReplaceAll(string(payload), "</", `<\/`))
	b.WriteString("</script>")
	for _, href := range c.Styles {
		b.WriteString(`<link rel="stylesheet" href="` + template.HTMLEscapeString(href) + `">`)
	}
	for _, src := range c.Scripts {
		b.WriteString(`<script type="module" src="` + template.HTMLEscapeString(src) + `"></script>`)
	}

	head := []byte("</head>")
	i := bytes.Index(raw, head)
	if i < 0 {
		// No head to splice into. Serving the document unchanged is better
		// than serving one this code guessed the shape of.
		return raw, nil
	}
	out := make([]byte, 0, len(raw)+b.Len())
	out = append(out, raw[:i]...)
	out = append(out, b.String()...)
	out = append(out, raw[i:]...)
	return out, nil
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

// oauthKey builds the AEAD key an OAuth grant is sealed with, or nil.
//
// Nil is an ordinary install: one that keeps nothing sensitive in its own
// database. What it cannot then do is hold a grant, which mcpoauth refuses
// rather than degrading — see ErrNoSecretKey.
func oauthKey(cfg config.Config) *secrets.Key {
	if cfg.SecretKey == "" {
		return nil
	}
	k, err := secrets.NewKey(cfg.SecretKey)
	if err != nil {
		// Cannot happen: the same material was already accepted at startup,
		// where a short key is a startup error rather than a nil here.
		slog.Error("the secret key could not be used for MCP grants", "err", err)
		return nil
	}
	return k
}
