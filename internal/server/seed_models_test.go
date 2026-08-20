package server

import (
	"context"
	"strconv"
	"testing"

	"github.com/orkcom-tech/cogitorium/internal/config"
	"github.com/orkcom-tech/cogitorium/internal/settings"
)

// A deployment that comes up ready.
//
// The starter compose file brings up Cogitorium next to an inference server in
// the same network. Without this, it would come up with an empty catalogue and
// the first thing anybody did would be to open a screen and retype the address
// of the container already sitting beside it.
func TestConfiguredProvidersAreSeededOnFirstStart(t *testing.T) {
	t.Parallel()
	in := newInstall(t, "127.0.0.1:8688", func(c *config.Config) {
		c.Providers = []config.SeedProvider{{
			Name: "local", Kind: "openai-compatible", BaseURL: "http://ollama:11434/v1",
			Models: []config.SeedModel{{Name: "qwen2.5:7b", Label: "local"}},
		}}
		c.OrchestratorModel = "local/qwen2.5:7b"
	})
	ctx := context.Background()
	if err := in.srv.Bootstrap(ctx); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}

	providers, err := in.cat.ListProviders(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(providers) != 1 || providers[0].Name != "local" {
		t.Fatalf("the configured provider was not created: %+v", providers)
	}
	models, err := in.cat.ListModels(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 1 || models[0].ModelName != "qwen2.5:7b" {
		t.Fatalf("the configured model was not offered: %+v", models)
	}
	// And the orchestrator already has one, so the first workspace is not asked.
	if got := in.srv.orchestratorModelID(ctx); got != models[0].ID {
		t.Errorf("the orchestrator's model was not set from configuration: %d", got)
	}

	// Starting again must change nothing. This is a SEED: an operator who
	// repointed the provider on the Models screen would otherwise find it
	// silently put back on every restart, and a second start would double the
	// catalogue.
	if err := in.srv.Bootstrap(ctx); err != nil {
		t.Fatalf("second bootstrap: %v", err)
	}
	providers, _ = in.cat.ListProviders(ctx)
	models, _ = in.cat.ListModels(ctx)
	if len(providers) != 1 || len(models) != 1 {
		t.Fatalf("starting again duplicated what was seeded: %d providers, %d models",
			len(providers), len(models))
	}
}

// An operator's edit outranks the file. Seeding that reapplied itself would be
// a setting somebody cannot change from the product it belongs to.
func TestSeedingNeverOverwritesWhatSomebodyChose(t *testing.T) {
	t.Parallel()
	in := newInstall(t, "127.0.0.1:8688", func(c *config.Config) {
		c.Providers = []config.SeedProvider{{
			Name: "local", Kind: "openai-compatible", BaseURL: "http://ollama:11434/v1",
			Models: []config.SeedModel{{Name: "qwen2.5:7b", Label: "local"}},
		}}
		c.OrchestratorModel = "local/qwen2.5:7b"
	})
	ctx := context.Background()

	// Somebody got there first, with a provider of the same name pointed
	// somewhere else.
	mine, err := in.cat.CreateProvider(ctx, "local", "openai-compatible", "http://127.0.0.1:1234/v1", "")
	if err != nil {
		t.Fatal(err)
	}
	theirs, err := in.cat.CreateModel(ctx, mine.ID, "llama3.2", "mine")
	if err != nil {
		t.Fatal(err)
	}
	if err := in.srv.settings.Set(ctx, settings.OrchestratorModel,
		strconv.FormatInt(theirs.ID, 10)); err != nil {
		t.Fatal(err)
	}

	if err := in.srv.Bootstrap(ctx); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}

	providers, _ := in.cat.ListProviders(ctx)
	if len(providers) != 1 {
		t.Fatalf("a second provider was created beside the operator's: %+v", providers)
	}
	if providers[0].BaseURL != "http://127.0.0.1:1234/v1" {
		t.Errorf("the operator's address was overwritten by the configuration: %s", providers[0].BaseURL)
	}
	if got := in.srv.orchestratorModelID(ctx); got != theirs.ID {
		t.Errorf("the operator's orchestrator model was overwritten: %d, want %d", got, theirs.ID)
	}
}

// A key never belongs in a configuration file: it is read by whatever can read
// the disk and ends up in whatever repository rendered it. key_env names the
// variable instead, which is what a Docker secret and a Kubernetes Secret
// already know how to fill.
func TestASeededProviderTakesItsKeyFromTheEnvironment(t *testing.T) {
	// Not parallel: t.Setenv and t.Parallel cannot both be used, and the
	// environment is the thing under test here.
	t.Setenv("COGITORIUM_TEST_PROVIDER_KEY", "sk-not-a-real-key")
	in := newInstall(t, "127.0.0.1:8688", func(c *config.Config) {
		c.Providers = []config.SeedProvider{{
			Name: "anthropic", Kind: "anthropic", KeyEnv: "COGITORIUM_TEST_PROVIDER_KEY",
		}}
	})
	ctx := context.Background()
	if err := in.srv.Bootstrap(ctx); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	providers, _ := in.cat.ListProviders(ctx)
	if len(providers) != 1 || !providers[0].HasKey {
		t.Fatalf("the key named in key_env did not reach the provider: %+v", providers)
	}
}
