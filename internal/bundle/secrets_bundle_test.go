package bundle

import (
	"context"
	"strings"
	"testing"

	"github.com/orkcom-tech/cogitorium/internal/gear"
	"github.com/orkcom-tech/cogitorium/internal/llm"
	"github.com/orkcom-tech/cogitorium/internal/secrets"
	"github.com/orkcom-tech/cogitorium/internal/workspace"
)

// What crosses a machine boundary, and what must not.
//
// A bundle is a document people forward by email. The names a gear asks for
// have to travel — the receiving operator cannot approve what they cannot see
// coming — and nothing else about those names may. Neither may the network
// grant: both grants are decided by the operator of the install the code will
// run on, while they are reading it.
//
// Two real installs, each its own SQLite database with its own ids, and the
// document handed from one to the other. Both claims are asserted against the
// marshalled JSON rather than the Go types, because what leaves the install is
// bytes and a field added later would still be bytes.

const (
	bundleSecret   = "sk-live-atlas-6c02f1ae-never-publish-this"
	bundleVariable = "https://atlas.example.com/public-endpoint"
	bundleKey      = "a-bundle-test-key-that-is-long-enough-to-be-accepted"
)

// namedValues gives an install a store that can hold secrets, so the values
// these tests hunt for actually exist somewhere on the exporting side.
func (i *install) namedValues() *secrets.Store {
	i.t.Helper()
	key, err := secrets.NewKey(bundleKey)
	if err != nil {
		i.t.Fatalf("derive the test key: %v", err)
	}
	return secrets.NewStore(i.db, key)
}

// seedDeclaring builds a workspace whose gear asks for two named values and has
// been granted the network — the two things that must not cross.
func (i *install) seedDeclaring(name string) (workspace.Workspace, gear.Gear) {
	i.t.Helper()
	ctx := context.Background()

	p := i.provider("anthropic-live", llm.TypeAnthropic, "sk-live-do-not-share")
	m := i.model(p, sonnet)
	ws := i.createWorkspace(name, workspace.AgentSpec{
		Name: workspace.OrchestratorName, Role: "You run the atlas project.", ModelID: &m.ID,
	})

	store := i.namedValues()
	if _, err := store.Set(ctx, nil, "ATLAS_TOKEN", secrets.KindSecret, bundleSecret, ""); err != nil {
		i.t.Fatalf("set ATLAS_TOKEN: %v", err)
	}
	if _, err := store.Set(ctx, nil, "ATLAS_ENDPOINT", secrets.KindVariable, bundleVariable, ""); err != nil {
		i.t.Fatalf("set ATLAS_ENDPOINT: %v", err)
	}

	g, err := i.stores.Gears.Forge(ctx, "atlas_reporter", "asks for two names", []string{"text"},
		"python", "main.py", `{"type":"object"}`, []string{"ATLAS_ENDPOINT", "ATLAS_TOKEN"},
		[]gear.File{{Path: "main.py", Content: "import os\nprint(os.environ['ATLAS_ENDPOINT'])\n"}}, ws.ID, 0)
	if err != nil {
		i.t.Fatalf("forge the gear: %v", err)
	}
	if g, err = i.stores.Gears.SetNetwork(ctx, g.ID, true, []string{"api.example.com"}); err != nil {
		i.t.Fatalf("grant the network: %v", err)
	}
	if g, err = i.stores.Gears.SetStatus(ctx, g.ID, gear.StatusApproved, gear.Actor{Name: "test-operator"}); err != nil {
		i.t.Fatalf("approve the gear: %v", err)
	}
	i.bind(g.ID, ws.ID, nil)
	return ws, g
}

