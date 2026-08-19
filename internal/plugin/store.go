package plugin

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// The store is what is on disk, and it is deliberately not the same question
// as what is running.
//
// Presence never means enabled. A plugin can be installed and off, and the
// operator's ordered enable list is the only thing that turns it on — so
// unpacking an archive is never, by itself, a decision. That separation is
// also what makes install-then-approve possible: the bytes are on disk and
// inspectable before anything about them has taken effect.
//
// Position in the list is precedence, because with late binding by name order
// IS semantics. No manifest carries a priority field: a plugin cannot bid for
// its own precedence, which is the only thing that makes the list trustworthy.

// Layout on disk, under <data>/plugins/:
//
//	<id>/<version>/plugin.yaml      the bundle, one directory per version
//	<id>/<version>/templates/...
//	<id>/current                    which version is installed, as text
//	<id>/.from-image                present when a derived image supplied it
//
// `current` is a text file rather than a symlink because Windows ships, and a
// symlink there needs a privilege an ordinary install does not have.

const (
	pluginsDir = "plugins"
	orderFile  = "plugins.order"
	currentTxt = "current"
	fromImage  = ".from-image"
	manifestNm = "plugin.yaml"
	templates  = "templates"
)

// Installed is one plugin as it exists on disk.
type Installed struct {
	Manifest Manifest
	Version  string
	// Dir is the version directory holding the bundle.
	Dir string
	// FromImage records that a derived image supplied this version. An
	// operator who upgraded through the UI is not clobbered by the image's
	// copy on the next start, so the marker has to survive.
	FromImage bool
	// Enabled reports whether the operator's list names it.
	Enabled bool
	// Order is its position in that list, or -1 when it is not in it.
	Order int
	// Broken is why this directory could not be read as a plugin. When it is
	// set, everything above it except the id is unreliable.
	//
	// Reported rather than skipped. A directory somebody installed that
	// silently does not appear is the worst answer available: the operator
	// sees no plugin, no error, and no reason to look anywhere.
	Broken error
	// ID is the directory name, which is the only thing known about a broken
	// install and the only thing needed to remove it.
	ID string
}

// Store is the plugin directory and the enable list beside it.
type Store struct {
	root  string // <data>/plugins
	order string // <data>/plugins.order
}

// Open prepares the store. It creates the directory but never the enable list:
// an absent list means nothing is enabled, which is the correct state for a
// fresh install and is different from an empty one somebody wrote on purpose.
func Open(dataDir string) (*Store, error) {
	if dataDir == "" {
		return nil, errors.New("plugin: a data directory is required")
	}
	s := &Store{
		root:  filepath.Join(dataDir, pluginsDir),
		order: filepath.Join(dataDir, orderFile),
	}
	if err := os.MkdirAll(s.root, 0o755); err != nil {
		return nil, fmt.Errorf("plugin: preparing %s: %w", s.root, err)
	}
	return s, nil
}

// Root is the plugin directory, for a message that points somebody at it.
func (s *Store) Root() string { return s.root }

// List reports every installed plugin, ordered the way they will be layered:
// enabled ones first in enable order, then the disabled ones by id so the
// library screen has something stable to render.
func (s *Store) List() ([]Installed, error) {
	entries, err := os.ReadDir(s.root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("plugin: reading %s: %w", s.root, err)
	}
	order, err := s.Order()
	if err != nil {
		return nil, err
	}
	pos := map[string]int{}
	for i, id := range order {
		pos[id] = i
	}

	var out []Installed
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		in, err := s.read(e.Name())
		if err != nil {
			// Reported, never skipped. One corrupt install must not stop the
			// server from starting — but it must not vanish either, or the
			// operator sees no plugin, no error, and no reason to look.
			out = append(out, Installed{ID: e.Name(), Broken: err, Order: -1})
			continue
		}
		in.ID = in.Manifest.ID
		if i, ok := pos[in.Manifest.ID]; ok {
			in.Enabled, in.Order = true, i
		} else {
			in.Order = -1
		}
		out = append(out, in)
	}

	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		switch {
		case a.Enabled != b.Enabled:
			return a.Enabled
		case a.Enabled:
			return a.Order < b.Order
		default:
			return a.ID < b.ID
		}
	})
	return out, nil
}

// Enabled is List filtered to what the operator turned on, in layer order.
// This is what the view stack is built from.
func (s *Store) Enabled() ([]Installed, error) {
	all, err := s.List()
	if err != nil {
		return nil, err
	}
	var out []Installed
	for _, in := range all {
		if in.Enabled && in.Broken == nil {
			out = append(out, in)
		}
	}
	return out, nil
}

// Get reads one installed plugin.
func (s *Store) Get(id string) (Installed, error) { return s.read(id) }

