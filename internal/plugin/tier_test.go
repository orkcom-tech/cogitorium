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
	m := withNeeds("native")
	m.Native = []Native{
		{OS: "darwin", Arch: "arm64", Path: "bin/mac"},
		{OS: "linux", Arch: "arm64", Libc: "glibc", Path: "bin/arm"},
	}

	r := Resolve(m, c)
	if r.Available {
		t.Fatal("linux/amd64/glibc was not published, so it must be refused")
	}
	if !strings.Contains(r.Refusal, "linux/amd64/glibc") {
		t.Errorf("the refusal must name the target this install is: %s", r.Refusal)
	}

	m.Native = append(m.Native, Native{OS: "linux", Arch: "amd64", Libc: "glibc", Path: "bin/x"})
	got := Resolve(m, c)
	if !got.Available {
		t.Fatalf("the published row should match: %s", got.Refusal)
	}
	if got.Native.Path != "bin/x" {
		t.Errorf("the matched row must come back so the caller knows what to run: %+v", got.Native)
	}
}

// libc "any" may be claimed only for a proved-static binary, and when it is,
// it matches whatever libc this install runs.
func TestNativeAnyLibcMatches(t *testing.T) {
	c := caps(channel.Docker, channel.Musl, true)
	m := withNeeds("native")
	m.Native = []Native{{OS: "linux", Arch: "amd64", Libc: "any", Path: "bin/static"}}
	if r := Resolve(m, c); !r.Available {
		t.Errorf("a static binary should run on musl: %s", r.Refusal)
	}
}

// A row for a target nobody can run is a row published for nothing, and
// learning that at install time on somebody else's machine is too late.
func TestNativeRowsAreValidated(t *testing.T) {
	m := Manifest{
		Schema: SchemaVersion, ID: "radar", Name: "R", Version: "1.0.0",
		Host: Host{Contract: Contract}, Needs: "native",
		Native: []Native{
			{OS: "plan9", Arch: "amd64", Path: "bin/x"},
			{OS: "linux", Arch: "sparc", Path: "bin/y"},
			{OS: "linux", Arch: "amd64", Libc: "uclibc", Path: "bin/z"},
			{OS: "darwin", Arch: "arm64", Libc: "musl", Path: "bin/w"},
			{OS: "linux", Arch: "arm64", Libc: "glibc", Path: "../escape"},
		},
	}
	ps := m.Validate()
	for _, f := range []string{"native[0].os", "native[1].arch", "native[2].libc",
		"native[3].libc", "native[4].path"} {
		if !hasField(ps, f) {
			t.Errorf("no problem reported for %s; got %v", f, ps)
		}
	}
}

func TestDuplicateNativeTargetsAreRefused(t *testing.T) {
	m := Manifest{
		Schema: SchemaVersion, ID: "radar", Name: "R", Version: "1.0.0",
		Host: Host{Contract: Contract}, Needs: "native",
		Native: []Native{
			{OS: "linux", Arch: "amd64", Libc: "musl", Path: "a"},
			{OS: "linux", Arch: "amd64", Libc: "musl", Path: "b"},
		},
	}
	if ps := m.Validate(); !hasField(ps, "native[1]") {
		t.Errorf("two rows for one target is ambiguous: %v", ps)
	}
}

// Rows that would never be read are a misunderstanding worth naming.
func TestNativeRowsWithoutTheNativeTierAreRefused(t *testing.T) {
	m := Manifest{
		Schema: SchemaVersion, ID: "radar", Name: "R", Version: "1.0.0",
		Host: Host{Contract: Contract}, Needs: "python@>=3.11",
		Native: []Native{{OS: "linux", Arch: "amd64", Libc: "musl", Path: "a"}},
	}
	if ps := m.Validate(); !hasField(ps, "native") {
		t.Errorf("native rows beside a non-native needs must be named: %v", ps)
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
