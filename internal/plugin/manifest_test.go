package plugin

import (
	"strings"
	"testing"
)

const minimal = `
schema: 1
id: release-radar
name: Release Radar
version: 1.4.0
host:
  contract: 1
`

func parseOK(t *testing.T, src string) Manifest {
	t.Helper()
	m, err := Parse([]byte(src))
	if err != nil {
		t.Fatalf("parsing: %v", err)
	}
	if ps := m.Validate(); len(ps) != 0 {
		t.Fatalf("unexpected problems: %v", ps)
	}
	return m
}

func problems(t *testing.T, src string) Problems {
	t.Helper()
	m, err := Parse([]byte(src))
	if err != nil {
		t.Fatalf("parsing: %v", err)
	}
	return m.Validate()
}

func hasField(ps Problems, field string) bool {
	for _, p := range ps {
		if p.Field == field {
			return true
		}
	}
	return false
}

func TestMinimalManifestIsValid(t *testing.T) {
	m := parseOK(t, minimal)
	if m.ID != "release-radar" {
		t.Errorf("id = %q", m.ID)
	}
	if m.PagePrefix() != "/p/release-radar/" {
		t.Errorf("page prefix = %q", m.PagePrefix())
	}
}

// A typo'd key that parses silently is a contribution the author believes they
// made and the operator never sees.
func TestUnknownFieldsAreRefused(t *testing.T) {
	_, err := Parse([]byte(minimal + "\npagez: []\n"))
	if err == nil {
		t.Fatal("an unknown field must be an error, not ignored")
	}
	if !strings.Contains(err.Error(), "pagez") {
		t.Errorf("the error should name the offending key, got: %v", err)
	}
}

// An author fixing a manifest wants the list, not a game where each run
// reveals one more problem.
func TestValidateReportsEveryProblemAtOnce(t *testing.T) {
	ps := problems(t, `
schema: 9
id: "A"
version: not-a-version
host:
  contract: 0
`)
	for _, f := range []string{"schema", "id", "name", "version", "host.contract"} {
		if !hasField(ps, f) {
			t.Errorf("no problem reported for %s; got %v", f, ps)
		}
	}
}

func TestReservedIDsAreRefused(t *testing.T) {
	for _, id := range []string{"cog", "cogitorium", "core", "api", "admin", "under"} {
		ps := problems(t, strings.Replace(minimal, "release-radar", id, 1))
		if !hasField(ps, "id") {
			t.Errorf("%q is reserved by the host and must be refused", id)
		}
	}
}

func TestIDShape(t *testing.T) {
	bad := []string{"ab", "-lead", "trail-", "Upper", "has_underscore", "has.dot", strings.Repeat("x", 49)}
	for _, id := range bad {
		ps := problems(t, strings.Replace(minimal, "release-radar", id, 1))
		if !hasField(ps, "id") {
			t.Errorf("id %q should be refused", id)
		}
	}
	for _, id := range []string{"abc", "a-b", "radar2", strings.Repeat("x", 48)} {
		ps := problems(t, strings.Replace(minimal, "release-radar", id, 1))
		if hasField(ps, "id") {
			t.Errorf("id %q should be accepted, got %v", id, ps)
		}
	}
}

// The contract is the real gate, and a plugin from the future is refused by
// name rather than half-loaded.
func TestContractFromTheFutureIsRefused(t *testing.T) {
	ps := problems(t, strings.Replace(minimal, "contract: 1", "contract: 99", 1))
	if !hasField(ps, "host.contract") {
		t.Fatal("a contract this build does not speak must be refused")
	}
}

func TestPagesMustLiveUnderThePluginsOwnPrefix(t *testing.T) {
	ps := problems(t, minimal+`
pages:
  - path: /admin
    template: release-radar.page.guide
`)
	if !hasField(ps, "pages[0].path") {
		t.Fatal("a page outside the plugin's prefix must be refused — that is what stops collisions")
	}

	parseOK(t, minimal+`
pages:
  - path: /p/release-radar/guide
    template: release-radar.page.guide
    title: Guide
`)
}

