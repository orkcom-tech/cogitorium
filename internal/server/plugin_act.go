package server

import (
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/orkcom-tech/cogitorium/internal/plugin"
	"github.com/orkcom-tech/cogitorium/internal/view"
)

// Changing the plugin set from the library screen, one plugin or twenty.
//
// Everything destructive comes through here. A row's Disable and Remove
// buttons post a single id; the bar under the list posts whatever is ticked.
// Both land on the same confirmation, which is the only place that asks the
// question worth asking — whether to restart the server afterwards — and the
// only place that says what that costs.
//
// The alternative was what was here before: a row acting immediately, and a
// banner afterwards saying a restart was owed. That taught an operator to
// press first and read second, and it meant the answer to "does this need a
// restart" arrived after the moment it could have mattered.

// handleActOnPluginsForm asks before it does anything.
func (s *Server) handleActOnPluginsForm(w http.ResponseWriter, r *http.Request) {
	if !s.pageAdmin(w, r) {
		return
	}
	if err := r.ParseForm(); err != nil {
		s.renderPlugins(w, r, "that form could not be read: "+err.Error(), "")
		return
	}
	do := r.Form.Get("do")
	ids := r.Form["ids"]

	model, err := s.pluginActModel(r, do, ids)
	if err != nil {
		// Nothing selected, or a verb this server does not have. Back to the
		// list with the reason rather than a screen asking about nothing.
		s.renderPlugins(w, r, err.Error(), "")
		return
	}
	// A whole page, not a fragment: this is a screen somebody stops on and
	// reads, so it needs the frame around it like any other. No fragment name,
	// because there is no half of it worth swapping into something else.
	s.renderPage(w, r, "cog.page.pluginact", "", "Plugins", model)
}

// handleRunPluginActForm is the answer to that question.
func (s *Server) handleRunPluginActForm(w http.ResponseWriter, r *http.Request) {
	if !s.pageAdmin(w, r) {
		return
	}
	if err := r.ParseForm(); err != nil {
		s.renderPlugins(w, r, "that form could not be read: "+err.Error(), "")
		return
	}
	do := r.Form.Get("do")
	ids := r.Form["ids"]
	restart := r.Form.Get("restart") == "1"

	store, err := plugin.Open(s.dataDir)
	if err != nil {
		s.renderPlugins(w, r, err.Error(), "")
		return
	}

	var done, failed []string
	for _, id := range ids {
		var err error
		switch do {
		case "disable":
			err = store.Disable(id)
		case "remove":
			err = store.Remove(id)
		case "update":
			err = s.updateOnePlugin(r, store, id)
		default:
			s.renderPlugins(w, r, "this server has no such action: "+do, "")
			return
		}
		if err != nil {
			// Named individually. "3 of 5 failed" is a number; which three is
			// the thing somebody can act on.
			failed = append(failed, id+" ("+err.Error()+")")
			continue
		}
		done = append(done, id)
	}

	// Once, after all of them, rather than per plugin: recomposing five times
	// to remove five plugins is four rebuilds nobody asked for.
	s.recomposePlugins()
	slog.Info("plugins acted on", "action", do, "done", strings.Join(done, ","),
		"failed", len(failed), "by", callerFrom(r.Context()).Name)

	if len(failed) > 0 {
		s.renderPlugins(w, r, strings.Join(failed, "; "), pluginActNotice(do, done))
		return
	}
	if restart && canRestart() {
		// The page first, then the restart — the same order handleRestart
		// uses and for the same reason: a browser whose connection is replaced
		// by the exec mid-response cannot tell "restarting" from "died".
		s.renderPlugins(w, r, "", pluginActNotice(do, done)+" Restarting now — this page will reconnect on its own.")
		s.restartSoon(callerFrom(r.Context()).Name)
		return
	}
	s.renderPlugins(w, r, "", pluginActNotice(do, done))
}

func pluginActNotice(do string, done []string) string {
	if len(done) == 0 {
		return ""
	}
	what := strings.Join(done, ", ")
	switch do {
	case "disable":
		return what + " switched off."
	case "remove":
		return what + " removed."
	case "update":
		return what + " updated, and switched off. Read the new code, then approve it."
	}
	return what + " done."
}

