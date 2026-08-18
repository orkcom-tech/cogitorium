package plugin

import (
	"strings"
	"testing"

	"github.com/orkcom-tech/cogitorium/internal/channel"
)

func caps(kind channel.Kind, libc channel.Libc, canExec bool) Capabilities {
	p := channel.Profile{
		Kind: kind, OS: "linux", Arch: "amd64", Libc: libc,
		DataDir: "/var/lib/cogitorium", CanExecFromData: canExec,
	}
	if !canExec {
		p.ExecRefusal = "the data volume at /var/lib/cogitorium is mounted noexec"
	}
	return Capabilities{Profile: p}
}

func withNeeds(needs string) Manifest {
	return Manifest{ID: "radar", Needs: needs}
}

// The hard floor. A plugin with no backend runs everywhere, unconditionally,
// because a template is data and the renderer is already installed.
func TestBundleRunsEverywhere(t *testing.T) {
	for _, k := range []channel.Kind{channel.Kubernetes, channel.Docker, channel.Local, channel.Desktop} {
		r := Resolve(withNeeds(""), caps(k, channel.Musl, false))
		if !r.Available || r.Tier != TierBundle {
			t.Errorf("%s: bundle must always be available, got %+v", k, r)
		}
	}
}

// The universal runtime, and the reason there is one: the engine is inside the
// binary, so nothing is fetched and nothing can be missing.
func TestWasmRunsEverywhereEvenWhereNothingMayExecute(t *testing.T) {
	for _, tech := range []string{"js", "rust", "tinygo", "go", "zig", "c", "wasm"} {
		r := Resolve(withNeeds(tech), caps(channel.Kubernetes, channel.Musl, false))
		if !r.Available {
			t.Errorf("%s must run even on a noexec volume: %s", tech, r.Refusal)
		}
		if r.Tier != TierWasm {
			t.Errorf("%s should land on wasm, got %s", tech, r.Tier)
		}
		if !r.Tier.Universal() {
			t.Errorf("%s should be a universal tier", tech)
		}
	}
}

// JavaScript is a universal answer rather than a provisioning problem — that
// is the whole reason the engine is embedded.
func TestJavaScriptNeedsNothingFetched(t *testing.T) {
	r := Resolve(withNeeds("js"), caps(channel.Docker, channel.Musl, false))
	if !r.Available || r.Tier != TierWasm {
		t.Fatalf("js must be universal, got %+v", r)
	}
}

func TestPythonIsProvisionedAndNeedsExecution(t *testing.T) {
	ok := Resolve(withNeeds("python@>=3.11"), caps(channel.Local, channel.Glibc, true))
	if !ok.Available || ok.Tier != TierProvisioned {
		t.Fatalf("python should be provisioned and available here, got %+v", ok)
	}

	no := Resolve(withNeeds("python@>=3.11"), caps(channel.Kubernetes, channel.Musl, false))
	if no.Available {
		t.Fatal("a noexec data volume must refuse a fetched interpreter")
	}
	if !strings.Contains(no.Refusal, "noexec") || !strings.Contains(no.Refusal, "radar") {
		t.Errorf("the refusal must name the plugin and the cause: %s", no.Refusal)
	}
}

// An author hearing "no" wants to know what to write instead.
func TestDenoOnMuslIsRefusedWithAnAlternative(t *testing.T) {
	r := Resolve(withNeeds("deno"), caps(channel.Docker, channel.Musl, true))
	if r.Available {
		t.Fatal("Deno publishes no musl build, so it cannot be available on musl")
	}
	if !strings.Contains(r.Refusal, "Bun") {
		t.Errorf("the refusal should name the alternative: %s", r.Refusal)
	}
	// On glibc it is fine.
	if g := Resolve(withNeeds("deno"), caps(channel.Local, channel.Glibc, true)); !g.Available {
		t.Errorf("Deno on glibc should work: %s", g.Refusal)
	}
}

