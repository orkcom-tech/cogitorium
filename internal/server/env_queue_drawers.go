package server

import (
	"net/http"

	"github.com/orkcom-tech/cogitorium/internal/schedule"
	"github.com/orkcom-tech/cogitorium/internal/secrets"
	"github.com/orkcom-tech/cogitorium/internal/view"
	"github.com/orkcom-tech/cogitorium/internal/work"
)

// The variables and queue drawers.
//
// Both are the workspace's own view of something the install also has, which
// is the shape that makes them worth converting early: a name set here wins
// over the same name set install-wide, and a lane here is one workspace's
// share of a queue everything else is also on.

// envModel is the named values a gear can be given.
//
// ws is nil for the install-wide list and set for a workspace's own, which is
// the whole difference between the two screens — a name set on a workspace
// wins over the same name set install-wide, and that is how one gear serves
// staging and production without being edited.
func (s *Server) envModel(r *http.Request, ws *int64, justSet string) view.Env {
	model := view.Env{
		Ctx:       s.viewCtx(r, callerFrom(r.Context())),
		Workspace: ws != nil,
		JustSet:   justSet,
	}
	if s.env == nil {
		model.Error = "this install stores no named values"
		return model
	}
	model.CanStoreSecrets = s.env.Store().HasKey()
	model.VariablesDir, model.SecretsDir = s.env.Sources()
	model.HasDirs = model.VariablesDir != "" || model.SecretsDir != ""

	values, err := s.env.Store().List(r.Context(), ws)
	if err != nil {
		model.Error = err.Error()
		return model
	}
	for _, v := range values {
		model.Names = append(model.Names, view.EnvName{
			Name: v.Name, Kind: v.Kind,
			// A secret's value is already empty here: the store has nowhere to
			// put one in a record, which is a stronger guarantee than a
			// template choosing not to render it.
			Value:       v.Value,
			Secret:      v.Kind == "secret",
			Description: v.Description,
			// Where it came from. A name can be supplied by this workspace, by
			// the install, or by a file on disk, and somebody whose value is
			// not the one they set has to be able to see which.
			Source:        sourceOf(v),
			FromWorkspace: v.WorkspaceID != nil,
		})
	}
	return model
}

// sourceOf says which of the three won, in the operator's words.
func sourceOf(v secrets.Record) string {
	if v.WorkspaceID != nil {
		return "this workspace"
	}
	return "the install"
}

// queueModel is what is waiting, and what will start on its own.
func (s *Server) queueModel(r *http.Request, wsID int64, problem, notice string) view.Queue {
	model := view.Queue{
		Ctx:    s.viewCtx(r, callerFrom(r.Context())),
		Error:  problem,
		Notice: notice,
	}

	if s.queue != nil {
		units, err := s.queue.Waiting(r.Context(), work.Lane(wsID), queueUnitsShown)
		if err != nil {
			model.Error = err.Error()
		}
		for _, u := range units {
			model.Units = append(model.Units, view.Unit{
				ID: u.ID, Kind: u.Kind, State: u.State, Lane: u.Lane,
				Attempts: u.Attempts, MaxAttempts: u.MaxAttempts,
				LastError: u.LastError, Failed: u.LastError != "",
			})
		}
	}

	if s.schedules != nil {
		list, err := s.schedules.List(r.Context(), wsID)
		if err != nil && model.Error == "" {
			model.Error = err.Error()
		}
		for _, sc := range list {
			row := view.Schedule{
				ID: sc.ID, Name: sc.Name, Spec: sc.Spec, TZ: sc.TZ,
				Enabled: sc.Enabled, Target: targetOf(sc), LastRun: sc.LastFiredAt,
			}
			if !sc.NextAt.IsZero() {
				row.NextRun = sc.NextAt.Format("2006-01-02 15:04")
			}
			// A clock whose target was deleted shows as broken rather than
			// vanishing, and this is where somebody finds out.
			if sc.Broken() {
				// It is switched off by the first tick that finds it this way,
				// so the sentence says what to do rather than describing a
				// loop that is no longer running.
				row.LastError = "its target is gone; point it at another one and switch it back on"
			} else if sc.LastOutcome != "" && sc.LastOutcome != "ok" {
				row.LastError = sc.LastOutcome
			}
			model.Schedules = append(model.Schedules, row)
		}
	}
	return model
}

// targetOf names what a clock dials, in the words an operator reads rather
// than the three columns the row is stored in.
func targetOf(sc schedule.Schedule) string {
	switch {
	case sc.TargetAgentID != nil:
		return "an agent"
	case sc.TargetGearID != nil:
		return "a gear"
	case sc.TaskID != nil:
		return "a receiver task"
	}
	return ""
}

// queueUnitsShown bounds the list. A queue with a thousand waiting units is a
// queue somebody needs the depth of rather than every row of.
const queueUnitsShown = 50

// handleVariablesPage is the install-wide list.
func (s *Server) handleVariablesPage(w http.ResponseWriter, r *http.Request) {
	if !s.pageAdmin(w, r) {
		return
	}
	s.renderPage(w, r, "cog.page.variables", "", "Variables & Secrets", s.envModel(r, nil, ""))
}
