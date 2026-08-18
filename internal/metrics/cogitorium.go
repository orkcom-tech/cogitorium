package metrics

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// What this product publishes, chosen rather than collected.
//
// The rule for whether something belongs here is: WOULD SOMEBODY BE PAGED ON
// IT. A count of database rows is interesting and is not that; a nightly gear
// that has failed four times running is exactly that. Everything below is at a
// point where slog already fires, because those are the places this product
// already decided were worth saying something about.
//
// NO NAME A PERSON CHOSE APPEARS IN A LABEL — not a workspace, an agent, a
// model, a tool or a user. See the package comment: a scrape has a different
// audience from an authenticated screen, and a label is unbounded cardinality
// besides. `provider` is the one that looks like an exception and is not: it is
// the provider TYPE, `anthropic` or `openai-compatible`, which is a closed set
// this codebase defines.
type Set struct {
	reg *Registry

	Build *Handle

	HTTPRequests *Handle
	HTTPSeconds  *Handle

	WorkQueued  *Handle
	WorkRunning *Handle
	WorkUnits   *Handle

	ScheduleFires *Handle

	GearRuns    *Handle
	GearSeconds *Handle

	ModelCalls  *Handle
	ModelTokens *Handle

	MCPCalls   *Handle
	MCPSeconds *Handle

	EgressRequests *Handle
}

// NewSet registers everything and stamps the build.
func NewSet(version string) *Set {
	r := New()
	s := &Set{
		reg: r,
		Build: r.Register("cogitorium_build_info",
			"The version of this build, as a label on a constant 1.", Gauge),

		HTTPRequests: r.Register("cogitorium_http_requests_total",
			"HTTP requests by method, route template and status.", Counter),
		// Web latency, so the buckets are web-shaped rather than the package
		// default, which is sized for model calls.
		HTTPSeconds: r.Register("cogitorium_http_request_seconds",
			"How long HTTP requests took.", Histogram,
			0.005, 0.025, 0.1, 0.5, 1, 2.5, 5, 10),

		WorkQueued: r.Register("cogitorium_work_queued",
			"Units waiting for a worker.", Gauge),
		WorkRunning: r.Register("cogitorium_work_running",
			"Units a worker is running now.", Gauge),
		WorkUnits: r.Register("cogitorium_work_units_total",
			"Queued units that finished, by kind and outcome.", Counter),

		ScheduleFires: r.Register("cogitorium_schedule_fires_total",
			"Schedule firings by outcome: fired, skipped or failed.", Counter),

		GearRuns: r.Register("cogitorium_gear_runs_total",
			"Gear executions by outcome.", Counter),
		GearSeconds: r.Register("cogitorium_gear_run_seconds",
			"How long gear executions took.", Histogram),

		ModelCalls: r.Register("cogitorium_model_calls_total",
			"Model calls by provider type and outcome.", Counter),
		// The one an alert is genuinely wanted on, because it is the bill.
		ModelTokens: r.Register("cogitorium_model_tokens_total",
			"Tokens billed, by direction. This is the money.", Counter),

		MCPCalls: r.Register("cogitorium_mcp_calls_total",
			"External MCP tool calls by transport and outcome.", Counter),
		MCPSeconds: r.Register("cogitorium_mcp_call_seconds",
			"How long external MCP tool calls took.", Histogram),

		EgressRequests: r.Register("cogitorium_egress_requests_total",
			"Outward search requests by what became of them.", Counter),
	}
	s.Build.Set(map[string]string{"version": version}, 1)
	return s
}

// Registry is what the handler renders.
func (s *Set) Registry() *Registry { return s.reg }

// Outcome is the label every "did it work" counter uses, so a dashboard does
// not have to learn three vocabularies.
func Outcome(err error) string {
	if err != nil {
		return "failed"
	}
	return "ok"
}

// Serve runs the metrics listener until ctx ends.
//
// ITS OWN LISTENER, NOT A ROUTE ON THE API, and that is the whole security
// design. The API is authenticated; a Prometheus scrape is not, and bolting an
// unauthenticated path onto an authenticated surface is how a metrics endpoint
// becomes the way in. A separate address can be bound to a private interface,
// firewalled, or reached only from inside a pod — decisions an operator makes
// about a port, which is what they already do for every other exporter they run.
//
// Empty address means OFF, and off is the default: this is a new thing
// listening on a machine, and a product that started one without being asked
// would be making that decision for somebody.
func (s *Set) Serve(ctx context.Context, addr string) {
	if strings.TrimSpace(addr) == "" {
		return
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, r *http.Request) {
		var b strings.Builder
		s.reg.Write(&b)
		// The version Prometheus and everything that reads its format expect.
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		_, _ = w.Write([]byte(b.String()))
	})
	// Anything else is 404 rather than the single-page app: this listener
	// serves one thing, and a scraper that mistyped should learn that here.
	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()
	go func() {
		slog.Info("metrics listening", "addr", addr, "path", "/metrics",
			"note", "unauthenticated by design — bind it where only your scraper can reach it")
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			// Not fatal. A metrics port that cannot bind must not stop a server
			// from doing its actual job, and the operator is told plainly.
			slog.Error("the metrics listener stopped", "addr", addr, "err", err)
		}
	}()
}