// pluginActModel is what the confirmation screen shows.
//
// Everything on it is read from disk NOW rather than carried in the form: a
// form says which ids were ticked and nothing else, and a screen that took its
// version numbers and its restart warning from a POST body would be a screen
// somebody could write.
func (s *Server) pluginActModel(r *http.Request, do string, ids []string) (view.PluginAct, error) {
	switch do {
	case "disable", "remove", "update":
	default:
		return view.PluginAct{}, fmt.Errorf("this server has no such action: %q", do)
	}
	if len(ids) == 0 {
		return view.PluginAct{}, fmt.Errorf("nothing was selected — tick a plugin first")
	}

	model := view.PluginAct{
		Ctx: s.viewCtx(r, callerFrom(r.Context())),
		Do:  do, Disable: do == "disable", Remove: do == "remove", Update: do == "update",
		CanRestart: canRestart(),
	}

	store, err := plugin.Open(s.dataDir)
	if err != nil {
		return view.PluginAct{}, err
	}
	installed, err := store.List()
	if err != nil {
		return view.PluginAct{}, err
	}
	by := map[string]plugin.Installed{}
	for _, in := range installed {
		by[in.ID] = in
	}

	// Only for an update, and only once: the catalog is a network fetch and
	// this screen must not make one per selected plugin.
	var newer map[string]string
	if model.Update {
		newer = s.newerVersions(r, installed)
	}

	for _, id := range ids {
		in, known := by[id]
		if !known {
			model.Items = append(model.Items, view.PluginActRow{
				ID: id, Name: id, Note: "not installed here any more",
			})
			continue
		}
		row := view.PluginActRow{
			ID: in.ID, Name: in.Manifest.Name, Version: in.Version,
			Backend: strings.TrimSpace(in.Manifest.Needs) != "",
		}
		if row.Name == "" {
			row.Name = in.ID
		}
		switch {
		case model.Disable && !in.Enabled:
			row.Note = "already off"
		case model.Update:
			if v, ok := newer[in.ID]; ok {
				row.Note = in.Version + " → " + v
			} else {
				row.Note = "nothing newer in the catalog"
			}
		}
		if row.Backend && s.needsRestart(in.ID) {
			model.NeedsRestart = true
		}
		model.Items = append(model.Items, row)
	}

	model.Count = len(model.Items)
	model.Single = model.Count == 1
	if model.Single {
		model.Only = model.Items[0].Name
	}
	return model, nil
}

// newerVersions is what the catalog offers for what is installed.
//
// A failure here is not a failure of the screen: the catalog is fetched over
// the network, and an install that cannot reach it can still be told which
// plugins were selected. The rows simply say nothing about versions.
func (s *Server) newerVersions(r *http.Request, installed []plugin.Installed) map[string]string {
	c := s.pluginCatalog()
	idx, err := c.Fetch(r.Context())
	if err != nil {
		slog.Warn("the plugin catalog could not be reached, so this screen cannot say what is newer",
			"err", err)
		return nil
	}
	newer := map[string]string{}
	for _, in := range installed {
		e, found := idx.Find(in.ID)
		if !found || e.Version == "" || e.Version == in.Version {
			continue
		}
		newer[in.ID] = e.Version
	}
	return newer
}

// updateOnePlugin replaces one plugin with the version the catalog lists.
//
// It arrives switched off, exactly as a fresh install does. An update is new
// code, and code is approved after somebody has read it — an update that
// silently kept its approval would be the one way to get unread code running
// on an install that otherwise refuses it.
func (s *Server) updateOnePlugin(r *http.Request, store *plugin.Store, id string) error {
	c := s.pluginCatalog()
	idx, err := c.Fetch(r.Context())
	if err != nil {
		return fmt.Errorf("the catalog could not be reached: %w", err)
	}
	e, found := idx.Find(id)
	if !found {
		return fmt.Errorf("the catalog does not list %s, so there is nothing to update it to", id)
	}
	if _, _, err := c.InstallFromCatalog(r.Context(), store, e, ""); err != nil {
		return err
	}
	return nil
}
