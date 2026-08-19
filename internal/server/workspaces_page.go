package server

import (
	"encoding/json"
	"github.com/orkcom-tech/cogitorium/internal/bundle"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/orkcom-tech/cogitorium/internal/view"
)

// The landing screen, served as a template.
//
// Sharing is a list rather than a picker, and that is the shape the model
// carries: a workspace can go to any number of teams, and each is withdrawn on
// its own. A picker would make "also share with Research" read as "stop
// sharing with Platform", which is the opposite of what somebody meant.

func (s *Server) handleWorkspacesPage(w http.ResponseWriter, r *http.Request) {
	s.renderWorkspaces(w, r, "", "")
}

func (s *Server) handleCreateWorkspaceForm(w http.ResponseWriter, r *http.Request) {
	caller := callerFrom(r.Context())
	if err := r.ParseForm(); err != nil {
		s.renderWorkspaces(w, r, "that form could not be read", "")
		return
	}
	modelID, err := strconv.ParseInt(r.PostFormValue("orchestrator_model_id"), 10, 64)
	if err != nil {
		s.renderWorkspaces(w, r, "a workspace needs a model for its orchestrator to think with", "")
		return
	}
	ws, err := s.workspaces.CreateWorkspace(r.Context(),
		strings.TrimSpace(r.PostFormValue("name")), r.PostFormValue("description"),
		modelID, caller.ID)
	if err != nil {
		s.renderWorkspaces(w, r, err.Error(), "")
		return
	}
	// Straight into it. Somebody who just made a workspace wants to talk to
	// its orchestrator, not to look at a list with one more row.
	http.Redirect(w, r, "/workspaces/"+strconv.FormatInt(ws.ID, 10), http.StatusSeeOther)
}

func (s *Server) handleCloneWorkspaceForm(w http.ResponseWriter, r *http.Request) {
	caller := callerFrom(r.Context())
	id, ok := workspacePathID(w, r, s)
	if !ok {
		return
	}
	_ = r.ParseForm()
	name := strings.TrimSpace(r.PostFormValue("name"))
	if name == "" {
		s.renderWorkspaces(w, r, "a copy needs a name", "")
		return
	}
	if _, err := s.workspaces.Clone(r.Context(), id, name, caller.ID); err != nil {
		s.renderWorkspaces(w, r, err.Error(), "")
		return
	}
	s.renderWorkspaces(w, r, "", "Copied into "+name+". Its history stays where it was.")
}

func (s *Server) handleDeleteWorkspaceForm(w http.ResponseWriter, r *http.Request) {
	id, ok := workspacePathID(w, r, s)
	if !ok {
		return
	}
	// Owner or administrator, checked here rather than only drawn: a form
	// hidden by a template is a form somebody can still post.
	ws, err := s.workspaces.GetWorkspace(r.Context(), id)
	if err != nil {
		s.renderWorkspaces(w, r, err.Error(), "")
		return
	}
	caller := callerFrom(r.Context())
	if !caller.IsAdmin() && (ws.OwnerID == nil || *ws.OwnerID != caller.ID) {
		s.renderWorkspaces(w, r, "that workspace is somebody else's to delete", "")
		return
	}
	if err := s.workspaces.DeleteWorkspace(r.Context(), id); err != nil {
		s.renderWorkspaces(w, r, err.Error(), "")
		return
	}
	s.renderWorkspaces(w, r, "", "")
}

func (s *Server) handleShareWorkspaceForm(w http.ResponseWriter, r *http.Request) {
	if !s.pageAdmin(w, r) {
		return
	}
	id, ok := workspacePathID(w, r, s)
	if !ok {
		return
	}
	_ = r.ParseForm()
	teamID, err := strconv.ParseInt(r.PostFormValue("team_id"), 10, 64)
	if err != nil {
		s.renderWorkspaces(w, r, "pick a team to share it with", "")
		return
	}
	if _, err := s.workspaces.ShareWith(r.Context(), id, teamID); err != nil {
		s.renderWorkspaces(w, r, err.Error(), "")
		return
	}
	s.renderWorkspaces(w, r, "", "")
}

func (s *Server) handleUnshareWorkspaceForm(w http.ResponseWriter, r *http.Request) {
	if !s.pageAdmin(w, r) {
		return
	}
	id, ok := workspacePathID(w, r, s)
	if !ok {
		return
	}
	_ = r.ParseForm()
	teamID, err := strconv.ParseInt(r.PostFormValue("team_id"), 10, 64)
	if err != nil {
		s.renderWorkspaces(w, r, "that is not a team", "")
		return
	}
	if _, err := s.workspaces.Unshare(r.Context(), id, teamID); err != nil {
		s.renderWorkspaces(w, r, err.Error(), "")
		return
	}
	s.renderWorkspaces(w, r, "", "")
}

func workspacePathID(w http.ResponseWriter, r *http.Request, s *Server) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		s.renderWorkspaces(w, r, "that is not a workspace", "")
		return 0, false
	}
	return id, true
}

