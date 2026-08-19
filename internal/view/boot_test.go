package view

import (
	"html/template"
	"strings"
	"testing"
	"testing/fstest"
)

func src(id string, files map[string]string) Source {
	f := fstest.MapFS{}
	for n, b := range files {
		f[n] = &fstest.MapFile{Data: []byte(b)}
	}
	return Source{ID: id, FS: f}
}

func coreFS(files map[string]string) fstest.MapFS {
	f := fstest.MapFS{}
	for n, b := range files {
		f[n] = &fstest.MapFile{Data: []byte(b)}
	}
	return f
}

type row struct{ Name string }

var core = coreFS(map[string]string{
	"r.html": `{{define "cog.row.gear"}}core:{{.Name}}{{end}}`,
})

var models = Models{"cog.row.gear": row{}}

func TestABrokenPluginIsDroppedAndTheRestStillServe(t *testing.T) {
	set, r, err := Boot(template.FuncMap{}, core, []Source{
		src("good", map[string]string{"r.html": `{{define "cog.row.gear"}}good:{{.Name}}{{end}}`}),
		src("bad", map[string]string{"b.html": `{{define "cog.page.home"}}{{.Gone}}{{end}}`}),
	}, Models{"cog.row.gear": row{}, "cog.page.home": row{}})
	if err != nil {
		t.Fatalf("boot must not fail over one bad plugin: %v", err)
	}
	if len(r.Disabled) != 1 || r.Disabled[0].ID != "bad" {
		t.Fatalf("expected only 'bad' disabled, got %+v", r.Disabled)
	}
	if len(r.Loaded) != 1 || r.Loaded[0] != "good" {
		t.Errorf("loaded = %v, want [good]", r.Loaded)
	}
	if got := render(t, set, "cog.row.gear", row{Name: "x"}); got != "good:x" {
		t.Errorf("the surviving plugin must still render, got %q", got)
	}
}

// Dropping a plugin changes what is beneath the ones above it, so the rebuilt
// set has to be validated again.
func TestAWrapperIsRevalidatedAfterWhatItWrappedIsDropped(t *testing.T) {
	set, r, err := Boot(template.FuncMap{}, core, []Source{
		src("bad", map[string]string{"r.html": `{{define "cog.row.gear"}}{{.Gone}}{{end}}`}),
		src("wrap", map[string]string{"r.html": `{{define "cog.row.gear"}}[{{template "under:cog.row.gear" .}}]{{end}}`}),
	}, models)
	if err != nil {
		t.Fatalf("boot: %v", err)
	}
	if len(r.Disabled) != 1 || r.Disabled[0].ID != "bad" {
		t.Fatalf("only the broken plugin should go, got %+v", r.Disabled)
	}
	if got := render(t, set, "cog.row.gear", row{Name: "x"}); got != "[core:x]" {
		t.Errorf("the wrapper should now wrap the host's body, got %q", got)
	}
}

// If the product's own templates cannot render there is nothing to serve, and
// starting anyway would put a broken page in front of somebody.
func TestABrokenHostTemplateIsFatal(t *testing.T) {
	broken := coreFS(map[string]string{"r.html": `{{define "cog.row.gear"}}{{.Nope}}{{end}}`})
	if _, _, err := Boot(template.FuncMap{}, broken, nil, models); err == nil {
		t.Fatal("a broken host template must be fatal")
	}
}

func TestAHealthyBootReportsCleanly(t *testing.T) {
	_, r, err := Boot(template.FuncMap{}, core, []Source{
		src("skin", map[string]string{"r.html": `{{define "cog.row.gear"}}skin:{{.Name}}{{end}}`}),
	}, models)
	if err != nil {
		t.Fatal(err)
	}
	if !r.OK() || len(r.Loaded) != 1 {
		t.Errorf("expected a clean boot, got %+v", r)
	}
}

// The reason a row on the plugins page shows must name the plugin, the
// template and the field.
func TestTheDisabledReasonIsReadable(t *testing.T) {
	_, r, err := Boot(template.FuncMap{}, core, []Source{
		src("bad", map[string]string{"r.html": `{{define "cog.row.gear"}}{{.Nmae}}{{end}}`}),
	}, models)
	if err != nil {
		t.Fatal(err)
	}
	reason := r.Disabled[0].Reason()
	for _, want := range []string{"bad", "cog.row.gear", "Nmae", "did you mean"} {
		if !strings.Contains(reason, want) {
			t.Errorf("the reason omits %q: %s", want, reason)
		}
	}
}

func TestSeveralBrokenPluginsAreAllReported(t *testing.T) {
	_, r, err := Boot(template.FuncMap{}, core, []Source{
		src("bad1", map[string]string{"a.html": `{{define "cog.row.gear"}}{{.X}}{{end}}`}),
		src("bad2", map[string]string{"b.html": `{{define "cog.page.home"}}{{.Y}}{{end}}`}),
		src("ok", map[string]string{"c.html": `{{define "ok.page.own"}}fine{{end}}`}),
	}, Models{"cog.row.gear": row{}, "cog.page.home": row{}})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Disabled) != 2 {
		t.Fatalf("both broken plugins should be reported, got %+v", r.Disabled)
	}
	if len(r.Loaded) != 1 || r.Loaded[0] != "ok" {
		t.Errorf("loaded = %v, want [ok]", r.Loaded)
	}
}

// A plugin whose templates will not parse is that plugin's problem. Failing
// the whole boot for it would let any stranger's typo take the product down.
func TestAPluginThatWillNotParseIsDroppedNotFatal(t *testing.T) {
	set, r, err := Boot(template.FuncMap{}, core, []Source{
		src("broken", map[string]string{"b.html": `{{define "cog.row.gear"}}{{if}}{{end}}`}),
		src("good", map[string]string{"g.html": `{{define "cog.row.gear"}}good:{{.Name}}{{end}}`}),
	}, models)
	if err != nil {
		t.Fatalf("one unparseable plugin must not fail the boot: %v", err)
	}
	if len(r.Disabled) != 1 || r.Disabled[0].ID != "broken" {
		t.Fatalf("expected only 'broken' disabled, got %+v", r.Disabled)
	}
	if r.Disabled[0].Reason() == "" {
		t.Error("the reason must survive to the plugins page")
	}
	if got := render(t, set, "cog.row.gear", row{Name: "x"}); got != "good:x" {
		t.Errorf("the working plugin must still render, got %q", got)
	}
}

// A plugin breaking a naming rule is also its own problem.
func TestAPluginWithABadTemplateNameIsDropped(t *testing.T) {
	_, r, err := Boot(template.FuncMap{}, core, []Source{
		src("rulebreaker", map[string]string{"b.html": `{{define "cog.sidebar.thing"}}x{{end}}`}),
	}, models)
	if err != nil {
		t.Fatalf("a bad name must not fail the boot: %v", err)
	}
	if len(r.Disabled) != 1 || r.Disabled[0].ID != "rulebreaker" {
		t.Fatalf("expected 'rulebreaker' disabled, got %+v", r.Disabled)
	}
}

// The host breaking its own rules is still fatal: there is nothing to serve.
func TestTheHostFailingToComposeIsStillFatal(t *testing.T) {
	broken := coreFS(map[string]string{"r.html": `{{define "cog.sidebar.thing"}}x{{end}}`})
	if _, _, err := Boot(template.FuncMap{}, broken, nil, models); err == nil {
		t.Fatal("the product breaking its own naming rules must be fatal")
	}
}
