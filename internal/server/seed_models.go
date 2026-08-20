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

// Doors a prepared workspace arrived with, opened from the environment.
//
// A bundle carries the shape of an inlet and never its key — that is what makes
// a restored door inert, and it is right. It is also the last manual step in a
// deployment that is otherwise one command: import the workspace, then visit a
// screen before anything outside can call it.
//
// This closes that without weakening the rule. The key does not come from the
// document; it comes from the environment, where the deployment already keeps
// its secrets and where the CALLER's copy comes from too. Both sides are
// configured from one source rather than one being read out of the other at run
// time.
//
// Applied on EVERY start, unlike the provider seeds. A provider's address is
// something an operator may reasonably change on a screen afterwards; an inlet
// key is one half of a pair, and an install that had drifted from the value the
// caller holds would refuse deliveries with nothing to read. If the environment
// says what the key is, the environment is right.
func (s *Server) openSeededInlets(ctx context.Context, seeds []config.SeedInletKey) {
	for _, seed := range seeds {
		address := strings.TrimSpace(seed.Address)
		if address == "" || strings.TrimSpace(seed.KeyEnv) == "" {
			slog.Error("an inlet_keys entry needs both an address and key_env; it was skipped",
				"address", address, "key_env", seed.KeyEnv)
			continue
		}
		key := os.Getenv(seed.KeyEnv)
		if key == "" {
			// Loud, because the failure it causes otherwise is a 401 at
			// somebody else's integration, hours later, with nothing here
			// saying why.
			slog.Error("inlet_keys names an environment variable that is empty, so this door stays shut",
				"address", address, "key_env", seed.KeyEnv)
			continue
		}
		door, err := s.inlets.ByAddress(ctx, address)
		if err != nil {
			// Ordinary on a first start where the workspace has not been
			// imported yet. Said at info: the next start will find it.
			slog.Info("inlet_keys names a door this install does not have yet",
				"address", address, "err", err)
			continue
		}
		if _, err := s.inlets.SetKey(ctx, door.ID, key); err != nil {
			slog.Error("the key from the environment could not be set on this door",
				"address", address, "key_env", seed.KeyEnv, "err", err)
			continue
		}
		slog.Info("inlet key set from the environment", "address", address, "key_env", seed.KeyEnv)
	}
}
