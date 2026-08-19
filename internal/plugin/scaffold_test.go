package plugin

import (
	"archive/zip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The first fifteen minutes. What is scaffolded has to be a plugin that loads
// — an author whose very first run fails has learned nothing except that this
// is fiddly.
func TestWhatIsScaffoldedIsAValidPlugin(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "release-radar")
	if err := Scaffold(dir, "release-radar", ""); err != nil {
		t.Fatalf("scaffolding: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "plugin.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	m, err := Parse(b)
	if err != nil {
		t.Fatalf("the scaffolded manifest does not parse: %v", err)
	}
	if ps := m.Validate(); len(ps) != 0 {
		t.Fatalf("the scaffolded manifest is invalid: %v", ps)
	}
	if m.ID != "release-radar" {
		t.Errorf("id = %q", m.ID)
	}
	// It has to actually contribute something, or the author's first run shows
	// them nothing and they have no idea whether it worked.
	if len(m.Pages) == 0 {
		t.Error("the scaffold should ship a page, so the first run shows something")
	}
}

// The template it ships must be the one the manifest points at, or the first
// run is a dangling reference.
func TestTheScaffoldedPageResolves(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "radar")
	if err := Scaffold(dir, "radar", ""); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "plugin.yaml"))
	m, _ := Parse(b)

	body, err := os.ReadFile(filepath.Join(dir, "templates", "radar.html"))
	if err != nil {
		t.Fatal(err)
	}
	want := `{{define "` + m.Pages[0].Template + `"}}`
	if !strings.Contains(string(body), want) {
		t.Errorf("the shipped template does not define %s", m.Pages[0].Template)
	}
}

// --override seeds a real name so the author starts from something that
// renders rather than a blank file and a naming rule to look up.
func TestOverrideScaffoldingUsesUnderNotCore(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "skin")
	if err := Scaffold(dir, "skin", "cog.row.nav"); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "templates", "override.html"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	if !strings.Contains(s, `{{define "cog.row.nav"}}`) {
		t.Error("the override should define the name that was asked for")
	}
	// under: is the default because it composes; core: discards what the
	// plugins below did and has to be typed on purpose.
	if !strings.Contains(s, `under:cog.row.nav`) {
		t.Error("the scaffold should reach for under:, which is what lets two plugins wrap one name")
	}
	if strings.Contains(s, `{{template "core:`) {
		t.Error("core: must not be the default anybody is handed")
	}
}

func TestScaffoldRefusesABadIDAndANonEmptyDirectory(t *testing.T) {
	if err := Scaffold(filepath.Join(t.TempDir(), "x"), "X", ""); err == nil {
		t.Error("an invalid id must be refused before any file is written")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "existing"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Scaffold(dir, "radar", ""); err == nil {
		t.Error("a directory with something in it must be refused")
	}
}

// ── building ──────────────────────────────────────────────────────────────

func scaffolded(t *testing.T, id string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), id)
	if err := Scaffold(dir, id, ""); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestBuildProducesAnInstallableBundle(t *testing.T) {
	dir := scaffolded(t, "radar")
	out, m, err := Build(dir, t.TempDir())
	if err != nil {
		t.Fatalf("building: %v", err)
	}
	if m.ID != "radar" {
		t.Errorf("manifest id = %q", m.ID)
	}

	// The proof that matters: it installs.
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	in, _, err := s.Install(out)
	if err != nil {
		t.Fatalf("what build produced does not install: %v", err)
	}
	if in.Manifest.ID != "radar" {
		t.Errorf("installed id = %q", in.Manifest.ID)
	}
}

