package view

import (
	"html/template"
	"strings"
	"testing"
	"testing/fstest"
)

// The host's own layer must compose and render on its own, before any plugin
// is involved. If this fails there is nothing to serve.
func TestTheCoreLayerComposesAndValidates(t *testing.T) {
	_, r, err := Boot(Funcs(), Core(), nil, CoreModels())
	if err != nil {
		t.Fatalf("the host's own templates must boot: %v", err)
	}
	if !r.OK() {
		t.Fatalf("unexpected failures: %+v", r.Disabled)
	}
	if len(r.Unvalidated) != 0 {
		t.Errorf("every core name should have a registered model; missing: %v", r.Unvalidated)
	}
}

// Every name the host ships has to obey the naming rules it asks plugins to
// obey. A host that exempted itself would be publishing a vocabulary it does
// not speak.
func TestEveryCoreNameIsAddressable(t *testing.T) {
	set, _, err := Boot(Funcs(), Core(), nil, CoreModels())
	if err != nil {
		t.Fatal(err)
	}
	names := set.Names()
	if len(names) < 8 {
		t.Fatalf("the core layer looks empty: %v", names)
	}
	models := CoreModels()
	for _, n := range names {
		if _, ok := models[n]; !ok {
			t.Errorf("core ships %q with no registered model, so nothing can check an override of it", n)
		}
	}
}

func TestTheDocumentCarriesTheApplicationThroughUntouched(t *testing.T) {
	set, _, err := Boot(Funcs(), Core(), nil, CoreModels())
	if err != nil {
		t.Fatal(err)
	}
	// What Vite actually writes: a hashed module and a hashed stylesheet. The
	// shell must not restate, rewrite or escape any of it.
	appHead := `<title>Cogitorium</title>` +
		`<script type="module" crossorigin src="/assets/index-D5NMYaXw.js"></script>` +
		`<link rel="stylesheet" crossorigin href="/assets/index-Doin25bG.css">`

	out := render(t, set, "cog.shell.document", Shell{
		Ctx:     Ctx{Lang: "en", Theme: "dark", T: DefaultStrings()},
		AppHead: template.HTML(appHead),
		Body:    template.HTML(`<div id="root"></div>`),
	})
	for _, want := range []string{"<!doctype html>", `lang="en"`, `data-theme="dark"`,
		"index-D5NMYaXw.js", "index-Doin25bG.css", "<title>Cogitorium</title>", `id="root"`} {
		if !strings.Contains(out, want) {
			t.Errorf("the document omits %q:\n%s", want, out)
		}
	}
	// Escaping the build output would serve a page whose script tag is text.
	if strings.Contains(out, "&lt;script") || strings.Contains(out, "&#34;") {
		t.Errorf("the application's head was escaped:\n%s", out)
	}
}

// The rail is rendered by the document now, which is what stopped
// cog.shell.rail, cog.row.nav and cog.slot.rail from being names an author
// could override to no effect.
//
// This test asserted the opposite until the shell started calling it — a
// deliberate placeholder, and the assertion that had to flip for the promise
// to become true.
func TestTheDocumentRendersTheRail(t *testing.T) {
	set, _, err := Boot(Funcs(), Core(), nil, CoreModels())
	if err != nil {
		t.Fatal(err)
	}
	doc := render(t, set, "cog.shell.document", Shell{
		Ctx: Ctx{T: DefaultStrings()},
		Nav: []NavItem{{Label: "Workspaces", Href: "/workspaces", Current: true}},
	})
	for _, want := range []string{"<nav", "Workspaces", `aria-current="page"`} {
		if !strings.Contains(doc, want) {
			t.Errorf("the document omits %q:\n%s", want, doc)
		}
	}

	// And the hypermedia layer is served from this binary rather than fetched:
	// the interface reaches nothing on the network, and a library would read
	// the same in a packet capture as anything else.
	for _, want := range []string{"/assets/htmx.min.js", "/assets/htmx-sse.js"} {
		if !strings.Contains(doc, want) {
			t.Errorf("the document does not load %q:\n%s", want, doc)
		}
	}
	if strings.Contains(doc, "//unpkg") || strings.Contains(doc, "//cdn") {
		t.Errorf("the document fetches a script from the network:\n%s", doc)
	}
}

// A plugin overriding the row gets its body on every entry, which is the whole
// reason the row is named separately from the rail around it.
func TestAPluginCanTakeOverTheRailRow(t *testing.T) {
	set, _, err := Boot(Funcs(), Core(), []Source{{
		ID: "skin",
		FS: fstest.MapFS{"t.html": &fstest.MapFile{
			Data: []byte(`{{define "cog.row.nav"}}<b class="mine">{{.Label}}</b>{{end}}`),
		}},
	}}, CoreModels())
	if err != nil {
		t.Fatal(err)
	}
	doc := render(t, set, "cog.shell.document", Shell{
		Ctx: Ctx{T: DefaultStrings()},
		Nav: []NavItem{{Label: "Workspaces", Href: "/workspaces"}, {Label: "Map", Href: "/map"}},
	})
	if strings.Count(doc, `class="mine"`) != 2 {
		t.Errorf("the override did not reach every row:\n%s", doc)
	}
}

