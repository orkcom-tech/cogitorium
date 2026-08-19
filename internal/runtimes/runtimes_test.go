package runtimes

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/orkcom-tech/cogitorium/internal/channel"
)

// The pinned rows are real digests from a real release. This is the test that
// stops one being edited into something plausible and wrong.
func TestThePinnedIndexIsWellFormed(t *testing.T) {
	rows := All()
	if len(rows) == 0 {
		t.Fatal("the index is empty")
	}
	seen := map[string]bool{}
	for _, r := range rows {
		key := r.Technology + "|" + r.Version + "|" + r.Target()
		if seen[key] {
			t.Errorf("two rows for %s", key)
		}
		seen[key] = true

		if len(r.SHA256) != 64 {
			t.Errorf("%s: a sha256 is 64 hex characters, got %q", key, r.SHA256)
		}
		if _, err := hex.DecodeString(r.SHA256); err != nil {
			t.Errorf("%s: the digest is not hex: %v", key, err)
		}
		if !strings.HasPrefix(r.URL, "https://") {
			t.Errorf("%s: a runtime is fetched over https or not at all: %q", key, r.URL)
		}
		if r.Exe == "" || r.Root == "" {
			t.Errorf("%s: the archive layout must be recorded, not guessed", key)
		}
		if (r.OS == "linux") != (r.Libc != "") {
			t.Errorf("%s: libc is part of the key on Linux and meaningless elsewhere", key)
		}
	}
}

// The Alpine image is the case this whole libc distinction exists for.
func TestBothMuslTargetsArePinned(t *testing.T) {
	want := map[string]bool{"linux/amd64/musl": false, "linux/arm64/musl": false}
	for _, r := range All() {
		if r.Technology == "python" {
			if _, ok := want[r.Target()]; ok {
				want[r.Target()] = true
			}
		}
	}
	for target, found := range want {
		if !found {
			t.Errorf("no python pinned for %s — the container channel needs it", target)
		}
	}
}

func profile(os_, arch string, libc channel.Libc, canExec bool) channel.Profile {
	p := channel.Profile{Kind: channel.Local, OS: os_, Arch: arch, Libc: libc, CanExecFromData: canExec}
	if !canExec {
		p.ExecRefusal = "the data volume is mounted noexec"
	}
	return p
}

func TestSelectFindsTheRowForThisMachine(t *testing.T) {
	r, err := Select("python", nil, profile("linux", "amd64", channel.Musl, true))
	if err != nil {
		t.Fatalf("selecting: %v", err)
	}
	if r.Target() != "linux/amd64/musl" {
		t.Errorf("target = %s", r.Target())
	}
}

// The refusal names the technology, the target and what IS pinned, because
// changing the plugin, changing the install and asking for a new target are
// three different actions from three different facts.
func TestAnUnpinnedTargetIsRefusedWithWhatExists(t *testing.T) {
	_, err := Select("python", nil, profile("plan9", "amd64", channel.LibcNone, true))
	if err == nil {
		t.Fatal("an unpinned target must be refused")
	}
	for _, want := range []string{"python", "plan9/amd64", "linux/amd64/musl"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal omits %q: %v", want, err)
		}
	}
}

func TestAnUnpinnedTechnologyIsRefusedWithTheList(t *testing.T) {
	_, err := Select("cobol", nil, profile("linux", "amd64", channel.Musl, true))
	if err == nil {
		t.Fatal("an unpinned technology must be refused")
	}
	if !strings.Contains(err.Error(), "python") {
		t.Errorf("the refusal should list what is pinned: %v", err)
	}
}

func TestAConstraintNothingSatisfiesIsRefused(t *testing.T) {
	_, err := Select("python", func(string) bool { return false }, profile("linux", "amd64", channel.Musl, true))
	if err == nil {
		t.Fatal("expected a refusal")
	}
	if !strings.Contains(err.Error(), "Upgrading Cogitorium") {
		t.Errorf("the refusal should say what moves the list: %v", err)
	}
}

// ── materialising ─────────────────────────────────────────────────────────

