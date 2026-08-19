package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sync"

	"github.com/orkcom-tech/cogitorium/internal/abi"
	"github.com/orkcom-tech/cogitorium/internal/channel"
	"github.com/orkcom-tech/cogitorium/internal/gearnet"
	"github.com/orkcom-tech/cogitorium/internal/imagert"
	"github.com/orkcom-tech/cogitorium/internal/plugin"
	"github.com/orkcom-tech/cogitorium/internal/runtimes"
	"github.com/orkcom-tech/cogitorium/internal/sandbox"
	"github.com/orkcom-tech/cogitorium/internal/wasmrt"
	"github.com/orkcom-tech/cogitorium/internal/work"
	"github.com/orkcom-tech/cogitorium/internal/worker"
)

// Where a plugin's backend gets called from.
//
// This is the join between three things that were built separately: the ABI
// says what a call looks like, the WebAssembly runtime knows how to make one,
// and the page handler knows when to. Until they met, a plugin's page could
// only render what its templates already contained.
//
// The universal tier is wired first because it is the one that needs nothing
// fetched and nothing probed — it works on every channel, so a plugin that
// uses it works everywhere the moment it is enabled.

// backends runs plugin exports.
type backends struct {
	mu   sync.Mutex
	wasm *wasmrt.Runtime
	// workers is the provisioned tier: an interpreter this install fetched,
	// supervised as a child process speaking the same ABI down a pipe. The
	// caller never learns which of the two answered, which is the whole point
	// of the tier model — an author declares a technology and the lane is not
	// their problem.
	workers *worker.Supervisor
	// images is the container tier: one container per invocation, on whatever
	// sandbox this server already has for gears. Nothing new to install and
	// nothing new to configure — if gears are sandboxed here, so is this.
	images  *imagert.Runner
	imageOf map[string]string
	dirOf   map[string]string
	grants  map[string]plugin.Grants
	// host answers cog.* for every tier, and is kept so the mux can be handed
	// to it once the routes exist.
	host *hostGateway
	// tier records which lane each plugin landed in, so a page for a plugin
	// with no backend at all renders its template alone rather than failing.
	tier map[string]plugin.Tier
}

func hostOf(url string) string {
	s := url
	for _, prefix := range []string{"https://", "http://"} {
		if len(s) > len(prefix) && s[:len(prefix)] == prefix {
			s = s[len(prefix):]
			break
		}
	}
	for i := 0; i < len(s); i++ {
		if s[i] == '/' || s[i] == ':' {
			return s[:i]
		}
	}
	return s
}

// startBackends compiles the modules of every live plugin that has one.
//
// At boot rather than on the first request, so a module that will not load is
// a line in the startup log rather than a page that fails the first time
// somebody visits it.
func startBackends(ctx context.Context, rt *pluginRuntime, enabled []plugin.Installed, dataDir string,
	sb sandbox.Runner, db *sql.DB, cfg map[string]map[string]any, gate *gearnet.Gate,
	queue *work.Store) *backends {
	b := &backends{tier: map[string]plugin.Tier{}, dirOf: map[string]string{}}

	grants := map[string]plugin.Grants{}
	var wasmPlugins, workerPlugins, nativePlugins, imagePlugins []plugin.Installed
	native := map[string]plugin.Native{}
	imageOf := map[string]string{}
	profile := channel.Detect(dataDir)
	images := imagert.New(sb)
	caps := plugin.Capabilities{Profile: profile, ContainerRunner: images.Available()}

	for _, in := range enabled {
		res := plugin.Resolve(in.Manifest, caps)
		if !res.Available {
			// Already explained by the resolver, and already on the plugins
			// screen as a refusal naming the runtime. Repeating it here would
			// be a second voice saying the same thing differently.
			continue
		}
		switch res.Tier {
		case plugin.TierWasm, plugin.TierProvisioned, plugin.TierNative, plugin.TierImage:
		default:
			// Tier 0. Its templates are its whole contribution and there is
			// nothing here to start.
			continue
		}
		g, err := plugin.ResolveGrants(in.Manifest)
		if err != nil {
			slog.Error("a plugin's grants could not be read; its backend will not run",
				"plugin", in.ID, "err", err)
			continue
		}
		grants[in.ID] = g
		switch res.Tier {
		case plugin.TierWasm:
			wasmPlugins = append(wasmPlugins, in)
		case plugin.TierProvisioned:
			workerPlugins = append(workerPlugins, in)
		case plugin.TierNative:
			native[in.ID] = res.Native
			nativePlugins = append(nativePlugins, in)
		case plugin.TierImage:
			imageOf[in.ID] = res.Technology
			imagePlugins = append(imagePlugins, in)
		}
	}

	gateway := newHostGateway(grants, db, rt, cfg, gate, queue)
	b.host = gateway
	if len(workerPlugins) > 0 || len(nativePlugins) > 0 {
		b.startWorkers(ctx, workerPlugins, nativePlugins, native, dataDir, profile, gateway)
	}
	if len(imagePlugins) > 0 {
		b.images = images
		b.grants = grants
		b.imageOf = map[string]string{}
		for _, in := range imagePlugins {
			b.imageOf[in.ID] = imageOf[in.ID]
			b.dirOf[in.ID] = in.Dir
			b.tier[in.ID] = plugin.TierImage
			slog.Info("plugin backend ready", "plugin", in.ID, "tier", "image",
				"image", imageOf[in.ID], "sandbox", images.Backend())
		}
	}
	if len(wasmPlugins) == 0 {
		return b
	}

	engine, err := wasmrt.New(ctx, gateway, wasmrt.DefaultLimits())
	if err != nil {
		slog.Error("the WebAssembly engine could not start; plugin backends are unavailable", "err", err)
		return b
	}
	b.wasm = engine

	for _, in := range wasmPlugins {
		path := filepath.Join(in.Dir, "plugin.wasm")
		module, err := os.ReadFile(path)
		if err != nil {
			slog.Error("a plugin needs a WebAssembly module and does not ship one",
				"plugin", in.ID, "expected", path, "err", err)
			continue
		}
		if err := engine.Compile(ctx, in.ID, module); err != nil {
			slog.Error("a plugin's module would not load", "plugin", in.ID, "err", err)
			continue
		}
		b.tier[in.ID] = plugin.TierWasm
		slog.Info("plugin backend ready", "plugin", in.ID, "tier", "wasm")
	}
	return b
}

