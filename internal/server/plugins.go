package server

import (
	"bytes"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/orkcom-tech/cogitorium/internal/channel"
	"github.com/orkcom-tech/cogitorium/internal/identity"
	"github.com/orkcom-tech/cogitorium/internal/plugin"
	"github.com/orkcom-tech/cogitorium/internal/view"
)

// The plugin runtime: the composed template set, and the pages it serves.
//
// Everything here is decided once at boot. Restart-to-activate is the model,
// so there is nothing to keep consistent while it changes — which is what lets
// a request handler read this without a lock and without wondering whether the
// set it is holding is the one that was validated.

type pluginRuntime struct {
	set   *view.Set
	pages map[string]pluginPage
	// assets is an exact allowlist, URL path to file on disk.
	//
	// Only what a manifest declared in styles: or scripts: is reachable —
	// never the bundle directory. A plugin ships whatever its author zipped,
	// including notes, sources and whatever else was in the folder, and
	// serving a directory would publish all of it because one file in it was
	// referenced.
	assets map[string]pluginAsset
	// nav is what plugins contributed to the rail, in enable order and then by
	// the order they asked for. Sorted rather than left in enable order alone:
	// an author who says 500 means to sit beside the other 500s, not behind
	// whichever plugin the operator happened to install first.
	nav []NavItem
	// styles and scripts are what every plugin asked to inject into the head.
	styles  []string
	scripts []view.Asset
	report  view.BootReport
}

// Contribution is what the plugins add to the application's own interface.
//
// Handed to the browser at boot rather than fetched, so the rail is not a
// destination that briefly has fewer entries than it will have in a moment.
type Contribution struct {
	Nav     []NavItem `json:"nav"`
	Styles  []string  `json:"styles"`
	Scripts []string  `json:"scripts"`
}

// NavItem is one destination a plugin contributed.
type NavItem struct {
	Label string `json:"label"`
	Icon  string `json:"icon,omitempty"`
	Href  string `json:"href"`
	Order int    `json:"order"`
	// When is always | workspace | admin, decided in the browser because that
	// is where the viewer's role is already known.
	When string `json:"when,omitempty"`
	// From names the plugin, so an operator debugging a rail entry can find
	// out where it came from without reading manifests.
	From string `json:"from"`
}

// pluginAsset is one declared file and how to answer for it.
type pluginAsset struct {
	PluginID string
	Path     string
	Type     string
}

// pluginPage is one declared page, resolved to what serving it needs.
type pluginPage struct {
	PluginID string
	Template string
	Title    string
	// Auth is what the manifest declared. This is the first place in this
	// server where a route's authentication is DECLARED rather than derived
	// from the shape of its path — and it has to be, because "this page is
	// open" is a decision somebody made about their own plugin, not a fact
	// about where the URL sits.
	Auth string
}

