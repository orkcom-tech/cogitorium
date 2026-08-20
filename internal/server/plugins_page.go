package server

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/orkcom-tech/cogitorium/internal/plugin"
	"github.com/orkcom-tech/cogitorium/internal/view"
)

// The library screen, rendered by the system it is about.
//
// A pleasing self-check and a real one: if the template stack, the layer
// order, the approval gate or the composed set were wrong, this is the screen
// that would fail to draw — and it is the screen an operator would be on when
// they needed it most.
//
// Built around the distinction the rest of this file exists for: enabled and
// live are different questions. A plugin can be switched on and not rendering,
// and the gap between those two is why somebody opens this page.

func (s *Server) handlePluginsPage(w http.ResponseWriter, r *http.Request) {
	if !s.pageAdmin(w, r) {
		return
	}
	s.renderPlugins(w, r, "", "")
}

func (s *Server) renderPlugins(w http.ResponseWriter, r *http.Request, problem, notice string) {
	model := s.pluginsModel(r, problem, notice)
	fragment := "cog.list.plugins"
	if model.Library {
		fragment = "cog.list.catalog"
	}
	s.renderPage(w, r, "cog.page.plugins", fragment, "Plugins", model)
}

func (s *Server) pluginsModel(r *http.Request, problem, notice string) view.Plugins {
	q := r.URL.Query()
	model := view.Plugins{
		Ctx:     s.viewCtx(r, callerFrom(r.Context())),
		Library: q.Get("view") == "library",
		Error:   problem,
		Notice:  notice,
		// Sticky across a render because it is sticky in fact: a restart is
		// owed until it happens.
		RestartOwed: q.Get("restart") == "owed",
		CanRestart:  canRestart(),
		Query:       q.Get("q"),
	}
	if model.Library {
		model.CatalogQuery = q.Get("q")
		s.fillCatalog(r, &model)
		return model
	}

	model.Narrowed = model.Query != ""
	store, err := plugin.Open(s.dataDir)
	if err != nil {
		model.Error = err.Error()
		return model
	}
	installed, err := store.List()
	if err != nil {
		model.Error = err.Error()
		return model
	}

	caps := s.pluginCaps()
	for _, in := range installed {
		row := s.pluginRow(in, caps)
		if model.Query != "" && !pluginMatches(row, model.Query) {
			continue
		}
		model.Items = append(model.Items, row)
	}
	return model
}

// pluginRow reuses the view the API already assembles, so the screen and the
// API cannot disagree about what a plugin does.
func (s *Server) pluginRow(in plugin.Installed, caps plugin.Capabilities) view.PluginRow {
	v := s.pluginView(in, caps)
	row := view.PluginRow{
		ID: v.ID, Name: v.Name, Version: v.Version, Docs: v.Docs, Source: v.Source,
		Readable: v.Readable, Pending: v.Pending, ApprovedBy: v.ApprovedBy,
		ApprovedAt: v.ApprovedAt, Dev: v.Dev, Enabled: v.Enabled, Order: v.Order,
		Live: v.Live, Problem: v.Problem, Tier: v.Tier, Available: v.Available,
		Refusal: v.Refusal, Hosts: v.Hosts, Secrets: v.Secrets, API: v.API,
		CanMoveUp: v.Order > 1,
	}
	if row.Name == "" {
		row.Name = v.ID
	}
	row.State, row.StateTone = pluginState(v)

	// Labelled here rather than in six template branches. The first draft
	// wanted a helper function for that, and the function set every template
	// may call is a permanent promise to every author.
	for _, g := range []struct {
		label string
		names []string
		tone  string
	}{
		{"Overrides", v.Overrides, ""},
		{"Adds", v.Adds, ""},
		{"Extends", v.Extends, ""},
		{"Overridden without declaring", v.Undeclared, "warn"},
		{"Inert — nothing installed owns that namespace", v.Inert, "warn"},
		{"Renders empty — that region is now blank", v.Silent, "warn"},
		{"Hosts it asks to reach", v.Hosts, ""},
		{"Named values it asks for", v.Secrets, ""},
		{"API it asks to call", v.API, ""},
	} {
		if len(g.names) > 0 {
			row.Names = append(row.Names, view.NameList{Label: g.label, Names: g.names, Tone: g.tone})
		}
	}
	// What the author ships to show what this does. From the runtime rather
	// than the manifest, because the runtime is what actually declared the
	// files and therefore what will actually serve them.
	if rt := s.pluginRT(); rt != nil {
		row.Media = rt.media[v.ID]
	}

	for _, p := range v.Pages {
		row.Pages = append(row.Pages, view.PluginPageRow{
			Path: p.Path, Title: p.Title, Auth: p.Auth, Live: v.Live,
		})
	}
	return row
}

