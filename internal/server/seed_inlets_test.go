package server

import (
	"context"
	"testing"

	"github.com/orkcom-tech/cogitorium/internal/config"
	"github.com/orkcom-tech/cogitorium/internal/inlet"
	"github.com/orkcom-tech/cogitorium/internal/workspace"
)

// A prepared workspace that comes up answering, with nobody visiting a screen.
//
// This is the last manual step in a deployment that is otherwise one command. A
// bundle carries the shape of a door and never its key — right, and the reason
// a restored door is inert — so bringing an install up from a bundle used to
// end with a person opening the interface and pressing a button before anything
// outside could call it. Every consumer of a prepared workspace wrote the same
// script to do that, and each one got it subtly wrong.
//
// The key still does not come from the document. It comes from the environment,
// where the deployment already keeps its secrets and where the CALLER's copy
// comes from too, so both halves of the integration are configured from one
// source instead of one being read out of the other at run time.

func TestADoorOpensFromTheEnvironmentAtStart(t *testing.T) {
	// Not parallel: the environment is the thing under test.
	const key = "test-key-that-is-long-enough-to-be-allowed"
	t.Setenv("ECHOPAGE_INLET_KEY", key)

	in := newInstall(t, "127.0.0.1:8688", func(c *config.Config) {
		c.InletKeys = []config.SeedInletKey{{Address: "echopage", KeyEnv: "ECHOPAGE_INLET_KEY"}}
	})
	ctx := context.Background()

	// The workspace and its door, as an import would leave them: created, and
	// shut.
	ws, err := in.spaces.CreateWorkspaceSpec(ctx, "imported", "",
		workspace.AgentSpec{Name: "orchestrator", Role: "You run this."}, 1)
	if err != nil {
		t.Fatal(err)
	}
	door, err := in.srv.inlets.CreateInlet(ctx, ws.ID, "echopage", "the import worker's door")
	if err != nil {
		t.Fatal(err)
	}
	if shut, _ := in.srv.inlets.GetInlet(ctx, door.ID); shut.HasKey {
		t.Fatal("a door was created with a key; the fixture is not testing what it says")
	}

	// The next start.
	if err := in.srv.Bootstrap(ctx); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}

	open, err := in.srv.inlets.ByAddress(ctx, "echopage")
	if err != nil {
		t.Fatal(err)
	}
	if !open.HasKey {
		t.Fatal("the door is still shut after a start that was told what its key is")
	}
	if !open.MatchesKey(key) {
		t.Fatal("the door opens, but not with the key the environment named — " +
			"which is the same as not opening, for the caller holding that key")
	}
	if open.MatchesKey(key + "x") {
		t.Error("the door opens with a key nobody set")
	}
}

// A door named in the configuration that this install does not have yet is
// ordinary rather than fatal: on a first start the workspace has not been
// imported, and the next start finds it.
func TestNamingADoorThatIsNotHereYetIsNotAFailure(t *testing.T) {
	t.Setenv("ECHOPAGE_INLET_KEY", "test-key-that-is-long-enough-to-be-allowed")
	in := newInstall(t, "127.0.0.1:8688", func(c *config.Config) {
		c.InletKeys = []config.SeedInletKey{{Address: "not-here", KeyEnv: "ECHOPAGE_INLET_KEY"}}
	})
	if err := in.srv.Bootstrap(context.Background()); err != nil {
		t.Fatalf("a start naming a door that does not exist failed: %v", err)
	}
}

// An empty variable leaves the door shut rather than opening it with nothing.
// The failure it would otherwise cause is a 401 at somebody else's integration,
// hours later, with nothing on this install saying why.
func TestAnEmptyVariableLeavesTheDoorShut(t *testing.T) {
	t.Setenv("ECHOPAGE_INLET_KEY", "")
	in := newInstall(t, "127.0.0.1:8688", func(c *config.Config) {
		c.InletKeys = []config.SeedInletKey{{Address: "echopage", KeyEnv: "ECHOPAGE_INLET_KEY"}}
	})
	ctx := context.Background()
	ws, err := in.spaces.CreateWorkspaceSpec(ctx, "imported", "",
		workspace.AgentSpec{Name: "orchestrator", Role: "You run this."}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := in.srv.inlets.CreateInlet(ctx, ws.ID, "echopage", ""); err != nil {
		t.Fatal(err)
	}
	if err := in.srv.Bootstrap(ctx); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	open, _ := in.srv.inlets.ByAddress(ctx, "echopage")
	if open.HasKey {
		t.Fatal("an empty environment variable opened the door")
	}
}

// A supplied key has a floor under it. An inlet key is a bearer credential on a
// public path, and a door whose key is "test" is a door.
func TestASuppliedKeyThatIsTooShortIsRefused(t *testing.T) {
	t.Parallel()
	in := newInstall(t, "127.0.0.1:8688", nil)
	ctx := context.Background()
	ws, err := in.spaces.CreateWorkspaceSpec(ctx, "w", "",
		workspace.AgentSpec{Name: "orchestrator", Role: "You run this."}, 1)
	if err != nil {
		t.Fatal(err)
	}
	door, err := in.srv.inlets.CreateInlet(ctx, ws.ID, "echopage", "")
	if err != nil {
		t.Fatal(err)
	}

	if _, err := in.srv.inlets.SetKey(ctx, door.ID, "test"); err == nil {
		t.Fatal("a four-character key was accepted on a door reachable from outside")
	}
	// And the generated path still works, unchanged.
	key, err := in.srv.inlets.SetKey(ctx, door.ID, "")
	if err != nil {
		t.Fatalf("generating a key: %v", err)
	}
	if len(key) < inlet.MinKeyLen {
		t.Errorf("a generated key is shorter than what a supplied one must be: %d", len(key))
	}
}
