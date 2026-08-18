package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/orkcom-tech/cogitorium/internal/gear"
	"github.com/orkcom-tech/cogitorium/internal/inlet"
	"github.com/orkcom-tech/cogitorium/internal/schedule"
)

// scheduleView is a schedule plus the two things a screen needs and the row
// cannot answer on its own: WHAT it dials, by name, and WHICH AGENT NODE the
// edge lands on.
//
// Resolved here rather than by the client making a call per schedule. The
// blueprint draws an edge from a clock to the thing it starts, and for a task
// schedule that thing is named two joins away — the task names an agent by
// name, inside an inlet that belongs to this workspace. A canvas that had to
// fetch every task to draw one edge would make the common case, a workspace
// with no schedules at all, cost nothing and the useful case cost a request per
// clock.
type scheduleView struct {
	schedule.Schedule
	// TargetName is what the node says under its own name: the agent, the gear,
	// or the task. Empty when the target is gone, which is what Broken means.
	TargetName string `json:"target_name,omitempty"`
	// EdgeAgentID is the agent this clock ultimately starts, for every kind
	// that ends at one — including a task schedule, whose agent is named by the
	// task rather than by the row. Nil for a gear schedule, which ends at a
	// gear, and for a broken one, which ends nowhere.
	EdgeAgentID *int64 `json:"edge_agent_id,omitempty"`
	// Broken is the row's own verdict, computed rather than stored so a screen
	// and the tick can never disagree about it.
	Broken bool `json:"broken"`
}

func (s *Server) handleListSchedules(w http.ResponseWriter, r *http.Request) {
	wsID, ok := s.workspaceScoped(w, r)
	if !ok {
		return
	}
	list, err := s.schedules.List(r.Context(), wsID)
	if err != nil {
		fail(w, r, err)
		return
	}
	out := make([]scheduleView, 0, len(list))
	for _, sc := range list {
		out = append(out, s.viewOf(r.Context(), wsID, sc))
	}
	writeJSON(w, http.StatusOK, out)
}

// viewOf resolves one schedule's target for a screen.
//
// Every lookup failure is silent and leaves the name empty, deliberately: this
// is a listing, and a workspace whose gear was deleted must still be able to
// draw its other four clocks. What the operator sees then is a clock that says
// it is broken, which is exactly the truth.
func (s *Server) viewOf(ctx context.Context, wsID int64, sc schedule.Schedule) scheduleView {
	v := scheduleView{Schedule: sc, Broken: sc.Broken()}
	switch sc.TargetKind {
	case schedule.TargetAgent:
		if sc.TargetAgentID == nil {
			return v
		}
		if a, err := s.workspaces.GetAgent(ctx, *sc.TargetAgentID); err == nil {
			v.TargetName = a.Name
			v.EdgeAgentID = sc.TargetAgentID
		}
	case schedule.TargetGear:
		if sc.TargetGearID == nil {
			return v
		}
		if g, err := s.gears.Get(ctx, *sc.TargetGearID); err == nil {
			v.TargetName = g.Name
		}
	default:
		if sc.TaskID == nil {
			return v
		}
		task, err := s.inlets.GetTask(ctx, *sc.TaskID)
		if err != nil {
			// The task is gone. A task schedule cascades away with its task, so
			// this is a row mid-delete rather than a state to draw.
			return v
		}
		v.TargetName = task.Name
		if a, err := s.workspaces.GetAgentByName(ctx, wsID, task.AgentName); err == nil {
			v.EdgeAgentID = &a.ID
		}
	}
	return v
}

