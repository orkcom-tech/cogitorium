package server

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/orkcom-tech/cogitorium/internal/schedule"
)

// The queue panel's write half.
//
// "add it" and the on/off switch posted to `/schedules`, which nothing served,
// so both answered a bare `404 page not found`.
//
// The form was also missing the two things a clock cannot be made without:
// what it starts, and what it says when it fires. A schedule with neither is a
// timer that goes off into an empty room, which is why nothing behind this
// could have worked even once the route existed.

func (s *Server) handleCreateScheduleForm(w http.ResponseWriter, r *http.Request) {
	wsID, ok := s.workspaceScoped(w, r)
	if !ok {
		return
	}
	if s.schedules == nil {
		s.renderQueue(w, r, wsID, "this install has no clocks", "")
		return
	}
	if err := r.ParseForm(); err != nil {
		s.renderQueue(w, r, wsID, "that form could not be read", "")
		return
	}

	name := strings.TrimSpace(r.PostFormValue("name"))
	spec := strings.TrimSpace(r.PostFormValue("spec"))
	tell := strings.TrimSpace(r.PostFormValue("tell"))
	if name == "" || spec == "" {
		s.renderQueue(w, r, wsID, "a clock needs a name and a time", "")
		return
	}
	agentName := strings.TrimSpace(r.PostFormValue("agent"))
	if agentName == "" {
		s.renderQueue(w, r, wsID, "say which agent it starts — a clock with no target fires into nothing", "")
		return
	}
	agent, err := s.workspaces.GetAgentByName(r.Context(), wsID, agentName)
	if err != nil {
		s.renderQueue(w, r, wsID, err.Error(), "")
		return
	}
	if tell == "" {
		// A firing with nothing to say is a turn with an empty prompt, which
		// is a model call that costs money and answers nothing.
		s.renderQueue(w, r, wsID, "say what to tell it when it fires", "")
		return
	}

	created, err := s.schedules.Create(r.Context(), schedule.Schedule{
		WorkspaceID: wsID, TargetKind: schedule.TargetAgent, TargetAgentID: &agent.ID,
		Name: name, Spec: spec, TZ: strings.TrimSpace(r.PostFormValue("tz")),
		Instruction: tell, Enabled: true,
	})
	if err != nil {
		// A cron line that does not parse is the ordinary mistake here, and
		// the parser's own words say which part it could not read.
		s.renderQueue(w, r, wsID, err.Error(), "")
		return
	}
	s.renderQueue(w, r, wsID, "", created.Name+" is set; it next fires "+created.NextAt.Format(time.RFC1123))
}

// handleToggleScheduleForm switches a clock on or off.
//
// Off is not deletion: the clock keeps its time and its instruction, and
// switching it back on re-bases the next firing rather than firing for every
// moment it was asleep.
func (s *Server) handleToggleScheduleForm(w http.ResponseWriter, r *http.Request) {
	if s.schedules == nil {
		http.Error(w, "this install has no clocks", http.StatusNotFound)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "that is not a clock", http.StatusBadRequest)
		return
	}
	found, err := s.schedules.Get(r.Context(), id)
	if err != nil {
		http.Error(w, "there is no such clock", http.StatusNotFound)
		return
	}
	if !s.requireWorkspace(w, r, found.WorkspaceID) {
		return
	}
	updated, err := s.schedules.SetEnabled(r.Context(), id, !found.Enabled)
	if err != nil {
		s.renderQueue(w, r, found.WorkspaceID, err.Error(), "")
		return
	}
	if updated.Enabled {
		s.renderQueue(w, r, found.WorkspaceID, "", updated.Name+" is on; it next fires "+updated.NextAt.Format(time.RFC1123))
		return
	}
	s.renderQueue(w, r, found.WorkspaceID, "", updated.Name+" is off; it keeps its time and says nothing until you switch it back")
}

func (s *Server) renderQueue(w http.ResponseWriter, r *http.Request, wsID int64, problem, notice string) {
	s.renderDrawer(w, r, "cog.drawer.queue", func() any { return s.queueModel(r, wsID, problem, notice) })
}
