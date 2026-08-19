package view

import (
	"html/template"
	"strings"
	"testing"
	"testing/fstest"
)

func layer(id string, files map[string]string) Layer {
	f := fstest.MapFS{}
	for name, body := range files {
		f[name] = &fstest.MapFile{Data: []byte(body)}
	}
	return Layer{ID: id, FS: f}
}

func render(t *testing.T, s *Set, name string, data any) string {
	t.Helper()
	var b strings.Builder
	if err := s.Execute(&b, name, data); err != nil {
		t.Fatalf("rendering %s: %v", name, err)
	}
	return b.String()
}

func compose(t *testing.T, layers ...Layer) *Set {
	t.Helper()
	s, err := Compose(template.FuncMap{}, layers...)
	if err != nil {
		t.Fatalf("composing: %v", err)
	}
	return s
}

// The whole point: a later layer replaces a name the earlier one never
// designated as extensible.
func TestALaterLayerReplacesByName(t *testing.T) {
	s := compose(t,
		layer("cog", map[string]string{"row.html": `{{define "cog.row.gear"}}core{{end}}`}),
		layer("skin", map[string]string{"row.html": `{{define "cog.row.gear"}}mine{{end}}`}),
	)
	if got := render(t, s, "cog.row.gear", nil); got != "mine" {
		t.Errorf("got %q, want the later layer's body", got)
	}
}

func TestAPluginAddsInItsOwnNamespace(t *testing.T) {
	s := compose(t,
		layer("cog", map[string]string{"a.html": `{{define "cog.page.home"}}home{{end}}`}),
		layer("radar", map[string]string{"b.html": `{{define "radar.page.guide"}}guide{{end}}`}),
	)
	if got := render(t, s, "radar.page.guide", nil); got != "guide" {
		t.Errorf("got %q", got)
	}
	if got := render(t, s, "cog.page.home", nil); got != "home" {
		t.Errorf("the host's own template must survive, got %q", got)
	}
}

// An override that wants to add rather than replace calls what it replaced.
func TestUnderReachesTheBodyBeneath(t *testing.T) {
	s := compose(t,
		layer("cog", map[string]string{"r.html": `{{define "cog.row.gear"}}CORE{{end}}`}),
		layer("a", map[string]string{"r.html": `{{define "cog.row.gear"}}[{{template "under:cog.row.gear" .}}]{{end}}`}),
	)
	if got := render(t, s, "cog.row.gear", nil); got != "[CORE]" {
		t.Errorf("got %q, want [CORE]", got)
	}
}

// Two plugins wrapping the same name both survive, and neither had to know the
// other existed. Without a per-layer alias the second would reach its own
// wrapper and recurse until the stack ran out.
func TestTwoPluginsWrappingTheSameNameBothSurvive(t *testing.T) {
	s := compose(t,
		layer("cog", map[string]string{"r.html": `{{define "cog.row.gear"}}CORE{{end}}`}),
		layer("a", map[string]string{"r.html": `{{define "cog.row.gear"}}a({{template "under:cog.row.gear" .}}){{end}}`}),
		layer("b", map[string]string{"r.html": `{{define "cog.row.gear"}}b({{template "under:cog.row.gear" .}}){{end}}`}),
	)
	if got := render(t, s, "cog.row.gear", nil); got != "b(a(CORE))" {
		t.Errorf("got %q, want b(a(CORE))", got)
	}
}

// core: has to be typed on purpose, and it discards whatever the plugins below
// did — which is exactly why it is not the default.
func TestCoreReachesPastEveryPlugin(t *testing.T) {
	s := compose(t,
		layer("cog", map[string]string{"r.html": `{{define "cog.row.gear"}}CORE{{end}}`}),
		layer("a", map[string]string{"r.html": `{{define "cog.row.gear"}}a({{template "under:cog.row.gear" .}}){{end}}`}),
		layer("b", map[string]string{"r.html": `{{define "cog.row.gear"}}b({{template "core:cog.row.gear" .}}){{end}}`}),
	)
	if got := render(t, s, "cog.row.gear", nil); got != "b(CORE)" {
		t.Errorf("got %q, want b(CORE)", got)
	}
}

