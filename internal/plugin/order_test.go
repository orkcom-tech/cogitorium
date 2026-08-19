package plugin

import (
	"errors"
	"strings"
	"testing"
)

func inst(id string, requires, overrides []string) Installed {
	return Installed{
		ID: id, Enabled: true,
		Manifest: Manifest{ID: id, Requires: requires, Overrides: overrides},
	}
}

// The operator's list is the tie-break, not the rule. A hand-written order
// with nothing to repair has to come back exactly as written, or somebody's
// deliberate arrangement is being quietly rearranged.
func TestAnOrderWithNothingToRepairIsUntouched(t *testing.T) {
	saved := []string{"c", "a", "b"}
	got, moves, err := LayerOrder([]Installed{inst("a", nil, nil), inst("b", nil, nil), inst("c", nil, nil)}, saved)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, ",") != "c,a,b" {
		t.Fatalf("order was rearranged: %v", got)
	}
	if len(moves) != 0 {
		t.Fatalf("reported moves with nothing to move: %+v", moves)
	}
}

// Position is precedence, so a wrapper placed FIRST is the thing being
// wrapped. This renders without error and is close to undiagnosable from the
// screen, which is exactly why it is repaired rather than reported.
func TestAPluginIsMovedAfterWhatItRequires(t *testing.T) {
	got, moves, err := LayerOrder(
		[]Installed{inst("wrapper", []string{"base"}, nil), inst("base", nil, nil)},
		[]string{"wrapper", "base"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, ",") != "base,wrapper" {
		t.Fatalf("wrapper did not land after base: %v", got)
	}
	if len(moves) != 1 || moves[0].ID != "wrapper" || moves[0].After != "base" {
		t.Fatalf("the move was not reported: %+v", moves)
	}
	if !strings.Contains(moves[0].Why, "requires") {
		t.Fatalf("the reason does not say where the edge came from: %q", moves[0].Why)
	}
}

// Overriding a name in somebody else's namespace is a statement that they have
// to be there first, and an operator who never wrote an edge should be told
// where this one came from.
func TestOverridingAnotherPluginsNamespaceOrdersAfterIt(t *testing.T) {
	got, moves, err := LayerOrder(
		[]Installed{inst("skin", nil, []string{"radar.row.item"}), inst("radar", nil, nil)},
		[]string{"skin", "radar"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, ",") != "radar,skin" {
		t.Fatalf("skin did not land after radar: %v", got)
	}
	if len(moves) != 1 || !strings.Contains(moves[0].Why, "radar.row.item") {
		t.Fatalf("the derived edge was not explained: %+v", moves)
	}
}

// Overriding a core name says nothing about other plugins, so it must not
// invent an edge to a plugin called "cog" that cannot exist.
func TestOverridingACoreNameCreatesNoEdge(t *testing.T) {
	_, moves, err := LayerOrder(
		[]Installed{inst("skin", nil, []string{"cog.shell.tokens"})}, []string{"skin"})
	if err != nil {
		t.Fatal(err)
	}
	if len(moves) != 0 {
		t.Fatalf("a core override invented an edge: %+v", moves)
	}
}

// The ordinary case for an optional companion. Refusing here would make every
// plugin that can cooperate with another one require it.
func TestRequiringSomethingNotInstalledIsNotAnError(t *testing.T) {
	got, _, err := LayerOrder([]Installed{inst("a", []string{"absent"}, nil)}, []string{"a"})
	if err != nil || strings.Join(got, ",") != "a" {
		t.Fatalf("got %v, %v", got, err)
	}
}

func TestALoopIsRefusedAndNamed(t *testing.T) {
	_, _, err := LayerOrder(
		[]Installed{inst("a", []string{"b"}, nil), inst("b", []string{"a"}, nil)},
		[]string{"a", "b"},
	)
	var ce CycleError
	if !errors.As(err, &ce) {
		t.Fatalf("a loop was not refused: %v", err)
	}
	if !strings.Contains(err.Error(), "a") || !strings.Contains(err.Error(), "b") {
		t.Fatalf("the refusal does not name the plugins: %v", err)
	}
}

func TestAManifestCannotRequireItself(t *testing.T) {
	m := Manifest{Schema: 1, ID: "loop", Name: "Loop", Version: "1.0.0",
		Host: Host{Contract: Contract}, Requires: []string{"loop"}}
	ps := m.Validate()
	if len(ps) == 0 || !strings.Contains(ps.Error(), "itself") {
		t.Fatalf("a self-requirement was accepted: %v", ps)
	}
}