// tarball builds a tiny archive shaped like a real runtime tree.
func tarball(t *testing.T, files map[string]string, exe string) ([]byte, string) {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range files {
		mode := int64(0o644)
		if name == exe {
			mode = 0o755
		}
		if err := tw.WriteHeader(&tar.Header{
			Name: name, Mode: mode, Size: int64(len(body)), Typeflag: tar.TypeReg,
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(buf.Bytes())
	return buf.Bytes(), hex.EncodeToString(sum[:])
}

type fakeFetch struct {
	body  []byte
	calls int
	err   error
}

func (f *fakeFetch) Fetch(_ context.Context, _ string) (io.ReadCloser, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return io.NopCloser(bytes.NewReader(f.body)), nil
}

// storeFor builds a Store whose index row is replaced with one pointing at the
// fake archive, so the path logic is tested without a hundred-megabyte
// download.
func storeFor(t *testing.T, body []byte, digest string, allowFetch bool) (*Store, *fakeFetch, Row) {
	t.Helper()
	row := Row{
		Technology: "fake", Version: "1.0.0",
		OS: "linux", Arch: "amd64", Libc: "musl",
		URL: "https://example.invalid/fake.tar.gz", SHA256: digest,
		Root: "fake", Exe: "fake/bin/run",
	}
	index.Rows = append(index.Rows, row)
	t.Cleanup(func() { index.Rows = index.Rows[:len(index.Rows)-1] })

	f := &fakeFetch{body: body}
	s := NewStore(t.TempDir(), "", profile("linux", "amd64", channel.Musl, true), f, allowFetch)
	return s, f, row
}

func TestMaterialiseVerifiesUnpacksAndShares(t *testing.T) {
	body, digest := tarball(t, map[string]string{
		"fake/bin/run": "#!/bin/sh\nexit 0\n",
		"fake/lib/x":   "data",
	}, "fake/bin/run")

	s, f, _ := storeFor(t, body, digest, true)

	got, err := s.Ensure(context.Background(), "fake", nil)
	if err != nil {
		t.Fatalf("ensuring: %v", err)
	}
	if _, err := os.Stat(got.Exe); err != nil {
		t.Fatalf("the interpreter is not where it was promised: %v", err)
	}
	fi, _ := os.Stat(got.Exe)
	if fi.Mode().Perm()&0o111 == 0 {
		t.Error("an interpreter has to be runnable")
	}

	// One runtime per version, shared: a second ask fetches nothing.
	if _, err := s.Ensure(context.Background(), "fake", nil); err != nil {
		t.Fatal(err)
	}
	if f.calls != 1 {
		t.Errorf("fetched %d times; a version is materialised once and shared", f.calls)
	}
}

// Checking afterwards would mean an archive that failed had already written
// itself across the disk.
func TestAWrongDigestUnpacksNothing(t *testing.T) {
	body, _ := tarball(t, map[string]string{"fake/bin/run": "x"}, "fake/bin/run")
	s, _, _ := storeFor(t, body, strings.Repeat("a", 64), true)

	_, err := s.Ensure(context.Background(), "fake", nil)
	if err == nil {
		t.Fatal("a digest mismatch must refuse")
	}
	if !strings.Contains(err.Error(), "Nothing was unpacked") {
		t.Errorf("the refusal should say what did not happen: %v", err)
	}
	entries, _ := os.ReadDir(filepath.Join(s.dataDir, "runtimes"))
	if len(entries) != 0 {
		t.Errorf("something was written despite the mismatch: %v", entries)
	}
}

// An air-gapped install is a supported deployment, and the refusal has to name
// what it wanted so somebody can bring it themselves.
func TestWithoutConsentNothingIsFetched(t *testing.T) {
	body, digest := tarball(t, map[string]string{"fake/bin/run": "x"}, "fake/bin/run")
	s, f, _ := storeFor(t, body, digest, false)

	_, err := s.Ensure(context.Background(), "fake", nil)
	if err == nil {
		t.Fatal("without consent nothing may be fetched")
	}
	if f.calls != 0 {
		t.Error("it reached the network anyway")
	}
	if !strings.Contains(err.Error(), "example.invalid") {
		t.Errorf("the refusal should name where it would have come from: %v", err)
	}
}

// Fetching a hundred megabytes and then discovering the volume will not
// execute it is the wrong order to find that out in.
func TestANoexecVolumeIsRefusedBeforeAnythingIsFetched(t *testing.T) {
	body, digest := tarball(t, map[string]string{"fake/bin/run": "x"}, "fake/bin/run")
	s, f, _ := storeFor(t, body, digest, true)
	s.profile = profile("linux", "amd64", channel.Musl, false)

	_, err := s.Ensure(context.Background(), "fake", nil)
	if err == nil {
		t.Fatal("a noexec volume must refuse a fetched interpreter")
	}
	if f.calls != 0 {
		t.Error("it downloaded before checking whether it could ever run")
	}
	if !strings.Contains(err.Error(), "noexec") {
		t.Errorf("the refusal should carry the probe's own words: %v", err)
	}
}

// The image's seed is used where it is. Copying it into the volume on every
// start would double the storage for nothing.
func TestTheImageSeedIsUsedInPlace(t *testing.T) {
	body, digest := tarball(t, map[string]string{"fake/bin/run": "x"}, "fake/bin/run")
	s, f, row := storeFor(t, body, digest, true)

	ref := t.TempDir()
	dir := filepath.Join(ref, "runtimes", row.Technology, row.Version, "fake", "bin")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "run"), []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	markDir := filepath.Join(ref, "runtimes", row.Technology, row.Version)
	if err := os.WriteFile(filepath.Join(markDir, ".ready"), []byte(digest), 0o644); err != nil {
		t.Fatal(err)
	}
	s.refDir = ref

	got, err := s.Ensure(context.Background(), "fake", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !got.FromSeed {
		t.Error("it should have come from the seed")
	}
	if f.calls != 0 {
		t.Error("it fetched despite the seed being present")
	}
	if !strings.HasPrefix(got.Dir, ref) {
		t.Errorf("the seed must be used in place, got %s", got.Dir)
	}
}

// A directory without the marker is a materialisation that did not finish, and
// an interpreter missing half its standard library fails in ways that look
// like the plugin's fault.
func TestAnUnfinishedRuntimeIsNotTrusted(t *testing.T) {
	body, digest := tarball(t, map[string]string{"fake/bin/run": "x"}, "fake/bin/run")
	s, f, row := storeFor(t, body, digest, true)

	half := filepath.Join(s.dataDir, "runtimes", row.Technology, row.Version, "fake", "bin")
	if err := os.MkdirAll(half, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(half, "run"), []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	// No .ready marker.

	if _, err := s.Ensure(context.Background(), "fake", nil); err != nil {
		t.Fatal(err)
	}
	if f.calls != 1 {
		t.Error("an unfinished tree must be replaced rather than handed out")
	}
}

// These bytes came off the network. A checksum proves they are the bytes
// somebody pinned, not that those bytes are polite.
func TestArchivePathEscapesAreRefused(t *testing.T) {
	for _, name := range []string{"../escape", "/etc/passwd", `..\win`} {
		body, digest := tarball(t, map[string]string{name: "x", "fake/bin/run": "y"}, "fake/bin/run")
		s, _, _ := storeFor(t, body, digest, true)
		if _, err := s.Ensure(context.Background(), "fake", nil); err == nil {
			t.Errorf("entry %q must be refused", name)
		}
	}
}

func TestAnArchiveWithoutTheInterpreterIsRefused(t *testing.T) {
	body, digest := tarball(t, map[string]string{"fake/lib/x": "data"}, "")
	s, _, _ := storeFor(t, body, digest, true)

	_, err := s.Ensure(context.Background(), "fake", nil)
	if err == nil {
		t.Fatal("an archive missing the interpreter must be refused")
	}
	if !strings.Contains(err.Error(), "fake/bin/run") {
		t.Errorf("the refusal should name what was missing: %v", err)
	}
}