// startWorkers brings up the provisioned tier.
//
// The interpreter is located, and fetched only if it must be — one runtime per
// version, shared by every plugin that asked for it. Nothing is started here:
// a worker's child process spawns on its first call, so a plugin that is
// enabled and never visited costs a row in a map and no memory.
func (b *backends) startWorkers(ctx context.Context, provisioned, nativePlugins []plugin.Installed,
	native map[string]plugin.Native, dataDir string, profile channel.Profile, host abi.Host) {
	store := runtimes.NewStore(dataDir, plugin.RefDir, profile, runtimes.HTTPFetcher{}, true)
	sup := worker.NewSupervisor()

	// Native first, because it needs nothing fetched: the author already
	// published the binary, and the only question was whether one matches this
	// machine — which the resolver answered before we got here.
	for _, in := range nativePlugins {
		n := native[in.ID]
		path := filepath.Join(in.Dir, filepath.FromSlash(n.Path))
		info, err := os.Stat(path)
		if err != nil {
			slog.Error("a native plugin does not ship the binary its manifest names",
				"plugin", in.ID, "target", n.Target(), "expected", n.Path, "err", err)
			continue
		}
		if info.Mode()&0o111 == 0 {
			// Zip does carry the mode, so this means the author built the
			// bundle from a checkout where it was never executable. Said
			// plainly rather than chmod'ed: quietly making somebody's file
			// executable is not a decision this server should take.
			slog.Error("a native plugin's binary is not executable, so it cannot be started",
				"plugin", in.ID, "path", n.Path, "mode", info.Mode().String())
			continue
		}
		sup.Register(worker.Spec{
			Plugin: in.ID, Path: path, Dir: in.Dir,
			Env:  []string{"COGITORIUM_PLUGIN=" + in.ID},
			Host: host,
		})
		b.tier[in.ID] = plugin.TierNative
		slog.Warn("plugin backend ready", "plugin", in.ID, "tier", "native",
			"target", n.Target(),
			// Warn, not Info: this is the one tier with no isolation of any
			// kind — the author's own machine code, as this server's user.
			// An operator approved it, and the log should still say so.
			"note", "runs as this server's user, unsandboxed")
	}

	for _, in := range provisioned {
		entry := plugin.EntryFile(in.Manifest.Needs)
		path := filepath.Join(in.Dir, entry)
		if _, err := os.Stat(path); err != nil {
			slog.Error("a plugin declares a technology whose entry file it does not ship",
				"plugin", in.ID, "needs", in.Manifest.Needs, "expected", entry)
			continue
		}

		// Any version of the technology. A plugin that needs a particular one
		// is a thing the manifest does not yet express, and inventing a
		// constraint here would be a second place versions are decided.
		res, err := store.Ensure(ctx, in.Manifest.Needs, func(string) bool { return true })
		if err != nil {
			slog.Error("a plugin's runtime could not be provided; its backend will not run",
				"plugin", in.ID, "needs", in.Manifest.Needs, "err", err)
			continue
		}

		sup.Register(worker.Spec{
			Plugin: in.ID,
			Path:   res.Exe,
			Args:   []string{path},
			Dir:    in.Dir,
			// Nothing of this server's environment is inherited. A child that
			// started with the server's own variables would hold its database
			// path and whatever else the operator exported, none of which a
			// plugin was granted.
			Env:  []string{"COGITORIUM_PLUGIN=" + in.ID},
			Host: host,
		})
		b.tier[in.ID] = plugin.TierProvisioned
		slog.Info("plugin backend ready", "plugin", in.ID, "tier", "provisioned",
			"runtime", in.Manifest.Needs+" "+res.Row.Version, "from_image", res.FromSeed)
	}
	b.workers = sup
}