// Wrapping a name the host never defined renders the wrapper and nothing else,
// rather than failing. A legitimately absent body is not an error.
func TestUnderIsEmptyWhenNothingWasThere(t *testing.T) {
	s := compose(t,
		layer("cog", map[string]string{"r.html": `{{define "cog.page.home"}}home{{end}}`}),
		layer("a", map[string]string{"r.html": `{{define "cog.slot.x"}}[{{template "under:cog.slot.x" .}}]{{end}}`}),
	)
	if got := render(t, s, "cog.slot.x", nil); !strings.Contains(got, "[]") {
		t.Errorf("got %q, want an empty under", got)
	}
}

// THE test. If append slots were last-wins, two plugins each adding a rail
// entry would erase each other and neither could prevent it.
func TestAppendSlotsConcatenateEveryContribution(t *testing.T) {
	s := compose(t,
		layer("cog", map[string]string{"s.html": `{{define "cog.slot.rail"}}core.{{end}}`}),
		layer("a", map[string]string{"s.html": `{{define "cog.slot.rail"}}a.{{end}}`}),
		layer("b", map[string]string{"s.html": `{{define "cog.slot.rail"}}b.{{end}}`}),
	)
	if got := render(t, s, "cog.slot.rail", nil); got != "core.a.b." {
		t.Errorf("got %q, want every contribution in enable order", got)
	}
}

func TestExtraSuffixAlsoAppends(t *testing.T) {
	s := compose(t,
		layer("cog", map[string]string{"s.html": `{{define "cog.row.gear.extra"}}{{end}}`}),
		layer("a", map[string]string{"s.html": `{{define "cog.row.gear.extra"}}A{{end}}`}),
		layer("b", map[string]string{"s.html": `{{define "cog.row.gear.extra"}}B{{end}}`}),
	)
	if got := render(t, s, "cog.row.gear.extra", nil); got != "AB" {
		t.Errorf("got %q, want AB", got)
	}
}

// Enable order is precedence, and install appends to the end so the thing you
// just installed does what it said.
func TestEnableOrderDecidesTheWinner(t *testing.T) {
	core := layer("cog", map[string]string{"r.html": `{{define "cog.row.gear"}}core{{end}}`})
	a := layer("a", map[string]string{"r.html": `{{define "cog.row.gear"}}a{{end}}`})
	b := layer("b", map[string]string{"r.html": `{{define "cog.row.gear"}}b{{end}}`})

	if got := render(t, compose(t, core, a, b), "cog.row.gear", nil); got != "b" {
		t.Errorf("got %q, want b", got)
	}
	if got := render(t, compose(t, core, b, a), "cog.row.gear", nil); got != "a" {
		t.Errorf("reordering must change the winner, got %q", got)
	}
}

// The ledger is computed from parsed bytes, so a manifest that lies changes
// nothing about what the operator is shown.
func TestLedgerIsComputedFromBytesNotClaims(t *testing.T) {
	s := compose(t,
		layer("cog", map[string]string{"r.html": `{{define "cog.row.gear"}}core{{end}}`}),
		layer("sneaky", map[string]string{
			"r.html": `{{define "cog.row.gear"}}mine{{end}}`,
			"o.html": `{{define "sneaky.page.own"}}own{{end}}`,
			"s.html": `{{define "cog.slot.rail"}}x{{end}}`,
		}),
	)
	l := s.Ledger()

	over := l.Overridden("sneaky")
	if len(over) != 1 || over[0] != "cog.row.gear" {
		t.Errorf("overrides computed wrong: %v", over)
	}

	byAction := map[LedgerAction]int{}
	for _, e := range l.For("sneaky") {
		byAction[e.Action]++
	}
	if byAction[Adds] != 1 || byAction[Overrides] != 1 || byAction[Extends] != 1 {
		t.Errorf("ledger actions wrong: %v", byAction)
	}
	for _, e := range l.For("sneaky") {
		if e.Action == Overrides && e.Took != "cog" {
			t.Errorf("the ledger should name who it took %s from, got %q", e.Name, e.Took)
		}
	}
}