// pluginState is the badge, and it never says "enabled" on its own.
//
// Unreadable and switched-off used to render the same, which left a working
// plugin labelled broken with no way back on. Not approved is a third thing
// again: off is a choice somebody made, and this is a decision nobody has
// made yet.
func pluginState(v PluginView) (string, string) {
	switch {
	case !v.Readable:
		return "unreadable", "danger"
	case v.Pending != "":
		return "needs approval", "warn"
	case !v.Enabled:
		return "off", ""
	case !v.Live:
		return "on, not loading", "danger"
	}
	return "live", "ok"
}

// pluginMatches searches what a plugin DOES, not only what it is called.
//
// "who overrode my gear row" is the question somebody actually arrives with,
// and a name-only search cannot answer it.
func pluginMatches(row view.PluginRow, query string) bool {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return true
	}
	hay := []string{row.ID, row.Name}
	for _, g := range row.Names {
		hay = append(hay, g.Names...)
	}
	for _, p := range row.Pages {
		hay = append(hay, p.Path)
	}
	return strings.Contains(strings.ToLower(strings.Join(hay, " ")), q)
}

func (s *Server) fillCatalog(r *http.Request, model *view.Plugins) {
	idx, err := s.pluginCatalog().Fetch(r.Context())
	if err != nil {
		model.CatalogFailed = err.Error()
		return
	}
	model.Cached = idx.Cached
	if !idx.Fetched.IsZero() {
		model.Fetched = idx.Fetched.Format("2006-01-02 15:04")
	}
	for _, e := range idx.Entries {
		if e.Version != "" {
			model.Versioned = true
			break
		}
	}

	store, err := plugin.Open(s.dataDir)
	if err != nil {
		model.CatalogFailed = err.Error()
		return
	}
	if installed, err := store.List(); err == nil {
		for _, u := range idx.Updates(installed) {
			model.Updates = append(model.Updates, view.CatalogUpdateRow{
				Name: u.Entry.Name, Installed: u.Installed, Available: u.Available,
			})
		}
	}

	matched := idx.Search(model.CatalogQuery)
	model.CatalogTotal = len(matched)
	for _, e := range matched {
		row := view.CatalogRow{
			ID: e.ID, Name: e.Name, Author: e.Author, Description: e.Description,
			Source: e.SourceURL(), Version: e.Version, Cover: e.Cover,
		}
		if in, err := store.Get(e.ID); err == nil {
			row.Installed, row.InstalledVersion = true, in.Version
			for _, u := range model.Updates {
				if u.Name == e.Name {
					row.Update = true
				}
			}
		}
		// Three states rather than a badge, decided here so the template does
		// not compare strings.
		c := idx.Verify(e.ID, row.InstalledVersion)
		switch c.State {
		case "verified":
			row.VerifiedRead = true
		case "verified-other-version":
			row.VerifiedOther, row.VerifiedAt = true, c.Version
		default:
			row.Unchecked = true
		}
		row.VerifiedBy, row.VerifiedNote = c.By, c.Note
		model.Catalog = append(model.Catalog, row)
	}
}

// The actions. Each answers with the page, so a failure keeps somebody on the
// screen beside the reason — and a restart owed by one of them stays owed
// across the render.

func (s *Server) pluginAction(w http.ResponseWriter, r *http.Request,
	do func(*plugin.Store, string) (string, bool, error)) {
	if !s.pageAdmin(w, r) {
		return
	}
	store, err := plugin.Open(s.dataDir)
	if err != nil {
		s.renderPlugins(w, r, err.Error(), "")
		return
	}
	id := r.PathValue("id")
	notice, restart, err := do(store, id)
	if err != nil {
		s.renderPlugins(w, r, err.Error(), "")
		return
	}
	// The interface, rebuilt from what is on disk now — so the page that comes
	// back is already without what was just removed, or already with what was
	// just enabled. This is what makes the reload after a removal show the
	// removal.
	s.recomposePlugins()
	// And a restart only if something recomposing cannot reach actually
	// changed. Every one of these actions used to claim one.
	if restart {
		restart = s.needsRestart(id)
	}
	if restart {
		// Carried in the URL rather than held here: a restart is owed by the
		// install, and a flag on this process would forget it the moment
		// somebody reloaded.
		q := "?restart=owed"
		http.Redirect(w, r, "/plugins"+q, http.StatusSeeOther)
		return
	}
	s.renderPlugins(w, r, "", notice)
}

func (s *Server) handleApprovePluginForm(w http.ResponseWriter, r *http.Request) {
	s.pluginAction(w, r, func(st *plugin.Store, id string) (string, bool, error) {
		a, err := st.Approve(id, callerFrom(r.Context()).Name)
		if err != nil {
			return "", false, err
		}
		// Approving changes nothing that is running: it makes enabling
		// possible, and enabling is what needs the restart.
		return id + " " + a.Version + " approved. Enable it to put it in the layer order.", false, nil
	})
}

