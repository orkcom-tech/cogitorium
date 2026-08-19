package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/orkcom-tech/cogitorium/internal/view"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/orkcom-tech/cogitorium/internal/plugin"
	"github.com/orkcom-tech/cogitorium/internal/update"
)

// Changing the plugin set from the browser.
//
// Every verb here answers with whether a restart is actually needed, and the
// answer is computed rather than assumed. Installing does not need one: a new
// plugin arrives switched off, so nothing that is running has changed.
// Enabling, disabling, reordering and removing something that was live all do.
//
// Saying "restart required" after every action would be easier and would teach
// an operator to ignore it, which is the same as not saying it.

// maxPluginUpload bounds an uploaded bundle. Generous next to what a template
// plugin weighs, and far under what the unpacker will refuse.
const maxPluginUpload = 64 << 20

type pluginActionResult struct {
	// Restart reports whether what is running differs from what is now on
	// disk. False is the interesting value: it means the operator can stop
	// reading.
	Restart bool `json:"restart_required"`
	// Plugin is the affected plugin as the library screen shows it, so the
	// browser does not have to re-fetch the list to redraw one card.
	Plugin *PluginView `json:"plugin,omitempty"`
	// Message is what to tell the person, in their terms.
	Message string `json:"message"`
}

// handleUploadPlugin installs a bundle sent from the browser.
//
// This is the path somebody developing a plugin actually uses: build a zip,
// drop it on the page, reload. Requiring them to reach the server's filesystem
// to try their own work would make the local install the least convenient one.
func (s *Server) handleUploadPlugin(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAdmin(w, r); !ok {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxPluginUpload)

	f, name, err := firstUploadedFile(r)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge,
				fmt.Sprintf("this bundle is larger than the %d MB upload limit", maxPluginUpload>>20))
			return
		}
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	defer f.Close()

	// The unpacker reads a file rather than a stream, because it opens the
	// archive twice — once for the manifest before anything touches disk, and
	// once to expand it. Spooling here keeps that property rather than
	// weakening it to suit an upload.
	tmp, err := os.CreateTemp("", "cogitorium-plugin-*.zip")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "the upload could not be stored")
		return
	}
	defer os.Remove(tmp.Name())
	if _, err := io.Copy(tmp, f); err != nil {
		tmp.Close()
		writeError(w, http.StatusBadRequest, "the upload could not be read: "+err.Error())
		return
	}
	tmp.Close()

	store, ok := s.pluginStore(w)
	if !ok {
		return
	}
	in, digest, err := store.Install(tmp.Name())
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	slog.Info("plugin installed from an upload",
		"plugin", in.ID, "version", in.Version, "file", filepath.Base(name), "digest", digest)

	v := s.pluginView(in, s.pluginCaps())
	writeJSON(w, http.StatusOK, pluginActionResult{
		// Nothing running changed: it arrived switched off, which is what
		// makes install-then-approve possible in the first place.
		Restart: false,
		Plugin:  &v,
		Message: fmt.Sprintf("%s %s installed and switched off.", in.Manifest.Name, in.Version),
	})
}

// firstUploadedFile takes the first part that has a filename, matching how
// inlet delivery reads an upload — one convention for "a file arrived".
func firstUploadedFile(r *http.Request) (io.ReadCloser, string, error) {
	base, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if !strings.HasPrefix(base, "multipart/") {
		return nil, "", errors.New("send the bundle as a multipart form with the file as a part")
	}
	mr, err := r.MultipartReader()
	if err != nil {
		return nil, "", fmt.Errorf("this multipart body could not be read: %w", err)
	}
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			return nil, "", errors.New("no file was found in this multipart body: send it as a part with a filename")
		}
		if err != nil {
			return nil, "", fmt.Errorf("this multipart body could not be read: %w", err)
		}
		if part.FileName() != "" {
			return part, part.FileName(), nil
		}
		part.Close()
	}
}

func (s *Server) handleEnablePlugin(w http.ResponseWriter, r *http.Request) {
	s.pluginSwitch(w, r, true)
}

func (s *Server) handleDisablePlugin(w http.ResponseWriter, r *http.Request) {
	s.pluginSwitch(w, r, false)
}

