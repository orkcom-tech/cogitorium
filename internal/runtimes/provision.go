package runtimes

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/orkcom-tech/cogitorium/internal/channel"
)

// Materialising a runtime, and where one is looked for.
//
// The order is: the data directory, then the image's read-only seed, then the
// network. The seed is used IN PLACE and never copied — a musl CPython is well
// over a hundred megabytes across tens of thousands of files, and copying it
// into the volume on every container start would double the storage and pay a
// per-start walk for nothing. Runtimes are immutable and content-addressed by
// version, so using them where they already are is safe by construction.

const (
	runtimesDir = "runtimes"
	// readyMark is written last. A directory without it is a materialisation
	// that did not finish, and it is discarded rather than trusted — an
	// interpreter missing half its standard library fails in ways that look
	// like the plugin's fault.
	readyMark = ".ready"
	// maxRuntime bounds what one archive may expand to.
	maxRuntime = 1 << 30
)

// Fetcher gets the bytes. An interface so a test can serve a small archive
// locally rather than downloading a hundred megabytes to prove a path check.
type Fetcher interface {
	Fetch(ctx context.Context, url string) (io.ReadCloser, error)
}

// HTTPFetcher is the real one.
type HTTPFetcher struct{ Client *http.Client }

func (f HTTPFetcher) Fetch(ctx context.Context, url string) (io.ReadCloser, error) {
	c := f.Client
	if c == nil {
		c = &http.Client{Timeout: 30 * time.Minute}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("%s answered %s", url, resp.Status)
	}
	return resp.Body, nil
}

// Store materialises runtimes under a data directory, optionally reading an
// image's seed first.
type Store struct {
	dataDir string
	// refDir is the read-only tree a derived image baked in. Empty when there
	// is none.
	refDir  string
	fetcher Fetcher
	profile channel.Profile
	// allowFetch is the operator's egress consent. False means an install
	// works from what is already on disk and refuses to reach out — which is
	// the correct behaviour for an air-gapped deployment and a surprise
	// nowhere else, because the refusal names what it wanted.
	allowFetch bool
}

// NewStore prepares the store.
func NewStore(dataDir, refDir string, p channel.Profile, f Fetcher, allowFetch bool) *Store {
	if f == nil {
		f = HTTPFetcher{}
	}
	return &Store{dataDir: dataDir, refDir: refDir, fetcher: f, profile: p, allowFetch: allowFetch}
}

// Resolved is a runtime ready to run.
type Resolved struct {
	Row Row
	// Dir is the version directory it lives in.
	Dir string
	// Exe is the interpreter, absolute.
	Exe string
	// FromSeed reports that this came from the image's read-only tree and is
	// being used in place.
	FromSeed bool
}

// Ensure locates a runtime, fetching it only if it must.
//
// One runtime per version, shared by every plugin that needs it: two plugins
// asking for the same Python get the same interpreter rather than two copies
// of a hundred megabytes.
func (s *Store) Ensure(ctx context.Context, tech string, ok Satisfies) (Resolved, error) {
	row, err := Select(tech, ok, s.profile)
	if err != nil {
		return Resolved{}, err
	}

	// The probe first. Fetching a hundred megabytes and then discovering the
	// volume will not execute it is the wrong order to find that out in.
	if !s.profile.CanExecFromData {
		return Resolved{}, fmt.Errorf("%s %s cannot run here: %s",
			tech, row.Version, s.profile.ExecRefusal)
	}

	if dir := s.readyDir(s.dataDir, row); dir != "" {
		return s.resolved(row, dir, false), nil
	}
	if s.refDir != "" {
		if dir := s.readyDir(s.refDir, row); dir != "" {
			// Used where it is. Immutable and content-addressed, so there is
			// nothing to gain by copying it and a great deal of disk to lose.
			return s.resolved(row, dir, true), nil
		}
	}

	if !s.allowFetch {
		return Resolved{}, fmt.Errorf("%s %s is not on this machine and this install is not "+
			"permitted to fetch it. It would have come from %s", tech, row.Version, row.URL)
	}

	dir, err := s.materialise(ctx, row)
	if err != nil {
		return Resolved{}, err
	}
	return s.resolved(row, dir, false), nil
}

func (s *Store) resolved(row Row, dir string, fromSeed bool) Resolved {
	return Resolved{Row: row, Dir: dir, Exe: filepath.Join(dir, filepath.FromSlash(row.Exe)), FromSeed: fromSeed}
}

// readyDir returns the version directory under root if it finished
// materialising, and "" otherwise.
func (s *Store) readyDir(root string, row Row) string {
	dir := filepath.Join(root, runtimesDir, row.Technology, row.Version)
	if _, err := os.Stat(filepath.Join(dir, readyMark)); err != nil {
		return ""
	}
	if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(row.Exe))); err != nil {
		// The marker is there and the interpreter is not, which means somebody
		// deleted part of it. Treated as absent so it is fetched again rather
		// than handed out and failing at exec.
		return ""
	}
	return dir
}

