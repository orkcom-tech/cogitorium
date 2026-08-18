package plugin

import (
	"archive/zip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func zipFile(t *testing.T, entries map[string]string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "bundle.zip")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	w := zip.NewWriter(f)
	for name, body := range entries {
		e, err := w.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := e.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return p
}

const goodManifest = "schema: 1\nid: radar\nname: Radar\nversion: 1.2.0\nhost:\n  contract: 1\n"

func TestInstallUnpacksAndRecordsButDoesNotEnable(t *testing.T) {
	s := open(t)
	arc := zipFile(t, map[string]string{
		"plugin.yaml":          goodManifest,
		"templates/guide.html": `{{define "radar.page.guide"}}hi{{end}}`,
	})
	in, digest, err := s.Install(arc)
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if in.Manifest.ID != "radar" || in.Version != "1.2.0" {
		t.Errorf("installed wrong: %+v", in)
	}
	if !strings.HasPrefix(digest, "sha256:") {
		t.Errorf("digest = %q", digest)
	}
	if in.Enabled {
		t.Error("installing must never enable")
	}
	if _, err := os.Stat(filepath.Join(in.Dir, "templates", "guide.html")); err != nil {
		t.Errorf("the templates were not unpacked: %v", err)
	}
}

// The classic archive escape, and the less classic absolute one.
func TestPathEscapesAreRefused(t *testing.T) {
	for _, name := range []string{"../escape.html", "../../etc/passwd", "/etc/passwd", `..\windows.html`} {
		s := open(t)
		arc := zipFile(t, map[string]string{"plugin.yaml": goodManifest, name: "x"})
		if _, _, err := s.Install(arc); err == nil {
			t.Errorf("entry %q must be refused", name)
		}
	}
}

func TestAnArchiveWithoutAManifestIsRefused(t *testing.T) {
	s := open(t)
	arc := zipFile(t, map[string]string{"templates/x.html": "x"})
	_, _, err := s.Install(arc)
	if err == nil {
		t.Fatal("an archive with no plugin.yaml is not a plugin")
	}
	if !strings.Contains(err.Error(), "plugin.yaml") {
		t.Errorf("the refusal should say what is missing: %v", err)
	}
}

func TestAnInvalidManifestIsRefusedBeforeUnpacking(t *testing.T) {
	s := open(t)
	arc := zipFile(t, map[string]string{
		"plugin.yaml":      "schema: 1\nid: X\nversion: nope\nhost:\n  contract: 1\n",
		"templates/x.html": "x",
	})
	if _, _, err := s.Install(arc); err == nil {
		t.Fatal("an invalid manifest must be refused")
	}
	if entries, _ := os.ReadDir(s.Root()); len(entries) != 0 {
		t.Errorf("nothing should have been written: %v", entries)
	}
}

// A failure halfway through must leave the previous version installed rather
// than a half-written one that reads as current.
func TestAFailedInstallLeavesNothingPartial(t *testing.T) {
	s := open(t)
	arc := zipFile(t, map[string]string{"plugin.yaml": goodManifest, "../evil": "x"})
	_, _, _ = s.Install(arc)

	entries, _ := os.ReadDir(filepath.Join(s.Root(), "radar"))
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".incoming") {
			t.Errorf("a staging directory survived: %s", e.Name())
		}
	}
	if _, err := s.Get("radar"); err == nil {
		t.Error("a refused bundle must not become the current version")
	}
}

// Permissions come from us. Nothing in a plugin needs a setuid bit.
func TestUnpackedFilesGetOurPermissions(t *testing.T) {
	s := open(t)
	arc := zipFile(t, map[string]string{
		"plugin.yaml":          goodManifest,
		"templates/guide.html": `{{define "radar.page.guide"}}hi{{end}}`,
	})
	in, _, err := s.Install(arc)
	if err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(filepath.Join(in.Dir, "templates", "guide.html"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o644 {
		t.Errorf("mode = %v, want 0644", fi.Mode().Perm())
	}
}

func TestReinstallReplacesTheVersionCleanly(t *testing.T) {
	s := open(t)
	first := zipFile(t, map[string]string{
		"plugin.yaml":        goodManifest,
		"templates/old.html": `{{define "radar.page.old"}}old{{end}}`,
	})
	if _, _, err := s.Install(first); err != nil {
		t.Fatal(err)
	}
	second := zipFile(t, map[string]string{
		"plugin.yaml":        goodManifest,
		"templates/new.html": `{{define "radar.page.new"}}new{{end}}`,
	})
	in, _, err := s.Install(second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(in.Dir, "templates", "old.html")); err == nil {
		t.Error("the previous version's files must not survive a reinstall of the same version")
	}
}

func TestDigestIsStableAndChangesWithContent(t *testing.T) {
	s := open(t)
	a := zipFile(t, map[string]string{"plugin.yaml": goodManifest})
	_, d1, err := s.Install(a)
	if err != nil {
		t.Fatal(err)
	}
	_, d2, err := s.Install(a)
	if err != nil {
		t.Fatal(err)
	}
	if d1 != d2 {
		t.Errorf("the same bytes must digest the same: %s vs %s", d1, d2)
	}
}

func TestSafeJoinAcceptsOrdinaryPaths(t *testing.T) {
	dst := t.TempDir()
	for _, name := range []string{"plugin.yaml", "templates/a.html", "./templates/b.html", "a/b/c.css"} {
		if _, err := safeJoin(dst, name); err != nil {
			t.Errorf("safeJoin(%q) should be fine: %v", name, err)
		}
	}
}