func (s *Server) renderWorkspaces(w http.ResponseWriter, r *http.Request, problem, notice string) {
	caller := callerFrom(r.Context())
	model := view.Workspaces{
		Ctx:       s.viewCtx(r, caller),
		Error:     problem,
		Notice:    notice,
		Creating:  r.URL.Query().Get("new") != "",
		Importing: r.URL.Query().Get("import") != "",
	}

	// What an orchestrator could think with. Offered rather than assumed: a
	// form with an empty picker lets somebody fill in a name and discover
	// afterwards that there is nothing to run it.
	if models, err := s.catalog.ListModels(r.Context()); err == nil {
		for _, m := range models {
			model.Models = append(model.Models, view.Model{
				ID: m.ID, Name: m.ModelName, Label: m.Label,
				Provider: m.ProviderName, Kind: m.ProviderType,
			})
		}
	}

	teams, _ := s.identity.ListTeams(r.Context())
	byID := map[int64]string{}
	for _, t := range teams {
		byID[t.ID] = t.Name
	}

	list, err := s.workspaces.ListWorkspacesFor(r.Context(), caller)
	if err != nil {
		model.Error = err.Error()
	}
	for _, ws := range list {
		mine := ws.OwnerID != nil && *ws.OwnerID == caller.ID
		row := view.WorkspaceRow{
			ID: ws.ID, Name: ws.Name, Description: ws.Description,
			Mine: mine, SharedWithMe: !mine,
			// Owner or administrator. Drawn from the same fact the handler
			// checks, so the button and the refusal cannot disagree.
			MayDelete: mine || caller.IsAdmin(),
			MayShare:  caller.IsAdmin(),
		}
		if ws.Hue != nil {
			row.Hue, row.HasHue = *ws.Hue, true
		}
		for _, h := range view.Palette {
			row.Palette = append(row.Palette, view.Hue{
				Degrees: h, Chosen: ws.Hue != nil && *ws.Hue == h,
			})
		}
		shared := map[int64]bool{}
		for _, id := range ws.TeamIDs {
			shared[id] = true
			row.Shared = append(row.Shared, view.WorkspaceTeam{ID: id, Name: byID[id]})
		}
		// Only the ones it could still go to. Offering a team it already has
		// makes "share" a button that does nothing.
		for _, t := range teams {
			if !shared[t.ID] {
				row.Teams = append(row.Teams, view.WorkspaceTeam{ID: t.ID, Name: t.Name})
			}
		}
		model.Items = append(model.Items, row)
	}

	s.renderPage(w, r, "cog.page.workspaces", "cog.list.workspaces", "Workspaces", model)
}

// handleColourWorkspaceForm gives a workspace a colour, or takes it back.
//
// An empty hue is not grey. It hands the workspace back the colour derived
// from its id and records that nobody picked — which is a different state from
// somebody choosing the shade that colour happens to be.
func (s *Server) handleColourWorkspaceForm(w http.ResponseWriter, r *http.Request) {
	id, ok := workspacePathID(w, r, s)
	if !ok {
		return
	}
	_ = r.ParseForm()

	var hue *int
	if raw := strings.TrimSpace(r.PostFormValue("hue")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil {
			s.renderWorkspaces(w, r, "a colour is a whole number of degrees", "")
			return
		}
		hue = &n
	}
	if _, err := s.workspaces.SetHue(r.Context(), id, hue); err != nil {
		s.renderWorkspaces(w, r, err.Error(), "")
		return
	}
	s.renderWorkspaces(w, r, "", "")
}

// handleImportWorkspaceForm builds a workspace from a bundle somebody exported.
//
// The file arrives as an upload rather than as JSON in a field: a bundle is a
// file somebody has on disk, and asking them to paste it into a textarea would
// be asking them to work around the form.
func (s *Server) handleImportWorkspaceForm(w http.ResponseWriter, r *http.Request) {
	caller := callerFrom(r.Context())
	if err := r.ParseMultipartForm(maxBundleBytes); err != nil {
		s.renderWorkspaces(w, r, "that bundle could not be read: "+err.Error(), "")
		return
	}
	file, _, err := r.FormFile("bundle")
	if err != nil {
		s.renderWorkspaces(w, r, "pick a bundle file to import", "")
		return
	}
	defer file.Close()

	var b bundle.Bundle
	if err := json.NewDecoder(io.LimitReader(file, maxBundleBytes)).Decode(&b); err != nil {
		s.renderWorkspaces(w, r, "that file is not a workspace bundle: "+err.Error(), "")
		return
	}

	res, err := bundle.Import(r.Context(), s.bundleStores(), b, bundle.ImportOptions{
		Name:    strings.TrimSpace(r.PostFormValue("name")),
		OwnerID: caller.ID,
		// Its gears arrive unapproved, like anything else that arrives. The
		// checkbox decides whether they come at all, never whether they run.
		IncludeGears:   r.PostFormValue("gears") == "on",
		IncludeContext: true,
	})
	if err != nil {
		s.renderWorkspaces(w, r, err.Error(), "")
		return
	}
	http.Redirect(w, r, "/workspaces/"+strconv.FormatInt(res.Workspace.ID, 10), http.StatusSeeOther)
}