// invoke runs one of a plugin's exports with nobody waiting on the answer.
//
// The same envelope a page's provider gets, in a different role. A task is not
// a provider: nothing renders what it returns, and an author reading the role
// knows whether they are answering a person or doing work.
func (b *backends) invoke(ctx context.Context, id, export string, args json.RawMessage) error {
	if b == nil {
		return fmt.Errorf("this install runs no plugin backends")
	}
	tier, has := b.tier[id]
	if !has {
		return fmt.Errorf("plugin %q has no backend, so it has nothing to run", id)
	}

	req := abi.Request{Export: export, Role: abi.RoleEvent, Input: args}

	var resp abi.Response
	var err error
	switch tier {
	case plugin.TierImage:
		resp, err = b.images.Call(ctx, imagert.Spec{
			Plugin: id, Image: b.imageOf[id], Dir: b.dirOf[id],
			Network: len(b.grants[id].Hosts()) > 0,
		}, req)
	case plugin.TierProvisioned, plugin.TierNative:
		resp, err = b.workers.Call(ctx, id, req)
	default:
		b.mu.Lock()
		resp, err = b.wasm.Call(ctx, id, req)
		b.mu.Unlock()
	}
	if err != nil {
		return err
	}
	if resp.Error != "" {
		// The plugin failing on purpose, in its own words. Returned rather
		// than logged and swallowed, so the queue marks the unit dead with a
		// reason somebody can read.
		return fmt.Errorf("%s", resp.Error)
	}
	return nil
}

// attachAPI hands the gateway this server's routes.
//
// Late, because the mux is built after the backends are: a plugin's first call
// cannot happen before the server is listening, so there is no window where
// this is nil and something needs it.
func (b *backends) attachAPI(h http.Handler) {
	if b == nil || b.host == nil {
		return
	}
	b.host.handler = h
}

// provide asks a plugin for the model behind one of its pages.
//
// The second result is false when the plugin has no backend, which is the
// ordinary case: a template-only plugin renders its own markup against the
// standard page model and needs nothing from here.
func (b *backends) provide(ctx context.Context, id, export string, req abi.Request) (any, bool, error) {
	if b == nil {
		return nil, false, nil
	}
	tier, has := b.tier[id]
	if !has {
		return nil, false, nil
	}

	req.Export, req.Role = export, abi.RoleProvider

	var resp abi.Response
	var err error
	switch tier {
	case plugin.TierImage:
		resp, err = b.images.Call(ctx, imagert.Spec{
			Plugin:  id,
			Image:   b.imageOf[id],
			Dir:     b.dirOf[id],
			Network: len(b.grants[id].Hosts()) > 0,
		}, req)
	case plugin.TierProvisioned, plugin.TierNative:
		// Not under b.mu: a worker serialises its own calls, and holding a
		// lock across every tier would make one plugin's slow interpreter
		// everybody's problem.
		resp, err = b.workers.Call(ctx, id, req)
	default:
		b.mu.Lock()
		resp, err = b.wasm.Call(ctx, id, req)
		b.mu.Unlock()
	}
	if err != nil {
		return nil, true, err
	}
	if resp.Error != "" {
		// The plugin refusing on purpose, in its own words.
		return nil, true, fmt.Errorf("%s", resp.Error)
	}
	if len(resp.Data) == 0 {
		return nil, true, nil
	}
	var model any
	if err := json.Unmarshal(resp.Data, &model); err != nil {
		return nil, true, fmt.Errorf("its model is not readable: %w", err)
	}
	return model, true, nil
}

// close releases the engine and stops every child.
//
// A server that exits leaving interpreters running has handed a machine's
// memory to nobody.
func (b *backends) close(ctx context.Context) {
	if b == nil {
		return
	}
	if b.workers != nil {
		b.workers.Close()
	}
	if b.wasm != nil {
		_ = b.wasm.Close(ctx)
	}
}
