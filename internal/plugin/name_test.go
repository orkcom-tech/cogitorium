package plugin

import (
	"strings"
	"testing"
)

func TestParseNameAcceptsTheShape(t *testing.T) {
	n, err := ParseName("cog.row.gear")
	if err != nil {
		t.Fatalf("ParseName: %v", err)
	}
	if n.Namespace != "cog" || n.Area != AreaRow || n.Subject != "gear" {
		t.Errorf("parsed wrong: %+v", n)
	}
	if !n.IsCore() {
		t.Error("cog.* is the host's namespace")
	}

	deep, err := ParseName("dark-metrics.field.run-count.label")
	if err != nil {
		t.Fatalf("ParseName: %v", err)
	}
	if deep.Subject != "run-count.label" {
		t.Errorf("subject = %q", deep.Subject)
	}
}

func TestParseNameRejections(t *testing.T) {
	cases := map[string]string{
		"":                  "empty",
		"cog":               "too few segments",
		"cog.row":           "too few segments",
		"Cog.row.gear":      "uppercase",
		"cog.sidebar.thing": "unknown area",
		"cog.row.-lead":     "segment starts with a hyphen",
		"cog.row.trail-":    "segment ends with a hyphen",
		"cog.row.has_under": "underscore",
	}
	for in, why := range cases {
		if _, err := ParseName(in); err == nil {
			t.Errorf("ParseName(%q) should fail (%s)", in, why)
		}
	}
}

// A name is an address. An address that changes when the thing at it changes
// is not an address.
func TestNamesMayNotCarryAVersion(t *testing.T) {
	for _, s := range []string{"cog.row.gear.v2", "cog.row.gear.1-2", "cog.page.thing.2.0"} {
		_, err := ParseName(s)
		if err == nil {
			t.Errorf("ParseName(%q) should refuse a version segment", s)
			continue
		}
		if !strings.Contains(err.Error(), "version") {
			t.Errorf("the refusal should say why: %v", err)
		}
	}
}

// The unknown-area error has to tell an author what they may have meant, not
// only what they got wrong.
func TestUnknownAreaErrorListsTheVocabulary(t *testing.T) {
	_, err := ParseName("cog.panel.thing")
	if err == nil {
		t.Fatal("expected a failure")
	}
	for _, want := range []string{"drawer", "stage", "row"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error should list the areas, missing %q: %v", want, err)
		}
	}
}

func TestEveryAreaParses(t *testing.T) {
	for _, a := range Areas() {
		if _, err := ParseName("cog." + a + ".thing"); err != nil {
			t.Errorf("area %q does not parse: %v", a, err)
		}
	}
}

func TestOwnership(t *testing.T) {
	n := MustParseName("dark-metrics.row.finding")
	if !n.OwnedBy("dark-metrics") {
		t.Error("a plugin owns its own namespace")
	}
	if n.OwnedBy("other") {
		t.Error("a plugin does not own somebody else's namespace")
	}
	if n.IsCore() {
		t.Error("a plugin namespace is not the host's")
	}
}

// If every name were last-wins, two plugins each adding a rail entry would
// both define the rail's own name and only the later would survive — two
// strangers erasing each other with neither able to prevent it.
func TestAppendSlotsConcatenateRatherThanReplace(t *testing.T) {
	appends := []string{"cog.slot.rail-bottom", "cog.row.gear.extra", "dark-metrics.slot.anything"}
	for _, s := range appends {
		n := MustParseName(s)
		if !n.Appends() {
			t.Errorf("%q must append, not replace", s)
		}
	}
	replaces := []string{"cog.row.gear", "cog.page.workspace", "dark-metrics.stage.panel"}
	for _, s := range replaces {
		n := MustParseName(s)
		if n.Appends() {
			t.Errorf("%q must replace, not append", s)
		}
	}
}

func TestSplitAlias(t *testing.T) {
	alias, name, ok := SplitAlias("under:cog.row.gear")
	if !ok || alias != AliasUnder || name != "cog.row.gear" {
		t.Errorf("under: split wrong: %q %q %v", alias, name, ok)
	}
	alias, name, ok = SplitAlias("core:cog.row.gear")
	if !ok || alias != AliasCore || name != "cog.row.gear" {
		t.Errorf("core: split wrong: %q %q %v", alias, name, ok)
	}
	if _, name, ok = SplitAlias("cog.row.gear"); ok || name != "cog.row.gear" {
		t.Errorf("a plain reference is not an alias: %q %v", name, ok)
	}
}

// The area vocabulary is closed, and closing it is the decision. An open set
// drifts into a second naming scheme within a year.
func TestAreaVocabularyIsStable(t *testing.T) {
	want := []string{
		"shell", "page", "stage", "drawer", "list", "row",
		"field", "action", "empty", "badge", "frag", "slot",
	}
	got := Areas()
	if len(got) != len(want) {
		t.Fatalf("the area vocabulary changed size: %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("area %d = %q, want %q. Published plugins name these; "+
				"removing or renaming one invalidates their templates.", i, got[i], want[i])
		}
	}
}

func TestMustParseNamePanicsOnHostBugs(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("a bad compiled-in name is a bug here and must not be silent")
		}
	}()
	MustParseName("nonsense")
}
