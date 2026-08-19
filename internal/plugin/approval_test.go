package plugin

import (
	"archive/zip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func bundle(t *testing.T, id, version, extra string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "b.zip")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	w := zip.NewWriter(f)
	add := func(name, body string) {
		e, err := w.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := e.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	add("plugin.yaml", "schema: 1\nid: "+id+"\nname: "+id+"\nversion: "+version+"\nhost:\n  contract: 1\n")
	add("templates/t.html", `{{define "`+id+`.page.home"}}`+extra+`{{end}}`)
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return p
}

// Installing is not a decision. This is the whole reason install and enable
// are separate verbs.
func TestAFreshInstallIsPending(t *testing.T) {
	s := open(t)
	if _, _, err := s.Install(bundle(t, "radar", "1.0.0", "a")); err != nil {
		t.Fatal(err)
	}
	why := s.Pending("radar")
	if why == "" {
		t.Fatal("a freshly installed plugin must not be enableable")
	}
	if !strings.Contains(why, "nobody has approved") {
		t.Errorf("the reason should say which state it is in: %q", why)
	}
	if err := s.Enable("radar"); err == nil {
		t.Fatal("enabling an unapproved plugin must be refused")
	}
}

func TestApprovingLetsItBeEnabled(t *testing.T) {
	s := open(t)
	if _, _, err := s.Install(bundle(t, "radar", "1.0.0", "a")); err != nil {
		t.Fatal(err)
	}
	a, err := s.Approve("radar", "admin")
	if err != nil {
		t.Fatalf("approving: %v", err)
	}
	if a.By != "admin" || a.Version != "1.0.0" || !strings.HasPrefix(a.Digest, "sha256:") {
		t.Errorf("the decision must record who, what version and which bytes: %+v", a)
	}
	if why := s.Pending("radar"); why != "" {
		t.Fatalf("it should be enableable now: %s", why)
	}
	if err := s.Enable("radar"); err != nil {
		t.Fatalf("enabling: %v", err)
	}
}

// The rule the gear catalog already lives by: approval names a version's
// bytes, not its name.
func TestANewVersionReturnsToPending(t *testing.T) {
	s := open(t)
	if _, _, err := s.Install(bundle(t, "radar", "1.0.0", "a")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Approve("radar", "admin"); err != nil {
		t.Fatal(err)
	}
	if err := s.Enable("radar"); err != nil {
		t.Fatal(err)
	}

	if _, _, err := s.Install(bundle(t, "radar", "2.0.0", "a")); err != nil {
		t.Fatal(err)
	}
	why := s.Pending("radar")
	if why == "" {
		t.Fatal("a new version must return to pending")
	}
	if !strings.Contains(why, "1.0.0") || !strings.Contains(why, "2.0.0") {
		t.Errorf("the reason should name both versions: %q", why)
	}
}

// The case a version number cannot catch: same version, different content.
// This is exactly the hole the gear catalog has and MCP does not.
func TestTheSameVersionWithDifferentContentReturnsToPending(t *testing.T) {
	s := open(t)
	if _, _, err := s.Install(bundle(t, "radar", "1.0.0", "original")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Approve("radar", "admin"); err != nil {
		t.Fatal(err)
	}

	// Same id, same version, different bytes.
	if _, _, err := s.Install(bundle(t, "radar", "1.0.0", "SOMETHING ELSE ENTIRELY")); err != nil {
		t.Fatal(err)
	}
	why := s.Pending("radar")
	if why == "" {
		t.Fatal("different content under the same version must return to pending")
	}
	if !strings.Contains(why, "exact bytes") {
		t.Errorf("the reason should say what approval covers: %q", why)
	}
	if err := s.Enable("radar"); err == nil {
		t.Fatal("and it must not be enableable")
	}
}

// Leaving something enabled whose approval was just withdrawn would make the
// withdrawal decorative until the next restart.
func TestRevokingAlsoDisables(t *testing.T) {
	s := open(t)
	if _, _, err := s.Install(bundle(t, "radar", "1.0.0", "a")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Approve("radar", "admin"); err != nil {
		t.Fatal(err)
	}
	if err := s.Enable("radar"); err != nil {
		t.Fatal(err)
	}

	if err := s.Revoke("radar"); err != nil {
		t.Fatal(err)
	}
	if order, _ := s.Order(); len(order) != 0 {
		t.Errorf("revoking must disable it too, got %v", order)
	}
	if _, ok := s.Approved("radar"); ok {
		t.Error("the decision should be gone")
	}
}

// An author approving their own edits on every save is a ceremony that
// teaches them to click through.
func TestADevelopmentLayerIsNeverPending(t *testing.T) {
	work := filepath.Join(t.TempDir(), "radar")
	if err := Scaffold(work, "radar", ""); err != nil {
		t.Fatal(err)
	}
	s := open(t)
	if _, err := s.AddDev(work); err != nil {
		t.Fatalf("a development layer must not need approving: %v", err)
	}
	enabled, err := s.Enabled()
	if err != nil {
		t.Fatal(err)
	}
	if len(enabled) != 1 || enabled[0].Pending != "" {
		t.Errorf("a development layer should be live without a ceremony: %+v", enabled)
	}
}

// A decision recorded only where the operator cannot reach it is a decision
// they cannot undo.
func TestTheDecisionIsReadableOnDisk(t *testing.T) {
	s := open(t)
	if _, _, err := s.Install(bundle(t, "radar", "1.0.0", "a")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Approve("radar", "admin"); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(s.Root(), "radar", "approved"))
	if err != nil {
		t.Fatalf("the decision must be a file somebody can read: %v", err)
	}
	for _, want := range []string{"digest sha256:", "version 1.0.0", "by admin", "at "} {
		if !strings.Contains(string(b), want) {
			t.Errorf("the record omits %q:\n%s", want, b)
		}
	}
}

// A decision can only ever be recorded about content this machine holds.
func TestApprovingSomethingNotInstalledIsRefused(t *testing.T) {
	s := open(t)
	if _, err := s.Approve("ghost", "admin"); err == nil {
		t.Fatal("there is nothing to approve")
	}
}

// Found by running it: replacing an approved plugin's bytes left it enabled,
// running on a decision made about different code. Approval being void has to
// take enablement with it, or the rule only holds for plugins nobody approved.
func TestReplacingApprovedContentAlsoDisablesIt(t *testing.T) {
	s := open(t)
	if _, _, err := s.Install(bundle(t, "radar", "1.0.0", "original")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Approve("radar", "admin"); err != nil {
		t.Fatal(err)
	}
	if err := s.Enable("radar"); err != nil {
		t.Fatal(err)
	}

	if _, _, err := s.Install(bundle(t, "radar", "1.0.0", "SOMETHING ELSE")); err != nil {
		t.Fatal(err)
	}
	if order, _ := s.Order(); len(order) != 0 {
		t.Fatalf("changed content must not stay enabled, got %v", order)
	}
	all, _ := s.List()
	if len(all) != 1 || all[0].Enabled {
		t.Errorf("it should be listed as off: %+v", all)
	}
	if all[0].Pending == "" {
		t.Error("and pending, with a reason")
	}
}

// A reinstall of the SAME bytes is not a change and must not disturb anything.
func TestReinstallingIdenticalContentLeavesItEnabled(t *testing.T) {
	s := open(t)
	b := bundle(t, "radar", "1.0.0", "same")
	if _, _, err := s.Install(b); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Approve("radar", "admin"); err != nil {
		t.Fatal(err)
	}
	if err := s.Enable("radar"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Install(b); err != nil {
		t.Fatal(err)
	}
	if order, _ := s.Order(); len(order) != 1 {
		t.Errorf("identical bytes are not a change: %v", order)
	}
}
