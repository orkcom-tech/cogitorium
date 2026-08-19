package plugin

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
)

// Reading a catalog file, and deciding what a pull request against it did.
//
// This is the half of the catalog that runs in CI rather than on somebody's
// install, and it lives here because it is the same code the client uses to
// read the same file. A validator written separately in the workflow would be
// a second opinion about the format, and the two would disagree eventually —
// with the disagreement landing on an author who did nothing wrong.

// ReadCatalog parses a catalog file and checks every entry in it.
//
// Duplicate ids are rejected here rather than deduplicated. Two entries with
// one id is two plugins that cannot coexist, and picking one silently would
// mean the catalog quietly disagreed with itself about which repository a name
// points at.
func ReadCatalog(path string) ([]Entry, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var entries []Entry
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&entries); err != nil {
		return nil, fmt.Errorf("%s is not a readable catalog: %w", path, err)
	}

	seen := map[string]bool{}
	for _, e := range entries {
		if err := e.Validate(); err != nil {
			return nil, err
		}
		if seen[e.ID] {
			return nil, fmt.Errorf("%q appears twice; two entries with one id is two plugins that cannot coexist", e.ID)
		}
		seen[e.ID] = true
	}
	return entries, nil
}

// Change is what one submission did to the catalog.
type Change struct {
	// Added are entries that were not there before. These are what auto-merge
	// exists for: a stranger listing their own plugin takes nothing away from
	// anybody.
	Added []Entry
	// Edited are entries whose fields moved. Edited separately from Added
	// because an edit can repoint an existing id's repo at somebody else's
	// repository — which is a takeover of that plugin's download URL for every
	// install that has it, and the single thing auto-merge must never do.
	Edited []EditedEntry
	// Removed are entries that disappeared. Also never automatic: a delisting
	// is a decision about somebody else's work.
	Removed []Entry
}

// EditedEntry is one entry before and after, so a reviewer reads the move
// rather than two files.
type EditedEntry struct {
	Before Entry
	After  Entry
	// Fields names what changed, so the summary line says "repo" rather than
	// making somebody diff two JSON objects by eye.
	Fields []string
}

// AutoMergeable reports whether this change may merge without a person, and
// says why not when it may not.
//
// Additions only. The rule is deliberately blunt: any mechanism that decided
// an edit was safe would have to know who owns an id, and nothing in a public
// JSON file can establish that — the pull request's author is whoever opened
// it, which is exactly the claim under question.
func (c Change) AutoMergeable() (bool, string) {
	switch {
	case len(c.Removed) > 0:
		return false, fmt.Sprintf("this removes %s — delisting somebody's plugin is a decision, not a merge",
			strings.Join(ids(c.Removed), ", "))
	case len(c.Edited) > 0:
		var lines []string
		for _, e := range c.Edited {
			lines = append(lines, fmt.Sprintf("%s (%s)", e.After.ID, strings.Join(e.Fields, ", ")))
		}
		return false, fmt.Sprintf("this edits an entry that already exists: %s. "+
			"An edit can point an id somebody already installed at a different repository, "+
			"so it waits for a person", strings.Join(lines, "; "))
	case len(c.Added) == 0:
		return false, "this changes no entries"
	}
	return true, ""
}

// Diff reports what changed between two catalogs.
func Diff(before, after []Entry) Change {
	old := map[string]Entry{}
	for _, e := range before {
		old[e.ID] = e
	}
	now := map[string]Entry{}
	for _, e := range after {
		now[e.ID] = e
	}

	var c Change
	for _, e := range after {
		was, existed := old[e.ID]
		if !existed {
			c.Added = append(c.Added, e)
			continue
		}
		if f := movedFields(was, e); len(f) > 0 {
			c.Edited = append(c.Edited, EditedEntry{Before: was, After: e, Fields: f})
		}
	}
	for _, e := range before {
		if _, still := now[e.ID]; !still {
			c.Removed = append(c.Removed, e)
		}
	}

	sort.Slice(c.Added, func(i, j int) bool { return c.Added[i].ID < c.Added[j].ID })
	sort.Slice(c.Removed, func(i, j int) bool { return c.Removed[i].ID < c.Removed[j].ID })
	sort.Slice(c.Edited, func(i, j int) bool { return c.Edited[i].After.ID < c.Edited[j].After.ID })
	return c
}

// movedFields names what differs, by the name an author would recognise.
//
// Version is deliberately not compared: it is written by the catalog's own
// index job rather than by a submitter, so treating it as an edit would make
// every scheduled refresh look like a takeover attempt.
func movedFields(a, b Entry) []string {
	var out []string
	for _, f := range []struct {
		name string
		a, b string
	}{
		{"name", a.Name, b.Name},
		{"author", a.Author, b.Author},
		{"description", a.Description, b.Description},
		{"repo", a.Repo, b.Repo},
	} {
		if f.a != f.b {
			out = append(out, f.name)
		}
	}
	return out
}

func ids(es []Entry) []string {
	out := make([]string, 0, len(es))
	for _, e := range es {
		out = append(out, e.ID)
	}
	return out
}
