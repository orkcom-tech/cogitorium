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
	if err := writePayload(&buf, dir, "/work"); err != nil {
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
	if err := writePayload(&buf, dir, "/work"); err != nil {
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