func (s *Store) read(id string) (Installed, error) {
	dir := filepath.Join(s.root, id)
	b, err := os.ReadFile(filepath.Join(dir, currentTxt))
	if err != nil {
		return Installed{}, fmt.Errorf("plugin %q: no installed version recorded: %w", id, err)
	}
	version := strings.TrimSpace(string(b))
	if version == "" {
		return Installed{}, fmt.Errorf("plugin %q: the recorded version is empty", id)
	}
	vdir := filepath.Join(dir, version)

	mb, err := os.ReadFile(filepath.Join(vdir, manifestNm))
	if err != nil {
		return Installed{}, fmt.Errorf("plugin %q %s: %w", id, version, err)
	}
	m, err := Parse(mb)
	if err != nil {
		return Installed{}, fmt.Errorf("plugin %q %s: %w", id, version, err)
	}
	if ps := m.Validate(); len(ps) > 0 {
		return Installed{}, fmt.Errorf("plugin %q %s: %w", id, version, ps)
	}
	// The directory name is part of the identity. A bundle whose manifest says
	// something else was renamed underneath us, and trusting either one over
	// the other would be a guess.
	if m.ID != id {
		return Installed{}, fmt.Errorf("plugin directory %q holds a manifest for %q", id, m.ID)
	}
	if m.Version != version {
		return Installed{}, fmt.Errorf("plugin %q: directory says version %s, manifest says %s",
			id, version, m.Version)
	}

	in := Installed{Manifest: m, ID: m.ID, Version: version, Dir: vdir}
	if _, err := os.Stat(filepath.Join(dir, fromImage)); err == nil {
		in.FromImage = true
	}
	return in, nil
}

// Templates is the layer FS for an installed plugin — its templates directory,
// and nothing above it.
func (in Installed) Templates() (fs.FS, error) {
	dir := filepath.Join(in.Dir, templates)
	if _, err := os.Stat(dir); err != nil {
		if os.IsNotExist(err) {
			// A plugin with no templates is legitimate: it may contribute only
			// a tool or a background job. An empty layer is the right answer,
			// not an error.
			return emptyFS{}, nil
		}
		return nil, err
	}
	return os.DirFS(dir), nil
}

type emptyFS struct{}

func (emptyFS) Open(string) (fs.File, error) { return nil, fs.ErrNotExist }

// ── the enable list ───────────────────────────────────────────────────────

// Order reads the operator's ordered enable list.
//
// One id per line, blank lines and # comments ignored, because an operator
// editing this by hand on a server is a first-class way to use it and a JSON
// array would make that unpleasant.
func (s *Store) Order() ([]string, error) {
	b, err := os.ReadFile(s.order)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("plugin: reading %s: %w", s.order, err)
	}
	var out []string
	seen := map[string]bool{}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if seen[line] {
			// A duplicate would give one plugin two positions, and position is
			// precedence. The first wins and the second is dropped rather than
			// the file being rejected.
			continue
		}
		seen[line] = true
		out = append(out, line)
	}
	return out, nil
}

// SetOrder writes the enable list.
func (s *Store) SetOrder(ids []string) error {
	var b strings.Builder
	b.WriteString("# Plugins this install has enabled, in layer order.\n" +
		"# Position is precedence: a plugin later in this list renders instead of\n" +
		"# one earlier when they define the same template name. Presence in the\n" +
		"# plugins directory does not enable anything — only this file does.\n")
	for _, id := range ids {
		b.WriteString(id + "\n")
	}
	return writeFileAtomic(s.order, []byte(b.String()))
}

// Enable adds a plugin to the end of the list, so the thing just installed
// does what it said it would rather than losing to something already there.
func (s *Store) Enable(id string) error {
	order, err := s.Order()
	if err != nil {
		return err
	}
	for _, existing := range order {
		if existing == id {
			return nil
		}
	}
	if _, err := s.read(id); err != nil {
		return fmt.Errorf("plugin: cannot enable %q: %w", id, err)
	}
	return s.SetOrder(append(order, id))
}

// Disable removes a plugin from the list, leaving it on disk.
func (s *Store) Disable(id string) error {
	order, err := s.Order()
	if err != nil {
		return err
	}
	out := make([]string, 0, len(order))
	for _, existing := range order {
		if existing != id {
			out = append(out, existing)
		}
	}
	if len(out) == len(order) {
		return nil
	}
	return s.SetOrder(out)
}

// Reorder replaces the list wholesale, refusing an id that is not installed —
// a list naming something absent is a list whose precedence nobody can read.
func (s *Store) Reorder(ids []string) error {
	for _, id := range ids {
		if _, err := s.read(id); err != nil {
			return fmt.Errorf("plugin: cannot order %q: %w", id, err)
		}
	}
	return s.SetOrder(ids)
}

// Remove deletes an installed plugin and takes it out of the enable list.
func (s *Store) Remove(id string) error {
	if err := s.Disable(id); err != nil {
		return err
	}
	dir := filepath.Join(s.root, id)
	if !strings.HasPrefix(filepath.Clean(dir), filepath.Clean(s.root)+string(os.PathSeparator)) {
		return fmt.Errorf("plugin: refusing to remove %q, which is not inside %s", id, s.root)
	}
	return os.RemoveAll(dir)
}

// SetCurrent records which version is installed. Callers unpack a bundle into
// <id>/<version>/ and then call this — so a half-unpacked archive is never the
// current one.
func (s *Store) SetCurrent(id, version string) error {
	dir := filepath.Join(s.root, id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(dir, currentTxt), []byte(version+"\n"))
}

// MarkFromImage records that a derived image supplied this plugin, so the
// every-start seed can tell its own copy from one an operator upgraded.
func (s *Store) MarkFromImage(id, version string) error {
	return writeFileAtomic(filepath.Join(s.root, id, fromImage), []byte(version+"\n"))
}

// writeFileAtomic writes through a temporary file in the same directory, so a
// crash mid-write leaves the previous content rather than a truncated file.
// The enable list decides what loads at boot; a half-written one would be a
// server that comes back up missing plugins for no visible reason.
func writeFileAtomic(path string, b []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)

	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(name, 0o644); err != nil {
		return err
	}
	return os.Rename(name, path)
}
