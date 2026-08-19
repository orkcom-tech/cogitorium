package plugin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeCatalog(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "plugins.json")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

const oneEntry = `[{"id":"release-radar","name":"Release Radar","author":"someone",
  "description":"Watches releases.","repo":"someone/release-radar"}]`

func TestReadCatalogRefusesTwoEntriesWithOneID(t *testing.T) {
	p := writeCatalog(t, `[
	  {"id":"radar","name":"A","author":"a","description":"d","repo":"a/x"},
	  {"id":"radar","name":"B","author":"b","description":"d","repo":"b/y"}]`)
	_, err := ReadCatalog(p)
	if err == nil || !strings.Contains(err.Error(), "twice") {
		t.Fatalf("expected a duplicate-id refusal, got %v", err)
	}
}

func TestReadCatalogRefusesAnUnknownField(t *testing.T) {
	// A field nobody reads is a field an author believes in. Refusing it in CI
	// is the only moment somebody is still watching.
	p := writeCatalog(t, `[{"id":"radar","name":"A","author":"a","description":"d",
	  "repo":"a/x","verified":true}]`)
	if _, err := ReadCatalog(p); err == nil {
		t.Fatal("an entry claiming its own verified flag was accepted")
	}
}

// The takeover. This is the one an auto-merging catalog gets wrong.
func TestRepointingSomebodyElsesEntryIsNotAutoMergeable(t *testing.T) {
	before, err := ReadCatalog(writeCatalog(t, oneEntry))
	if err != nil {
		t.Fatal(err)
	}
	after, err := ReadCatalog(writeCatalog(t, strings.Replace(oneEntry,
		"someone/release-radar", "astranger/release-radar", 1)))
	if err != nil {
		t.Fatal(err)
	}

	c := Diff(before, after)
	if len(c.Edited) != 1 || c.Edited[0].Fields[0] != "repo" {
		t.Fatalf("the repo move was not seen as an edit: %+v", c)
	}
	ok, why := c.AutoMergeable()
	if ok {
		t.Fatal("a stranger repointing an existing entry would have merged itself")
	}
	if !strings.Contains(why, "different repository") {
		t.Fatalf("the refusal does not say what the risk is: %s", why)
	}
}

func TestDelistingIsNotAutoMergeable(t *testing.T) {
	before, _ := ReadCatalog(writeCatalog(t, oneEntry))
	after, _ := ReadCatalog(writeCatalog(t, `[]`))
	ok, why := Diff(before, after).AutoMergeable()
	if ok || !strings.Contains(why, "release-radar") {
		t.Fatalf("removal auto-merged, or said nothing useful: %v %q", ok, why)
	}
}

func TestAddingYourOwnPluginAutoMerges(t *testing.T) {
	before, _ := ReadCatalog(writeCatalog(t, `[]`))
	after, err := ReadCatalog(writeCatalog(t, oneEntry))
	if err != nil {
		t.Fatal(err)
	}
	c := Diff(before, after)
	if len(c.Added) != 1 {
		t.Fatalf("addition not seen: %+v", c)
	}
	if ok, why := c.AutoMergeable(); !ok {
		t.Fatalf("a plain addition was held back: %s", why)
	}
}

// The index job fills in versions on entries it did not write. If that counted
// as an edit, every scheduled refresh would look like a takeover attempt and
// the catalog would need a human every day for a machine's own work.
func TestTheIndexJobFillingInAVersionIsNotAnEdit(t *testing.T) {
	before, _ := ReadCatalog(writeCatalog(t, oneEntry))
	after, _ := ReadCatalog(writeCatalog(t, strings.Replace(oneEntry,
		`"repo":"someone/release-radar"`, `"repo":"someone/release-radar","version":"1.2.0"`, 1)))
	if c := Diff(before, after); len(c.Edited) != 0 {
		t.Fatalf("a version fill-in was treated as an edit: %+v", c.Edited)
	}
}
