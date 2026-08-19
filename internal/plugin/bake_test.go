package plugin

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The property bake and seed exist to give: a plugin set is a property of the
// IMAGE, not of the volume. Wipe the volume and the same plugins come up.
func TestABakedPluginSurvivesAWipedVolume(t *testing.T) {
	ref := t.TempDir()
	if _, err := Bake(ref, []string{bundle(t, "radar", "1.0.0", "a")}); err != nil {
		t.Fatalf("baking: %v", err)
	}

	data := t.TempDir()
	seeded, err := Seed(ref, data)
	if err != nil {
		t.Fatalf("seeding: %v", err)
	}
	if len(seeded) != 1 || seeded[0] != "radar" {
		t.Fatalf("seeded = %v", seeded)
	}

	s, _ := Open(data)
	enabled, err := s.Enabled()
	if err != nil {
		t.Fatal(err)
	}
	if len(enabled) != 1 || enabled[0].ID != "radar" {
		t.Fatalf("a seeded plugin should be live: %+v", enabled)
	}

	// Wipe it. The next start brings it back, which is the whole claim.
	if err := os.RemoveAll(data); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(data, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Seed(ref, data); err != nil {
		t.Fatal(err)
	}
	s2, _ := Open(data)
	again, _ := s2.Enabled()
	if len(again) != 1 {
		t.Errorf("a wiped volume must come back with the image's plugins: %+v", again)
	}
}

// Choosing the bundles IS the decision, made by whoever built the image.
// Asking the operator who later runs it to approve them again would be asking
// them to ratify a choice they cannot change without rebuilding.
func TestBakedPluginsAreApprovedByTheBuild(t *testing.T) {
	ref := t.TempDir()
	if _, err := Bake(ref, []string{bundle(t, "radar", "1.0.0", "a")}); err != nil {
		t.Fatal(err)
	}
	data := t.TempDir()
	if _, err := Seed(ref, data); err != nil {
		t.Fatal(err)
	}
	s, _ := Open(data)
	if why := s.Pending("radar"); why != "" {
		t.Errorf("a baked plugin should arrive approved: %s", why)
	}
	a, ok := s.Approved("radar")
	if !ok || a.By != "image build" {
		t.Errorf("the decision should say who made it: %+v", a)
	}
}

// The detail the cluster channel turns on. There is no single runtime user, so
// an ownership-based tree reads fine under compose and is invisible in the pod.
func TestTheBakedTreeIsReadableByMode(t *testing.T) {
	ref := t.TempDir()
	if _, err := Bake(ref, []string{bundle(t, "radar", "1.0.0", "a")}); err != nil {
		t.Fatal(err)
	}
	err := filepath.WalkDir(filepath.Join(ref, "plugins"), func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		perm := info.Mode().Perm()
		if d.IsDir() {
			if perm&0o005 != 0o005 {
				t.Errorf("%s is %o — a baked directory must be readable and traversable by anyone", p, perm)
			}
			return nil
		}
		if perm&0o004 == 0 {
			t.Errorf("%s is %o — a baked file must be readable by anyone", p, perm)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// An upgrade through the interface must survive the next container start.
func TestSeedingDoesNotClobberWhatIsAlreadyThere(t *testing.T) {
	ref := t.TempDir()
	if _, err := Bake(ref, []string{bundle(t, "radar", "1.0.0", "from image")}); err != nil {
		t.Fatal(err)
	}
	data := t.TempDir()
	if _, err := Seed(ref, data); err != nil {
		t.Fatal(err)
	}

	// The operator upgrades it themselves.
	s, _ := Open(data)
	if _, _, err := s.Install(bundle(t, "radar", "2.0.0", "operator's own")); err != nil {
		t.Fatal(err)
	}

	seeded, err := Seed(ref, data)
	if err != nil {
		t.Fatal(err)
	}
	if len(seeded) != 0 {
		t.Errorf("the seed replaced an operator's own upgrade: %v", seeded)
	}
	in, err := s.Get("radar")
	if err != nil {
		t.Fatal(err)
	}
	if in.Version != "2.0.0" {
		t.Errorf("version = %s; the operator's upgrade must survive a container restart", in.Version)
	}
}

// The image supplies plugins; the operator owns precedence. A seed that
// rewrote the order would silently change which plugin renders.
func TestSeedingAppendsWithoutDisturbingTheOperatorsOrder(t *testing.T) {
	ref := t.TempDir()
	if _, err := Bake(ref, []string{bundle(t, "radar", "1.0.0", "a")}); err != nil {
		t.Fatal(err)
	}
	data := t.TempDir()
	s, _ := Open(data)
	if _, _, err := s.Install(bundle(t, "mine", "1.0.0", "b")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Approve("mine", "admin"); err != nil {
		t.Fatal(err)
	}
	if err := s.Enable("mine"); err != nil {
		t.Fatal(err)
	}

	if _, err := Seed(ref, data); err != nil {
		t.Fatal(err)
	}
	order, _ := s.Order()
	if len(order) != 2 || order[0] != "mine" {
		t.Errorf("the operator's own plugin must keep its position: %v", order)
	}
}

// Running with no volume at all is a supported way to run this image.
func TestSeedingIntoAFreshDirectoryWorks(t *testing.T) {
	ref := t.TempDir()
	if _, err := Bake(ref, []string{bundle(t, "radar", "1.0.0", "a")}); err != nil {
		t.Fatal(err)
	}
	fresh := filepath.Join(t.TempDir(), "never-existed")
	if _, err := Seed(ref, fresh); err != nil {
		t.Fatalf("a data directory that does not exist yet is the no-volume case: %v", err)
	}
}

// An image with nothing baked in is the ordinary one, and must not be an error.
func TestSeedingWithNothingBakedIsQuiet(t *testing.T) {
	seeded, err := Seed(t.TempDir(), t.TempDir())
	if err != nil {
		t.Fatalf("an image with no baked plugins is the normal case: %v", err)
	}
	if len(seeded) != 0 {
		t.Errorf("seeded = %v", seeded)
	}
}

// A missing plugin set becomes a line an operator can read rather than an
// interface that quietly has nothing extra in it.
func TestCheckRefNamesAnUnreadableTree(t *testing.T) {
	if err := CheckRef(t.TempDir()); err != nil {
		t.Errorf("an absent tree is the ordinary case: %v", err)
	}

	ref := t.TempDir()
	if _, err := Bake(ref, []string{bundle(t, "radar", "1.0.0", "a")}); err != nil {
		t.Fatal(err)
	}
	if err := CheckRef(ref); err != nil {
		t.Errorf("a properly baked tree should check out: %v", err)
	}

	if os.Getuid() == 0 {
		t.Skip("root reads everything, so unreadability cannot be demonstrated")
	}
	if err := os.Chmod(filepath.Join(ref, "plugins", "radar", "current"), 0o000); err != nil {
		t.Fatal(err)
	}
	err := CheckRef(ref)
	if err == nil {
		t.Fatal("an unreadable baked plugin must be reported")
	}
	if !strings.Contains(err.Error(), "radar") || !strings.Contains(err.Error(), "MODE") {
		t.Errorf("the message should name the plugin and the cause: %v", err)
	}
}
