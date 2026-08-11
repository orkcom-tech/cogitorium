package sandbox

import (
	"archive/tar"
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"
)

// The payload is the fix for a bug that was invisible from the outside: the
// sandbox could not enter a directory it had been handed, and owned nothing it
// had been given. Both facts live in tar headers, so that is what this asserts
// — against a source tree with the tight modes the server actually uses.
func TestPayloadOwnershipAndModes(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "notes"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "notes", "hello.md"), []byte("# hello\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "run.sh"), []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	if err := writePayload(&buf, dir, "/work", payloadOwner{writable: true}); err != nil {
		t.Fatalf("writePayload: %v", err)
	}

	seen := map[string]*tar.Header{}
	tr := tar.NewReader(&buf)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("read payload: %v", err)
		}
		seen[h.Name] = h
	}

	for name, want := range map[string]int64{
		"work/":               0o755, // the process must be able to create files here
		"work/notes/":         0o755, // 0700 on the host, and unenterable before this
		"work/notes/hello.md": 0o644,
		"work/run.sh":         0o755, // an executable stays executable
	} {
		h, ok := seen[name]
		if !ok {
			t.Fatalf("payload is missing %q; it has %v", name, keys(seen))
		}
		if h.Mode != want {
			t.Errorf("%s: mode %o, want %o", name, h.Mode, want)
		}
		// The literal, deliberately, not payloadUID. Asserting a constant
		// against itself passes however wrong the constant is — which this
		// test did, until changing 65534 to the host's uid failed to fail it.
		// 65534 is what `--user 65534:65534` in createArgs makes the process,
		// and owning the payload is only meaningful against that number.
		if h.Uid != 65534 || h.Gid != 65534 {
			t.Errorf("%s: owned by %d:%d, want 65534:65534 — the user the container runs as must own what it is handed",
				name, h.Uid, h.Gid)
		}
	}
}

// A call that carries files asks for the other shape, and this is the whole of
// what makes in/ read-only inside the sandbox: root owns the code and the
// inputs, 65534 owns out/ and nothing else. The modes matter less than the
// ownership — a directory the process does not own cannot have entries created
// or removed in it, whatever the files inside say — so both are asserted.
//
// The sandboxed end of this is proved by running: internal/gear's
// TestSandboxedGearOwnsOutAndNothingElse asks a real container what it can do
// with exactly this payload.
func TestPayloadKeepsOnlyOutWritable(t *testing.T) {
	dir := t.TempDir()
	for _, sub := range []string{"in/data", "out/sub", "outside"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for path, mode := range map[string]os.FileMode{
		"main.sh":            0o700,
		"in/data/report.bin": 0o400,
		"out/sub/keep":       0o600,
		"outside/notes.md":   0o600,
	} {
		if err := os.WriteFile(filepath.Join(dir, filepath.FromSlash(path)), []byte("x"), mode); err != nil {
			t.Fatal(err)
		}
	}

	var buf bytes.Buffer
	if err := writePayload(&buf, dir, "/work", payloadOwner{writable: false, out: "out"}); err != nil {
		t.Fatalf("writePayload: %v", err)
	}
	seen := map[string]*tar.Header{}
	tr := tar.NewReader(&buf)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("read payload: %v", err)
		}
		seen[h.Name] = h
	}

	// uid 0 and no write bit anywhere is the read-only half; 65534 with a write
	// bit is the writable half. The numbers are literals on purpose — asserting
	// payloadUID against itself passes however wrong it is.
	for name, want := range map[string]struct {
		uid  int
		mode int64
	}{
		"work/":                   {0, 0o555}, // nothing may be created beside the code
		"work/main.sh":            {0, 0o555}, // still executable, no longer writable
		"work/in/":                {0, 0o555}, // no file may be planted in the inputs
		"work/in/data/":           {0, 0o555},
		"work/in/data/report.bin": {0, 0o444},
		"work/outside/":           {0, 0o555}, // "out" is not a prefix match on "outside"
		"work/outside/notes.md":   {0, 0o444},
		"work/out/":               {65534, 0o755}, // the one directory the gear owns
		"work/out/sub/":           {65534, 0o755},
		"work/out/sub/keep":       {65534, 0o644},
	} {
		h, ok := seen[name]
		if !ok {
			t.Fatalf("payload is missing %q; it has %v", name, keys(seen))
		}
		if h.Uid != want.uid || h.Gid != want.uid {
			t.Errorf("%s: owned by %d:%d, want %d:%d", name, h.Uid, h.Gid, want.uid, want.uid)
		}
		if h.Mode != want.mode {
			t.Errorf("%s: mode %o, want %o", name, h.Mode, want.mode)
		}
	}
}

// A symlink out of the tree must stay a symlink. Following it would copy
// whatever it pointed at on the host into a container that was given one
// directory.
func TestPayloadDoesNotFollowSymlinks(t *testing.T) {
	dir := t.TempDir()
	secret := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(secret, []byte("provider api key"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(dir, "link")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	var buf bytes.Buffer
	if err := writePayload(&buf, dir, "/work", payloadOwner{writable: true}); err != nil {
		t.Fatalf("writePayload: %v", err)
	}
	tr := tar.NewReader(&buf)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if h.Name != "work/link" {
			continue
		}
		if h.Typeflag != tar.TypeSymlink {
			t.Fatalf("work/link came through as type %q, not a symlink — the target was copied in", h.Typeflag)
		}
		body, _ := io.ReadAll(tr)
		if bytes.Contains(body, []byte("provider api key")) {
			t.Fatal("the symlink's target was written into the payload")
		}
		return
	}
	t.Fatal("work/link is missing from the payload")
}

func keys(m map[string]*tar.Header) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// The payload's ownership is only correct relative to the user the container
// actually runs as. These two numbers live in different files and mean nothing
// apart, so a change to either without the other has to fail somewhere.
func TestContainerRunsAsThePayloadOwner(t *testing.T) {
	d := &Docker{bin: "docker", Image: "test"}
	args := d.createArgs(Spec{Command: "sh"}, false)
	for i, a := range args {
		if a == "--user" && i+1 < len(args) {
			if args[i+1] != "65534:65534" {
				t.Fatalf("the container runs as %q but the payload is written for 65534:65534", args[i+1])
			}
			return
		}
	}
	t.Fatal("createArgs no longer passes --user; the payload's ownership is now a guess")
}
