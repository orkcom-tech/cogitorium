package plugin

import (
	"context"
	"fmt"
	"github.com/orkcom-tech/cogitorium/internal/channel"
	"github.com/orkcom-tech/cogitorium/internal/runtimes"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Baking plugins into a derived image, and seeding them out of it.
//
// Three mechanisms sit next to each other here and operators conflate them,
// which is how plugin sets go missing. They are named separately on purpose:
//
//	bake  — materialise into an image layer at BUILD time
//	seed  — copy that layer into the volume at every START
//	the ordinary install — an operator adding one to a running server
//
// The property the first two exist to give is that a plugin set is a property
// of the IMAGE rather than of the volume. Wipe the volume, land on a fresh
// node, or run with no volume at all, and the same plugins come up.

// RefDir is where a derived image keeps its baked plugins. Compiled in rather
// than configurable: a path an operator can change is a path the entrypoint
// and the Dockerfile can disagree about.
const RefDir = "/usr/share/cogitorium/ref"

// refModeDir and refModeFile are what a baked tree is created with.
//
// Mode-readable, never owner-readable, and this is the detail that decides
// whether the container channel works at all. There is no single runtime user:
// the image's own adduser gives uid 1000, and the Helm pod overrides to 65532,
// which has no passwd entry in the image. An ownership-based ref tree is
// readable under compose and INVISIBLE in the cluster — silently emptying the
// plugin set on exactly the channel where the seed is the whole guarantee. The
// PVC only works because fsGroup chowns it, and an image layer gets no fsGroup
// treatment.
const (
	refModeDir  = 0o755
	refModeFile = 0o644
)

// Bake materialises bundles into a ref tree for a derived image.
//
// It runs against the ref directory rather than the data directory, so nothing
// it writes depends on a volume that does not exist at build time.
func Bake(refDir string, bundles []string) ([]Installed, error) {
	if refDir == "" {
		refDir = RefDir
	}
	s, err := Open(refDir)
	if err != nil {
		return nil, err
	}

	var out []Installed
	for _, b := range bundles {
		in, digest, err := s.Install(b)
		if err != nil {
			return nil, fmt.Errorf("baking %s: %w", filepath.Base(b), err)
		}
		// A baked plugin is approved by the person who built the image. That
		// is the decision the Dockerfile IS — somebody chose these bundles and
		// committed them to a layer — and asking the operator who later runs
		// the image to approve them again would be asking them to ratify a
		// choice they cannot change without rebuilding.
		if _, err := s.Approve(in.ID, "image build"); err != nil {
			return nil, fmt.Errorf("baking %s: %w", in.ID, err)
		}
		if err := s.Enable(in.ID); err != nil {
			return nil, fmt.Errorf("baking %s: %w", in.ID, err)
		}
		if err := s.MarkFromImage(in.ID, in.Version); err != nil {
			return nil, err
		}
		_ = digest
		out = append(out, in)
	}

	// The runtimes, too, or baking is only half done.
	//
	// A baked plugin set is a property of the IMAGE: wipe the volume, land on
	// a fresh node, run with no volume at all, and the same plugins come up.
	// A provisioned plugin whose interpreter was not baked comes up and then
	// reaches the network for a hundred megabytes — on every fresh node, and
	// on the air-gapped installs this feature exists for, not at all.
	if err := bakeRuntimes(refDir, out); err != nil {
		return nil, err
	}

	if err := chmodTree(filepath.Join(refDir, pluginsDir)); err != nil {
		return nil, err
	}
	if err := chmodTree(filepath.Join(refDir, "runtimes")); err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	if err := os.Chmod(filepath.Join(refDir, orderFile), refModeFile); err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	return out, nil
}

// bakeRuntimes fetches the interpreter each provisioned plugin needs.
//
// Into the ref tree, where it is used in place and read-only at runtime rather
// than copied into the volume — a hundred megabytes per interpreter copied on
// every start would double storage for nothing.
//
// A tier this cannot materialise is named rather than passed over. An image
// plugin needs its image on whatever will run it and a native plugin already
// carries its binary, so both are fine — but an operator who baked one should
// be told which of the two they got, because only one of them will still work
// with no registry reachable.
func bakeRuntimes(refDir string, baked []Installed) error {
	profile := channel.Detect(refDir)
	caps := Capabilities{Profile: profile, ContainerRunner: true}
	store := runtimes.NewStore(refDir, "", profile, runtimes.HTTPFetcher{}, true)

	for _, in := range baked {
		res := Resolve(in.Manifest, caps)
		switch res.Tier {
		case TierProvisioned:
			r, err := store.Ensure(context.Background(), res.Technology, func(string) bool { return true })
			if err != nil {
				return fmt.Errorf("baking %s: its %s runtime could not be fetched, so the image "+
					"would come up and reach the network for it: %w", in.ID, res.Technology, err)
			}
			slog.Info("baked a plugin runtime into the image",
				"plugin", in.ID, "technology", res.Technology, "version", r.Row.Version)
		case TierImage:
			// Nothing to put in the layer: the image is pulled by whatever
			// runs the container, not by this build.
			slog.Warn("a baked plugin runs from a container image, which this build cannot carry",
				"plugin", in.ID, "image", res.Technology,
				"note", "whatever runs this must be able to pull it")
		case TierNative:
			slog.Info("a baked plugin carries its own binary", "plugin", in.ID)
		}
	}
	return nil
}

// chmodTree makes everything readable by mode rather than by owner.
func chmodTree(root string) error {
	return filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		mode := os.FileMode(refModeFile)
		if d.IsDir() {
			mode = refModeDir
		}
		return os.Chmod(p, mode)
	})
}

