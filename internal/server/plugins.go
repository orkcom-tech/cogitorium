package server

import (
	"bytes"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

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
	// styles and scripts are what every plugin asked to inject into the head.
	styles  []string
	scripts []view.Asset
	report  view.BootReport
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

	rt := &pluginRuntime{set: set, pages: map[string]pluginPage{}, report: report}

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
		for _, st := range m.Styles {
			rt.styles = append(rt.styles, pluginAssetPath(m.ID, st))
		}
		for _, sc := range m.Scripts {
			rt.scripts = append(rt.scripts, view.Asset{Src: pluginAssetPath(m.ID, sc.Src)})
		}
	}

	if len(report.Loaded) > 0 {
		slog.Info("plugins loaded", "plugins", strings.Join(report.Loaded, ", "),
			"pages", len(rt.pages))
	}
	return rt, nil
}

func pluginAssetPath(id, rel string) string {
	return pluginPagePrefix + id + "/assets/" + strings.TrimPrefix(rel, "/")
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