// Image availability is bound to the live backend, never to the channel's
// name: the shipped compose image is a container itself and still cannot start
// one.
func TestImageAvailabilityFollowsTheBackendNotTheChannel(t *testing.T) {
	c := caps(channel.Docker, channel.Musl, true)
	r := Resolve(withNeeds("ghcr.io/acme/tool:1.2"), c)
	if r.Available {
		t.Fatal("no container backend means no image tier, even inside a container")
	}
	if r.Tier != TierImage {
		t.Errorf("tier = %s, want image", r.Tier)
	}

	c.ContainerRunner = true
	if r := Resolve(withNeeds("ghcr.io/acme/tool:1.2"), c); !r.Available {
		t.Errorf("with a container backend it must run: %s", r.Refusal)
	}
}

func TestImageIsRecognisedByShapeNotKeyword(t *testing.T) {
	if !looksLikeImage("ghcr.io/acme/tool:1.2") {
		t.Error("a registry path is an image")
	}
	if !looksLikeImage("acme/tool@sha256:abc") {
		t.Error("a digest-pinned reference is an image")
	}
	if looksLikeImage("python@>=3.11") {
		t.Error("a version constraint is not a digest")
	}
	if looksLikeImage("js") {
		t.Error("a bare technology is not an image")
	}
}

// The only platform-keyed structure in the system, and a missing row is a
// refusal naming the target rather than a mystery.
func TestNativeNeedsAPublishedRow(t *testing.T) {
	c := caps(channel.Local, channel.Glibc, true)
	c.NativeRows = []string{"darwin/arm64/", "linux/arm64/glibc"}

	r := Resolve(withNeeds("native"), c)
	if r.Available {
		t.Fatal("linux/amd64/glibc was not published, so it must be refused")
	}
	if !strings.Contains(r.Refusal, "linux/amd64/glibc") {
		t.Errorf("the refusal must name the target this install is: %s", r.Refusal)
	}

	c.NativeRows = append(c.NativeRows, "linux/amd64/glibc")
	if r := Resolve(withNeeds("native"), c); !r.Available {
		t.Errorf("the published row should match: %s", r.Refusal)
	}
}

// libc "any" may be claimed only for a proved-static binary, and when it is,
// it matches whatever libc this install runs.
func TestNativeAnyLibcMatches(t *testing.T) {
	c := caps(channel.Docker, channel.Musl, true)
	c.NativeRows = []string{"linux/amd64/any"}
	if r := Resolve(withNeeds("native"), c); !r.Available {
		t.Errorf("a static binary should run on musl: %s", r.Refusal)
	}
}

// A technology that gets renamed keeps working, or every published plugin
// naming it becomes uninstallable on the next release.
func TestARenamedTechnologyStillResolves(t *testing.T) {
	r := Resolve(withNeeds("javascript"), caps(channel.Local, channel.Glibc, true))
	if !r.Available {
		t.Fatalf("the older spelling must keep working: %s", r.Refusal)
	}
	if r.Technology != "js" || r.Superseded != "js" {
		t.Errorf("it should resolve to the current name and say so: %+v", r)
	}
}

// A refusal that lists what exists is a better answer than a fetch that fails
// later against a name nobody owns.
func TestUnknownTechnologyListsTheVocabulary(t *testing.T) {
	r := Resolve(withNeeds("cobol"), caps(channel.Local, channel.Glibc, true))
	if r.Available {
		t.Fatal("an unknown technology must be refused")
	}
	for _, want := range []string{"python", "js", "rust"} {
		if !strings.Contains(r.Refusal, want) {
			t.Errorf("the refusal should list %q: %s", want, r.Refusal)
		}
	}
}

func TestUnreadableNeedsIsRefusedNotGuessed(t *testing.T) {
	r := Resolve(withNeeds("python@wat"), caps(channel.Local, channel.Glibc, true))
	if r.Available || r.Refusal == "" {
		t.Fatalf("an unreadable needs must be refused with a reason, got %+v", r)
	}
}

// Nothing is fetched before the answer, which is the point of resolving
// against the profile first.
func TestOnlyBundleAndWasmClaimToBeUniversal(t *testing.T) {
	universal := map[Tier]bool{TierBundle: true, TierWasm: true}
	for _, tier := range []Tier{TierBundle, TierWasm, TierProvisioned, TierImage, TierNative} {
		if tier.Universal() != universal[tier] {
			t.Errorf("%s.Universal() = %v", tier, tier.Universal())
		}
	}
}