// loadPlugins composes the enabled plugins into a servable runtime.
//
// A plugin that cannot render is dropped by name with its reason logged; a
// broken install is named too. The server starts either way — one bad plugin
// must not take the product down — but nothing is ever silently absent.
func loadPlugins(dataDir string) (*pluginRuntime, error) {
	store, err := plugin.Open(dataDir)
	if err != nil {
		return nil, err
	}
	all, err := store.List()
	if err != nil {
		return nil, err
	}
	for _, in := range all {
		if in.Broken != nil {
			slog.Error("a plugin directory could not be read and is being ignored",
				"plugin", in.ID, "err", in.Broken)
		}
	}

	enabled, err := store.Enabled()
	if err != nil {
		return nil, err
	}
	sources, err := view.Sources(enabled)
	if err != nil {
		return nil, err
	}
	set, report, err := view.Boot(view.Funcs(), view.Core(), sources, view.CoreModels())
	if err != nil {
		// The host's own templates failing is the one fatal case: there is
		// nothing to serve, and starting anyway would put a broken page in
		// front of somebody instead of a refusal in a log.
		return nil, fmt.Errorf("composing the interface: %w", err)
	}

	rt := &pluginRuntime{
		set:    set,
		pages:  map[string]pluginPage{},
		assets: map[string]pluginAsset{},
		report: report,
	}

	live := map[string]bool{}
	for _, id := range report.Loaded {
		live[id] = true
	}
	for _, d := range report.Disabled {
		slog.Error("plugin disabled: its templates cannot render against this version",
			"plugin", d.ID, "reason", d.Reason())
	}

	for _, in := range enabled {
		if !live[in.ID] {
			continue
		}
		m := in.Manifest
		for _, p := range m.Pages {
			auth := p.Auth
			if auth == "" {
				auth = plugin.AuthDefault
			}
			rt.pages[p.Path] = pluginPage{
				PluginID: m.ID, Template: p.Template, Title: p.Title, Auth: auth,
			}
			if auth == "none" {
				// Said at WARN because it is the one declaration that gives
				// something away, and an operator who approved it in a list
				// should meet it again in their log rather than find it.
				slog.Warn("a plugin page is reachable without signing in",
					"plugin", m.ID, "path", p.Path)
			}
		}
		for _, n := range m.Nav {
			when := n.When
			if when == "" {
				when = "always"
			}
			rt.nav = append(rt.nav, NavItem{
				Label: n.Label, Icon: n.Icon, Href: n.Href,
				Order: n.Order, When: when, From: m.ID,
			})
		}
		for _, st := range m.Styles {
			url := rt.declareAsset(m.ID, in.Dir, st)
			rt.styles = append(rt.styles, url)
		}
		for _, sc := range m.Scripts {
			url := rt.declareAsset(m.ID, in.Dir, sc.Src)
			rt.scripts = append(rt.scripts, view.Asset{Src: url})
		}
	}

	sortNav(rt)

	if len(report.Loaded) > 0 {
		slog.Info("plugins loaded", "plugins", strings.Join(report.Loaded, ", "),
			"pages", len(rt.pages))
	}
	return rt, nil
}

func pluginAssetPath(id, rel string) string {
	return pluginPagePrefix + id + "/assets/" + strings.TrimPrefix(rel, "/")
}

// declareAsset adds one file to the allowlist and returns the URL for it.
//
// The file is resolved and confined here rather than at request time: a
// containment check that runs per request is a containment check somebody
// eventually forgets to run.
func (rt *pluginRuntime) declareAsset(id, bundleDir, rel string) string {
	url := pluginAssetPath(id, rel)
	abs := filepath.Join(bundleDir, filepath.FromSlash(strings.TrimPrefix(rel, "/")))

	if inside, err := filepath.Rel(bundleDir, abs); err != nil ||
		inside == ".." || strings.HasPrefix(inside, ".."+string(os.PathSeparator)) {
		slog.Error("a plugin declared an asset outside its own bundle and it will not be served",
			"plugin", id, "asset", rel)
		return url
	}
	if _, err := os.Stat(abs); err != nil {
		// Declared but absent. Named at boot rather than discovered as a
		// stylesheet that quietly never arrives.
		slog.Error("a plugin declares an asset that is not in its bundle",
			"plugin", id, "asset", rel, "err", err)
		return url
	}

	rt.assets[url] = pluginAsset{
		PluginID: id,
		Path:     abs,
		Type:     assetType(rel),
	}
	return url
}

// assetType names the media type from the extension. Declared rather than
// sniffed: sniffing turns a mislabelled file into whatever its first bytes
// resemble, and a stylesheet served as text/plain is ignored by the browser
// with no error anybody can see.
func assetType(rel string) string {
	switch strings.ToLower(filepath.Ext(rel)) {
	case ".css":
		return "text/css; charset=utf-8"
	case ".js", ".mjs":
		return "text/javascript; charset=utf-8"
	case ".svg":
		return "image/svg+xml"
	case ".png":
		return "image/png"
	case ".woff2":
		return "font/woff2"
	case ".json":
		return "application/json"
	}
	return "application/octet-stream"
}

