package plugin

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// A bundle is a zip with plugin.yaml at its root.
//
// Unpacking is the one place in this package where bytes somebody else wrote
// touch the filesystem, so every rule here is about a path or a size rather
// than about plugins. The rules are dull on purpose: an archive that tries
// anything interesting is refused with a sentence naming the entry, and
// nothing partial is ever left where the store would find it.

const (
	// maxBundleBytes bounds what one archive may expand to. A plugin is
	// templates and maybe a module; anything approaching this is either a
	// mistake or an attempt to fill somebody's disk.
	maxBundleBytes = 256 << 20
	// maxBundleFiles bounds the entry count, because a million empty files
	// costs inodes rather than bytes and the size limit would never notice.
	maxBundleFiles = 20000
)

// ErrNoManifest is returned when the archive has no plugin.yaml at its root.
var ErrNoManifest = errors.New("plugin: the bundle has no plugin.yaml at its root")

// Install unpacks a bundle into the store and records it as the current
// version. It does NOT enable it: presence is not a decision, and the operator
// approves what the manifest declares before the plugin ever renders.
//
// The digest of the archive is returned so a caller can check it against what
// the catalog said before doing anything else with it.
func (s *Store) Install(archive string) (Installed, string, error) {
	digest, err := fileDigest(archive)
	if err != nil {
		return Installed{}, "", err
	}

	m, err := readManifestFromZip(archive)
	if err != nil {
		return Installed{}, digest, err
	}
	if ps := m.Validate(); len(ps) > 0 {
		return Installed{}, digest, fmt.Errorf("plugin %q: %w", m.ID, ps)
	}

	// Unpack beside the destination and rename, so a failure halfway through
	// leaves the previous version installed rather than a half-written one
	// that reads as current.
	final := filepath.Join(s.root, m.ID, m.Version)
	staging := final + ".incoming"
	_ = os.RemoveAll(staging)
	if err := os.MkdirAll(filepath.Dir(final), 0o755); err != nil {
		return Installed{}, digest, err
	}
	if err := unzip(archive, staging); err != nil {
		_ = os.RemoveAll(staging)
		return Installed{}, digest, err
	}

	if err := os.RemoveAll(final); err != nil {
		_ = os.RemoveAll(staging)
		return Installed{}, digest, err
	}
	if err := os.Rename(staging, final); err != nil {
		_ = os.RemoveAll(staging)
		return Installed{}, digest, err
	}
	if err := s.SetCurrent(m.ID, m.Version); err != nil {
		return Installed{}, digest, err
	}
	// Recorded so approval has something to bind to. Without it a decision
	// could only ever name a version, and a version is a label an author
	// controls rather than the content an operator read.
	if err := s.setDigest(m.ID, digest); err != nil {
		return Installed{}, digest, err
	}
	// Content that is no longer what was approved must not stay enabled.
	//
	// Without this, replacing an approved plugin's bytes leaves it running on
	// a decision made about different code — which is the exact hole approval
	// exists to close, and it would close it only for plugins nobody had
	// approved yet.
	if why := s.Pending(m.ID); why != "" {
		if err := s.Disable(m.ID); err != nil {
			return Installed{}, digest, err
		}
	}

	in, err := s.Get(m.ID)
	return in, digest, err
}

// readManifestFromZip reads plugin.yaml without unpacking anything, so an
// archive that is not a plugin at all is refused before it touches the disk.
func readManifestFromZip(archive string) (Manifest, error) {
	r, err := zip.OpenReader(archive)
	if err != nil {
		return Manifest{}, fmt.Errorf("plugin: %s is not a readable bundle: %w", filepath.Base(archive), err)
	}
	defer r.Close()

	for _, f := range r.File {
		if path.Clean(f.Name) != manifestNm {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return Manifest{}, err
		}
		defer rc.Close()
		b, err := io.ReadAll(io.LimitReader(rc, 1<<20))
		if err != nil {
			return Manifest{}, err
		}
		return Parse(b)
	}
	return Manifest{}, ErrNoManifest
}