// A page rendering a name the plugin did not ship is a dangling reference
// dressed up as a contribution.
func TestAPageMustRenderATemplateThePluginOwns(t *testing.T) {
	ps := problems(t, minimal+`
pages:
  - path: /p/release-radar/guide
    template: cog.page.workspace
`)
	if !hasField(ps, "pages[0].template") {
		t.Fatal("a page pointing at somebody else's template must be refused")
	}
}

func TestPageAuthValues(t *testing.T) {
	for _, a := range []string{"", "token", "admin", "none"} {
		src := minimal + "\npages:\n  - path: /p/release-radar/g\n    template: release-radar.page.g\n"
		if a != "" {
			src += "    auth: " + a + "\n"
		}
		if ps := problems(t, src); hasField(ps, "pages[0].auth") {
			t.Errorf("auth %q should be accepted, got %v", a, ps)
		}
	}
	src := minimal + "\npages:\n  - path: /p/release-radar/g\n    template: release-radar.page.g\n    auth: sudo\n"
	if ps := problems(t, src); !hasField(ps, "pages[0].auth") {
		t.Error("an unknown auth class must be refused rather than silently treated as none")
	}
}

func TestDuplicatePagePathsAreRefused(t *testing.T) {
	ps := problems(t, minimal+`
pages:
  - path: /p/release-radar/g
    template: release-radar.page.a
  - path: /p/release-radar/g
    template: release-radar.page.b
`)
	if !hasField(ps, "pages[1].path") {
		t.Error("two pages on one path must be refused at parse, not resolved by luck at boot")
	}
}

func TestNavShape(t *testing.T) {
	parseOK(t, minimal+`
nav:
  - area: rail
    label: Releases
    icon: tag
    href: /p/release-radar/guide
    order: 500
    when: workspace
`)
	ps := problems(t, minimal+`
nav:
  - area: sidebar
    label: X
    href: relative
    when: sometimes
`)
	for _, f := range []string{"nav[0].area", "nav[0].href", "nav[0].when"} {
		if !hasField(ps, f) {
			t.Errorf("no problem for %s; got %v", f, ps)
		}
	}
}

func TestAssetsMayNotEscapeTheBundle(t *testing.T) {
	ps := problems(t, minimal+`
styles: ["../../etc/passwd"]
scripts:
  - src: /abs.js
`)
	if !hasField(ps, "styles[0]") || !hasField(ps, "scripts[0].src") {
		t.Errorf("assets must stay inside the bundle; got %v", ps)
	}
}

// A manifest never carries a credential value, only the name of one.
func TestSecretsAreNamesNotValues(t *testing.T) {
	ps := problems(t, minimal+"\nsecrets: [\"sk-live-abcdef\"]\n")
	if !hasField(ps, "secrets[0]") {
		t.Fatal("a lowercase secret literal must be refused — a manifest carries names")
	}
	parseOK(t, minimal+"\nsecrets: [ACME_TOKEN]\n")
}

func TestGrantShapes(t *testing.T) {
	parseOK(t, minimal+`
hosts: ["api.acme.com", "*.example.org"]
api: ["runs:read", "workspaces:read"]
`)
	ps := problems(t, minimal+"\nhosts: [\"api.acme.com:443\"]\napi: [\"everything\"]\n")
	if !hasField(ps, "hosts[0]") || !hasField(ps, "api[0]") {
		t.Errorf("expected host and api problems, got %v", ps)
	}
}

// Declaring a name you own is not an override, and an author who wrote one has
// misunderstood the mechanism.
func TestDeclaringYourOwnNamespaceAsAnOverrideIsRefused(t *testing.T) {
	ps := problems(t, minimal+"\noverrides: [\"release-radar.row.thing\"]\n")
	if !hasField(ps, "overrides[0]") {
		t.Fatal("overriding your own namespace is a misunderstanding worth naming")
	}
	parseOK(t, minimal+"\noverrides: [\"cog.row.gear\"]\n")
}

