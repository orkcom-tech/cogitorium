package server

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/orkcom-tech/cogitorium/internal/inlet"
	"github.com/orkcom-tech/cogitorium/internal/schedule"
)

func (s *Server) handleListSchedules(w http.ResponseWriter, r *http.Request) {
	id, ok := s.workspaceScoped(w, r)
	if !ok {
		return
	}
	list, err := s.schedules.List(r.Context(), id)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) handleCreateSchedule(w http.ResponseWriter, r *http.Request) {
	wsID, ok := s.workspaceScoped(w, r)
	if !ok {
		return
	}
	var in struct {
		TaskID  int64           `json:"task_id"`
		Name    string          `json:"name"`
		Spec    string          `json:"spec"`
		TZ      string          `json:"tz"`
		Payload json.RawMessage `json:"payload"`
		OnMiss  string          `json:"on_miss"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, "the body is not JSON: "+err.Error())
		return
	}

	// Everything that can be checked is checked HERE, while the person who
	// typed it is still looking at it. A schedule that first fails at 02:00 is
	// a schedule nobody finds until the job stops happening.
	task, err := s.inlets.GetTask(r.Context(), in.TaskID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "no such task")
		return
	}
	door, err := s.inlets.GetInlet(r.Context(), task.InletID)
	if err != nil || door.WorkspaceID != wsID {
		writeError(w, http.StatusBadRequest, "that task belongs to another workspace")
		return
	}
	if task.Accepts != inlet.AcceptsJSON {
		writeError(w, http.StatusBadRequest,
			"only a JSON task can be scheduled: a file task is given a path to bytes somebody delivered, "+
				"and a clock has no bytes to give it")
		return
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
		return
	}
	if _, err := s.workspaces.GetAgentByName(r.Context(), wsID, task.AgentName); err != nil {
		writeError(w, http.StatusBadRequest,
			"this task targets agent "+task.AgentName+", which this workspace no longer has")
		return
	}

	sc, err := s.schedules.Create(r.Context(), schedule.Schedule{
		WorkspaceID: wsID, TaskID: task.ID, Name: in.Name, Spec: in.Spec,
		TZ: in.TZ, Payload: payload, OnMiss: in.OnMiss,
	})
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
	writeJSON(w, http.StatusCreated, sc)
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
	writeJSON(w, http.StatusOK, updated)
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