// isAsset reports whether a path is a declared asset.
func (rt *pluginRuntime) isAsset(path string) bool {
	if rt == nil {
		return false
	}
	_, ok := rt.assets[path]
	return ok
}

// sortNav puts contributed entries in the order their authors asked for.
//
// Stable, so two plugins that both say 500 keep the operator's enable order
// between them — an author asking for a position is expressing where they sit
// relative to others, not claiming a unique slot.
func sortNav(rt *pluginRuntime) {
	sort.SliceStable(rt.nav, func(i, j int) bool { return rt.nav[i].Order < rt.nav[j].Order })
}

// Contribution is what the browser is told at boot.
func (rt *pluginRuntime) Contribution() Contribution {
	c := Contribution{Nav: []NavItem{}, Styles: []string{}, Scripts: []string{}}
	if rt == nil {
		return c
	}
	c.Nav = append(c.Nav, rt.nav...)
	c.Styles = append(c.Styles, rt.styles...)
	for _, s := range rt.scripts {
		c.Scripts = append(c.Scripts, s.Src)
	}
	return c
}

// pageAuth reports what a plugin path requires. The second result is false for
// any path in plugin space that is not a declared page — which is a 404, never
// a guess about what somebody meant.
func (rt *pluginRuntime) pageAuth(path string) (string, bool) {
	if rt == nil {
		return "", false
	}
	p, ok := rt.pages[path]
	if !ok {
		return "", false
	}
	return p.Auth, true
}

// pluginHandler serves declared pages. Anything else under /p/ is a 404: this
// server knows exactly which pages exist, and a path that is not one of them
// is a mistake rather than an invitation to serve something close to it.
func (s *Server) pluginHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rt := s.plugins
		if rt == nil {
			http.NotFound(w, r)
			return
		}
		if a, ok := rt.assets[r.URL.Path]; ok {
			serveAsset(w, r, a)
			return
		}

		page, ok := rt.pages[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}

		// admin is enforced here rather than by the middleware, for the same
		// reason every other admin route in this server does it: the
		// middleware decides which credential opened the door, and who is
		// allowed through it is a separate question with a separate answer.
		caller := callerFrom(r.Context())
		if page.Auth == "admin" && !caller.IsAdmin() {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}

		model := view.Page{
			Ctx:    s.viewCtx(r, caller),
			Title:  page.Title,
			Params: map[string]string{},
			Query:  flattenQuery(r.URL.Query()),
		}

		var body bytes.Buffer
		if err := rt.set.Execute(&body, page.Template, model); err != nil {
			// A page that validated at boot and fails now is a bug worth
			// seeing, not a blank region. The visitor gets a plain refusal and
			// the operator gets the reason.
			slog.Error("a plugin page failed to render",
				"plugin", page.PluginID, "template", page.Template, "err", err)
			http.Error(w, "this page could not be rendered", http.StatusInternalServerError)
			return
		}

		shell := view.Shell{
			Ctx:   model.Ctx,
			Title: page.Title,
			// No AppHead: a plugin page is not the single-page application, and
			// loading its bundle here would boot React over the top of what the
			// plugin just rendered.
			Body:    template.HTML(body.String()),
			Styles:  rt.styles,
			Scripts: rt.scripts,
		}

		var out bytes.Buffer
		if err := rt.set.Execute(&out, "cog.shell.document", shell); err != nil {
			slog.Error("the shell failed to render", "err", err)
			http.Error(w, "this page could not be rendered", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(out.Bytes())
	})
}

// serveAsset answers for one declared file.
func serveAsset(w http.ResponseWriter, r *http.Request, a pluginAsset) {
	f, err := os.Open(a.Path)
	if err != nil {
		slog.Error("a declared plugin asset could not be read",
			"plugin", a.PluginID, "path", a.Path, "err", err)
		http.NotFound(w, r)
		return
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		http.NotFound(w, r)
		return
	}
	// The type is set before ServeContent so it never sniffs. ServeContent
	// handles range requests and conditional gets, which a stylesheet behind a
	// caching proxy will use.
	w.Header().Set("Content-Type", a.Type)
	http.ServeContent(w, r, filepath.Base(a.Path), info.ModTime(), f)
}

