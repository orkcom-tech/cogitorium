package view

import (
	"strings"
	"testing"
)

type gearRow struct {
	Name      string
	CreatedAt string
	Actions   []action
}

type action struct {
	Label string
	Href  string
}

// The scenario the whole check exists for: the host renamed a field, a package
// manager replaced the binary, and a plugin built for the old model is sitting
// in the data directory.
func TestARenamedFieldDisablesThePluginByName(t *testing.T) {
	s := compose(t,
		layer("cog", map[string]string{"r.html": `{{define "cog.row.gear"}}{{.Name}}{{end}}`}),
		layer("radar", map[string]string{"r.html": `{{define "cog.row.gear"}}{{.Titel}}{{end}}`}),
	)
	r := Validate(s, Models{"cog.row.gear": gearRow{}})

	if r.OK() {
		t.Fatal("a template referencing a field that does not exist must fail")
	}
	failed := r.FailedLayers()
	if _, ok := failed["radar"]; !ok {
		t.Fatalf("the failure must be attributed to the plugin whose body renders: %v", failed)
	}
	if _, ok := failed["cog"]; ok {
		t.Error("the host's own layer did not fail and must not be blamed")
	}

	f := failed["radar"][0]
	if f.Field != "Titel" {
		t.Errorf("the failure should name the field, got %q", f.Field)
	}
	msg := f.String()
	for _, want := range []string{"radar", "cog.row.gear", "Titel"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the message omits %q: %s", want, msg)
		}
	}
}

// An error an author can act on beats one they have to read source to
// understand.
func TestASuggestionIsMadeFromTheRealModel(t *testing.T) {
	s := compose(t,
		layer("cog", map[string]string{"r.html": `{{define "cog.row.gear"}}{{.Name}}{{end}}`}),
		layer("radar", map[string]string{"r.html": `{{define "cog.row.gear"}}{{.Nmae}}{{end}}`}),
	)
	r := Validate(s, Models{"cog.row.gear": gearRow{}})
	if r.OK() {
		t.Fatal("expected a failure")
	}
	f := r.Failures[0]
	if f.Suggestion != "Name" {
		t.Errorf("suggestion = %q, want Name", f.Suggestion)
	}
	if !strings.Contains(f.String(), "did you mean") {
		t.Errorf("the suggestion should reach the message: %s", f.String())
	}
}

// A suggestion that is wrong sends an author to change something that was
// already right, so nothing is offered when nothing is close.
func TestNoSuggestionWhenNothingIsClose(t *testing.T) {
	s := compose(t,
		layer("cog", map[string]string{"r.html": `{{define "cog.row.gear"}}{{.Name}}{{end}}`}),
		layer("radar", map[string]string{"r.html": `{{define "cog.row.gear"}}{{.Zebra}}{{end}}`}),
	)
	r := Validate(s, Models{"cog.row.gear": gearRow{}})
	if r.OK() {
		t.Fatal("expected a failure")
	}
	if got := r.Failures[0].Suggestion; got != "" {
		t.Errorf("a distant name must not be suggested, got %q", got)
	}
}

// One bad plugin must not take the product down with it.
func TestOneFailingPluginDoesNotCondemnTheOthers(t *testing.T) {
	s := compose(t,
		layer("cog", map[string]string{
			"r.html": `{{define "cog.row.gear"}}{{.Name}}{{end}}`,
			"p.html": `{{define "cog.page.home"}}{{.Name}}{{end}}`,
		}),
		layer("good", map[string]string{"p.html": `{{define "cog.page.home"}}ok {{.Name}}{{end}}`}),
		layer("bad", map[string]string{"r.html": `{{define "cog.row.gear"}}{{.Gone}}{{end}}`}),
	)
	r := Validate(s, Models{"cog.row.gear": gearRow{}, "cog.page.home": gearRow{}})

	failed := r.FailedLayers()
	if _, ok := failed["bad"]; !ok {
		t.Error("the broken plugin must fail")
	}
	if _, ok := failed["good"]; ok {
		t.Error("a working plugin must not be caught up in another's failure")
	}
}