// handleCreateSchedule writes a clock, whatever it dials.
//
// Everything that can be checked is checked HERE, while the person who typed it
// is still looking at it. A schedule that first fails at 02:00 is a schedule
// nobody finds until the job has stopped happening for a week.
//
// WHO MAY CREATE ONE depends on what it dials, and the difference is not
// cosmetic. A task or an agent schedule is workspace work: it starts a turn by
// an agent somebody already wired, inside a workspace they already reach. A
// GEAR schedule is unattended execution of approved code by somebody who did
// not approve it, on a clock, with nobody watching — which is the same class of
// act as granting an MCP server, and that is an administrator's.
func (s *Server) handleCreateSchedule(w http.ResponseWriter, r *http.Request) {
	wsID, ok := s.workspaceScoped(w, r)
	if !ok {
		return
	}
	var in CreateScheduleBody
	if !decodeJSON(w, r, &in) {
		return
	}
	kind := in.TargetKind
	if kind == "" {
		// Callers written before a schedule could dial anything else say task
		// by saying nothing.
		kind = schedule.TargetTask
	}

	want := schedule.Schedule{
		WorkspaceID: wsID, TargetKind: kind, Name: in.Name, Spec: in.Spec,
		TZ: in.TZ, OnMiss: in.OnMiss, Payload: "{}", Args: "{}",
	}

	switch kind {
	case schedule.TargetTask:
		if !s.scheduleOnTask(w, r, wsID, in, &want) {
			return
		}
	case schedule.TargetAgent:
		if !s.scheduleOnAgent(w, r, wsID, in, &want) {
			return
		}
	case schedule.TargetGear:
		// The admin gate, before anything is read or written.
		if _, ok := requireAdmin(w, r); !ok {
			return
		}
		if !s.scheduleOnGear(w, r, in, &want) {
			return
		}
	default:
		writeError(w, http.StatusBadRequest,
			"target_kind is \""+kind+"\"; a clock may dial a receiver task, an agent, or a gear")
		return
	}

	sc, err := s.schedules.Create(r.Context(), want)
	switch {
	case errors.Is(err, schedule.ErrConflict):
		writeError(w, http.StatusConflict, err.Error())
		return
	case err != nil:
		// Everything schedule.Create refuses is something the operator wrote,
		// and its errors are written to be read by them.
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	// The SAME shape the listing returns. A create that answered with a bare
	// row would make `broken` and `target_name` absent on exactly the response
	// a client is most likely to render straight back, and a field that is
	// sometimes there is a field every caller has to guard.
	writeJSON(w, http.StatusCreated, s.viewOf(r.Context(), wsID, sc))
}

// scheduleOnTask is the original path, unchanged in what it checks.
func (s *Server) scheduleOnTask(w http.ResponseWriter, r *http.Request, wsID int64,
	in CreateScheduleBody, want *schedule.Schedule) bool {
	if in.TaskID == nil {
		writeError(w, http.StatusBadRequest, "a schedule on a receiver task needs task_id")
		return false
	}
	task, err := s.inlets.GetTask(r.Context(), *in.TaskID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "no such task")
		return false
	}
	door, err := s.inlets.GetInlet(r.Context(), task.InletID)
	if err != nil || door.WorkspaceID != wsID {
		writeError(w, http.StatusBadRequest, "that task belongs to another workspace")
		return false
	}
	if task.Accepts != inlet.AcceptsJSON {
		writeError(w, http.StatusBadRequest,
			"only a JSON task can be scheduled: a file task is given a path to bytes somebody delivered, "+
				"and a clock has no bytes to give it")
		return false
	}
	payload := "{}"
	if len(in.Payload) > 0 {
		payload = string(in.Payload)
	}
	// The payload is held against the task's own schema now rather than at
	// 02:00 every night forever.
	if err := inlet.ValidatePayload(task.Schema, []byte(payload)); err != nil {
		writeError(w, http.StatusBadRequest,
			"this payload does not match what the task accepts, so every firing would be refused: "+err.Error())
		return false
	}
	if _, err := s.workspaces.GetAgentByName(r.Context(), wsID, task.AgentName); err != nil {
		writeError(w, http.StatusBadRequest,
			"this task targets agent "+task.AgentName+", which this workspace no longer has")
		return false
	}
	id := task.ID
	want.TaskID = &id
	want.Payload = payload
	return true
}

