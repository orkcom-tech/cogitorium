package server

import (
	"net/http"
)

func (s *Server) handleListProviders(w http.ResponseWriter, r *http.Request) {
	providers, err := s.catalog.ListProviders(r.Context())
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, providers)
}

// Providers hold this install's API keys, so configuring one is an install
// decision rather than a workspace one. Everything that mutates a provider —
// or that makes the server dial one — is admin-only.
func (s *Server) handleCreateProvider(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAdmin(w, r); !ok {
		return
	}
	var in struct {
		Name    string `json:"name"`
		Type    string `json:"type"`
		BaseURL string `json:"base_url"`
		APIKey  string `json:"api_key"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	p, err := s.catalog.CreateProvider(r.Context(), in.Name, in.Type, in.BaseURL, in.APIKey)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, p)
}

func (s *Server) handleUpdateProvider(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAdmin(w, r); !ok {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var in struct {
		Name    *string `json:"name"`
		BaseURL *string `json:"base_url"`
		APIKey  *string `json:"api_key"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	// Repointing a provider without re-supplying the key would make the
	// server deliver its own credential to whatever the new address is. The
	// key does not travel to an address its owner did not name.
	if in.BaseURL != nil && in.APIKey == nil {
		existing, err := s.catalog.GetProvider(r.Context(), id)
		if err != nil {
			fail(w, r, err)
			return
		}
		if existing.BaseURL != *in.BaseURL {
			writeError(w, http.StatusBadRequest,
				"changing base_url requires supplying api_key again — the stored key is not carried to a new address")
			return
		}
	}
	p, err := s.catalog.UpdateProvider(r.Context(), id, in.Name, in.BaseURL, in.APIKey)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) handleDeleteProvider(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAdmin(w, r); !ok {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := s.catalog.DeleteProvider(r.Context(), id); err != nil {
		fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleTestProvider probes the provider by listing its models: one call
// verifies URL, key, and protocol, and gives the UI the pick-list.
func (s *Server) handleTestProvider(w http.ResponseWriter, r *http.Request) {
	// Admin-only because it makes the server dial an address holding a stored
	// credential — the exact step a repointed provider needs to complete.
	if _, ok := requireAdmin(w, r); !ok {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	client, _, err := s.catalog.Client(r.Context(), id)
	if err != nil {
		fail(w, r, err)
		return
	}
	models, err := client.ListModels(r.Context())
	if err != nil {
		// The provider being unreachable is a result, not a server fault.
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "models": models})
}

func (s *Server) handleListModels(w http.ResponseWriter, r *http.Request) {
	models, err := s.catalog.ListModels(r.Context())
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, models)
}

func (s *Server) handleCreateModel(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ProviderID int64  `json:"provider_id"`
		ModelName  string `json:"model_name"`
		Label      string `json:"label"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	m, err := s.catalog.CreateModel(r.Context(), in.ProviderID, in.ModelName, in.Label)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, m)
}

func (s *Server) handleDeleteModel(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := s.catalog.DeleteModel(r.Context(), id); err != nil {
		fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