// 9. A bundle carries the names and never a value, and an imported gear arrives
// with neither approval nor network.
func TestABundleCarriesTheNamesAndNeitherAValueNorTheNetworkGrant(t *testing.T) {
	src := newInstall(t)
	dst := newInstall(t)
	srcWS, exported := src.seedDeclaring("atlas")

	// This test proves nothing unless the exporting install really holds both
	// values and really granted the network.
	if !exported.NetworkGranted || len(exported.NetworkHosts) != 1 {
		t.Fatalf("the exported gear is not granted the network (%v, %v), so its absence downstream means nothing",
			exported.NetworkGranted, exported.NetworkHosts)
	}
	held, err := src.namedValues().List(context.Background(), nil)
	if err != nil {
		t.Fatalf("list the exporting install's named values: %v", err)
	}
	if len(held) != 2 {
		t.Fatalf("the exporting install holds %d named values, want 2", len(held))
	}

	b := src.export(srcWS.ID, Options{Gears: true})
	if len(b.Gears) != 1 {
		t.Fatalf("the bundle carries %d gears, want 1: %+v", len(b.Gears), b.Gears)
	}

	// The names travel, sorted, because the receiving operator has to know what
	// this gear will want before it runs and fails.
	if got := strings.Join(b.Gears[0].EnvNames, ","); got != "ATLAS_ENDPOINT,ATLAS_TOKEN" {
		t.Errorf("the bundle carries env_names %q; the names are the half that must travel", got)
	}

	document := mustJSON(t, b)
	// The vacuity guard: the search below is the same search, and it finds the
	// names. An absence it reported without this would be an absence of a
	// search that had stopped working.
	for _, name := range []string{"ATLAS_ENDPOINT", "ATLAS_TOKEN"} {
		if !strings.Contains(document, name) {
			t.Fatalf("the bundle does not name %s, so searching it for values proves nothing", name)
		}
	}
	for what, value := range map[string]string{
		"a secret's value":   bundleSecret,
		"a variable's value": bundleVariable,
	} {
		if i := strings.Index(document, value); i >= 0 {
			from := max(i-80, 0)
			to := min(i+len(value)+80, len(document))
			t.Errorf("the bundle carries %s:\n…%s…", what, document[from:to])
		}
	}

	// And the format has nowhere to put either a value or a grant. A field that
	// exists is a field somebody fills in later.
	for _, key := range jsonKeys(t, b.Gears) {
		switch k := strings.ToLower(key); k {
		case "env_values", "values", "secrets", "variables":
			t.Errorf("a bundled gear has a %q field; a value must not be expressible, let alone carried", key)
		case "network", "network_granted", "network_hosts", "hosts", "destinations":
			t.Errorf("a bundled gear has a %q field; the network grant must not cross a machine boundary", key)
		}
	}
	if strings.Contains(document, "api.example.com") {
		t.Errorf("the bundle carries the destination the exporting operator granted:\n%s", document)
	}

	// The other side.
	res := dst.mustImport(b, ImportOptions{IncludeGears: true})
	if len(res.GearsImported) != 1 {
		t.Fatalf("gears imported = %v, want the one", res.GearsImported)
	}
	arrived := dst.gearByName("atlas_reporter")
	if got := strings.Join(arrived.EnvNames, ","); got != "ATLAS_ENDPOINT,ATLAS_TOKEN" {
		t.Errorf("the imported gear declares %q; it must arrive asking for what it asked for", got)
	}
	if arrived.Status != gear.StatusPending {
		t.Errorf("the imported gear is %q, want pending", arrived.Status)
	}
	if arrived.NetworkGranted || len(arrived.NetworkHosts) != 0 {
		t.Errorf("the imported gear arrived able to reach the network (%v, %v) — a permission nobody on this install granted",
			arrived.NetworkGranted, arrived.NetworkHosts)
	}

	// And the importing install learned nothing about what those names mean.
	// The gear will be refused by name on its first call, which is the correct
	// outcome and the reason the names travelled.
	landed, err := dst.namedValues().List(context.Background(), nil)
	if err != nil {
		t.Fatalf("list the importing install's named values: %v", err)
	}
	if len(landed) != 0 {
		t.Errorf("importing a bundle set %d named values on this install: %+v", len(landed), landed)
	}
}
