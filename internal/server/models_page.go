package server

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/orkcom-tech/cogitorium/internal/view"
)

// The model catalogue, served as a template.
//
// Two lists on one page because they are two decisions: where models come
// from, and which of them this install offers an agent. A plugin can take over
// either without touching the other.
//
// Every action is a form that answers with the page. A failure keeps somebody
// on the screen beside the reason, which a redirect could not do — and the
// whole thing works with scripting switched off, which is what makes it
// testable without a browser.

func (s *Server) handleModelsPage(w http.ResponseWriter, r *http.Request) {
	s.renderModels(w, r, "", nil)
}

// handleCreateProviderForm adds a place models come from.
func (s *Server) handleCreateProviderForm(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAdmin(w, r); !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		s.renderModels(w, r, "that form could not be read", nil)
		return
	}
	_, err := s.catalog.CreateProvider(r.Context(),
		r.PostFormValue("name"), r.PostFormValue("kind"),
		r.PostFormValue("base_url"), r.PostFormValue("api_key"))
	if err != nil {
		s.renderModels(w, r, err.Error(), nil)
		return
	}
	s.renderModels(w, r, "", nil)
}

func (s *Server) handleDeleteProviderForm(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAdmin(w, r); !ok {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		s.renderModels(w, r, "that is not a provider", nil)
		return
	}
	if err := s.catalog.DeleteProvider(r.Context(), id); err != nil {
		s.renderModels(w, r, err.Error(), nil)
		return
	}
	s.renderModels(w, r, "", nil)
}

// handleTestProviderForm dials the provider and reports what it said.
//
// Admin-only for the same reason the JSON route is: it makes this server dial
// an address holding a stored credential, which is the exact step a repointed
// provider needs to complete.
func (s *Server) handleTestProviderForm(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAdmin(w, r); !ok {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		s.renderModels(w, r, "that is not a provider", nil)
		return
	}

	result := &providerTest{id: id, tested: true}
	client, _, err := s.catalog.Client(r.Context(), id)
	if err != nil {
		result.err = err.Error()
		s.renderModels(w, r, "", result)
		return
	}
	models, err := client.ListModels(r.Context())
	if err != nil {
		// The provider being unreachable is a result, not a fault of this
		// server, and it belongs on the row rather than at the top of the page.
		result.err = err.Error()
		s.renderModels(w, r, "", result)
		return
	}
	result.ok, result.offers = true, models
	s.renderModels(w, r, "", result)
}

// handleCreateModelForm offers one of a provider's models to agents.
func (s *Server) handleCreateModelForm(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAdmin(w, r); !ok {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		s.renderModels(w, r, "that is not a provider", nil)
		return
	}
	if err := r.ParseForm(); err != nil {
		s.renderModels(w, r, "that form could not be read", nil)
		return
	}
	name := strings.TrimSpace(r.PostFormValue("model_name"))
	if name == "" {
		s.renderModels(w, r, "a model needs the name its provider knows it by", nil)
		return
	}
	if _, err := s.catalog.CreateModel(r.Context(), id, name, r.PostFormValue("label")); err != nil {
		s.renderModels(w, r, err.Error(), nil)
		return
	}
	s.renderModels(w, r, "", nil)
}

func (s *Server) handleDeleteModelForm(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAdmin(w, r); !ok {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		s.renderModels(w, r, "that is not a model", nil)
		return
	}
	if err := s.catalog.DeleteModel(r.Context(), id); err != nil {
		s.renderModels(w, r, err.Error(), nil)
		return
	}
	s.renderModels(w, r, "", nil)
}

// providerTest is the result of a check somebody just made.
//
// Carried into the render rather than stored, because a connection that worked
// a minute ago is not a fact about now — and a row that remembered its last
// success would be telling somebody about a state it cannot see.
type providerTest struct {
	id     int64
	tested bool
	ok     bool
	err    string
	offers []string
}

func (s *Server) renderModels(w http.ResponseWriter, r *http.Request, problem string, test *providerTest) {
	model := view.ModelCatalog{
		Ctx:   s.viewCtx(r, callerFrom(r.Context())),
		Error: problem,
	}

	providers, err := s.catalog.ListProviders(r.Context())
	if err != nil {
		model.Error = err.Error()
	}
	models, err := s.catalog.ListModels(r.Context())
	if err != nil && model.Error == "" {
		model.Error = err.Error()
	}

	byProvider := map[int64][]view.Model{}
	for _, m := range models {
		row := view.Model{
			ID: m.ID, Name: m.ModelName, Label: m.Label,
			Provider: m.ProviderName, Kind: m.ProviderType,
		}
		model.Models = append(model.Models, row)
		byProvider[m.ProviderID] = append(byProvider[m.ProviderID], row)
	}

	for _, p := range providers {
		row := view.Provider{
			ID: p.ID, Name: p.Name, Kind: p.Type, BaseURL: p.BaseURL,
			// Whether a credential is stored, never the credential. A template
			// that could render a key is a template somebody eventually will.
			HasKey: p.HasKey,
			Models: byProvider[p.ID],
		}
		if test != nil && test.id == p.ID {
			row.Tested, row.TestOK, row.TestError, row.Offers = test.tested, test.ok, test.err, test.offers
		}
		model.Providers = append(model.Providers, row)
	}

	s.renderPage(w, r, "cog.page.models", "cog.list.models", "Model catalogue", model)
}