func (s *Server) pluginSwitch(w http.ResponseWriter, r *http.Request, on bool) {
	if _, ok := requireAdmin(w, r); !ok {
		return
	}
	store, ok := s.pluginStore(w)
	if !ok {
		return
	}
	id := r.PathValue("id")

	before, err := store.Order()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if on {
		err = store.Enable(id)
	} else {
		err = store.Disable(id)
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	after, err := store.Order()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	in, err := store.Get(id)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	in.Enabled, in.Order = positionIn(after, id)
	v := s.pluginView(in, s.pluginCaps())

	changed := !sameOrder(before, after)
	verb := "disabled"
	if on {
		verb = "enabled"
	}
	slog.Info("plugin "+verb, "plugin", id, "by", callerFrom(r.Context()).Name)
	writeJSON(w, http.StatusOK, pluginActionResult{
		Restart: changed,
		Plugin:  &v,
		Message: restartLine(fmt.Sprintf("%s %s.", in.Manifest.Name, verb), changed),
	})
}

// handleOrderPlugins replaces the enable list.
//
// Position is precedence, so this is not cosmetic: it decides which plugin
// renders when two define the same template name.
func (s *Server) handleOrderPlugins(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAdmin(w, r); !ok {
		return
	}
	var in struct {
		Order []string `json:"order"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	store, ok := s.pluginStore(w)
	if !ok {
		return
	}
	before, err := store.Order()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := store.Reorder(in.Order); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	changed := !sameOrder(before, in.Order)
	slog.Info("plugin order set", "order", strings.Join(in.Order, ","),
		"by", callerFrom(r.Context()).Name)
	writeJSON(w, http.StatusOK, pluginActionResult{
		Restart: changed,
		Message: restartLine("Order saved.", changed),
	})
}

func (s *Server) handleRemovePlugin(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAdmin(w, r); !ok {
		return
	}
	store, ok := s.pluginStore(w)
	if !ok {
		return
	}
	id := r.PathValue("id")

	// Whether it was live decides whether anything running changes. Asked
	// before the removal, because afterwards there is nothing to ask.
	wasLive := false
	if s.plugins != nil {
		for _, loaded := range s.plugins.report.Loaded {
			if loaded == id {
				wasLive = true
			}
		}
	}
	if err := store.Remove(id); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	slog.Info("plugin removed", "plugin", id, "by", callerFrom(r.Context()).Name)
	writeJSON(w, http.StatusOK, pluginActionResult{
		Restart: wasLive,
		Message: restartLine(fmt.Sprintf("%s removed.", id), wasLive),
	})
}

// handlePreviewPlugin renders one name this plugin overrides, against an
// example, so somebody can see what they are agreeing to.
//
// "It overrides cog.row.nav" is a fact an operator cannot evaluate. A picture
// of the row as it will render is one they can, and it is the difference
// between approval as a ritual and approval as a decision.
//
// Through the composed stack, so what comes back is what will actually render
// — including another plugin layered over the same name. Rendering the
// plugin's own file in isolation would show something nobody will ever see.
func (s *Server) handlePreviewPlugin(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAdmin(w, r); !ok {
		return
	}
	id := r.PathValue("id")
	name := r.URL.Query().Get("name")
	if name == "" {
		writeError(w, http.StatusBadRequest, "which name?")
		return
	}
	if s.plugins == nil || s.plugins.set == nil {
		writeError(w, http.StatusServiceUnavailable, "nothing is composed on this install")
		return
	}
	// Only what this plugin actually contributes. Otherwise this is a way to
	// render any template with any exemplar through an id in a URL.
	owns := false
	for _, e := range s.plugins.set.Ledger().For(id) {
		if e.Name == name {
			owns = true
		}
	}
	if !owns {
		writeError(w, http.StatusNotFound,
			fmt.Sprintf("%s does not contribute %s on this install", id, name))
		return
	}

	html, err := view.Preview(s.plugins.set, name)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"name": name,
		"html": html,
		// Said rather than left to be noticed. An override that renders
		// nothing looks identical to a preview that failed to load.
		"empty": strings.TrimSpace(html) == "",
	})
}

// handleApprovePlugin records the operator's decision about what is installed.
//
// It approves the CONTENT on disk, not the name: the digest is read from what
// this machine holds rather than taken from the request, so a decision can
// only ever be about bytes somebody could have looked at.
func (s *Server) handleApprovePlugin(w http.ResponseWriter, r *http.Request) {
	caller, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	store, ok := s.pluginStore(w)
	if !ok {
		return
	}
	id := r.PathValue("id")

	a, err := store.Approve(id, caller.Name)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	// Written down at the moment it happens, because this is the decision the
	// whole product's trust story rests on and a log is where somebody looks
	// afterwards to find out who made it.
	slog.Warn("a plugin was approved",
		"plugin", id, "version", a.Version, "digest", a.Digest, "by", caller.Name)

	in, err := store.Get(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	in.Pending, in.Approval = store.Pending(id), a
	v := s.pluginView(in, s.pluginCaps())
	writeJSON(w, http.StatusOK, pluginActionResult{
		// Approving changes nothing that is running: it makes enabling
		// possible, and enabling is what needs the restart.
		Restart: false,
		Plugin:  &v,
		Message: fmt.Sprintf("%s %s approved. Enable it to put it in the layer order.",
			in.Manifest.Name, in.Version),
	})
}

// handleRevokePlugin withdraws the decision and disables the plugin.
func (s *Server) handleRevokePlugin(w http.ResponseWriter, r *http.Request) {
	caller, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	store, ok := s.pluginStore(w)
	if !ok {
		return
	}
	id := r.PathValue("id")

	wasLive := false
	if s.plugins != nil {
		for _, loaded := range s.plugins.report.Loaded {
			if loaded == id {
				wasLive = true
			}
		}
	}
	if err := store.Revoke(id); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	slog.Warn("a plugin's approval was withdrawn", "plugin", id, "by", caller.Name)
	writeJSON(w, http.StatusOK, pluginActionResult{
		Restart: wasLive,
		Message: restartLine(fmt.Sprintf("%s is no longer approved and has been disabled.", id), wasLive),
	})
}

func (s *Server) pluginStore(w http.ResponseWriter) (*plugin.Store, bool) {
	store, err := plugin.Open(s.dataDir)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "the plugin directory could not be opened")
		return nil, false
	}
	return store, true
}

func restartLine(msg string, restart bool) string {
	if !restart {
		return msg
	}
	return msg + " Restart Cogitorium to apply it — what is running has not changed yet."
}

func positionIn(order []string, id string) (bool, int) {
	for i, existing := range order {
		if existing == id {
			return true, i
		}
	}
	return false, -1
}

func sameOrder(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// ── the catalog ───────────────────────────────────────────────────────────

// pluginCatalog builds a client gated on the same consent the MCP registry and the
// update check already answer to. One switch, not three: an operator who said
// this install may not reach out said it once.
func (s *Server) pluginCatalog() *plugin.Catalog {
	return plugin.NewCatalog(s.dataDir, nil, func() bool {
		return s.updates.Mode() != update.ModeOff
	})
}

// CatalogEntryView is one listing, plus what this install already knows about
// it. The second half is what stops the browse screen being a list somebody
// has to cross-reference against the installed one by eye.
type CatalogEntryView struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Author      string `json:"author"`
	Description string `json:"description"`
	Repo        string `json:"repo"`
	Source      string `json:"source"`
	// Installed and InstalledVersion say whether this machine already has it.
	Installed        bool   `json:"installed"`
	InstalledVersion string `json:"installed_version,omitempty"`

	// Verified is what the team said about it: read this version, read a
	// different one, or nobody has looked. Three states rather than a badge,
	// because a badge that survives a version change is a badge about a name
	// rather than about code.
	Verified        string `json:"verified"`
	VerifiedVersion string `json:"verified_version,omitempty"`
	VerifiedBy      string `json:"verified_by,omitempty"`
	VerifiedNote    string `json:"verified_note,omitempty"`

	// Version is what the catalog is currently offering, filled in by the
	// catalog's own index job rather than by the author. Empty when no index
	// has been published, and empty is reported rather than guessed at: a
	// screen that cannot tell should say so, not say "up to date".
	Version string `json:"version,omitempty"`
	// Update is set when what is offered is newer than what is installed.
	// Computed rather than compared on the client, because "newer" is a
	// version comparison and a string compare gets 1.10 wrong.
	Update bool `json:"update,omitempty"`
}

type catalogView struct {
	Entries []CatalogEntryView `json:"entries"`
	// Updates is every installed plugin the catalog offers a newer version of,
	// computed here from the whole index. Present on the browse response
	// rather than behind a route of its own: the answer comes from the file
	// that was just fetched, and a second endpoint would fetch it twice to
	// give the same answer.
	//
	// Empty when no index.json has been published — which is "cannot tell"
	// rather than "nothing to do", and the screen says which.
	Updates []UpdateView `json:"updates"`
	// Versioned is whether the catalog told us any versions at all. Without it
	// an empty Updates list is ambiguous, and the ambiguity resolves the
	// flattering way by default.
	Versioned bool `json:"versioned"`
	// Cached and Fetched are shown rather than hidden. A cached list is not a
	// current one, and presenting yesterday's as today's is how somebody
	// installs a version that was withdrawn yesterday.
	Cached  bool   `json:"cached"`
	Fetched string `json:"fetched,omitempty"`
}

// UpdateView is one installed plugin with a newer version on offer.
type UpdateView struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Installed string `json:"installed"`
	Available string `json:"available"`
}

func (s *Server) handleBrowseCatalog(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAdmin(w, r); !ok {
		return
	}
	idx, err := s.pluginCatalog().Fetch(r.Context())
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	store, ok := s.pluginStore(w)
	if !ok {
		return
	}

	out := catalogView{Cached: idx.Cached, Entries: []CatalogEntryView{}, Updates: []UpdateView{}}
	if !idx.Fetched.IsZero() {
		out.Fetched = idx.Fetched.Format(time.RFC3339)
	}
	for _, e := range idx.Entries {
		if e.Version != "" {
			out.Versioned = true
			break
		}
	}

	// Computed over everything installed rather than over the search results:
	// somebody typing in the search box is not saying they stopped caring
	// about the other seven plugins that have updates.
	if installed, err := store.List(); err == nil {
		for _, u := range idx.Updates(installed) {
			out.Updates = append(out.Updates, UpdateView{
				ID: u.Entry.ID, Name: u.Entry.Name,
				Installed: u.Installed, Available: u.Available,
			})
		}
	}
	for _, e := range idx.Search(r.URL.Query().Get("q")) {
		v := CatalogEntryView{
			ID: e.ID, Name: e.Name, Author: e.Author,
			Description: e.Description, Repo: e.Repo, Source: e.SourceURL(),
		}
		v.Version = e.Version
		if in, err := store.Get(e.ID); err == nil {
			v.Installed, v.InstalledVersion = true, in.Version
			for _, u := range out.Updates {
				if u.ID == e.ID {
					v.Update = true
				}
			}
		}
		// Checked against the version this machine holds when it holds one, so
		// the answer is about the code here rather than about the listing.
		c := idx.Verify(e.ID, v.InstalledVersion)
		v.Verified, v.VerifiedVersion = c.State, c.Version
		v.VerifiedBy, v.VerifiedNote = c.By, c.Note
		out.Entries = append(out.Entries, v)
	}
	writeJSON(w, http.StatusOK, out)
}

// handleInstallFromCatalog downloads a listed plugin and installs it.
//
// It arrives switched off and unapproved, exactly like every other way a
// plugin gets onto this machine. Being in a catalog is not a decision anybody
// made about running it — it is a decision about listing it, which is a
// different thing and belongs to a different person.
func (s *Server) handleInstallFromCatalog(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAdmin(w, r); !ok {
		return
	}
	id := r.PathValue("id")

	c := s.pluginCatalog()
	idx, err := c.Fetch(r.Context())
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	e, found := idx.Find(id)
	if !found {
		writeError(w, http.StatusNotFound,
			fmt.Sprintf("the catalog does not list %q", id))
		return
	}
	store, ok := s.pluginStore(w)
	if !ok {
		return
	}

	in, digest, err := c.InstallFromCatalog(r.Context(), store, e, r.URL.Query().Get("version"))
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	slog.Info("plugin installed from the catalog",
		"plugin", in.ID, "version", in.Version, "from", e.SourceURL(), "digest", digest)

	in.Pending = store.Pending(in.ID)
	v := s.pluginView(in, s.pluginCaps())
	writeJSON(w, http.StatusOK, pluginActionResult{
		Restart: false,
		Plugin:  &v,
		Message: fmt.Sprintf("%s %s installed and switched off. Read the source, then approve it.",
			in.Manifest.Name, in.Version),
	})
}
