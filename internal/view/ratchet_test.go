package view

import (
	"strings"
	"testing"
	"testing/fstest"
)

// The tables are empty today and that is correct rather than unfinished:
// nothing has been renamed. This test exists so the first rename is a one-line
// change with something already watching it.
func TestTheRatchetTablesAreEmptyUntilSomethingIsRenamed(t *testing.T) {
	if len(Ratchets()) != 0 {
		t.Logf("%d compatibility rules are in force", len(Ratchets()))
		for _, r := range Ratchets() {
			if r.From == "" || r.Since == "" || r.Why == "" {
				t.Errorf("a rule must say what, when and why: %+v", r)
			}
			if r.From == r.To {
				t.Errorf("a rule that renames a name to itself does nothing: %+v", r)
			}
		}
	}
}

// withRatchet installs a rule for the length of a test, which is how the
// mechanism is exercised while the real table is empty.
func withRatchet(t *testing.T, r Ratchet) {
	t.Helper()
	templateRatchets = append(templateRatchets, r)
	t.Cleanup(func() { templateRatchets = templateRatchets[:len(templateRatchets)-1] })
}

// The whole point: a plugin published against last year's name keeps
// rendering.
func TestARenamedNameStillRenders(t *testing.T) {
	withRatchet(t, Ratchet{From: "cog.row.gear", To: "cog.row.tool", Since: "2.4.0",
		Why: "a gear is one kind of tool and the row shows all of them."})

	core := fstest.MapFS{"c.html": &fstest.MapFile{Data: []byte(
		`{{define "cog.row.tool"}}core{{end}}`)}}
	old := fstest.MapFS{"o.html": &fstest.MapFile{Data: []byte(
		`{{define "cog.row.gear"}}from an old plugin{{end}}`)}}

	set, err := Compose(Funcs(),
		Layer{ID: "cog", FS: core}, Layer{ID: "vintage", FS: old})
	if err != nil {
		t.Fatalf("composing: %v", err)
	}
	if got := render(t, set, "cog.row.tool", nil); got != "from an old plugin" {
		t.Errorf("the old name should resolve to the new one, got %q", got)
	}
}

// A rule nobody is told about is a rule that becomes permanent.
func TestARenameIsReportedToBothPeopleWhoCareAboutIt(t *testing.T) {
	withRatchet(t, Ratchet{From: "cog.row.gear", To: "cog.row.tool", Since: "2.4.0",
		Why: "a gear is one kind of tool."})

	core := fstest.MapFS{"c.html": &fstest.MapFile{Data: []byte(`{{define "cog.row.tool"}}x{{end}}`)}}
	old := fstest.MapFS{"o.html": &fstest.MapFile{Data: []byte(`{{define "cog.row.gear"}}y{{end}}`)}}

	set, err := Compose(Funcs(), Layer{ID: "cog", FS: core}, Layer{ID: "vintage", FS: old})
	if err != nil {
		t.Fatal(err)
	}
	notes := set.Ledger().Notes
	if len(notes) != 1 {
		t.Fatalf("expected one note, got %+v", notes)
	}
	n := notes[0]
	if n.Layer != "vintage" || n.What != "cog.row.gear" {
		t.Errorf("the note must name the plugin and the name: %+v", n)
	}
	for _, want := range []string{"cog.row.tool", "2.4.0", "not permanent"} {
		if !strings.Contains(n.Message, want) {
			t.Errorf("the message omits %q: %s", want, n.Message)
		}
	}
}

// An updated plugin that still ships the old name must not have its own
// current definition overwritten by its own leftover.
func TestAPluginDefiningBothKeepsItsCurrentOne(t *testing.T) {
	withRatchet(t, Ratchet{From: "cog.row.gear", To: "cog.row.tool", Since: "2.4.0", Why: "x."})

	core := fstest.MapFS{"c.html": &fstest.MapFile{Data: []byte(`{{define "cog.row.tool"}}core{{end}}`)}}
	both := fstest.MapFS{"b.html": &fstest.MapFile{Data: []byte(
		`{{define "cog.row.gear"}}OLD{{end}}{{define "cog.row.tool"}}NEW{{end}}`)}}

	set, err := Compose(Funcs(), Layer{ID: "cog", FS: core}, Layer{ID: "updated", FS: both})
	if err != nil {
		t.Fatal(err)
	}
	if got := render(t, set, "cog.row.tool", nil); got != "NEW" {
		t.Errorf("the plugin's current definition must win over its own leftover, got %q", got)
	}
}

// A retired name has no replacement, which is a different fact and needs a
// different sentence.
func TestARetiredNameSaysItHasNoReplacement(t *testing.T) {
	withRatchet(t, Ratchet{From: "cog.row.gear", Since: "3.0.0",
		Why: "the row it belonged to no longer exists."})

	core := fstest.MapFS{"c.html": &fstest.MapFile{Data: []byte(`{{define "cog.page.home"}}x{{end}}`)}}
	old := fstest.MapFS{"o.html": &fstest.MapFile{Data: []byte(`{{define "cog.row.gear"}}y{{end}}`)}}

	set, err := Compose(Funcs(), Layer{ID: "cog", FS: core}, Layer{ID: "vintage", FS: old})
	if err != nil {
		t.Fatal(err)
	}
	notes := set.Ledger().Notes
	if len(notes) != 1 {
		t.Fatalf("notes = %+v", notes)
	}
	if !strings.Contains(notes[0].Message, "no replacement") {
		t.Errorf("a retirement must say so: %s", notes[0].Message)
	}
}

func TestLookupsAnswerForCurrentNames(t *testing.T) {
	if _, ok := TemplateRatchet("cog.row.nav"); ok {
		t.Error("a current name is not ratcheted")
	}
	if _, ok := ModelRatchet("cog.row.nav", "Label"); ok {
		t.Error("a current field is not ratcheted")
	}
	if _, ok := TokenRatchet("--cog-ground"); ok {
		t.Error("a current token is not ratcheted")
	}
}