// viewCtx is the context every template gets, so a deeply nested override
// never has to ask for it to be threaded down.
func (s *Server) viewCtx(r *http.Request, caller identity.User) view.Ctx {
	c := view.Ctx{
		Path: r.URL.Path,
		Lang: "en",
		T:    view.DefaultStrings(),
	}
	if caller.ID != 0 {
		c.Viewer = view.Viewer{
			ID: caller.ID, Name: caller.Name, IsAdmin: caller.IsAdmin(), SignedIn: true,
		}
	}
	return c
}

// flattenQuery keeps the first value per key. A template asking for .Query.q
// wants a string, and handing it a slice would make every plugin write index 0.
func flattenQuery(v url.Values) map[string]string {
	out := make(map[string]string, len(v))
	for k, vals := range v {
		if len(vals) > 0 {
			out[k] = vals[0]
		}
	}
	return out
}

// ── the HTTP surface ──────────────────────────────────────────────────────

// PluginView is one plugin as the library screen sees it.
//
// It answers the three questions somebody has in front of that screen: what is
// installed, what is each one actually doing to my interface, and why is that
// one not working. The second is computed from the templates each plugin
// ships, never from what its manifest claimed — so this endpoint cannot be
// made to say something flattering by writing a nicer manifest.
type PluginView struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Version string `json:"version"`
	Author  string `json:"author,omitempty"`
	Docs    string `json:"docs,omitempty"`
	Source  string `json:"source,omitempty"`

	// Readable is false only when the directory itself could not be read as a
	// plugin. That is a different thing from a plugin whose templates failed,
	// and conflating them costs an operator the ability to switch a
	// still-installed plugin back on.
	Readable bool `json:"readable"`

	// Pending is why this plugin may not be enabled yet, empty when it may.
	// Installing is not a decision; approval is, and it covers exact content.
	Pending string `json:"pending,omitempty"`
	// ApprovedBy and ApprovedAt record the decision, when there is one.
	ApprovedBy string `json:"approved_by,omitempty"`
	ApprovedAt string `json:"approved_at,omitempty"`
	// Dev marks a working directory rather than an installed version. An
	// operator should never have to wonder whether what they are looking at is
	// somebody's working copy.
	Dev bool `json:"dev"`

	Enabled bool `json:"enabled"`
	// Order is the position in the enable list, 1-based, or 0 when off.
	// Position is precedence: a plugin later in the list renders instead of
	// one earlier when they define the same name.
	Order int `json:"order"`
	// Live reports whether it is actually rendering. An enabled plugin that
	// failed to load is enabled and not live, and the difference is the whole
	// reason somebody is looking at this screen.
	Live bool `json:"live"`
	// Problem is why it is not live, in the words the operator needs: which
	// plugin, which template, which field. It survives being switched off,
	// because it is what somebody needs to read BEFORE switching it back on —
	// the screen says when it applies.
	Problem string `json:"problem,omitempty"`

	Tier      string `json:"tier"`
	Available bool   `json:"available"`
	// Refusal explains an unavailable tier, naming the runtime and the reason
	// this install cannot provide it.
	Refusal string `json:"refusal,omitempty"`

	// Overrides, Adds and Extends are read off the composed set. A name in
	// Overrides is a screen this plugin took over from somebody.
	Overrides []string `json:"overrides,omitempty"`
	Adds      []string `json:"adds,omitempty"`
	Extends   []string `json:"extends,omitempty"`
	// Inert is a name it defines that nothing installed owns, so it never
	// renders. Reported because a silently inert override is the hardest kind
	// of plugin bug to find.
	Inert []string `json:"inert,omitempty"`
	// Undeclared is what it overrides without having said so in its manifest.
	// Not an error — declaration is advisory by design — but it is the
	// difference between what an operator approved and what is happening.
	Undeclared []string `json:"undeclared,omitempty"`

	Pages   []PluginPageView `json:"pages,omitempty"`
	Hosts   []string         `json:"hosts,omitempty"`
	Secrets []string         `json:"secrets,omitempty"`
	API     []string         `json:"api,omitempty"`
}