// scheduleOnAgent dials an agent directly. The instruction is the whole of what
// makes this different from a task: without one there is nothing to say at
// 03:00.
func (s *Server) scheduleOnAgent(w http.ResponseWriter, r *http.Request, wsID int64,
	in CreateScheduleBody, want *schedule.Schedule) bool {
	if in.TargetAgentID == nil {
		writeError(w, http.StatusBadRequest, "a schedule on an agent needs target_agent_id")
		return false
	}
	agent, err := s.workspaces.GetAgent(r.Context(), *in.TargetAgentID)
	if err != nil || agent.WorkspaceID != wsID {
		writeError(w, http.StatusBadRequest, "no such agent in this workspace")
		return false
	}
	// Checked now rather than at 03:00: an agent with nothing to think with
	// cannot take a turn, and discovering that unattended means one failed run
	// per night until somebody reads a log.
	if agent.ModelID == nil {
		writeError(w, http.StatusBadRequest,
			"agent "+agent.Name+" has no model bound, so a firing would have nothing to think with")
		return false
	}
	want.TargetAgentID = &agent.ID
	want.Instruction = in.Instruction
	return true
}

// scheduleOnGear runs code with nobody in the loop, which is the most useful
// and the most dangerous thing here — a nightly backup, a report, a sync, the
// jobs where a model is a liability rather than a help.
func (s *Server) scheduleOnGear(w http.ResponseWriter, r *http.Request,
	in CreateScheduleBody, want *schedule.Schedule) bool {
	if in.TargetGearID == nil {
		writeError(w, http.StatusBadRequest, "a schedule on a gear needs target_gear_id")
		return false
	}
	g, err := s.gears.Get(r.Context(), *in.TargetGearID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "no such gear")
		return false
	}
	// A schedule pointing at unapproved code is refused at the door rather than
	// drawn and left inert. A gear binding may be drawn before approval because
	// an agent still cannot call it; a clock has no such second gate — it is
	// the caller.
	if g.Status != gear.StatusApproved {
		writeError(w, http.StatusBadRequest,
			"gear "+g.Name+" is "+g.Status+". A clock is the caller, so it may only point at code somebody read and approved")
		return false
	}
	args := "{}"
	if len(in.Args) > 0 {
		args = string(in.Args)
	}
	// Held against the gear's own schema now, for the same reason the task
	// payload is: every firing forever would otherwise fail the same way.
	if err := inlet.ValidatePayload(g.ArgsSchema, []byte(args)); err != nil {
		writeError(w, http.StatusBadRequest,
			"these arguments do not fit what "+g.Name+" takes, so every firing would be refused: "+err.Error())
		return false
	}
	want.TargetGearID = &g.ID
	want.Args = args
	return true
}

// handleSetScheduleEnabled is the pause button. Kept separate from a general
// edit because turning a schedule off is the thing an operator does in a hurry,
// at night, and it should not require sending back the fields they are not
// changing.
func (s *Server) handleSetScheduleEnabled(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.scheduleScoped(w, r)
	if !ok {
		return
	}
	var in struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, "the body is not JSON: "+err.Error())
		return
	}
	updated, err := s.schedules.SetEnabled(r.Context(), sc.ID, in.Enabled)
	if err != nil {
		fail(w, r, err)
		return
	}
	// The enriched shape here too, for the same reason create returns it: three
	// routes answering with the same noun must answer with the same fields.
	writeJSON(w, http.StatusOK, s.viewOf(r.Context(), sc.WorkspaceID, updated))
}

