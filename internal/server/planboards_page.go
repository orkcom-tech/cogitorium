package server

import (
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/orkcom-tech/cogitorium/internal/planboard"
	"github.com/orkcom-tech/cogitorium/internal/view"
)

// Planboards, served as a template like the library beside it.
//
// The screen exists for one question a plan on its own cannot answer: how far
// has it got. A plan is text until it is attached to something, and then it is
// a marker moving through a workflow — so every row carries its position, in
// every workspace it is attached in, and the controls for moving that marker
// sit next to it rather than three screens away.

// stepSeparators are how a person writes "this step, and here is the detail".
// Both dashes, because a plan pasted out of a document has whichever one that
// document used and neither is a mistake worth an error message.
var stepSeparators = []string{" — ", " -- ", " – "}

// parseSteps reads the steps out of a textarea: one per line, in order.
//
// A form with an "add step" button and six growing rows is a form somebody
// fills in once. A list is how people write lists, and the order is the order
// the lines are in — which is also what makes a plan pasteable from wherever
// it was written down first.
func parseSteps(raw string) []planboard.Step {
	var out []planboard.Step
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// Numbering somebody typed is stripped: they meant the order, and the
		// order is already the line's position. Left in, "1." would become
		// part of the step's title and be read out to the model.
		line = strings.TrimSpace(strings.TrimLeft(line, "0123456789.)-• \t"))
		if line == "" {
			continue
		}
		step := planboard.Step{Title: line}
		for _, sep := range stepSeparators {
			if title, body, found := strings.Cut(line, sep); found {
				step = planboard.Step{Title: strings.TrimSpace(title), Body: strings.TrimSpace(body)}
				break
			}
		}
		out = append(out, step)
	}
	return out
}

func (s *Server) handlePlanboardsPage(w http.ResponseWriter, r *http.Request) {
	s.renderPage(w, r, "cog.page.planboards", "cog.frag.planboards", "Planboards", s.planboardsModel(r, ""))
}

// handleSavePlanboardForm creates a plan or replaces an existing one by name.
//
// It answers with the page rather than redirecting, so a rejection keeps what
// somebody typed beside the reason. A redirect would show them an empty form
// and a message about a plan they can no longer see.
func (s *Server) handleSavePlanboardForm(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.renderPlanboards(w, r, "that form could not be read")
		return
	}
	name := r.PostFormValue("name")
	if err := planboard.ValidateName(name); err != nil {
		s.renderPlanboards(w, r, err.Error())
		return
	}
	steps := parseSteps(r.PostFormValue("steps"))
	if len(steps) == 0 {
		s.renderPlanboards(w, r, "a plan with no steps is a name and nothing else — write one step per line")
		return
	}
	saved, err := s.plans.Save(r.Context(), name, r.PostFormValue("description"),
		splitTags(r.PostFormValue("tags")), planboard.Mode(r.PostFormValue("mode")), steps, 0, 0)
	if err != nil {
		s.renderPlanboards(w, r, err.Error())
		return
	}
	// Back to the plan, open, so somebody who just wrote six steps can read
	// them back in the order the engine will hand them out.
	http.Redirect(w, r, "/planboards?open="+strconv.FormatInt(saved.ID, 10), http.StatusSeeOther)
}

func (s *Server) handleDeletePlanboardForm(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		s.renderPlanboards(w, r, "that is not a plan")
		return
	}
	if err := s.plans.Delete(r.Context(), id); err != nil {
		s.renderPlanboards(w, r, err.Error())
		return
	}
	s.renderPlanboards(w, r, "")
}

// handleAttachPlanboardForm gives a plan to an agent, or to a whole workspace.
func (s *Server) handleAttachPlanboardForm(w http.ResponseWriter, r *http.Request) {
	id, wsID, agentID, err := s.planScopeFromForm(r)
	if err != nil {
		s.renderPlanboards(w, r, err.Error())
		return
	}
	if _, err := s.plans.Bind(r.Context(), id, wsID, agentID); err != nil {
		s.renderPlanboards(w, r, err.Error())
		return
	}
	s.renderPlanboards(w, r, "")
}

func (s *Server) handleDetachPlanboardForm(w http.ResponseWriter, r *http.Request) {
	id, wsID, agentID, err := s.planScopeFromForm(r)
	if err != nil {
		s.renderPlanboards(w, r, err.Error())
		return
	}
	if err := s.plans.Unbind(r.Context(), id, wsID, agentID); err != nil {
		s.renderPlanboards(w, r, err.Error())
		return
	}
	s.renderPlanboards(w, r, "")
}

// handleMovePlanboardForm moves the marker, for when the plan and the world
// have got out of step.
func (s *Server) handleMovePlanboardForm(w http.ResponseWriter, r *http.Request) {
	id, wsID, agentID, err := s.planScopeFromForm(r)
	if err != nil {
		s.renderPlanboards(w, r, err.Error())
		return
	}
	b, err := s.plans.Bind(r.Context(), id, wsID, agentID)
	if err != nil {
		s.renderPlanboards(w, r, err.Error())
		return
	}
	step, convErr := strconv.Atoi(strings.TrimSpace(r.PostFormValue("step")))
	if convErr != nil {
		s.renderPlanboards(w, r, "that is not a step number")
		return
	}
	if _, err := s.plans.Seek(r.Context(), b.ID, step); err != nil {
		s.renderPlanboards(w, r, err.Error())
		return
	}
	s.renderPlanboards(w, r, "")
}

