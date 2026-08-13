package server

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/orkcom-tech/cogitorium/internal/engine"
	"github.com/orkcom-tech/cogitorium/internal/inlet"
	"github.com/orkcom-tech/cogitorium/internal/work"
)

// The queue, seen and stopped.
//
// Both halves arrive together on purpose. A queue an operator cannot see is
// discovered by being refused by it, and a queue they can see but not stop is
// worse than one they cannot see at all: fifty jobs visibly waiting for a run
// that is wedged, and a button that does not exist.

// queueEntry is one unit as an operator reads it. Deliberately not the whole
// row — args carry a delivery's prompt, which is the caller's payload, and a
// queue view is not a place to read other people's data.
type queueEntry struct {
	Unit     int64      `json:"unit"`
	Kind     string     `json:"kind"`
	State    string     `json:"state"`
	Run      *int64     `json:"run,omitempty"`
	Position int        `json:"position"`
	Since    time.Time  `json:"since"`
	Deadline *time.Time `json:"deadline,omitempty"`
}

type queueView struct {
	Queued  int          `json:"queued"`
	Running int          `json:"running"`
	Entries []queueEntry `json:"entries"`
}

// handleWorkspaceQueue answers what is waiting and what is running.
func (s *Server) handleWorkspaceQueue(w http.ResponseWriter, r *http.Request) {
	id, ok := s.workspaceScoped(w, r)
	if !ok {
		return
	}
	lane := work.Lane(id)
	queued, running, err := s.queue.Depth(r.Context(), lane)
	if err != nil {
		fail(w, r, err)
		return
	}
	units, err := s.queue.Waiting(r.Context(), lane, 100)
	if err != nil {
		fail(w, r, err)
		return
	}
	view := queueView{Queued: queued, Running: running, Entries: []queueEntry{}}
	for i, u := range units {
		e := queueEntry{
			Unit: u.ID, Kind: u.Kind, State: u.State, Run: u.RunID,
			Position: i, Since: u.CreatedAt,
		}
		if !u.Deadline.IsZero() {
			d := u.Deadline
			e.Deadline = &d
		}
		view.Entries = append(view.Entries, e)
	}
	writeJSON(w, http.StatusOK, view)
}

// handleCancelQueued stops a unit, whether it is waiting or already running.
//
// One route for both, because an operator pressing stop does not know which it
// was and should not have to. What differs is only what stopping means: a
// waiting unit never starts, and a running one is asked to stop — the worker
// notices when its unit is no longer claimed and the turn it is inside ends at
// the next thing it does.
func (s *Server) handleCancelQueued(w http.ResponseWriter, r *http.Request) {
	unitID, ok := pathID(w, r)
	if !ok {
		return
	}
	unit, err := s.queue.Get(r.Context(), unitID)
	if err != nil {
		fail(w, r, err)
		return
	}
	// Scoped through the unit's own workspace, so cancelling is exactly as
	// reachable as the workspace it belongs to and no more.
	if !s.requireWorkspace(w, r, unit.WorkspaceID) {
		return
	}

	caller := callerFrom(r.Context())
	reason := "stopped by " + caller.Name
	if err := s.queue.Kill(r.Context(), unitID, reason); err != nil {
		if errors.Is(err, work.ErrNotFound) {
			writeError(w, http.StatusConflict,
				"this unit has already finished, so there is nothing left to stop")
			return
		}
		fail(w, r, err)
		return
	}

	// Stop the work itself, not only the row. A unit already in flight would
	// otherwise carry on spending money on a job somebody has stopped, and the
	// operator would watch a cancelled row while the model kept answering.
	interrupted := s.pool.Cancel(unitID)

	// The ledger row it was going to fill in must say so too, or a caller
	// polling that row waits forever for an answer nobody will write.
	if unit.RunID != nil {
		s.inlets.SettleOrLog(context.WithoutCancel(r.Context()), *unit.RunID,
			inlet.StateInterrupted, "", reason, ledgerRecord(engine.Record{}))
	}
	slog.Info("work unit cancelled", "unit", unitID, "kind", unit.Kind,
		"lane", unit.Lane, "was", unit.State, "interrupted", interrupted, "by", caller.Name)
	w.WriteHeader(http.StatusNoContent)
}
