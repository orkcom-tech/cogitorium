package bundle

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/orkcom-tech/cogitorium/internal/inlet"
	"github.com/orkcom-tech/cogitorium/internal/workspace"
)

// The door is what other systems talk to, and it used not to travel.
//
// A workspace restored from a bundle answered 404 at POST /i/{address}/{task}
// until somebody created the inlet and re-typed every task by hand — the
// schema, the instruction, what success is. Two installs restored from one
// document were therefore not the same install, and the difference showed up as
// a caller's integration behaving differently on staging and in production.

func TestADoorTravelsWithEverythingACallerDependsOn(t *testing.T) {
	t.Parallel()
	i := newInstall(t)
	ctx := context.Background()
	from := i.workspaceWithInlet(t)

	b, err := Export(ctx, i.stores, from.ID, Options{Inlets: true})
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	if len(b.Inlets) != 1 {
		t.Fatalf("the bundle carries %d doors, want 1", len(b.Inlets))
	}
	door := b.Inlets[0]
	if door.Address != "echopage" {
		t.Errorf("address = %q", door.Address)
	}
	if len(door.Tasks) != 1 {
		t.Fatalf("the door carries %d tasks, want 1", len(door.Tasks))
	}

	task := door.Tasks[0]
	if task.Name != "extract" || task.Agent != "reader" {
		t.Errorf("task = %q for agent %q", task.Name, task.Agent)
	}
	// The schema is checked BEFORE a model is called, so a task that lost it on
	// the way across accepts anything and the caller finds out from an answer
	// rather than from a 400.
	if !strings.Contains(task.Schema, "sourceUrl") {
		t.Errorf("the payload schema did not travel: %q", task.Schema)
	}
	if !strings.Contains(task.Instruction, "Never invent") {
		t.Errorf("the instruction did not travel: %q", task.Instruction)
	}
	// And what success is. Without it an imported door answers 200 with
	// whatever the model said, which is the failure `expect` exists to stop.
	if task.Expect == nil || !strings.Contains(task.Expect.Schema, "displayName") {
		t.Fatalf("expect did not travel: %+v", task.Expect)
	}
}

// The key is the one thing that must not travel, and there is nowhere in the
// document to put it. An imported door exists, has the right shape, and refuses
// every delivery until somebody on the receiving install opens it on purpose.
func TestAnImportedDoorArrivesShut(t *testing.T) {
	t.Parallel()
	i := newInstall(t)
	ctx := context.Background()
	from := i.workspaceWithInlet(t)

	// The exporting install's door is open.
	live, err := i.stores.Inlets.ByAddress(ctx, "echopage")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := i.stores.Inlets.IssueKey(ctx, live.ID); err != nil {
		t.Fatal(err)
	}

	b, err := Export(ctx, i.stores, from.ID, Options{Inlets: true})
	if err != nil {
		t.Fatalf("export: %v", err)
	}

	// Nothing in the document is the key, whatever it is called.
	raw, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), inlet.KeyPrefix+"-echopage-") {
		t.Fatal("the bundle carries the inlet key; it is a credential and a bundle is a document people forward")
	}
	for _, word := range []string{`"key"`, `"key_hash"`, `"has_key"`} {
		if strings.Contains(string(raw), word) {
			t.Errorf("the bundle carries %s, which is a field it must not have", word)
		}
	}

	// Import into a second install, and the door is inert.
	other := newInstall(t)
	res, err := Import(ctx, other.stores, b, ImportOptions{
		Name: "restored", OwnerID: other.owner, IncludeInlets: true,
	})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if len(res.InletsImported) != 1 {
		t.Fatalf("imported %v, want one task", res.InletsImported)
	}
	if len(res.NeedsKey) != 1 || res.NeedsKey[0] != "echopage" {
		t.Fatalf("the result does not say the door needs a key: %v", res.NeedsKey)
	}

	restored, err := other.stores.Inlets.ByAddress(ctx, "echopage")
	if err != nil {
		t.Fatalf("the door was not created: %v", err)
	}
	if restored.HasKey {
		t.Fatal("an imported door arrived with a key")
	}
	// And it matches nothing, including the empty string a request with no
	// Authorization header would present.
	for _, attempt := range []string{"", "cgi-echopage-anything"} {
		if restored.MatchesKey(attempt) {
			t.Errorf("a shut door accepted %q", attempt)
		}
	}
}

