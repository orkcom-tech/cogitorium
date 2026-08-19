package bundle

import (
	"context"
	"testing"

	"github.com/orkcom-tech/cogitorium/internal/workspace"
)

// What an agent reads has to survive the journey.
//
// A bundle used to carry the workspace's own documents and lose the wiring —
// which agent was told to read which one — so the same agents arrived on the
// far side behaving differently and nothing said why. An instruction nobody is
// bound to is an instruction nobody reads.
func TestWhatAnAgentReadsSurvivesTheRoundTrip(t *testing.T) {
	from := newInstall(t)
	ctx := context.Background()

	ws := from.createWorkspace("research", workspace.AgentSpec{Name: workspace.OrchestratorName})
	drafter := from.createAgent(ws.ID, workspace.AgentSpec{Name: "drafter", Role: "you draft"})

	// Two bindings that travel differently, and both have to arrive: one to a
	// document of this workspace's own, which is re-rooted under the new
	// workspace's branch, and one to the shared library, which keeps the path
	// it has on every install.
	//
	// The documents themselves are not written here. Whether a document exists
	// is a question for the install that reads it; this is about whether the
	// WIRING survives, and it needs no contextd to answer.
	own, err := ContextPath(ws.Branch, "notes.md")
	if err != nil {
		t.Fatalf("path: %v", err)
	}
	if _, err := from.stores.Workspaces.CreateContextBinding(ctx, ws.ID, own, &drafter.ID); err != nil {
		t.Fatalf("bind the workspace's own: %v", err)
	}
	if _, err := from.stores.Workspaces.CreateContextBinding(ctx, ws.ID, "library/house-style.md", nil); err != nil {
		t.Fatalf("bind the library document: %v", err)
	}

	// Without Options{Context: true}: the wiring travels either way, because
	// three fields naming an agent are not the document they name.
	b, err := Export(ctx, from.stores, ws.ID, Options{})
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	if len(b.Reads) != 2 {
		t.Fatalf("the bundle carries %d bindings; it must carry both", len(b.Reads))
	}

	to := newInstall(t)
	res, err := Import(ctx, to.stores, b, ImportOptions{
		Name: "research", OwnerID: to.owner,
	})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if res.Reads != 2 {
		t.Fatalf("%d bindings arrived, and %v could not: both were expected", res.Reads, res.ReadsSkipped)
	}

	landed, err := to.stores.Workspaces.ListContextBindings(ctx, res.Workspace.ID)
	if err != nil {
		t.Fatalf("read them back: %v", err)
	}
	byPath := map[string]bool{}
	for _, b := range landed {
		byPath[b.Path] = b.AgentID != nil
	}

	// The library document keeps its path, because that is where it lives on
	// any install.
	if _, ok := byPath["library/house-style.md"]; !ok {
		t.Errorf("the library binding did not arrive; what did: %v", byPath)
	}
	// The workspace's own document is re-rooted under the NEW branch. A
	// binding still carrying the exporting workspace's branch would point at
	// nothing here, or at somebody else's work.
	arrived, err := ContextPath(res.Workspace.Branch, "notes.md")
	if err != nil {
		t.Fatalf("path: %v", err)
	}
	toAgent, ok := byPath[arrived]
	if !ok {
		t.Errorf("the workspace's own binding was not re-rooted; what arrived: %v", byPath)
	}
	if !toAgent {
		t.Error("the binding arrived for the whole workspace; it was bound to one agent")
	}
}