// planScopeFromForm reads the three things every attachment control needs:
// which plan, which workspace, and whose position — an agent's, or the
// workspace's shared one.
func (s *Server) planScopeFromForm(r *http.Request) (planID, wsID int64, agentID *int64, err error) {
	if parseErr := r.ParseForm(); parseErr != nil {
		return 0, 0, nil, errBadForm
	}
	planID, convErr := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if convErr != nil {
		return 0, 0, nil, errNotAPlan
	}
	wsID, convErr = strconv.ParseInt(r.PostFormValue("ws"), 10, 64)
	if convErr != nil {
		return 0, 0, nil, errNoWorkspace
	}
	if name := strings.TrimSpace(r.PostFormValue("agent")); name != "" {
		a, agentErr := s.workspaces.GetAgentByName(r.Context(), wsID, name)
		if agentErr != nil {
			return 0, 0, nil, agentErr
		}
		agentID = &a.ID
	}
	return planID, wsID, agentID, nil
}

func (s *Server) renderPlanboards(w http.ResponseWriter, r *http.Request, msg string) {
	s.renderPage(w, r, "cog.page.planboards", "cog.frag.planboards", "Planboards", s.planboardsModel(r, msg))
}

// planboardsModel assembles the screen: the plans, and for each of them every
// place it is attached with the marker's position there.
func (s *Server) planboardsModel(r *http.Request, msg string) view.Planboards {
	q := r.URL.Query()
	model := view.Planboards{
		Ctx:      s.viewCtx(r, callerFrom(r.Context())),
		Query:    strings.TrimSpace(q.Get("q")),
		Tag:      strings.TrimSpace(q.Get("tag")),
		Error:    msg,
		Narrowed: strings.TrimSpace(q.Get("q")) != "" || strings.TrimSpace(q.Get("tag")) != "",
	}

	items, err := s.plans.List(r.Context(), model.Tag, model.Query)
	if err != nil {
		model.Error = err.Error()
	}

	// Every tag, not only the surviving ones: a filter you cannot get out of
	// because its option vanished is a trap.
	all, _ := s.plans.List(r.Context(), "", "")
	seen := map[string]bool{}
	for _, p := range all {
		for _, t := range p.Tags {
			seen[t] = true
		}
	}
	for t := range seen {
		model.Tags = append(model.Tags, view.Tag{Name: t, Selected: t == model.Tag})
	}
	sort.Slice(model.Tags, func(i, j int) bool { return model.Tags[i].Name < model.Tags[j].Name })

	// Every attachment in the install, gathered once. A plan can be attached
	// in several workspaces, and the page is global, so "where has this got
	// to" is a question with more than one answer.
	positions := s.planPositions(r)

	open, _ := strconv.ParseInt(q.Get("open"), 10, 64)
	for _, p := range items {
		row := view.PlanboardRow{
			ID: p.ID, Name: p.Name, Description: p.Description, Tags: p.Tags,
			UpdatedAt: p.UpdatedAt,
			Resume:    p.Mode == planboard.ModeResume,
			Restart:   p.Mode == planboard.ModeRestart,
			Open:      p.ID == open && open != 0,
			Attached:  positions[p.ID],
		}
		for _, st := range p.Steps {
			row.Steps = append(row.Steps, view.PlanStepRow{Ordinal: st.Ordinal, Title: st.Title, Body: st.Body})
		}
		model.Items = append(model.Items, row)
	}
	return model
}

// planPositions is where every attached plan stands, keyed by plan.
//
// A workspace that cannot be read is skipped rather than failing the page: one
// broken workspace must not make every plan unreadable.
func (s *Server) planPositions(r *http.Request) map[int64][]view.PlanPosition {
	out := map[int64][]view.PlanPosition{}
	workspaces, err := s.workspaces.ListWorkspaces(r.Context())
	if err != nil {
		return out
	}
	for _, ws := range workspaces {
		bindings, err := s.plans.Bindings(r.Context(), ws.ID)
		if err != nil {
			continue
		}
		for _, b := range bindings {
			state, err := s.plans.State(r.Context(), b.ID)
			if err != nil {
				continue
			}
			p, err := s.plans.Get(r.Context(), b.PlanboardID)
			if err != nil {
				continue
			}
			pos := view.PlanPosition{
				Where:       ws.Name + " · everyone",
				WorkspaceID: ws.ID,
				Step:        state.Step,
				Total:       len(p.Steps),
				Passes:      state.Cycle,
				Blocked:     state.BlockedNote,
			}
			if b.AgentID != nil {
				pos.Where = ws.Name + " · " + b.Agent
				pos.AgentName = b.Agent
			}
			for _, st := range p.Steps {
				if st.Ordinal == state.Step {
					pos.StepTitle = st.Title
				}
			}
			out[b.PlanboardID] = append(out[b.PlanboardID], pos)
		}
	}
	return out
}

// Named errors, so the three controls that all need a workspace and a plan
// give the same words for the same mistake.
var (
	errBadForm     = errPlain("that form could not be read")
	errNotAPlan    = errPlain("that is not a plan")
	errNoWorkspace = errPlain("that control did not say which workspace")
)

type errPlain string

func (e errPlain) Error() string { return string(e) }
