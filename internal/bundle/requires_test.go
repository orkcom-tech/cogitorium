package bundle

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/orkcom-tech/cogitorium/internal/workspace"
)

// A bundle may SAY what it needs. It may not have it.
//
// The rule that a document never carries a permission is the load-bearing one
// and is not weakened here: see the Gear comment on the network grant. What was
// missing is the other half — an imported workspace whose agent has to read a
// public page arrived with no way to say so, so the operator found out by
// running it and watching it fail, from a document that otherwise describes the
// workspace completely.

func TestADeclarationReachesTheOperatorAndGrantsNothing(t *testing.T) {
	t.Parallel()
	i := newInstall(t)
	ctx := context.Background()
	from := i.createWorkspace("source", workspace.AgentSpec{Name: "orchestrator", Role: "You run this."})
	i.createAgent(from.ID, workspace.AgentSpec{Name: "reader", Role: "You read a page."})

	b, err := Export(ctx, i.stores, from.ID, Options{})
	if err != nil {
		t.Fatal(err)
	}
	// Written by whoever prepared the bundle, in their words.
	b.Requires = &Requires{Egress: []EgressNeed{{
		Agent:  "reader",
		Reason: "reads the public page a customer asked us to import",
		Hosts:  []string{"*"},
	}}}

	other := newInstall(t)
	res, err := Import(ctx, other.stores, b, ImportOptions{Name: "restored", OwnerID: other.owner})
	if err != nil {
		t.Fatalf("import: %v", err)
	}

	if len(res.Requires) != 1 {
		t.Fatalf("the declaration did not reach the operator: %+v", res.Requires)
	}
	need := res.Requires[0]
	if need.Kind != "egress" || need.Agent != "reader" {
		t.Errorf("what was asked for came through wrong: %+v", need)
	}
	// The reason is the whole value of it: "needs the internet" is not
	// something an operator can weigh.
	if !strings.Contains(need.Reason, "customer asked us to import") {
		t.Errorf("the reason did not survive: %q", need.Reason)
	}
	if need.Granted {
		t.Fatal("importing a bundle granted what the bundle asked for; a declaration is not a permission")
	}

	// And nothing on this install actually gained the web. The grant is a row
	// an operator writes, and no import writes one.
	granted, err := other.stores.Workspaces.ListEgressGrants(ctx, res.Workspace.ID)
	if err != nil {
		t.Fatalf("read the outward grants: %v", err)
	}
	if len(granted) != 0 {
		t.Fatalf("the import granted outward access to %d agents", len(granted))
	}
}

// A bundle that declares nothing says nothing, rather than an empty promise.
func TestABundleWithNoDeclarationAsksForNothing(t *testing.T) {
	t.Parallel()
	i := newInstall(t)
	ctx := context.Background()
	from := i.createWorkspace("source", workspace.AgentSpec{Name: "orchestrator", Role: "You run this."})

	b, err := Export(ctx, i.stores, from.ID, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if b.Requires != nil {
		t.Fatalf("an export invented a requirement: %+v", b.Requires)
	}

	other := newInstall(t)
	res, err := Import(ctx, other.stores, b, ImportOptions{Name: "restored", OwnerID: other.owner})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Requires) != 0 {
		t.Fatalf("an import invented a requirement: %+v", res.Requires)
	}
}

// The declaration survives a round trip through JSON, because a bundle is a
// document that is written on one machine and read on another.
func TestADeclarationSurvivesTheDocument(t *testing.T) {
	t.Parallel()
	i := newInstall(t)
	ctx := context.Background()
	from := i.createWorkspace("source", workspace.AgentSpec{Name: "orchestrator", Role: "You run this."})

	b, err := Export(ctx, i.stores, from.ID, Options{})
	if err != nil {
		t.Fatal(err)
	}
	b.Requires = &Requires{Egress: []EgressNeed{{Reason: "fetches the page", Hosts: []string{"example.com"}}}}

	again := writtenAndRead(t, b)
	if again.Requires == nil || len(again.Requires.Egress) != 1 {
		t.Fatalf("the declaration did not survive being written and read: %+v", again.Requires)
	}
	if got := again.Requires.Egress[0].Hosts; len(got) != 1 || got[0] != "example.com" {
		t.Errorf("the hosts did not survive: %v", got)
	}
}

// writtenAndRead puts a bundle through JSON, which is what actually happens
// between the two installs.
func writtenAndRead(t *testing.T, b Bundle) Bundle {
	t.Helper()
	raw, err := json.Marshal(b)
	if err != nil {
		t.Fatalf("write the bundle: %v", err)
	}
	var again Bundle
	if err := json.Unmarshal(raw, &again); err != nil {
		t.Fatalf("read the bundle back: %v", err)
	}
	return again
}
