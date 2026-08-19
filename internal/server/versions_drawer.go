package server

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/orkcom-tech/cogitorium/internal/view"
	"github.com/orkcom-tech/cogitorium/internal/workflow"
)

// A workflow's history: what it was, at moments somebody chose to record.
//
// Saved by a person with a message. The alternative — a version per change —
// is four hundred entries nobody reads, and a history nobody reads is not a
// history.

func (s *Server) versionsModel(r *http.Request, wsID int64, problem, notice string, missing []string) view.Versions {
	model := view.Versions{
		Ctx:         s.viewCtx(r, callerFrom(r.Context())),
		WorkspaceID: wsID,
		Problem:     problem,
		Notice:      notice,
		Missing:     missing,
	}
	if s.versions == nil {
		model.Problem = "this install keeps no history"
		return model
	}

	list, err := s.versions.List(r.Context(), wsID)
	if err != nil {
		model.Problem = err.Error()
		return model
	}
	for i, v := range list {
		row := view.VersionRow{
			WorkspaceID: wsID, Number: v.Number, Message: v.Message, Author: v.Author,
			At: v.CreatedAt, RestoredFrom: v.RestoredFrom,
			// Newest first, so the first row is where the workflow is.
			Current: i == 0,
		}
		// The summary needs the snapshot, and the list deliberately does not
		// carry them — so it is read for the rows on screen rather than for
		// every version ever saved.
		if full, err := s.versions.Get(r.Context(), wsID, v.Number); err == nil {
			row.Summary = full.Snapshot.Summary()
		}
		model.Items = append(model.Items, row)
	}

	// Whether saving again would record anything.
	if latest, err := s.versions.Latest(r.Context(), wsID); err == nil {
		if now, err := workflow.Take(r.Context(), s.workflowStores(), wsID); err == nil {
			model.Unchanged = workflow.Same(latest.Snapshot, now)
		}
	}
	return model
}

func (s *Server) workflowStores() workflow.Stores {
	return workflow.Stores{Spaces: s.workspaces, Gears: s.gears, Schedules: s.schedules}
}

// handleSaveVersionForm records the workflow as it stands.
func (s *Server) handleSaveVersionForm(w http.ResponseWriter, r *http.Request) {
	wsID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "that is not a workspace", http.StatusBadRequest)
		return
	}
	if s.versions == nil {
		s.renderVersions(w, r, wsID, "this install keeps no history", "", nil)
		return
	}
	if err := r.ParseForm(); err != nil {
		s.renderVersions(w, r, wsID, "that form could not be read", "", nil)
		return
	}
	message := strings.TrimSpace(r.PostFormValue("message"))
	if message == "" {
		// A version with no message is a date, and a list of dates answers
		// nothing about which one to go back to.
		s.renderVersions(w, r, wsID, "say what changed — a version with no message is a date", "", nil)
		return
	}

	snap, err := workflow.Take(r.Context(), s.workflowStores(), wsID)
	if err != nil {
		s.renderVersions(w, r, wsID, err.Error(), "", nil)
		return
	}
	if latest, err := s.versions.Latest(r.Context(), wsID); err == nil && workflow.Same(latest.Snapshot, snap) {
		s.renderVersions(w, r, wsID,
			"nothing has changed since v"+strconv.Itoa(latest.Number)+", so this would record a duplicate", "", nil)
		return
	}

	v, err := s.versions.Save(r.Context(), wsID, snap, message, callerFrom(r.Context()).Name, 0)
	if err != nil {
		s.renderVersions(w, r, wsID, err.Error(), "", nil)
		return
	}
	s.renderVersions(w, r, wsID, "", "saved as v"+strconv.Itoa(v.Number), nil)
}

// handleRestoreVersionForm puts the workflow back to a version.
//
// And records the rollback as a version of its own rather than deleting what
// came after it: a history that can be rewritten cannot be produced in an
// argument about what ran.
func (s *Server) handleRestoreVersionForm(w http.ResponseWriter, r *http.Request) {
	wsID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "that is not a workspace", http.StatusBadRequest)
		return
	}
	if s.versions == nil {
		s.renderVersions(w, r, wsID, "this install keeps no history", "", nil)
		return
	}
	number, err := strconv.Atoi(r.PathValue("number"))
	if err != nil {
		s.renderVersions(w, r, wsID, "that is not a version", "", nil)
		return
	}
	want, err := s.versions.Get(r.Context(), wsID, number)
	if errors.Is(err, workflow.ErrNoVersion) {
		s.renderVersions(w, r, wsID, "there is no v"+strconv.Itoa(number), "", nil)
		return
	}
	if err != nil {
		s.renderVersions(w, r, wsID, err.Error(), "", nil)
		return
	}

	// What is there now, first. Rolling back over unsaved work without keeping
	// it would make the undo button the most dangerous control on the screen.
	if current, err := workflow.Take(r.Context(), s.workflowStores(), wsID); err == nil {
		if latest, lErr := s.versions.Latest(r.Context(), wsID); lErr != nil || !workflow.Same(latest.Snapshot, current) {
			if _, err := s.versions.Save(r.Context(), wsID, current,
				"before rolling back to v"+strconv.Itoa(number), callerFrom(r.Context()).Name, 0); err != nil {
				s.renderVersions(w, r, wsID, err.Error(), "", nil)
				return
			}
		}
	}

	missing, err := workflow.Restore(r.Context(), s.workflowStores(), wsID, want.Snapshot)
	if err != nil {
		s.renderVersions(w, r, wsID, err.Error(), "", nil)
		return
	}
	if _, err := s.versions.Save(r.Context(), wsID, want.Snapshot,
		"rolled back to v"+strconv.Itoa(number), callerFrom(r.Context()).Name, number); err != nil {
		s.renderVersions(w, r, wsID, err.Error(), "", missing)
		return
	}
	s.renderVersions(w, r, wsID, "", "rolled back to v"+strconv.Itoa(number), missing)
}

func (s *Server) renderVersions(w http.ResponseWriter, r *http.Request, wsID int64, problem, notice string, missing []string) {
	s.renderDrawer(w, r, "cog.drawer.versions", func() any {
		return s.versionsModel(r, wsID, problem, notice, missing)
	})
}