// A task pointing at an agent the bundle does not have would answer every
// delivery with a failure nobody could read, so it is refused by name.
func TestATaskNamingAnAgentTheBundleLacksIsSkippedByName(t *testing.T) {
	t.Parallel()
	i := newInstall(t)
	ctx := context.Background()
	from := i.workspaceWithInlet(t)

	b, err := Export(ctx, i.stores, from.ID, Options{Inlets: true})
	if err != nil {
		t.Fatal(err)
	}
	b.Inlets[0].Tasks[0].Agent = "nobody"

	other := newInstall(t)
	res, err := Import(ctx, other.stores, b, ImportOptions{
		Name: "restored", OwnerID: other.owner, IncludeInlets: true,
	})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if len(res.InletsImported) != 0 {
		t.Errorf("a task naming a missing agent was created: %v", res.InletsImported)
	}
	if len(res.InletsSkipped) != 1 || !strings.Contains(res.InletsSkipped[0].Why, "nobody") {
		t.Fatalf("the refusal does not name what it could not find: %+v", res.InletsSkipped)
	}
}

// An address is what a caller outside has in its configuration. Merging a
// bundle's tasks into somebody's existing door would change what an unrelated
// system gets back.
func TestAnAddressAlreadyHereIsLeftAlone(t *testing.T) {
	t.Parallel()
	i := newInstall(t)
	ctx := context.Background()
	from := i.workspaceWithInlet(t)
	b, err := Export(ctx, i.stores, from.ID, Options{Inlets: true})
	if err != nil {
		t.Fatal(err)
	}

	other := newInstall(t)
	mine := other.createWorkspace("mine", workspace.AgentSpec{Name: "orchestrator", Role: "You run this."})
	existing, err := other.stores.Inlets.CreateInlet(ctx, mine.ID, "echopage", "already here")
	if err != nil {
		t.Fatal(err)
	}

	res, err := Import(ctx, other.stores, b, ImportOptions{
		Name: "restored", OwnerID: other.owner, IncludeInlets: true,
	})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if len(res.InletsSkipped) != 1 || !strings.Contains(res.InletsSkipped[0].Why, "already exists") {
		t.Fatalf("the clash was not reported: %+v", res.InletsSkipped)
	}
	// Untouched: same workspace, same description, no new tasks.
	after, err := other.stores.Inlets.GetInlet(ctx, existing.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.WorkspaceID != mine.ID || after.Description != "already here" || len(after.Tasks) != 0 {
		t.Fatalf("the existing door was changed: %+v", after)
	}
}

// Off unless asked for. A door is the part other systems have in their
// configuration, so carrying one is a decision.
func TestDoorsDoNotTravelUnlessAskedFor(t *testing.T) {
	t.Parallel()
	i := newInstall(t)
	from := i.workspaceWithInlet(t)

	b, err := Export(context.Background(), i.stores, from.ID, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Inlets) != 0 {
		t.Fatalf("a door travelled without being asked for: %+v", b.Inlets)
	}
}

// workspaceWithInlet builds the shape EchoPage described: one agent, one door,
// one task, with a schema on the way in and a schema on the way out.
func (i *install) workspaceWithInlet(t *testing.T) workspace.Workspace {
	t.Helper()
	ctx := context.Background()
	ws := i.createWorkspace("source", workspace.AgentSpec{Name: "orchestrator", Role: "You run this."})
	i.createAgent(ws.ID, workspace.AgentSpec{Name: "reader", Role: "You read a page and describe it."})

	door, err := i.stores.Inlets.CreateInlet(ctx, ws.ID, "echopage", "the import worker's door")
	if err != nil {
		t.Fatalf("create inlet: %v", err)
	}
	if _, err := i.stores.Inlets.AddTask(ctx, door.ID, inlet.Task{
		Name: "extract", Accepts: inlet.AcceptsJSON, AgentName: "reader",
		Schema: `{"type":"object","required":["jobId","sourceUrl"],"additionalProperties":false,` +
			`"properties":{"jobId":{"type":"string"},"sourceUrl":{"type":"string"}}}`,
		Instruction: "Read the page and describe whose it is. Never invent anything that is not on it.",
		Expect: inlet.Expect{
			Schema: json.RawMessage(`{"type":"object","required":["displayName"],` +
				`"properties":{"displayName":{"type":"string"}}}`),
		},
	}); err != nil {
		t.Fatalf("add task: %v", err)
	}
	return ws
}