// Overriding one template reskins the whole product with no code. This is the
// cheapest thing a plugin can do and it has to keep working.
func TestAPluginCanReskinByOverridingOneName(t *testing.T) {
	skin := fstest.MapFS{"t.html": &fstest.MapFile{Data: []byte(
		`{{define "cog.shell.tokens"}}<style>:root{--ground:#000}</style>{{end}}`)}}

	set, r, err := Boot(Funcs(), Core(), []Source{{ID: "midnight", FS: skin}}, CoreModels())
	if err != nil {
		t.Fatal(err)
	}
	if !r.OK() {
		t.Fatalf("the skin should load: %+v", r.Disabled)
	}
	out := render(t, set, "cog.shell.document", Shell{Ctx: Ctx{T: DefaultStrings()}})
	if !strings.Contains(out, "--ground:#000") {
		t.Errorf("the override did not reach the document:\n%s", out)
	}
}

// Two plugins adding a rail entry must both get one. This is the append-slot
// rule reaching the real shell rather than a synthetic fixture.
func TestTwoPluginsBothGetARailEntry(t *testing.T) {
	a := fstest.MapFS{"t.html": &fstest.MapFile{Data: []byte(
		`{{define "cog.slot.rail"}}<a href="/p/alfa/">Alfa</a>{{end}}`)}}
	b := fstest.MapFS{"t.html": &fstest.MapFile{Data: []byte(
		`{{define "cog.slot.rail"}}<a href="/p/bravo/">Bravo</a>{{end}}`)}}

	set, r, err := Boot(Funcs(), Core(), []Source{{ID: "alfa", FS: a}, {ID: "bravo", FS: b}}, CoreModels())
	if err != nil {
		t.Fatal(err)
	}
	if !r.OK() {
		t.Fatalf("both should load: %+v", r.Disabled)
	}
	out := render(t, set, "cog.shell.rail", Shell{Ctx: Ctx{T: DefaultStrings()}})
	if !strings.Contains(out, "Alfa") || !strings.Contains(out, "Bravo") {
		t.Errorf("both rail entries must survive:\n%s", out)
	}
}

// A button the host adds later must appear inside an override somebody wrote
// before it existed. That only works because actions are data.
func TestAnActionAddedLaterAppearsInsideAnOldOverride(t *testing.T) {
	// The plugin overrode the action list a long time ago and knows nothing
	// about whatever actions exist now.
	old := fstest.MapFS{"t.html": &fstest.MapFile{Data: []byte(
		`{{define "cog.list.actions"}}<div class="mine">{{range .}}{{template "cog.action.button" .}}{{end}}</div>{{end}}`)}}

	set, r, err := Boot(Funcs(), Core(), []Source{{ID: "old", FS: old}}, CoreModels())
	if err != nil {
		t.Fatal(err)
	}
	if !r.OK() {
		t.Fatalf("%+v", r.Disabled)
	}
	out := render(t, set, "cog.list.actions", []Action{
		{Label: "Approve", Href: "/a", Method: "POST"},
		{Label: "Delete", Href: "/d", Method: "POST", Danger: true},
	})
	if !strings.Contains(out, "Approve") || !strings.Contains(out, "Delete") {
		t.Errorf("a new action must render inside the old override:\n%s", out)
	}
	if !strings.Contains(out, `class="mine"`) {
		t.Errorf("the override should still be in charge of the container:\n%s", out)
	}
	if !strings.Contains(out, "is-danger") {
		t.Errorf("the action's own flags must survive:\n%s", out)
	}
}

// A template has no business holding a credential, and a model that carried
// one would eventually render it.
func TestTheViewerModelCarriesNoSecrets(t *testing.T) {
	for _, f := range fieldsOf(Viewer{}) {
		switch strings.ToLower(f) {
		case "password", "passwordhash", "hash", "token", "secret", "apikey":
			t.Errorf("Viewer exposes %q to templates", f)
		}
	}
}

// Every function is a permanent promise: a call that no longer resolves is a
// parse error that takes a whole plugin down, so the set stays small and
// deliberate.
func TestTheFunctionSetIsDeliberate(t *testing.T) {
	want := map[string]bool{"join": true, "hasPrefix": true}
	got := Funcs()
	if len(got) != len(want) {
		t.Fatalf("the function set changed size: %v. Every name here is a permanent "+
			"promise to every plugin author — add one deliberately, with this test.", keysOf(got))
	}
	for name := range want {
		if _, ok := got[name]; !ok {
			t.Errorf("missing function %q", name)
		}
	}
}

func keysOf[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
