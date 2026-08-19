package jsrt

import (
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// The committed engine and the source it was built from must agree.
//
// Same shape as the openapi.yaml, plugin.schema.json and registry.json checks,
// and here it matters more than any of them: this is the one file in the
// repository nobody can read. A binary that has drifted from its source is a
// binary somebody has to take on faith, which is exactly what building it from
// Go source instead of vendoring QuickJS was meant to avoid.
//
// Rebuild with: sh internal/jsrt/generate.sh
func TestTheEmbeddedEngineMatchesItsSource(t *testing.T) {
	want, err := os.ReadFile("engine.source-hash")
	if err != nil {
		t.Fatalf("%v\n\nBuild it:\n  sh internal/jsrt/generate.sh", err)
	}

	var files []string
	err = filepath.WalkDir("guest", func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if strings.HasSuffix(p, ".go") || d.Name() == "go.mod" || d.Name() == "go.sum" {
			files = append(files, p)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(files)

	// Shelled out rather than reimplemented, so the test and the script cannot
	// disagree about what "the hash of the source" means.
	inner := exec.Command("shasum", append([]string{"-a", "256"}, files...)...)
	sum, err := inner.Output()
	if err != nil {
		t.Skipf("shasum is unavailable here: %v", err)
	}
	outer := exec.Command("shasum", "-a", "256")
	outer.Stdin = strings.NewReader(string(sum))
	got, err := outer.Output()
	if err != nil {
		t.Fatal(err)
	}

	if strings.Fields(string(got))[0] != strings.TrimSpace(string(want)) {
		t.Fatal("the embedded JavaScript engine no longer matches internal/jsrt/guest.\n\n" +
			"Rebuild it:\n  sh internal/jsrt/generate.sh")
	}
}

// It has to decompress, and it has to be a WebAssembly module.
//
// Cheap, and it catches the failure that would otherwise surface as every
// JavaScript plugin on the install refusing at once with a message about
// wazero: a truncated or wrongly-committed artifact.
func TestTheEngineDecompressesToAModule(t *testing.T) {
	m, err := Module()
	if err != nil {
		t.Fatal(err)
	}
	if len(m) < 1<<20 {
		t.Fatalf("the engine is %d bytes, which is too small to be one", len(m))
	}
	if string(m[:4]) != "\x00asm" {
		t.Fatalf("the engine does not start with the WebAssembly magic: %q", m[:4])
	}
}