// handleEditSchedule is the other half of the pause button.
//
// PUT rather than another PATCH on the same path, because PATCH already means
// "turn this off" and an operator in a hurry must keep the shortest route to
// that. A gear schedule stays an administrator's to change, for the same reason
// it was theirs to create: it is unattended execution of approved code, and an
// edit that could move its spec is an edit that decides when that happens.
func (s *Server) handleEditSchedule(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.scheduleScoped(w, r)
	if !ok {
		return
	}
	if sc.TargetKind == schedule.TargetGear {
		if _, ok := requireAdmin(w, r); !ok {
			return
		}
	}
	var in EditScheduleBody
	if !decodeJSON(w, r, &in) {
		return
	}

	want := schedule.Schedule{
		Name: in.Name, Spec: in.Spec, TZ: in.TZ, OnMiss: in.OnMiss, Instruction: in.Instruction,
	}
	// A payload and arguments are checked against the same schema they were
	// checked against when the schedule was written — now, while the operator
	// is looking at it, rather than at 03:00 every night forever.
	if len(in.Payload) > 0 {
		if sc.TaskID == nil {
			writeError(w, http.StatusBadRequest, "this schedule has no task, so it takes no payload")
			return
		}
		task, err := s.inlets.GetTask(r.Context(), *sc.TaskID)
		if err != nil {
			fail(w, r, err)
			return
		}
		if err := inlet.ValidatePayload(task.Schema, in.Payload); err != nil {
			writeError(w, http.StatusBadRequest,
				"this payload does not match what the task accepts, so every firing would be refused: "+err.Error())
			return
		}
		want.Payload = string(in.Payload)
	}
	if len(in.Args) > 0 {
		if sc.TargetGearID == nil {
			writeError(w, http.StatusBadRequest, "this schedule does not run a gear, so it takes no arguments")
			return
		}
		g, err := s.gears.Get(r.Context(), *sc.TargetGearID)
		if err != nil {
			fail(w, r, err)
			return
		}
		if err := inlet.ValidatePayload(g.ArgsSchema, in.Args); err != nil {
			writeError(w, http.StatusBadRequest,
				"these arguments do not fit what "+g.Name+" takes, so every firing would be refused: "+err.Error())
			return
		}
		want.Args = string(in.Args)
	}

	updated, err := s.schedules.Update(r.Context(), sc.ID, want)
	switch {
	case errors.Is(err, schedule.ErrConflict):
		writeError(w, http.StatusConflict, err.Error())
		return
	case err != nil:
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, s.viewOf(r.Context(), sc.WorkspaceID, updated))
}

func (s *Server) handleDeleteSchedule(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.scheduleScoped(w, r)
	if !ok {
		return
	}
	if err := s.schedules.Delete(r.Context(), sc.ID); err != nil {
		fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleRunScheduleNow fires a schedule by hand, without moving its clock.
//
// It exists because the first thing anybody does with a new schedule is want to
// know whether it works, and waiting until 02:00 to find out is how a broken
// job stays broken for a day.
func (s *Server) handleRunScheduleNow(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.scheduleScoped(w, r)
	if !ok {
		return
	}
	unitID, err := s.enqueueScheduled(r.Context(), sc)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.pool.Wake()
	writeJSON(w, http.StatusAccepted, map[string]int64{"unit": unitID})
}

// scheduleScoped resolves a schedule and proves the caller may reach the
// workspace it belongs to.
func (s *Server) scheduleScoped(w http.ResponseWriter, r *http.Request) (schedule.Schedule, bool) {
	id, ok := pathID(w, r)
	if !ok {
		return schedule.Schedule{}, false
	}
	sc, err := s.schedules.Get(r.Context(), id)
	if err != nil {
		if errors.Is(err, schedule.ErrNotFound) {
			writeError(w, http.StatusNotFound, "no such schedule")
			return schedule.Schedule{}, false
		}
		fail(w, r, err)
		return schedule.Schedule{}, false
	}
	if !s.requireWorkspace(w, r, sc.WorkspaceID) {
		return schedule.Schedule{}, false
	}
	return sc, true
}