func TestNeedsParsing(t *testing.T) {
	for _, s := range []string{"js", "python@>=3.11", "node@>=22.0.0", "bun"} {
		if _, err := ParseNeeds(s); err != nil {
			t.Errorf("ParseNeeds(%q): %v", s, err)
		}
	}
	for _, s := range []string{"Python", "py thon", "python@nonsense"} {
		if _, err := ParseNeeds(s); err == nil {
			t.Errorf("ParseNeeds(%q) should fail", s)
		}
	}
	n, _ := ParseNeeds("python@>=3.11")
	if n.Technology != "python" || n.String() != "python@>=3.11.0" {
		t.Errorf("round trip: %+v %q", n, n.String())
	}
}

func TestVersionParsing(t *testing.T) {
	ok := []string{"1.4.0", "v1.4.0", "0.0.1", "1.4.0-beta.1", "1.4.0+build", "1.4.0-rc1+build"}
	for _, s := range ok {
		if _, err := ParseVersion(s); err != nil {
			t.Errorf("ParseVersion(%q): %v", s, err)
		}
	}
	bad := []string{"", "1.4", "1.4.0.1", "a.b.c", "01.4.0", "-1.4.0"}
	for _, s := range bad {
		if _, err := ParseVersion(s); err == nil {
			t.Errorf("ParseVersion(%q) should fail", s)
		}
	}
}

// A prerelease sorting before its release is the one rule that stops
// 1.4.0-beta.1 satisfying ">=1.4.0".
func TestPrereleaseSortsBeforeItsRelease(t *testing.T) {
	beta, _ := ParseVersion("1.4.0-beta.1")
	rel, _ := ParseVersion("1.4.0")
	if beta.Compare(rel) >= 0 {
		t.Fatal("a prerelease must sort before its release")
	}
	c, _ := ParseConstraint(">=1.4.0")
	if c.Satisfied(beta) {
		t.Error("1.4.0-beta.1 must not satisfy >=1.4.0")
	}
	if !c.Satisfied(rel) {
		t.Error("1.4.0 must satisfy >=1.4.0")
	}
}

// ">=1.9" is what an author naturally writes; refusing it would be pedantry
// charged to the person we are trying to make comfortable.
func TestTwoPartConstraintsAreAccepted(t *testing.T) {
	c, err := ParseConstraint(">=1.9")
	if err != nil {
		t.Fatalf("ParseConstraint: %v", err)
	}
	v, _ := ParseVersion("1.9.0")
	if !c.Satisfied(v) {
		t.Error("1.9.0 should satisfy >=1.9")
	}
	older, _ := ParseVersion("1.8.9")
	if c.Satisfied(older) {
		t.Error("1.8.9 should not satisfy >=1.9")
	}
}

func TestConstraintOperators(t *testing.T) {
	v := func(s string) Version { x, _ := ParseVersion(s); return x }
	cases := []struct {
		c    string
		v    string
		want bool
	}{
		{">=1.0.0", "1.0.0", true}, {">1.0.0", "1.0.0", false},
		{"<=1.0.0", "1.0.0", true}, {"<1.0.0", "1.0.0", false},
		{"1.0.0", "1.0.0", true}, {"1.0.0", "1.0.1", false},
		{"", "9.9.9", true},
	}
	for _, tc := range cases {
		c, err := ParseConstraint(tc.c)
		if err != nil {
			t.Fatalf("ParseConstraint(%q): %v", tc.c, err)
		}
		if got := c.Satisfied(v(tc.v)); got != tc.want {
			t.Errorf("%q satisfied by %q = %v, want %v", tc.c, tc.v, got, tc.want)
		}
	}
}

// The contract is compiled in and read by the catalog's CI. A silent bump
// would invalidate every published plugin without anything saying so.
func TestContractIsDeliberate(t *testing.T) {
	if Contract != 1 {
		t.Fatalf("Contract moved to %d. It moves only on a break to the template model or the "+
			"host ABI, and moving it invalidates every published plugin — update the catalog "+
			"schema and this test together, deliberately.", Contract)
	}
}
