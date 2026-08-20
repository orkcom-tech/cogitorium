package server

import (
	"context"
	"log/slog"
	"os"
	"strconv"
	"strings"

	"github.com/orkcom-tech/cogitorium/internal/config"
	"github.com/orkcom-tech/cogitorium/internal/settings"
)

// Models an install already knows about on its first start.
//
// This exists so a deployment can come up READY. Bringing up Cogitorium next
// to an inference server in the same compose file and then telling somebody to
// open a screen and retype the address of the container already sitting next
// to it is a product that installs in one command and works in three.
//
// Seeded, never enforced. Each provider is created only when this install has
// no provider by that name; nothing is ever updated and nothing is ever
// removed. An operator who changes an address on the Models screen has changed
// it, and a restart must not put it back — which also means deleting one and
// restarting is how to start over.

func (s *Server) seedModels(ctx context.Context, seeds []config.SeedProvider, orchestrator string) {
	if len(seeds) == 0 && strings.TrimSpace(orchestrator) == "" {
		return
	}

	existing, err := s.catalog.ListProviders(ctx)
	if err != nil {
		slog.Error("the configured providers could not be seeded", "err", err)
		return
	}
	have := map[string]bool{}
	for _, p := range existing {
		have[strings.ToLower(p.Name)] = true
	}

	for _, seed := range seeds {
		name := strings.TrimSpace(seed.Name)
		if name == "" {
			slog.Error("a configured provider has no name and was skipped")
			continue
		}
		if have[strings.ToLower(name)] {
			// Already here. Not a warning: this is what every start after the
			// first one looks like.
			continue
		}
		key := ""
		if seed.KeyEnv != "" {
			key = os.Getenv(seed.KeyEnv)
			if key == "" {
				// Said out loud, because the failure it causes otherwise is a
				// model that answers 401 much later, on somebody's first turn.
				slog.Warn("a configured provider names an environment variable for its key and "+
					"that variable is empty; it is being created without one",
					"provider", name, "key_env", seed.KeyEnv)
			}
		}
		p, err := s.catalog.CreateProvider(ctx, name, seed.Kind, seed.BaseURL, key)
		if err != nil {
			slog.Error("a configured provider could not be created", "provider", name, "err", err)
			continue
		}
		slog.Info("provider seeded from configuration", "provider", name, "kind", seed.Kind)

		for _, m := range seed.Models {
			if strings.TrimSpace(m.Name) == "" {
				continue
			}
			if _, err := s.catalog.CreateModel(ctx, p.ID, m.Name, m.Label); err != nil {
				slog.Error("a configured model could not be offered",
					"provider", name, "model", m.Name, "err", err)
				continue
			}
			slog.Info("model seeded from configuration", "provider", name, "model", m.Name)
		}
	}

	s.seedOrchestratorModel(ctx, orchestrator)
}

// seedOrchestratorModel fills in the blank on the orchestrator template, if
// nobody has filled it in yet.
//
// Named as "<provider>/<model>" rather than by id, because an id is a fact
// about one database and a configuration file is written before that database
// exists.
func (s *Server) seedOrchestratorModel(ctx context.Context, want string) {
	want = strings.TrimSpace(want)
	if want == "" {
		return
	}
	// A choice already made is a choice. This is a seed, not a setting that
	// gets reapplied every start.
	if s.orchestratorModelID(ctx) != 0 {
		return
	}

	provider, model, ok := strings.Cut(want, "/")
	if !ok {
		slog.Error("orchestrator_model must be written as <provider>/<model>", "value", want)
		return
	}
	provider, model = strings.TrimSpace(provider), strings.TrimSpace(model)

	models, err := s.catalog.ListModels(ctx)
	if err != nil {
		slog.Error("the orchestrator's model could not be chosen", "err", err)
		return
	}
	for _, m := range models {
		if !strings.EqualFold(m.ProviderName, provider) {
			continue
		}
		// By the provider's own spelling or by the label, because both are on
		// the screen and either is what somebody would write.
		if m.ModelName != model && !strings.EqualFold(m.Label, model) {
			continue
		}
		if err := s.settings.Set(ctx, settings.OrchestratorModel, strconv.FormatInt(m.ID, 10)); err != nil {
			slog.Error("the orchestrator's model could not be stored", "err", err)
			return
		}
		slog.Info("the orchestrator's model was set from configuration", "model", want)
		return
	}
	slog.Error("orchestrator_model names a model this install does not offer; "+
		"every new workspace will be asked which model to use instead", "value", want)
}