// A silently inert override is the hardest kind of plugin bug to find, so it
// is named rather than left to be discovered.
func TestOverridingAnAbsentPluginIsReportedAsDangling(t *testing.T) {
	s := compose(t,
		layer("cog", map[string]string{"r.html": `{{define "cog.page.home"}}home{{end}}`}),
		layer("a", map[string]string{"r.html": `{{define "not-installed.row.thing"}}x{{end}}`}),
	)
	var found bool
	for _, e := range s.Ledger().For("a") {
		if e.Action == Dangling && e.Name == "not-installed.row.thing" {
			found = true
		}
	}
	if !found {
		t.Fatalf("an override of an uninstalled plugin must be reported: %+v", s.Ledger().Entries)
	}
}

// Names() is the author-facing contract. Alias and segment machinery is not
// part of it and must never leak into an inspector or the docs.
func TestNamesHidesTheMachinery(t *testing.T) {
	s := compose(t,
		layer("cog", map[string]string{"r.html": `{{define "cog.row.gear"}}core{{end}}` +
			`{{define "cog.slot.rail"}}{{end}}`}),
		layer("a", map[string]string{"r.html": `{{define "cog.row.gear"}}{{template "under:cog.row.gear" .}}{{end}}`}),
	)
	for _, n := range s.Names() {
		if strings.Contains(n, "\x00") || strings.HasPrefix(n, "under:") || strings.HasPrefix(n, "core:") {
			t.Errorf("machinery leaked into the public name list: %q", n)
		}
	}
	var sawGear bool
	for _, n := range s.Names() {
		if n == "cog.row.gear" {
			sawGear = true
		}
	}
	if !sawGear {
		t.Errorf("the public list is missing a real name: %v", s.Names())
	}
}

// Every template must be inside a {{define}} so it has a name to be overridden
// by. Markup loose in a file is a template nobody can address.
func TestMarkupOutsideADefineIsRefused(t *testing.T) {
	_, err := Compose(template.FuncMap{},
		layer("cog", map[string]string{"r.html": `<p>loose</p>{{define "cog.row.gear"}}x{{end}}`}),
	)
	if err == nil {
		t.Fatal("markup outside a define must be refused")
	}
	if !strings.Contains(err.Error(), "define") {
		t.Errorf("the refusal should say what to do: %v", err)
	}
}

func TestBadTemplateNameIsRefusedWithTheLayerNamed(t *testing.T) {
	_, err := Compose(template.FuncMap{},
		layer("cog", map[string]string{"r.html": `{{define "cog.sidebar.thing"}}x{{end}}`}),
	)
	if err == nil {
		t.Fatal("an unknown area must be refused")
	}
	if !strings.Contains(err.Error(), "cog") {
		t.Errorf("the refusal should name the layer: %v", err)
	}
}

func TestDefiningTheSameNameTwiceInOneLayerIsRefused(t *testing.T) {
	_, err := Compose(template.FuncMap{}, layer("cog", map[string]string{
		"a.html": `{{define "cog.row.gear"}}one{{end}}`,
		"b.html": `{{define "cog.row.gear"}}two{{end}}`,
	}))
	if err == nil {
		t.Fatal("one layer defining a name twice is ambiguous and must be refused")
	}
}

// A misspelled function is a parse error naming the plugin's own file, not a
// surprise in front of a user.
func TestUnknownFunctionIsAParseError(t *testing.T) {
	_, err := Compose(template.FuncMap{"known": func() string { return "" }},
		layer("cog", map[string]string{"r.html": `{{define "cog.row.gear"}}{{unknwon}}{{end}}`}),
	)
	if err == nil {
		t.Fatal("an unknown function must fail at compose, not at render")
	}
	if !strings.Contains(err.Error(), "unknwon") {
		t.Errorf("the error should name the function: %v", err)
	}
}

