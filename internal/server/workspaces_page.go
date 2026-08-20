package server

import (
	"encoding/json"
	"fmt"
	"github.com/orkcom-tech/cogitorium/internal/bundle"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/orkcom-tech/cogitorium/internal/view"
	"github.com/orkcom-tech/cogitorium/internal/workflow"
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
		// The house orchestrator's model, if somebody has set one on the Models
		// screen. That is what setting it is FOR: a workspace should not have to
		// be told the same answer every time it is made.
		modelID = s.orchestratorModelID(r.Context())
	}
	if modelID == 0 {
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
	s.done(w, r, "/workspaces")
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
	s.done(w, r, "/workspaces")
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
	s.done(w, r, "/workspaces")
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

	// Somebody was sent here from a screen or a control that is an
	// administrator's. Saying which one, and saying it FIRST, so a complaint
	// this list happens to have of its own cannot be mistaken for the answer.
	if refused := refusedScreen(r.URL.Query().Get("refused")); refused != "" && model.Error == "" {
		model.Error = refused
	}

	// What an orchestrator could think with. Offered rather than assumed: a
	// form with an empty picker lets somebody fill in a name and discover
	// afterwards that there is nothing to run it.
	if models, err := s.catalog.ListModels(r.Context()); err == nil {
		for _, m := range models {
			// A model need not have a label, and most do not. Showing the
			// label alone rendered the option as "— house": the provider, a
			// dash, and no sign of WHICH model it was.
			label := m.Label
			if strings.TrimSpace(label) == "" {
				label = m.ModelName
			}
			model.Models = append(model.Models, view.Model{
				ID: m.ID, Name: m.ModelName, Label: label,
				Provider: m.ProviderName, Kind: m.ProviderType,
			})
		}
	}
	// The same template the Models screen shows, so the picker here opens on
	// whatever was chosen there.
	model.Orchestrator = s.orchestratorTemplate(r, model.Models)

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
		// Its newest version, and whether it still describes what is running.
		// A number on its own says what was recorded; what somebody scanning
		// this list wants to know is which workflows have drifted from it.
		if s.versions != nil {
			if latest, err := s.versions.Latest(r.Context(), ws.ID); err == nil {
				row.Version = latest.Number
				if now, err := workflow.Take(r.Context(), s.workflowStores(), ws.ID); err == nil {
					row.Unsaved = !workflow.Same(latest.Snapshot, now)
				}
			}
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
	s.done(w, r, "/workspaces")
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
		// The order a workflow runs in is the workflow, not an extra: an
		// imported workspace whose plans stayed behind is a set of agents with
		// nothing telling them what comes first.
		IncludePlanboards: true,
	})
	if err != nil {
		s.renderWorkspaces(w, r, err.Error(), "")
		return
	}
	// What did not arrive, before anything else.
	//
	// The import already worked all of this out and the screen used to throw
	// it away: a bundle whose gears were skipped and whose agents lost their
	// models imported "successfully" and dropped somebody into a workspace
	// with agents that cannot think. Being told afterwards is the difference
	// between a workspace you can fix and one you have to diagnose.
	if notes := importNotes(res); len(notes) > 0 {
		s.renderWorkspaces(w, r, "", strings.Join(notes, " · "))
		return
	}
	http.Redirect(w, r, "/workspaces/"+strconv.FormatInt(res.Workspace.ID, 10), http.StatusSeeOther)
}

// importNotes is what an operator has to know about an import that worked.
//
// Every one of these is a workflow that is quietly not the workflow that was
// exported, which is exactly the case where silence costs the most.
func importNotes(res bundle.Result) []string {
	var notes []string
	for _, g := range res.GearsSkipped {
		notes = append(notes, fmt.Sprintf("the gear %q did not arrive: %s", g.Name, g.Why))
	}
	for _, m := range res.MCPSkipped {
		notes = append(notes, fmt.Sprintf("the MCP server %q did not arrive: %s", m.Name, m.Why))
	}
	for _, u := range res.UnresolvedModels {
		notes = append(notes, fmt.Sprintf(
			"%s has no model: this install has no %s serving %s, so it cannot think until you give it one",
			u.Agent, u.ProviderType, u.ModelName))
	}
	return notes
}

// refusedScreen turns the segment a refusal came from into a sentence.
//
// A closed list rather than the raw path: what arrives here is a URL segment,
// and a URL segment reflected into a page is a way to write a sentence on
// somebody else's screen.
func refusedScreen(segment string) string {
	switch segment {
	case "/people":
		return "People is an administrator's screen: accounts, roles and teams reach every workspace on this install."
	case "/plugins":
		return "Plugins is an administrator's screen: a plugin runs code and can take over any screen in the product."
	case "/context":
		return "Context is an administrator's screen: it reads and writes every document on this install. What your agents read is Memory, on each agent."
	case "/env":
		return "Named values are an administrator's: one name here reaches every workspace on this install."
	case "/terminal":
		return "The terminal is an administrator's: it is a shell on the machine this server runs on."
	case "/gears":
		return "Writing a gear is an administrator's: a gear is code that runs on this machine. You can read the catalogue and use what is approved."
	case "/models":
		return "The model catalog is an administrator's: it holds the credentials this install talks to providers with."
	case "":
		return ""
	}
	return "That is an administrator's screen."
}
