package plugin

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func install(t *testing.T, s *Store, id, version string, extra ...string) {
	t.Helper()
	dir := filepath.Join(s.Root(), id, version)
	if err := os.MkdirAll(filepath.Join(dir, "templates"), 0o755); err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf("schema: 1\nid: %s\nname: %s\nversion: %s\nhost:\n  contract: 1\n",
		id, strings.ToUpper(id), version)
	for _, e := range extra {
		body += e + "\n"
	}
	if err := os.WriteFile(filepath.Join(dir, "plugin.yaml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "templates", "t.html"),
		[]byte(fmt.Sprintf(`{{define "%s.page.home"}}x{{end}}`, id)), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := s.SetCurrent(id, version); err != nil {
		t.Fatal(err)
	}
}

func open(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// Unpacking an archive is never by itself a decision. This is what makes
// install-then-approve possible: the bytes are inspectable before anything
// about them has taken effect.
func TestPresenceOnDiskDoesNotEnable(t *testing.T) {
	s := open(t)
	install(t, s, "radar", "1.0.0")

	all, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 {
		t.Fatalf("expected one installed plugin, got %d", len(all))
	}
	if all[0].Enabled {
		t.Error("installing must not enable")
	}
	en, _ := s.Enabled()
	if len(en) != 0 {
		t.Errorf("nothing should be enabled yet, got %v", en)
	}
}

// Install appends, so the thing just installed does what it said rather than
// losing to something already there.
func TestEnableAppendsToTheEnd(t *testing.T) {
	s := open(t)
	install(t, s, "alfa", "1.0.0")
	install(t, s, "bravo", "1.0.0")

	if err := s.Enable("alfa"); err != nil {
		t.Fatal(err)
	}
	if err := s.Enable("bravo"); err != nil {
		t.Fatal(err)
	}
	order, _ := s.Order()
	if len(order) != 2 || order[0] != "alfa" || order[1] != "bravo" {
		t.Fatalf("order = %v, want [alfa bravo]", order)
	}
}

func TestEnableIsIdempotent(t *testing.T) {
	s := open(t)
	install(t, s, "alfa", "1.0.0")
	for i := 0; i < 3; i++ {
		if err := s.Enable("alfa"); err != nil {
			t.Fatal(err)
		}
	}
	if order, _ := s.Order(); len(order) != 1 {
		t.Errorf("enabling twice must not add twice: %v", order)
	}
}

func TestEnableRefusesSomethingNotInstalled(t *testing.T) {
	s := open(t)
	if err := s.Enable("ghost"); err == nil {
		t.Fatal("enabling something absent must fail")
	}
}

func TestDisableLeavesItOnDisk(t *testing.T) {
	s := open(t)
	install(t, s, "alfa", "1.0.0")
	_ = s.Enable("alfa")

	if err := s.Disable("alfa"); err != nil {
		t.Fatal(err)
	}
	all, _ := s.List()
	if len(all) != 1 || all[0].Enabled {
		t.Fatalf("disable must keep it installed and off: %+v", all)
	}
}

// Position is precedence, so the list has to be reorderable and a list naming
// something absent is a list whose precedence nobody can read.
func TestReorderRefusesAnUninstalledID(t *testing.T) {
	s := open(t)
	install(t, s, "alfa", "1.0.0")
	if err := s.Reorder([]string{"alfa", "ghost"}); err == nil {
		t.Fatal("ordering something absent must fail")
	}
	if err := s.Reorder([]string{"alfa"}); err != nil {
		t.Fatal(err)
	}
}

func TestListPutsEnabledFirstInOrder(t *testing.T) {
	s := open(t)
	for _, id := range []string{"alfa", "bravo", "charlie"} {
		install(t, s, id, "1.0.0")
	}
	if err := s.Reorder([]string{"charlie", "alfa"}); err != nil {
		t.Fatal(err)
	}
	all, _ := s.List()
	var got []string
	for _, in := range all {
		got = append(got, in.Manifest.ID)
	}
	if len(got) != 3 || got[0] != "charlie" || got[1] != "alfa" || got[2] != "bravo" {
		t.Errorf("order = %v, want [charlie alfa bravo] — enabled in enable order, then the rest by id", got)
	}
}

// A duplicate would give one plugin two positions, and position is precedence.
func TestDuplicateInTheOrderFileIsCollapsed(t *testing.T) {
	s := open(t)
	install(t, s, "alfa", "1.0.0")
	install(t, s, "bravo", "1.0.0")
	path := filepath.Join(filepath.Dir(s.Root()), "plugins.order")
	if err := os.WriteFile(path, []byte("alfa\nbravo\nalfa\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	order, err := s.Order()
	if err != nil {
		t.Fatal(err)
	}
	if len(order) != 2 || order[0] != "alfa" || order[1] != "bravo" {
		t.Errorf("order = %v, want the first position to win", order)
	}
}

// An operator editing this by hand on a server is a first-class way to use it.
func TestOrderFileIgnoresCommentsAndBlanks(t *testing.T) {
	s := open(t)
	install(t, s, "alfa", "1.0.0")
	path := filepath.Join(filepath.Dir(s.Root()), "plugins.order")
	if err := os.WriteFile(path, []byte("# a note\n\nalfa\n\n# another\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	order, _ := s.Order()
	if len(order) != 1 || order[0] != "alfa" {
		t.Errorf("order = %v, want [alfa]", order)
	}
}

// An absent list means nothing is enabled, which is correct for a fresh
// install and different from an empty one somebody wrote on purpose.
func TestAnAbsentOrderFileMeansNothingEnabled(t *testing.T) {
	s := open(t)
	install(t, s, "alfa", "1.0.0")
	order, err := s.Order()
	if err != nil {
		t.Fatalf("an absent list must not be an error: %v", err)
	}
	if len(order) != 0 {
		t.Errorf("order = %v, want empty", order)
	}
}

// The directory name is part of the identity. Trusting either side over the
// other would be a guess.
func TestAManifestThatDisagreesWithItsDirectoryIsRefused(t *testing.T) {
	s := open(t)
	install(t, s, "radar", "1.0.0")
	// Rewrite the manifest to claim a different id.
	p := filepath.Join(s.Root(), "radar", "1.0.0", "plugin.yaml")
	b, _ := os.ReadFile(p)
	_ = os.WriteFile(p, []byte(strings.Replace(string(b), "id: radar", "id: impostor", 1)), 0o644)

	if _, err := s.Get("radar"); err == nil {
		t.Fatal("a manifest claiming another id must be refused")
	}
}

func TestAVersionMismatchIsRefused(t *testing.T) {
	s := open(t)
	install(t, s, "radar", "1.0.0")
	if err := s.SetCurrent("radar", "9.9.9"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get("radar"); err == nil {
		t.Fatal("current pointing at a version that is not there must be refused")
	}
}

// A directory somebody installed that silently does not appear is the worst
// answer available: no plugin, no error, and no reason to look anywhere.
func TestACorruptDirectoryIsReportedNotSkipped(t *testing.T) {
	s := open(t)
	install(t, s, "good", "1.0.0")
	if err := os.MkdirAll(filepath.Join(s.Root(), "broken"), 0o755); err != nil {
		t.Fatal(err)
	}
	all, err := s.List()
	if err != nil {
		t.Fatalf("a broken directory must not fail the listing: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("both the good and the broken install must appear, got %+v", all)
	}

	var broken *Installed
	for i := range all {
		if all[i].ID == "broken" {
			broken = &all[i]
		}
	}
	if broken == nil {
		t.Fatal("the broken directory is missing from the listing")
	}
	if broken.Broken == nil {
		t.Error("a broken install must carry the reason it is broken")
	}
}

// Broken means it cannot be layered, whatever the enable list says.
func TestABrokenPluginIsNeverEnabled(t *testing.T) {
	s := open(t)
	install(t, s, "good", "1.0.0")
	_ = s.Enable("good")
	if err := os.MkdirAll(filepath.Join(s.Root(), "broken"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(s.Root()), "plugins.order"),
		[]byte("good\nbroken\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	enabled, err := s.Enabled()
	if err != nil {
		t.Fatal(err)
	}
	if len(enabled) != 1 || enabled[0].ID != "good" {
		t.Errorf("a broken plugin must not reach the layer stack: %+v", enabled)
	}
}

// A plugin with no templates is legitimate — it may contribute only a tool.
func TestAPluginWithoutTemplatesGetsAnEmptyLayer(t *testing.T) {
	s := open(t)
	install(t, s, "toolonly", "1.0.0")
	if err := os.RemoveAll(filepath.Join(s.Root(), "toolonly", "1.0.0", "templates")); err != nil {
		t.Fatal(err)
	}
	in, err := s.Get("toolonly")
	if err != nil {
		t.Fatal(err)
	}
	fsys, err := in.Templates()
	if err != nil {
		t.Fatalf("a plugin with no templates must not be an error: %v", err)
	}
	if _, err := fsys.Open("anything.html"); err == nil {
		t.Error("the empty layer should hold nothing")
	}
}

// An operator who upgraded through the UI must not be clobbered by the image's
// copy on the next start.
func TestFromImageMarkerSurvives(t *testing.T) {
	s := open(t)
	install(t, s, "alfa", "1.0.0")
	if err := s.MarkFromImage("alfa", "1.0.0"); err != nil {
		t.Fatal(err)
	}
	in, err := s.Get("alfa")
	if err != nil {
		t.Fatal(err)
	}
	if !in.FromImage {
		t.Error("the marker must be readable back")
	}
}

func TestRemoveTakesItOutOfTheListToo(t *testing.T) {
	s := open(t)
	install(t, s, "alfa", "1.0.0")
	_ = s.Enable("alfa")

	if err := s.Remove("alfa"); err != nil {
		t.Fatal(err)
	}
	if order, _ := s.Order(); len(order) != 0 {
		t.Errorf("removing must clear it from the enable list, got %v", order)
	}
	all, _ := s.List()
	if len(all) != 0 {
		t.Errorf("removing must delete it, got %+v", all)
	}
}

// The enable list decides what loads at boot; a half-written one would be a
// server that comes back missing plugins for no visible reason.
func TestOrderIsWrittenAtomically(t *testing.T) {
	s := open(t)
	install(t, s, "alfa", "1.0.0")
	if err := s.Enable("alfa"); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Dir(s.Root())
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "plugins.order.") {
			t.Errorf("a temporary file was left behind: %s", e.Name())
		}
	}
}

func TestOpenRefusesAnEmptyDataDir(t *testing.T) {
	if _, err := Open(""); err == nil {
		t.Fatal("a store needs a data directory")
	}
}