func TestHostFuncsAreAvailableToPlugins(t *testing.T) {
	funcs := template.FuncMap{"shout": strings.ToUpper}
	s, err := Compose(funcs,
		layer("cog", map[string]string{"r.html": `{{define "cog.row.gear"}}{{shout "hi"}}{{end}}`}),
	)
	if err != nil {
		t.Fatalf("composing: %v", err)
	}
	if got := render(t, s, "cog.row.gear", nil); got != "HI" {
		t.Errorf("got %q", got)
	}
}

func TestComposeNeedsAtLeastOneLayer(t *testing.T) {
	if _, err := Compose(template.FuncMap{}); err == nil {
		t.Fatal("composing nothing must be an error")
	}
}

// Aliases are rewritten on the parse tree, so spacing variants and a mention
// of the alias inside ordinary prose both behave.
func TestAliasRewritingIsNotATextSubstitution(t *testing.T) {
	s := compose(t,
		layer("cog", map[string]string{"r.html": `{{define "cog.row.gear"}}CORE{{end}}`}),
		layer("a", map[string]string{"r.html": `{{define "cog.row.gear"}}` +
			`<p>write under:cog.row.gear to wrap</p>{{ template "under:cog.row.gear" . }}{{end}}`}),
	)
	got := render(t, s, "cog.row.gear", nil)
	if !strings.Contains(got, "write under:cog.row.gear to wrap") {
		t.Errorf("prose mentioning the alias was mangled: %q", got)
	}
	if !strings.Contains(got, "CORE") {
		t.Errorf("the spaced alias did not resolve: %q", got)
	}
}

// Using under: on a name that appends is a misunderstanding, not a crime. It
// renders nothing and the ledger says why, rather than taking the whole plugin
// away over a no-op.
func TestUnderOnAnAppendNameWarnsRatherThanFails(t *testing.T) {
	s := compose(t,
		layer("cog", map[string]string{"s.html": `{{define "cog.slot.rail"}}core{{end}}`}),
		layer("a", map[string]string{"s.html": `{{define "cog.slot.rail"}}[{{template "under:cog.slot.rail" .}}]{{end}}`}),
	)
	if got := render(t, s, "cog.slot.rail", nil); got != "core[]" {
		t.Errorf("got %q, want the contributions concatenated with an empty under", got)
	}
	var warned bool
	for _, w := range s.Ledger().Warnings {
		if w.Layer == "a" && w.Name == "cog.slot.rail" {
			warned = true
			if !strings.Contains(w.Message, "concatenated") {
				t.Errorf("the warning should explain what to do instead: %s", w.Message)
			}
		}
	}
	if !warned {
		t.Error("an under: that can never resolve must be said out loud")
	}
}

// A replaced name with nothing beneath it renders the wrapper alone.
func TestUnderOnAReplaceNameWithNoCoreBody(t *testing.T) {
	s := compose(t,
		layer("cog", map[string]string{"r.html": `{{define "cog.page.home"}}home{{end}}`}),
		layer("a", map[string]string{"r.html": `{{define "cog.row.gear"}}[{{template "under:cog.row.gear" .}}]{{end}}`}),
	)
	if got := render(t, s, "cog.row.gear", nil); got != "[]" {
		t.Errorf("got %q, want []", got)
	}
}

// core: on a name the host never defined must resolve to nothing rather than
// fail at render — absence is legitimate, and the wrong failure for it is a
// broken page.
func TestCoreOnAnAbsentHostBodyResolvesToNothing(t *testing.T) {
	s := compose(t,
		layer("cog", map[string]string{"r.html": `{{define "cog.page.home"}}home{{end}}`}),
		layer("a", map[string]string{"r.html": `{{define "cog.row.gear"}}[{{template "core:cog.row.gear" .}}]{{end}}`}),
	)
	if got := render(t, s, "cog.row.gear", nil); got != "[]" {
		t.Errorf("got %q, want []", got)
	}
}