// An author's local paths and history should not travel to somebody else's
// machine.
func TestBuildLeavesOutVersionControlAndDroppings(t *testing.T) {
	dir := scaffolded(t, "radar")
	for _, junk := range []string{".git/config", "node_modules/x/index.js", ".DS_Store"} {
		p := filepath.Join(dir, filepath.FromSlash(junk))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	out, _, err := Build(dir, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	r, err := zip.OpenReader(out)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	for _, f := range r.File {
		for _, junk := range []string{".git", "node_modules", ".DS_Store"} {
			if strings.Contains(f.Name, junk) {
				t.Errorf("the bundle carries %s", f.Name)
			}
		}
	}
}

// At build time, where the author can fix it — rather than at install time on
// somebody else's machine, where it is a stylesheet that quietly never
// arrives.
func TestBuildRefusesAManifestNamingAMissingFile(t *testing.T) {
	dir := scaffolded(t, "radar")
	if err := os.Remove(filepath.Join(dir, "theme.css")); err != nil {
		t.Fatal(err)
	}
	_, _, err := Build(dir, t.TempDir())
	if err == nil {
		t.Fatal("a manifest naming a file that is not there must be refused")
	}
	if !strings.Contains(err.Error(), "theme.css") {
		t.Errorf("the refusal should name what is missing: %v", err)
	}
}

func TestBuildRefusesAnInvalidManifest(t *testing.T) {
	dir := scaffolded(t, "radar")
	if err := os.WriteFile(filepath.Join(dir, "plugin.yaml"),
		[]byte("schema: 1\nid: R\nversion: nope\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Build(dir, t.TempDir()); err == nil {
		t.Fatal("an author should learn their plugin is unloadable here, not after somebody downloads it")
	}
}

// ── development layers ────────────────────────────────────────────────────

// No version directory, no digest, no signature. Asking an author to build an
// archive before trying their own work would make their machine the least
// convenient place to do it.
func TestADevelopmentLayerLoadsStraightFromADirectory(t *testing.T) {
	work := scaffolded(t, "radar")
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	in, err := s.AddDev(work)
	if err != nil {
		t.Fatalf("registering: %v", err)
	}
	if !in.Dev || in.Manifest.ID != "radar" {
		t.Fatalf("registered wrong: %+v", in)
	}

	enabled, err := s.Enabled()
	if err != nil {
		t.Fatal(err)
	}
	if len(enabled) != 1 || !enabled[0].Dev {
		t.Fatalf("the development layer should be enabled and marked as one: %+v", enabled)
	}
	// It behaves like any other layer, which is the point: a plugin works the
	// same whether it is being developed or deployed.
	fsys, err := enabled[0].Templates()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fsys.Open("radar.html"); err != nil {
		t.Errorf("its templates should be readable: %v", err)
	}
}

func TestRegisteringADevelopmentLayerTwiceIsIdempotent(t *testing.T) {
	work := scaffolded(t, "radar")
	s, _ := Open(t.TempDir())
	for i := 0; i < 3; i++ {
		if _, err := s.AddDev(work); err != nil {
			t.Fatal(err)
		}
	}
	order, _ := s.Order()
	if len(order) != 1 {
		t.Errorf("order = %v", order)
	}
}

func TestADevelopmentLayerCanBeRemoved(t *testing.T) {
	work := scaffolded(t, "radar")
	s, _ := Open(t.TempDir())
	if _, err := s.AddDev(work); err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveDev(work); err != nil {
		t.Fatal(err)
	}
	if order, _ := s.Order(); len(order) != 0 {
		t.Errorf("order = %v", order)
	}
}

// A broken working directory is reported like any other broken layer rather
// than making the whole listing fail.
func TestABrokenDevelopmentLayerIsReported(t *testing.T) {
	work := scaffolded(t, "radar")
	s, _ := Open(t.TempDir())
	if _, err := s.AddDev(work); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "plugin.yaml"), []byte("not yaml: ["), 0o644); err != nil {
		t.Fatal(err)
	}
	all, err := s.List()
	if err != nil {
		t.Fatalf("a broken development layer must not fail the listing: %v", err)
	}
	if len(all) != 1 || all[0].Broken == nil {
		t.Fatalf("it should be listed with its reason: %+v", all)
	}
	if !all[0].Dev {
		t.Error("and still be marked as a development layer")
	}
}

// Found by using it: the default output directory IS the directory being
// walked, so building in place folded the archive into itself, half-written,
// and produced a zip that read as corrupt with no clue why.
func TestBuildingInPlaceProducesAReadableBundle(t *testing.T) {
	dir := scaffolded(t, "radar")

	out, _, err := Build(dir, dir)
	if err != nil {
		t.Fatalf("building in place: %v", err)
	}
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Install(out); err != nil {
		t.Fatalf("a bundle built in place must install: %v", err)
	}
}

// And building twice must not fold the first bundle into the second.
func TestBuildingTwiceDoesNotNestTheFirstBundle(t *testing.T) {
	dir := scaffolded(t, "radar")
	if _, _, err := Build(dir, dir); err != nil {
		t.Fatal(err)
	}
	out, _, err := Build(dir, dir)
	if err != nil {
		t.Fatal(err)
	}
	r, err := zip.OpenReader(out)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	for _, f := range r.File {
		if strings.HasSuffix(f.Name, ".zip") {
			t.Errorf("the bundle contains a bundle: %s", f.Name)
		}
	}
}

// TestBuildProducesTheNameTheCatalogFetches pins the two ends of publishing
// together.
//
// They were written apart and did not match: the builder emitted
// <id>-<version>.zip while Entry.BundleURL asks GitHub for <id>.zip, so the
// documented publishing instruction — tag a release, attach what you built —
// produced a release nobody could install from. Nothing failed at build time
// and nothing failed at submission time; the first person to find out would
// have been a stranger whose plugin 404ed for everybody.
func TestBuildProducesTheNameTheCatalogFetches(t *testing.T) {
	dir := t.TempDir()
	manifest := "schema: 1\nid: acme\nname: Acme\nversion: 2.4.1\nlicense: Apache-2.0\nhost:\n  contract: 1\n"
	if err := os.WriteFile(filepath.Join(dir, "plugin.yaml"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}

	out, m, err := Build(dir, t.TempDir())
	if err != nil {
		t.Fatalf("build: %v", err)
	}

	want := m.ID + ".zip"
	if got := filepath.Base(out); got != want {
		t.Fatalf("built %q, want %q", got, want)
	}
	// The version is still recoverable — from the manifest inside, which is
	// the copy install trusts anyway.
	if m.Version != "2.4.1" {
		t.Fatalf("version %q", m.Version)
	}

	url := Entry{ID: m.ID, Repo: "someone/acme"}.BundleURL("")
	if !strings.HasSuffix(url, "/"+want) {
		t.Fatalf("catalog fetches %q, builder wrote %q", url, want)
	}
}
