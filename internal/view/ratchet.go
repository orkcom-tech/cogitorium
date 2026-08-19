package view

import (
	"fmt"
	"sort"
)

// Ratchets: how this product renames its own things without invalidating a
// plugin somebody published a year ago.
//
// The problem is structural. A template name is an address a stranger wrote
// into a file they shipped, and a model field is a word they typed into
// markup. Renaming either would ordinarily break every plugin that used it —
// so either the names are frozen forever, or published plugins break on every
// tidy-up. Neither is acceptable, and this is the third option.
//
// A retired name keeps working, resolved to its replacement, and the ledger
// says so. The plugin's author fixes it when convenient rather than urgently,
// and an operator is told which of their plugins is running on an old name
// rather than discovering it when the ratchet is eventually removed.
//
// The entries here are permanent in one direction: adding one is how a rename
// ships, and removing one is a deliberate break that needs the affected
// plugins to be known first. That is what makes it a ratchet rather than a
// map — it only ever turns one way.

// Ratchet is one retired name and what replaced it.
type Ratchet struct {
	// From is what a published plugin might still say.
	From string
	// To is the current name. Empty means the thing is gone entirely, which is
	// a different fact and needs a different sentence.
	To string
	// Since is the release that made the change, so an author reading the
	// ledger knows how old their reference is.
	Since string
	// Why is one line an author can act on.
	Why string
}

// Retired reports whether this ratchet retires a name outright rather than
// renaming it.
func (r Ratchet) Retired() bool { return r.To == "" }

// templateRatchets are retired template names.
//
// Empty today, and that is correct rather than unfinished: nothing has been
// renamed yet. The table exists now so the first rename is a one-line change
// with a test already watching it, instead of a decision somebody has to make
// under pressure while a published plugin is broken.
var templateRatchets = []Ratchet{}

// modelRatchets are retired fields on a model. Keyed by "template.Field",
// because the same field name on two models is two different things and only
// one of them may have moved.
var modelRatchets = []Ratchet{}

// tokenRatchets are retired CSS custom properties. A plugin's stylesheet is
// not parsed by this package, so these are reported rather than rewritten —
// but reporting is what turns a silently unstyled region into a line.
var tokenRatchets = []Ratchet{}

// ratchetIndex is the lookup built from a table.
type ratchetIndex map[string]Ratchet

func indexOf(rs []Ratchet) ratchetIndex {
	out := make(ratchetIndex, len(rs))
	for _, r := range rs {
		out[r.From] = r
	}
	return out
}

// TemplateRatchet resolves a template name a plugin used.
//
// The second result is false when the name is current, which is the ordinary
// case and costs one map lookup.
func TemplateRatchet(name string) (Ratchet, bool) {
	r, ok := indexOf(templateRatchets)[name]
	return r, ok
}

// ModelRatchet resolves a field a plugin referenced on a model.
func ModelRatchet(template, field string) (Ratchet, bool) {
	r, ok := indexOf(modelRatchets)[template+"."+field]
	return r, ok
}

// TokenRatchet resolves a CSS custom property a plugin's stylesheet used.
func TokenRatchet(token string) (Ratchet, bool) {
	r, ok := indexOf(tokenRatchets)[token]
	return r, ok
}

// Note is what the ledger and the plugins page say about a ratcheted name.
type Note struct {
	Layer string
	// What is the name as the plugin wrote it.
	What string
	// Message is written for the plugin's author, because they are the person
	// who can fix it, and for the operator, because they are the person who
	// has to decide whether to wait.
	Message string
}

// noteFor builds the sentence.
func noteFor(layer, what string, r Ratchet) Note {
	if r.Retired() {
		return Note{Layer: layer, What: what, Message: fmt.Sprintf(
			"%s was retired in %s and has no replacement, so this renders nothing. %s",
			what, r.Since, r.Why)}
	}
	return Note{Layer: layer, What: what, Message: fmt.Sprintf(
		"%s was renamed to %s in %s and still works through a compatibility rule. "+
			"Update it when convenient — the rule is not permanent. %s",
		what, r.To, r.Since, r.Why)}
}

// applyRatchets rewrites retired template names in a layer's definitions and
// returns what it had to do.
//
// Rewriting rather than refusing is the whole point: a plugin published
// against last year's names keeps rendering, and its author hears about it
// through the ledger rather than through a support request from an operator
// whose screen went blank.
func applyRatchets(layer string, defined map[string]bool) (renamed map[string]string, notes []Note) {
	idx := indexOf(templateRatchets)
	renamed = map[string]string{}

	names := make([]string, 0, len(defined))
	for n := range defined {
		names = append(names, n)
	}
	sort.Strings(names)

	for _, name := range names {
		r, ok := idx[name]
		if !ok {
			continue
		}
		notes = append(notes, noteFor(layer, name, r))
		if !r.Retired() {
			// A plugin that already defines the NEW name as well has been
			// updated and simply still ships the old one. Its own current
			// definition wins; the retired one is dropped rather than allowed
			// to overwrite it.
			if !defined[r.To] {
				renamed[name] = r.To
			}
		}
	}
	return renamed, notes
}

// Ratchets returns every rule, for a command that prints them and for a test
// that watches the table.
func Ratchets() []Ratchet {
	out := make([]Ratchet, 0, len(templateRatchets)+len(modelRatchets)+len(tokenRatchets))
	out = append(out, templateRatchets...)
	out = append(out, modelRatchets...)
	out = append(out, tokenRatchets...)
	return out
}
