package server

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"github.com/orkcom-tech/cogitorium/internal/settings"
	"github.com/orkcom-tech/cogitorium/internal/view"
	"github.com/orkcom-tech/cogitorium/internal/workspace"
)

// The model catalogue, served as a template.
//
// Three sections on one page because they are three decisions: where models
// come from, which of them this install offers an agent, and which one the
// orchestrator this product makes on its own thinks with. A plugin can take
// over any of them without touching the others.
//
// Every action is a form. A failure answers with the page, because the reason
// belongs beside the form that produced it; a success sends the browser back
// to /models, because a rendered POST leaves its own URL in the address bar
// and a refresh then adds a second provider, or dials somebody's endpoint
// again. The whole thing works with scripting switched off, which is what
// makes it testable without a browser.

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
	s.done(w, r, "/models")
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
	s.done(w, r, "/models")
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
		s.renderModelsInto(w, r, "that is not a provider", nil, "cog.list.providers")
		return
	}

	result := &providerTest{id: id, tested: true}
	client, _, err := s.catalog.Client(r.Context(), id)
	if err != nil {
		result.err = err.Error()
		s.renderModelsInto(w, r, "", result, "cog.list.providers")
		return
	}
	models, err := client.ListModels(r.Context())
	if err != nil {
		// The provider being unreachable is a result, not a fault of this
		// server, and it belongs on the row rather than at the top of the page.
		result.err = err.Error()
		s.renderModelsInto(w, r, "", result, "cog.list.providers")
		return
	}
	result.ok, result.offers = true, models
	s.renderModelsInto(w, r, "", result, "cog.list.providers")
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
	s.done(w, r, "/models")
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
	s.done(w, r, "/models")
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
	s.renderModelsInto(w, r, problem, test, "cog.list.models")
}

// renderModelsInto is renderModels naming which panel an htmx request should
// get back. The catalogue is the usual one; testing a connection writes its
// answer onto the PROVIDER's row, so that action asks for the provider list
// instead — otherwise the swap would replace the providers with the catalogue.
func (s *Server) renderModelsInto(w http.ResponseWriter, r *http.Request, problem string, test *providerTest, fragment string) {
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

	model.Orchestrator = s.orchestratorTemplate(r, model.Models)
	s.renderPage(w, r, "cog.page.models", fragment, "Model catalogue", model)
}

// The orchestrator, as a thing on a screen.
//
// It is the one agent this product makes by itself: every workspace is created
// with one, bound to a model. That was only ever visible as a picker on the
// new-workspace form labelled "the orchestrator thinks with", so somebody
// looking for where an orchestrator comes from found a catalogue of models,
// no orchestrator anywhere, and concluded there was a step they were missing.
//
// There was not. What was missing was the template being shown: a role this
// product already wrote, with one blank in it. Fill in the model here and
// every workspace made afterwards starts from that instead of a question.

func (s *Server) orchestratorTemplate(r *http.Request, models []view.Model) view.OrchestratorTemplate {
	t := view.OrchestratorTemplate{
		// The instruction in full, because an orchestrator is not a mystery box
		// and the fastest way to say what one IS is to let somebody read what
		// it is told.
		Role:     workspace.DefaultOrchestratorRole,
		NoModels: len(models) == 0,
	}
	chosen := s.orchestratorModelID(r.Context())
	for _, m := range models {
		label := m.Label
		if label == "" {
			label = m.Name
		}
		c := view.OrchestratorChoice{
			ID: m.ID, Label: label, Provider: m.Provider, Selected: m.ID == chosen,
		}
		if c.Selected {
			t.Chosen = label + " — " + m.Provider
		}
		t.Choices = append(t.Choices, c)
	}
	return t
}

// orchestratorModelID is the house orchestrator's model, or 0.
//
// A stored id whose model has since been deleted answers 0 rather than a
// dangling number: the screen then reads as "nobody has chosen", which is what
// is true, instead of showing a picker with nothing marked in it.
func (s *Server) orchestratorModelID(ctx context.Context) int64 {
	raw, err := s.settings.Get(ctx, settings.OrchestratorModel)
	if err != nil || strings.TrimSpace(raw) == "" {
		return 0
	}
	id, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil {
		return 0
	}
	if _, err := s.catalog.GetModel(ctx, id); err != nil {
		return 0
	}
	return id
}

// handleOrchestratorModelForm fills in the blank.
func (s *Server) handleOrchestratorModelForm(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAdmin(w, r); !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		s.renderModels(w, r, "that form could not be read", nil)
		return
	}
	raw := strings.TrimSpace(r.PostFormValue("model_id"))
	if raw == "" {
		// Clearing it is a decision too: the new-workspace form goes back to
		// asking, which is where this started.
		if err := s.settings.Set(r.Context(), settings.OrchestratorModel, ""); err != nil {
			s.renderModels(w, r, err.Error(), nil)
			return
		}
		s.done(w, r, "/models")
		return
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		s.renderModels(w, r, "that is not a model", nil)
		return
	}
	// Checked before it is stored. A setting pointing at a model this install
	// does not have is a workspace that fails to create with a puzzle for a
	// reason.
	if _, err := s.catalog.GetModel(r.Context(), id); err != nil {
		s.renderModels(w, r, "this install has no such model", nil)
		return
	}
	if err := s.settings.Set(r.Context(), settings.OrchestratorModel, raw); err != nil {
		s.renderModels(w, r, err.Error(), nil)
		return
	}
	s.done(w, r, "/models")
}