// unzip expands an archive into dst, refusing anything that tries to leave it.
func unzip(archive, dst string) error {
	r, err := zip.OpenReader(archive)
	if err != nil {
		return fmt.Errorf("plugin: %s is not a readable bundle: %w", filepath.Base(archive), err)
	}
	defer r.Close()

	if len(r.File) > maxBundleFiles {
		return fmt.Errorf("plugin: the bundle holds %d entries, more than the %d allowed",
			len(r.File), maxBundleFiles)
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}

	var written int64
	for _, f := range r.File {
		target, err := safeJoin(dst, f.Name)
		if err != nil {
			return err
		}

		switch {
		case f.FileInfo().IsDir():
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			continue
		case !f.Mode().IsRegular():
			// A symlink is the other way out of the directory, and a device
			// node has no business in a template bundle. Named rather than
			// skipped, so an author who packaged one learns why it vanished.
			return fmt.Errorf("plugin: the bundle entry %q is not a regular file; "+
				"bundles hold files and directories only", f.Name)
		}

		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		n, err := copyEntry(f, target, maxBundleBytes-written)
		if err != nil {
			return err
		}
		written += n
	}
	return nil
}

func copyEntry(f *zip.File, target string, budget int64) (int64, error) {
	if budget <= 0 {
		return 0, fmt.Errorf("plugin: the bundle expands past the %d byte limit", int64(maxBundleBytes))
	}
	rc, err := f.Open()
	if err != nil {
		return 0, err
	}
	defer rc.Close()

	// Permissions come from us, not from the archive. A bundle that shipped a
	// setuid bit or a world-writable file would otherwise have it honoured,
	// and nothing in a plugin needs either.
	out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return 0, err
	}
	defer out.Close()

	// Copy through a bounded reader rather than trusting the declared size:
	// the header is written by whoever made the archive.
	n, err := io.Copy(out, io.LimitReader(rc, budget+1))
	if err != nil {
		return n, err
	}
	if n > budget {
		return n, fmt.Errorf("plugin: the bundle expands past the %d byte limit", int64(maxBundleBytes))
	}
	return n, nil
}

// safeJoin refuses any entry that would land outside the destination.
//
// The classic archive escape is "../../etc/something", and the less classic
// one is an absolute path or a Windows drive letter. Both are answered the
// same way: build the path, then prove it is still inside.
func safeJoin(dst, name string) (string, error) {
	if name == "" {
		return "", errors.New("plugin: the bundle holds an entry with no name")
	}
	// Zip names are always slash-separated by spec, so a backslash here is
	// either a Windows-made archive or somebody hoping the check only looks
	// for slashes.
	slashed := strings.ReplaceAll(name, `\`, "/")

	// Refused before cleaning, not after. path.Clean would quietly turn
	// "../secret.html" into "secret.html" — safe, but the author would never
	// learn that the file they packaged is not where they put it, and an
	// archive containing a traversal at all is worth saying no to out loud.
	if strings.HasPrefix(slashed, "/") {
		return "", fmt.Errorf("plugin: the bundle entry %q is an absolute path; "+
			"bundle entries are relative to the bundle root", name)
	}
	if len(slashed) > 1 && slashed[1] == ':' {
		return "", fmt.Errorf("plugin: the bundle entry %q names a drive; "+
			"bundle entries are relative to the bundle root", name)
	}
	for _, seg := range strings.Split(slashed, "/") {
		if seg == ".." {
			return "", fmt.Errorf("plugin: the bundle entry %q climbs out of the bundle; "+
				"bundle entries are relative to the bundle root", name)
		}
	}

	clean := path.Clean("/" + slashed)
	target := filepath.Join(dst, filepath.FromSlash(strings.TrimPrefix(clean, "/")))

	rel, err := filepath.Rel(dst, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("plugin: the bundle entry %q would be written outside the plugin "+
			"directory, so the bundle is refused", name)
	}
	return target, nil
}

func fileDigest(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}