// PluginPageView is one page a plugin serves.
type PluginPageView struct {
	Path  string `json:"path"`
	Title string `json:"title,omitempty"`
	Auth  string `json:"auth"`
}

// handleListPlugins answers with everything the library screen needs.
//
// Admin only. What is installed on this machine and what it is allowed to
// reach is an operator's business, not a workspace member's.
func (s *Server) handleListPlugins(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAdmin(w, r); !ok {
		return
	}
	store, err := plugin.Open(s.dataDir)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "the plugin directory could not be read")
		return
	}
	all, err := store.List()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "the plugin directory could not be read")
		return
	}

	caps := s.pluginCaps()
	out := make([]PluginView, 0, len(all))
	for _, in := range all {
		out = append(out, s.pluginView(in, caps))
	}
	writeJSON(w, http.StatusOK, out)
}

// pluginCaps is what this install can run, asked once per request rather than
// once per plugin: the channel probe is cached, but the intent is that a list
// of forty plugins does not look like forty separate decisions.
func (s *Server) pluginCaps() plugin.Capabilities {
	return plugin.Capabilities{Profile: channel.Detect(s.dataDir)}
}

func (s *Server) pluginView(in plugin.Installed, caps plugin.Capabilities) PluginView {
	if in.Broken != nil {
		// Everything except the id is unreliable for a broken install, so
		// nothing else is claimed about it.
		return PluginView{ID: in.ID, Readable: false, Problem: in.Broken.Error()}
	}

	m := in.Manifest
	v := PluginView{
		ID: m.ID, Name: m.Name, Version: in.Version, Readable: true,
		Docs: m.Docs, Source: m.Source,
		Enabled: in.Enabled, Pending: in.Pending, Dev: in.Dev,
		Hosts: m.Hosts, Secrets: m.Secrets, API: m.API,
	}
	if in.Approval.Digest != "" {
		v.ApprovedBy, v.ApprovedAt = in.Approval.By, in.Approval.At.Format(time.RFC3339)
	}
	if in.Enabled {
		v.Order = in.Order + 1
	}
	for _, p := range m.Pages {
		auth := p.Auth
		if auth == "" {
			auth = plugin.AuthDefault
		}
		v.Pages = append(v.Pages, PluginPageView{Path: p.Path, Title: p.Title, Auth: auth})
	}

	res := plugin.Resolve(m, caps)
	v.Tier, v.Available, v.Refusal = string(res.Tier), res.Available, res.Refusal

	rt := s.plugins
	if rt == nil {
		return v
	}
	for _, d := range rt.report.Disabled {
		if d.ID == m.ID {
			v.Problem = d.Reason()
			return v
		}
	}
	for _, id := range rt.report.Loaded {
		if id == m.ID {
			v.Live = true
		}
	}
	if !v.Live {
		return v
	}

	declared := map[string]bool{}
	for _, o := range m.Overrides {
		declared[o] = true
	}
	for _, e := range rt.set.Ledger().For(m.ID) {
		switch e.Action {
		case view.Overrides:
			v.Overrides = append(v.Overrides, e.Name)
			if !declared[e.Name] {
				v.Undeclared = append(v.Undeclared, e.Name)
			}
		case view.Adds:
			v.Adds = append(v.Adds, e.Name)
		case view.Extends:
			v.Extends = append(v.Extends, e.Name)
		case view.Dangling:
			v.Inert = append(v.Inert, e.Name)
		}
	}
	return v
}
