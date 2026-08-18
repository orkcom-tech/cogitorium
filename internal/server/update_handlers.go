package server

import (
	"log/slog"
	"net/http"

	"github.com/orkcom-tech/cogitorium/internal/update"
)

// Knowing there is a new version, and being able to say no to being told.
//
// Three routes and a strict split of who may do what. READING is for anybody
// signed in: an update is a fact about the install, and a member who can see
// the health of the server can see that it is a version behind. DECIDING is an
// administrator's, both because the answer applies to everybody on the install
// and because "may this server make an outbound request" is the same class of
// question as every other switch in the config file.
//
// Nothing here ever writes a binary. What an operator gets is the release notes
// and the command belonging to whoever owns the file — see update.Install for
// why a self-updater that fights a package manager is worse than no updater.

func (s *Server) handleUpdateStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.updates.Report())
}

// handleUpdateCheckNow asks now, rather than waiting for the daily one.
//
// It exists for the operator who left the automatic check off and wants to look
// today, so it deliberately works under "ask" without changing the setting: one
// press is one look, not consent to a request every morning. Under "off" it is
// refused like everything else, because that setting is a statement about the
// machine rather than a preference this product may talk itself past.
func (s *Server) handleUpdateCheckNow(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAdmin(w, r); !ok {
		return
	}
	report, err := s.updates.Check(r.Context())
	if err != nil {
		// 409 rather than 403: the caller has the right role, and the request
		// is refused by the state of the install rather than by who is asking.
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, report)
}

// handleSetUpdateMode records the answer to the question the interface asked.
//
// The one transition it will not make is out of "off". That value is set in the
// config file on the server's own disk, and a browser that could undo it would
// make the file a suggestion — see update.Checker.SetMode.
func (s *Server) handleSetUpdateMode(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAdmin(w, r); !ok {
		return
	}
	var in UpdateModeBody
	if !decodeJSON(w, r, &in) {
		return
	}
	if err := s.updates.SetMode(r.Context(), in.Mode); err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	// One check, here, so the operator sees the result of the thing they have
	// just agreed to rather than an empty panel and a "never asked". SetMode
	// has already started the daily timer, and it deliberately waits a full
	// interval before its first pass so this is not doubled.
	//
	// Its failure is not this request's failure: the setting stuck. Reporting
	// the answer as unsaved because GitHub was unreachable would be a lie the
	// operator discovers at the next restart.
	if in.Mode == update.ModeOn {
		if _, err := s.updates.Check(r.Context()); err != nil {
			slog.Info("the first check after an operator switched it on did not get through", "err", err)
		}
	}
	writeJSON(w, http.StatusOK, s.updates.Report())
}
