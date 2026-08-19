package plugin

import (
	"strings"
	"testing"
)

func grantsFor(t *testing.T, hosts, secrets, api []string) Grants {
	t.Helper()
	g, err := ResolveGrants(Manifest{ID: "radar", Hosts: hosts, Secrets: secrets, API: api})
	if err != nil {
		t.Fatalf("resolving: %v", err)
	}
	return g
}

// The rule that differs from a gear's, deliberately and in the strict
// direction: a gear with a network grant and no destinations means anywhere,
// because an operator switched networking on and declined to narrow it. A
// plugin has no such switch, so absence is a refusal rather than a blank
// cheque.
func TestNoHostsMeansNoNetworkNotAnyNetwork(t *testing.T) {
	g := grantsFor(t, nil, nil, nil)
	if g.Networked() {
		t.Error("a plugin that asked for nothing is not networked")
	}
	err := g.AllowHost("api.example.com")
	if err == nil {
		t.Fatal("an ungranted plugin must reach nothing")
	}
	if !strings.Contains(err.Error(), "not granted any network") {
		t.Errorf("the refusal should say why: %v", err)
	}
}

func TestAGrantedHostIsAllowedAndOthersAreNot(t *testing.T) {
	g := grantsFor(t, []string{"api.example.com", "cdn.example.org"}, nil, nil)

	for _, ok := range []string{"api.example.com", "API.EXAMPLE.COM", "api.example.com.", "api.example.com:443"} {
		if err := g.AllowHost(ok); err != nil {
			t.Errorf("%s should be allowed: %v", ok, err)
		}
	}
	err := g.AllowHost("evil.example.net")
	if err == nil {
		t.Fatal("an ungranted destination must be refused")
	}
	// The person reading this is either an author who mistyped or an operator
	// deciding whether to widen the grant, and both need the same two facts.
	if !strings.Contains(err.Error(), "evil.example.net") || !strings.Contains(err.Error(), "api.example.com") {
		t.Errorf("the refusal must name the destination and what was granted: %v", err)
	}
}

// The wildcard is the gate's own form, and the manifest has to accept what the
// gate accepts or a valid grant becomes unwritable.
func TestTheWildcardFormWorksEndToEnd(t *testing.T) {
	m := Manifest{
		Schema: SchemaVersion, ID: "radar", Name: "Radar", Version: "1.0.0",
		Host:  Host{Contract: Contract},
		Hosts: []string{"*.example.com"},
	}
	if ps := m.Validate(); len(ps) != 0 {
		t.Fatalf("a wildcard host must be a writable grant: %v", ps)
	}
	g, err := ResolveGrants(m)
	if err != nil {
		t.Fatal(err)
	}
	if err := g.AllowHost("api.example.com"); err != nil {
		t.Errorf("a subdomain should match: %v", err)
	}
	// The gate's rule, inherited rather than reimplemented: the apex is not a
	// subdomain, and notexample.com is not example.com.
	if err := g.AllowHost("notexample.com"); err == nil {
		t.Error("notexample.com must not match *.example.com")
	}
}

func TestSecretsAreNamedNotHeld(t *testing.T) {
	g := grantsFor(t, nil, []string{"ACME_TOKEN"}, nil)
	if err := g.AllowSecret("ACME_TOKEN"); err != nil {
		t.Errorf("a declared credential should be nameable: %v", err)
	}
	err := g.AllowSecret("OTHER_TOKEN")
	if err == nil {
		t.Fatal("an undeclared credential must be refused")
	}
	if !strings.Contains(err.Error(), "ACME_TOKEN") {
		t.Errorf("the refusal should list what was declared: %v", err)
	}
}

func TestScopesGateTheAPI(t *testing.T) {
	g := grantsFor(t, nil, nil, []string{"runs:read"})
	if err := g.AllowScope("runs:read"); err != nil {
		t.Errorf("a granted scope should pass: %v", err)
	}
	if err := g.AllowScope("workspaces:write"); err == nil {
		t.Error("an ungranted scope must be refused")
	}
}

// A plugin granted runs:write that could not read a run back would have to be
// granted both to do one thing, and an approval screen listing two lines for
// one capability teaches an operator to skim.
func TestWriteImpliesReadOnTheSameSubject(t *testing.T) {
	g := grantsFor(t, nil, nil, []string{"runs:write"})
	if err := g.AllowScope("runs:read"); err != nil {
		t.Errorf("write should imply read on the same subject: %v", err)
	}
	if err := g.AllowScope("workspaces:read"); err == nil {
		t.Error("it must not imply read on a different subject")
	}
}

func TestAnUngrantedPluginRefusesWithoutListingNothing(t *testing.T) {
	g := grantsFor(t, nil, nil, nil)
	for _, err := range []error{g.AllowSecret("X"), g.AllowScope("runs:read")} {
		if err == nil {
			t.Fatal("expected a refusal")
		}
		if strings.Contains(err.Error(), "among what") {
			t.Errorf("with nothing granted the refusal should say so plainly: %v", err)
		}
	}
}

func TestABadHostIsRefusedAtResolveNotAtUse(t *testing.T) {
	if _, err := ResolveGrants(Manifest{ID: "radar", Hosts: []string{"not a host"}}); err == nil {
		t.Fatal("an unusable destination must be refused when the grant is read")
	}
}