// materialise downloads, verifies and unpacks.
//
// Verification happens against the whole stream before a single byte is
// unpacked. Checking afterwards would mean an archive that failed had already
// written itself across the disk, and checking as it goes would mean deciding
// what to do with the half that was already there.
func (s *Store) materialise(ctx context.Context, row Row) (string, error) {
	slog.Info("fetching a plugin runtime",
		"technology", row.Technology, "version", row.Version, "target", row.Target())

	tmp, err := os.CreateTemp("", "cogitorium-runtime-*.tar.gz")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())

	body, err := s.fetcher.Fetch(ctx, row.URL)
	if err != nil {
		tmp.Close()
		return "", fmt.Errorf("fetching %s %s: %w", row.Technology, row.Version, err)
	}
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(tmp, h), io.LimitReader(body, maxRuntime+1)); err != nil {
		body.Close()
		tmp.Close()
		return "", fmt.Errorf("fetching %s %s: %w", row.Technology, row.Version, err)
	}
	body.Close()
	tmp.Close()

	if got := hex.EncodeToString(h.Sum(nil)); got != row.SHA256 {
		return "", fmt.Errorf("the %s %s archive does not match the digest this build pins: "+
			"expected %s, got %s. Nothing was unpacked",
			row.Technology, row.Version, row.SHA256, got)
	}

	final := filepath.Join(s.dataDir, runtimesDir, row.Technology, row.Version)
	staging := final + ".incoming"
	_ = os.RemoveAll(staging)
	if err := os.MkdirAll(filepath.Dir(final), 0o755); err != nil {
		return "", err
	}

	if err := untar(tmp.Name(), staging); err != nil {
		_ = os.RemoveAll(staging)
		return "", fmt.Errorf("unpacking %s %s: %w", row.Technology, row.Version, err)
	}
	if _, err := os.Stat(filepath.Join(staging, filepath.FromSlash(row.Exe))); err != nil {
		_ = os.RemoveAll(staging)
		return "", fmt.Errorf("the %s %s archive does not contain %s where this build expects it",
			row.Technology, row.Version, row.Exe)
	}
	// Written last, so a directory without it is one that did not finish.
	if err := os.WriteFile(filepath.Join(staging, readyMark), []byte(row.SHA256+"\n"), 0o644); err != nil {
		_ = os.RemoveAll(staging)
		return "", err
	}

	_ = os.RemoveAll(final)
	if err := os.Rename(staging, final); err != nil {
		_ = os.RemoveAll(staging)
		return "", err
	}
	return final, nil
}

// untar expands a gzipped tar, refusing anything that would leave the tree.
//
// The same rules as a plugin bundle, and for the same reason: these bytes came
// off the network, and a checksum proves they are the bytes somebody pinned,
// not that those bytes are polite.
func untar(archive, dst string) error {
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()

	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()

	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	tr := tar.NewReader(gz)
	var written int64

	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		target, err := safeJoin(dst, hdr.Name)
		if err != nil {
			return err
		}

		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			// The executable bit is honoured and nothing else is. An
			// interpreter has to be runnable; setuid and world-writable are
			// not things a runtime archive has any business setting.
			mode := os.FileMode(0o644)
			if hdr.Mode&0o111 != 0 {
				mode = 0o755
			}
			n, err := writeEntry(tr, target, mode, maxRuntime-written)
			if err != nil {
				return err
			}
			written += n
		case tar.TypeSymlink:
			// Upstream trees use symlinks for python3 -> python3.13. Allowed,
			// but only pointing back inside the tree.
			if _, err := safeJoin(dst, path.Join(path.Dir(hdr.Name), hdr.Linkname)); err != nil {
				return fmt.Errorf("the archive links out of its own tree: %s -> %s",
					hdr.Name, hdr.Linkname)
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			_ = os.Remove(target)
			if err := os.Symlink(hdr.Linkname, target); err != nil {
				return err
			}
		default:
			// A device node or a fifo in an interpreter archive is not
			// something to quietly skip.
			return fmt.Errorf("the archive holds %s, which is not a file, a directory or a link",
				hdr.Name)
		}
	}
}

func writeEntry(r io.Reader, target string, mode os.FileMode, budget int64) (int64, error) {
	if budget <= 0 {
		return 0, fmt.Errorf("the archive expands past the %d byte limit", int64(maxRuntime))
	}
	out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return 0, err
	}
	defer out.Close()

	n, err := io.Copy(out, io.LimitReader(r, budget+1))
	if err != nil {
		return n, err
	}
	if n > budget {
		return n, fmt.Errorf("the archive expands past the %d byte limit", int64(maxRuntime))
	}
	return n, nil
}

func safeJoin(dst, name string) (string, error) {
	slashed := strings.ReplaceAll(name, `\`, "/")
	if strings.HasPrefix(slashed, "/") {
		return "", fmt.Errorf("the archive holds an absolute path: %s", name)
	}
	for _, seg := range strings.Split(slashed, "/") {
		if seg == ".." {
			return "", fmt.Errorf("the archive climbs out of its own tree: %s", name)
		}
	}
	target := filepath.Join(dst, filepath.FromSlash(path.Clean("/" + slashed)[1:]))
	rel, err := filepath.Rel(dst, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("the archive would write outside its own tree: %s", name)
	}
	return target, nil
}