// A check that quietly covers less than it appears to is worse than no check,
// because it is trusted.
func TestUnvalidatedNamesAreReportedNotSkipped(t *testing.T) {
	s := compose(t,
		layer("cog", map[string]string{"r.html": `{{define "cog.row.gear"}}{{.Name}}{{end}}`}),
		layer("radar", map[string]string{"p.html": `{{define "radar.page.guide"}}static{{end}}`}),
	)
	r := Validate(s, Models{"cog.row.gear": gearRow{}})

	var found bool
	for _, n := range r.Unvalidated {
		if n == "radar.page.guide" {
			found = true
		}
	}
	if !found {
		t.Errorf("a name with no registered model must be reported: %v", r.Unvalidated)
	}
}

// The host's own templates are validated too. A core template that cannot
// render is a bug here, and learning it from a test beats learning it from a
// user.
func TestTheHostIsNotExemptFromItsOwnCheck(t *testing.T) {
	s := compose(t,
		layer("cog", map[string]string{"r.html": `{{define "cog.row.gear"}}{{.NotThere}}{{end}}`}),
	)
	r := Validate(s, Models{"cog.row.gear": gearRow{}})
	if r.OK() {
		t.Fatal("a broken core template must be reported, not exempted")
	}
	if _, ok := r.FailedLayers()["cog"]; !ok {
		t.Errorf("the failure should be attributed to the host: %v", r.FailedLayers())
	}
}

// Every contributor to an append name renders, so each is on the hook for its
// own segment rather than one of them being blamed for all.
func TestEveryContributorToAnAppendNameIsAccountable(t *testing.T) {
	s := compose(t,
		layer("cog", map[string]string{"s.html": `{{define "cog.slot.rail"}}{{end}}`}),
		layer("a", map[string]string{"s.html": `{{define "cog.slot.rail"}}{{.Name}}{{end}}`}),
		layer("b", map[string]string{"s.html": `{{define "cog.slot.rail"}}{{.Missing}}{{end}}`}),
	)
	r := Validate(s, Models{"cog.slot.rail": gearRow{}})
	if r.OK() {
		t.Fatal("expected the broken segment to fail")
	}
	failed := r.FailedLayers()
	if len(failed) == 0 {
		t.Fatal("no layer was blamed")
	}
}

// A working set validates clean, including through wrappers and ranges.
func TestAHealthySetValidatesClean(t *testing.T) {
	s := compose(t,
		layer("cog", map[string]string{"r.html": `{{define "cog.row.gear"}}` +
			`{{.Name}} {{.CreatedAt}}{{range .Actions}}<a href="{{.Href}}">{{.Label}}</a>{{end}}{{end}}`}),
		layer("radar", map[string]string{"r.html": `{{define "cog.row.gear"}}` +
			`<b>{{.Name}}</b>{{template "under:cog.row.gear" .}}{{end}}`}),
	)
	r := Validate(s, Models{"cog.row.gear": gearRow{}})
	if !r.OK() {
		t.Fatalf("a healthy set must validate clean: %v", r.Failures)
	}
}

// Nested model types are walked, so a suggestion can be made for a field that
// lives on a row inside a list rather than only on the top level.
func TestFieldsOfWalksNestedModels(t *testing.T) {
	got := fieldsOf(gearRow{})
	want := map[string]bool{"Name": true, "CreatedAt": true, "Actions": true, "Label": true, "Href": true}
	for w := range want {
		var found bool
		for _, g := range got {
			if g == w {
				found = true
			}
		}
		if !found {
			t.Errorf("fieldsOf missed %q: %v", w, got)
		}
	}
}

// missingkey=error is set on the set, so a map-backed model reports an absent
// key instead of rendering the string "<no value>" into somebody's page.
func TestAMissingMapKeyIsAnErrorNotAnEmptyString(t *testing.T) {
	s := compose(t,
		layer("cog", map[string]string{"r.html": `{{define "cog.row.gear"}}{{.absent}}{{end}}`}),
	)
	r := Validate(s, Models{"cog.row.gear": map[string]string{"present": "x"}})
	if r.OK() {
		t.Fatal("a missing map key must be an error")
	}
	if r.Failures[0].Field != "absent" {
		t.Errorf("the key should be named, got %q", r.Failures[0].Field)
	}
}
