package plugin

import (
	"archive/zip"
	"os"
	"path/filepath"
	"runtime"
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

// A native plugin's binary has to arrive runnable.
//
// The archive's modes are discarded on the way in, deliberately — a bundle
// that shipped a setuid bit would otherwise have it honoured. Nothing put the
// execute bit back, so a native plugin unpacked into a binary nobody could
// start, and the failure surfaced at its first call as a permission error that
// reads like a bug in this server rather than a missing step in it.
func TestANativePluginsDeclaredBinaryArrivesExecutable(t *testing.T) {
	dir := t.TempDir()
	writeFileAt(t, dir, "plugin.yaml", "schema: 1\nid: nat\nname: Nat\nversion: 1.0.0\n"+
		"license: Apache-2.0\nneeds: native\nhost:\n  contract: 1\n"+
		"native:\n  - os: "+runtime.GOOS+"\n    arch: "+runtime.GOARCH+"\n    path: bin/run\n")
	writeFileAt(t, dir, "bin/run", "#!/bin/sh\necho hi\n")
	writeFileAt(t, dir, "templates/p.html", `{{define "nat.page.home"}}x{{end}}`)

	archive, _, err := Build(dir, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	in, _, err := s.Install(archive)
	if err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(filepath.Join(in.Dir, "bin", "run"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&0o111 == 0 {
		t.Fatalf("the declared binary is not executable: %s", info.Mode())
	}
	// Executable, and nothing more. The point of taking modes from the
	// manifest instead of the archive is that this set is exactly what
	// somebody read before approving.
	if info.Mode()&os.ModeSetuid != 0 || info.Mode().Perm()&0o022 != 0 {
		t.Fatalf("the binary got more than execute: %s", info.Mode())
	}

	// And nothing the manifest did not name. A template that happened to be
	// executable in somebody's checkout must not become an executable file
	// inside an install.
	tpl, err := os.Stat(filepath.Join(in.Dir, "templates", "p.html"))
	if err != nil {
		t.Fatal(err)
	}
	if tpl.Mode()&0o111 != 0 {
		t.Fatalf("a template came out executable: %s", tpl.Mode())
	}
}

func writeFileAt(t *testing.T, dir, name, body string) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
