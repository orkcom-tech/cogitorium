package server

import (
	"errors"
	"net/http"

	"github.com/orkcom-tech/cogitorium/internal/secrets"
)

// The named values a gear may be given: variables, whose value is shown, and
// secrets, whose value is shown once when set and never again.
//
// The global scope is administrator-only, on the same reasoning as gear
// approval and deletion: one name here reaches every workspace on the install,
// so it is an administrator's decision and not a member's. A workspace's own
// overrides go through the same access rule as everything else in that
// workspace — the person who may run its agents is the person who may say what
// its names mean.
//
// No route ever returns a secret's value. secrets.Record is the type that
// leaves this package, and it has nowhere to put one.

// failEnv separates "you typed something impossible" from "the server broke".
// A bad name or a missing key is the operator's to fix and must answer 400,
// not 500 — a 500 tells them to look at the logs, and the logs will say what
// this message already says.
func failEnv(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, secrets.ErrNotFound) {
		fail(w, r, err)
		return
	}
	writeError(w, http.StatusBadRequest, err.Error())
}

type envInput struct {
	Kind        string `json:"kind"`
	Value       string `json:"value"`
	Description string `json:"description"`
}

// envSources is what the interface needs in order to explain itself: whether a
// secret can be stored at all, and which directories are also being read. An
// operator whose value is not the one they set has to know a directory exists
// before they can suspect it.
type envSources struct {
	CanStoreSecrets bool   `json:"can_store_secrets"`
	VariablesDir    string `json:"variables_dir"`
	SecretsDir      string `json:"secrets_dir"`
}

func (s *Server) sources() envSources {
	variablesDir, secretsDir := s.env.Sources()
	return envSources{
		CanStoreSecrets: s.env.Store().HasKey(),
		VariablesDir:    variablesDir,
		SecretsDir:      secretsDir,
	}
}

func (s *Server) handleListEnv(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAdmin(w, r); !ok {
		return
	}
	values, err := s.env.Store().List(r.Context(), nil)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"values": values, "sources": s.sources()})
}

func (s *Server) handleSetEnv(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAdmin(w, r); !ok {
		return
	}
	var in envInput
	if !decodeJSON(w, r, &in) {
		return
	}
	rec, err := s.env.Store().Set(r.Context(), nil, r.PathValue("name"), in.Kind, in.Value, in.Description)
	if err != nil {
		failEnv(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, rec)
}

func (s *Server) handleDeleteEnv(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAdmin(w, r); !ok {
		return
	}
	if err := s.env.Store().Delete(r.Context(), nil, r.PathValue("name")); err != nil {
		failEnv(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleListWorkspaceEnv(w http.ResponseWriter, r *http.Request) {
	id, ok := s.workspaceScoped(w, r)
	if !ok {
		return
	}
	values, err := s.env.Store().List(r.Context(), &id)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"values": values, "sources": s.sources()})
}

func (s *Server) handleSetWorkspaceEnv(w http.ResponseWriter, r *http.Request) {
	id, ok := s.workspaceScoped(w, r)
	if !ok {
		return
	}
	var in envInput
	if !decodeJSON(w, r, &in) {
		return
	}
	rec, err := s.env.Store().Set(r.Context(), &id, r.PathValue("name"), in.Kind, in.Value, in.Description)
	if err != nil {
		failEnv(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, rec)
}

// handleDeleteWorkspaceEnv removes an override, which uncovers the install-wide
// value again rather than leaving a hole. That is what "narrower scope wins"
// means when the narrower scope goes away, and it is worth knowing before
// deleting: a gear here does not stop working, it starts seeing the other value.
func (s *Server) handleDeleteWorkspaceEnv(w http.ResponseWriter, r *http.Request) {
	id, ok := s.workspaceScoped(w, r)
	if !ok {
		return
	}
	if err := s.env.Store().Delete(r.Context(), &id, r.PathValue("name")); err != nil {
		failEnv(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
