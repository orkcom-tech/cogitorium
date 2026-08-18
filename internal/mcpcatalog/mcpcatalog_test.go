package mcpcatalog

import (
	"regexp"
	"strings"
	"testing"
)

// Every entry here is a claim this project makes about somebody else's
// software. A wrong one sends an operator to debug a spawn failure in a product
// that told them it would work, so the claims are checked mechanically where
// they can be.

// mcpstore.Install refuses a name that does not match this. An entry whose name
// is refused is an entry that cannot be installed at all — a broken row in a
// list whose entire purpose is to be installed from.
var nameRule = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,39}$`)

func TestEveryEntryCouldActuallyBeInstalled(t *testing.T) {
	for _, e := range All() {
		if !nameRule.MatchString(e.Name) {
			t.Errorf("%s: name %q would be refused by the store's own rule", e.ID, e.Name)
		}
		if e.Command == "" {
			t.Errorf("%s: no command, so installing it produces a server that cannot start", e.ID)
		}
		// One string would mean a shell, and a shell means the arguments are
		// parsed by something with its own opinion about quoting.
		if strings.ContainsAny(e.Command, " \t") {
			t.Errorf("%s: the command %q carries its arguments; they belong in Args", e.ID, e.Command)
		}
	}
}

func TestIdsAndNamesAreUnique(t *testing.T) {
	seenID, seenName := map[string]bool{}, map[string]bool{}
	for _, e := range All() {
		if seenID[e.ID] {
			t.Errorf("two entries share the id %q; ids are referenced by the interface and must be stable", e.ID)
		}
		if seenName[e.Name] {
			t.Errorf("two entries share the name %q, and mcp_servers.name is UNIQUE — "+
				"installing the second would fail with a constraint violation", e.Name)
		}
		seenID[e.ID], seenName[e.Name] = true, true
	}
}

// An entry that does not state its prerequisite produces a spawn failure whose
// message nobody can read: `npx: executable file not found in $PATH` says
// nothing about node.
func TestEveryEntryStatesWhatItNeeds(t *testing.T) {
	for _, e := range All() {
		if strings.TrimSpace(e.Needs) == "" {
			t.Errorf("%s: does not say what has to be on the machine", e.ID)
		}
		if strings.TrimSpace(e.Reaches) == "" {
			t.Errorf("%s: does not say what it reaches, which is the thing an operator is approving", e.ID)
		}
		if strings.TrimSpace(e.Docs) == "" {
			t.Errorf("%s: no documentation link, so an operator cannot check what its arguments mean", e.ID)
		}
	}
}

// A credential NAME is a hint; a credential VALUE in a list compiled into the
// binary would be a leak shipped to every install.
func TestNoEntryCarriesAValue(t *testing.T) {
	for _, e := range All() {
		for _, n := range e.EnvNames {
			if strings.Contains(n, "=") {
				t.Errorf("%s: env name %q looks like a name=value pair", e.ID, n)
			}
			if n != strings.ToUpper(n) {
				t.Errorf("%s: env name %q is not an environment variable name", e.ID, n)
			}
		}
		// The obvious shapes of a real credential, in case one is ever pasted
		// in while copying an example from a README.
		blob := strings.Join(append([]string{e.Command}, e.Args...), " ")
		for _, bad := range []string{"ghp_", "xoxb-", "sk-", "AKIA"} {
			if strings.Contains(blob, bad) {
				t.Errorf("%s: its command line contains something shaped like a credential (%q)", e.ID, bad)
			}
		}
	}
}

// An entry that needs a credential must say so in Needs as well as in EnvNames,
// because Needs is the sentence an operator actually reads.
func TestAnEntryNeedingACredentialSaysSo(t *testing.T) {
	for _, e := range All() {
		for _, n := range e.EnvNames {
			if !strings.Contains(e.Needs, n) {
				t.Errorf("%s: needs %s and its prerequisite sentence does not mention it", e.ID, n)
			}
		}
	}
}

func TestSearchFindsByWhatSomebodyWouldType(t *testing.T) {
	if len(Search("")) != len(All()) {
		t.Fatal("an empty search is not the whole list")
	}
	if got := Search("github"); len(got) == 0 {
		t.Fatal("searching for github found nothing")
	}
	// Nobody types the package name; they type the thing they want.
	if got := Search("issues"); len(got) == 0 {
		t.Fatal("searching for `issues` found nothing, and two entries are issue trackers")
	}
	if got := Search("zzz-not-a-thing"); len(got) != 0 {
		t.Fatalf("a nonsense search returned %d entries", len(got))
	}
}

func TestGetIsByIdAndMissesCleanly(t *testing.T) {
	if _, ok := Get("github"); !ok {
		t.Fatal("github is in the list and Get did not find it")
	}
	if _, ok := Get("nope"); ok {
		t.Fatal("Get invented an entry")
	}
}

// All returns a copy, or a caller that sorts the result reorders the package's
// own list for every later caller in the process.
func TestAllIsACopy(t *testing.T) {
	a := All()
	if len(a) == 0 {
		t.Fatal("the catalogue is empty")
	}
	a[0].Name = "clobbered"
	if All()[0].Name == "clobbered" {
		t.Fatal("All handed out the package's own slice")
	}
}