// Seed copies a baked plugin set into the data directory.
//
// Run on EVERY start, not the first: that is what makes the plugin set a
// property of the image. A first-start-only seed comes up empty the moment
// somebody recreates the container against a volume that already exists.
//
// Only plugins are copied. Runtimes are used where they are, read-only — a
// musl CPython is well over a hundred megabytes across tens of thousands of
// files, and copying that into the volume on every start would double the
// storage and pay a per-start walk for nothing.
func Seed(refDir, dataDir string) ([]string, error) {
	if refDir == "" {
		refDir = RefDir
	}
	src := filepath.Join(refDir, pluginsDir)
	if _, err := os.Stat(src); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	dst, err := Open(dataDir)
	if err != nil {
		return nil, err
	}
	ref, err := Open(refDir)
	if err != nil {
		return nil, err
	}
	baked, err := ref.List()
	if err != nil {
		return nil, err
	}

	var seeded []string
	for _, in := range baked {
		if in.Broken != nil {
			continue
		}
		// An operator who upgraded through the interface is not clobbered by
		// the image's copy on the next start. The marker records which version
		// the image supplied; a different version on disk is somebody's own
		// decision and it wins.
		if _, err := dst.read(in.ID); err == nil {
			// Something is already there. Whether it came from an earlier seed
			// or from an operator's own install, it is not this seed's to
			// replace: an upgrade through the interface must survive the next
			// container start, and a seed that overwrote it would undo their
			// work every time the pod moved.
			continue
		}
		if err := copyTree(filepath.Join(src, in.ID), filepath.Join(dataDir, pluginsDir, in.ID)); err != nil {
			return seeded, fmt.Errorf("seeding %s: %w", in.ID, err)
		}
		seeded = append(seeded, in.ID)
	}

	if len(seeded) > 0 {
		if err := mergeOrder(ref, dst, seeded); err != nil {
			return seeded, err
		}
	}
	sort.Strings(seeded)
	return seeded, nil
}

// mergeOrder appends the image's plugins to the operator's list without
// disturbing what they arranged. The image supplies plugins; the operator owns
// precedence, and a seed that rewrote their order would silently change which
// plugin renders.
func mergeOrder(ref, dst *Store, seeded []string) error {
	have, err := dst.Order()
	if err != nil {
		return err
	}
	present := map[string]bool{}
	for _, id := range have {
		present[id] = true
	}
	baked, err := ref.Order()
	if err != nil {
		return err
	}
	for _, id := range baked {
		for _, s := range seeded {
			if id == s && !present[id] {
				have = append(have, id)
				present[id] = true
			}
		}
	}
	return dst.SetOrder(have)
}

func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if !d.Type().IsRegular() {
			return nil
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		in, err := os.Open(p)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
		if err != nil {
			return err
		}
		defer out.Close()
		_, err = io.Copy(out, in)
		return err
	})
}

// CheckRef reports whether a baked tree is actually readable as the running
// user.
//
// A missing plugin set becomes a line an operator can read rather than an
// interface that quietly has nothing extra in it. This is the check that
// catches an ownership-based ref tree on the cluster channel — where it is
// present, correct, and invisible.
func CheckRef(refDir string) error {
	if refDir == "" {
		refDir = RefDir
	}
	src := filepath.Join(refDir, pluginsDir)
	entries, err := os.ReadDir(src)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("the baked plugin tree at %s is not readable as uid %d: %w",
			src, os.Getuid(), err)
	}
	var unreadable []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if _, err := os.ReadFile(filepath.Join(src, e.Name(), currentTxt)); err != nil {
			unreadable = append(unreadable, e.Name())
		}
	}
	if len(unreadable) > 0 {
		sort.Strings(unreadable)
		return fmt.Errorf("the baked plugins %s are present at %s but not readable as uid %d — "+
			"a ref tree has to be readable by MODE, because there is no single runtime user: "+
			"the image's own is 1000 and the Helm pod overrides it",
			strings.Join(unreadable, ", "), src, os.Getuid())
	}
	return nil
}