func (s *Server) handleRevokePluginForm(w http.ResponseWriter, r *http.Request) {
	s.pluginAction(w, r, func(st *plugin.Store, id string) (string, bool, error) {
		return "", true, st.Revoke(id)
	})
}

func (s *Server) handleEnablePluginForm(w http.ResponseWriter, r *http.Request) {
	s.pluginAction(w, r, func(st *plugin.Store, id string) (string, bool, error) {
		return "", true, st.Enable(id)
	})
}

// handleMovePluginForm shifts one plugin a place in the layer order.
//
// Position is precedence, so this is the control that decides which of two
// plugins defining the same name actually renders.
func (s *Server) handleMovePluginForm(w http.ResponseWriter, r *http.Request, dir int) {
	s.pluginAction(w, r, func(st *plugin.Store, id string) (string, bool, error) {
		order, err := st.Order()
		if err != nil {
			return "", false, err
		}
		at := -1
		for i, x := range order {
			if x == id {
				at = i
			}
		}
		if at < 0 {
			return "", false, nil
		}
		to := at + dir
		if to < 0 || to >= len(order) {
			// Already at the end it was moving towards. Not an error: the
			// button is there, and pressing it once more is not a mistake
			// worth a red line.
			return "", false, nil
		}
		order[at], order[to] = order[to], order[at]
		return "", true, st.SetOrder(order)
	})
}

func (s *Server) handlePluginUpForm(w http.ResponseWriter, r *http.Request) {
	s.handleMovePluginForm(w, r, -1)
}

func (s *Server) handlePluginDownForm(w http.ResponseWriter, r *http.Request) {
	s.handleMovePluginForm(w, r, 1)
}

// handleUploadPluginForm installs a bundle somebody dropped.
func (s *Server) handleUploadPluginForm(w http.ResponseWriter, r *http.Request) {
	if !s.pageAdmin(w, r) {
		return
	}
	if err := r.ParseMultipartForm(maxBundleBytes); err != nil {
		s.renderPlugins(w, r, "that bundle could not be read: "+err.Error(), "")
		return
	}
	file, header, err := r.FormFile("bundle")
	if err != nil {
		s.renderPlugins(w, r, "choose a bundle to install", "")
		return
	}
	defer file.Close()
	// The name the person chose, for anything that goes wrong below. The
	// reader downstream names the file it was handed, which is this server's
	// own temp file — a name the person has never seen and cannot go and look
	// at, on the one screen where they are being asked to trust a file.
	chosen := filepath.Base(header.Filename)
	if chosen == "" || chosen == "." {
		chosen = "that file"
	}

	tmp, err := os.CreateTemp("", "cogitorium-upload-*.zip")
	if err != nil {
		s.renderPlugins(w, r, err.Error(), "")
		return
	}
	defer os.Remove(tmp.Name())
	if _, err := io.Copy(tmp, io.LimitReader(file, maxBundleBytes)); err != nil {
		tmp.Close()
		s.renderPlugins(w, r, err.Error(), "")
		return
	}
	tmp.Close()

	store, err := plugin.Open(s.dataDir)
	if err != nil {
		s.renderPlugins(w, r, err.Error(), "")
		return
	}
	in, _, err := store.Install(tmp.Name())
	if err != nil {
		s.renderPlugins(w, r, strings.ReplaceAll(err.Error(), filepath.Base(tmp.Name()), chosen), "")
		return
	}
	// Switched off and unapproved, like everything else that arrives.
	s.renderPlugins(w, r, "",
		in.Manifest.Name+" "+in.Version+" installed and switched off. Read the source, then approve it.")
}

// handleInstallFromCatalogForm installs one entry from the shared catalog.
func (s *Server) handleInstallFromCatalogForm(w http.ResponseWriter, r *http.Request) {
	if !s.pageAdmin(w, r) {
		return
	}
	id := r.PathValue("id")
	c := s.pluginCatalog()
	idx, err := c.Fetch(r.Context())
	if err != nil {
		s.renderPlugins(w, r, err.Error(), "")
		return
	}
	e, found := idx.Find(id)
	if !found {
		s.renderPlugins(w, r, "the catalog does not list "+id, "")
		return
	}
	store, err := plugin.Open(s.dataDir)
	if err != nil {
		s.renderPlugins(w, r, err.Error(), "")
		return
	}
	in, _, err := c.InstallFromCatalog(r.Context(), store, e, "")
	if err != nil {
		s.renderPlugins(w, r, err.Error(), "")
		return
	}
	s.renderPlugins(w, r, "",
		in.Manifest.Name+" "+in.Version+" installed and switched off. Read the source, then approve it.")
}

// handleRestartFromPluginsForm is the restart the banner offers.
func (s *Server) handleRestartFromPluginsForm(w http.ResponseWriter, r *http.Request) {
	s.handleRestart(w, r)
}
