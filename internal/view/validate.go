package view

import (
	"fmt"
	"io"
	"reflect"
	"regexp"
	"sort"
	"strings"
)

// Validation is the compatibility check that actually fires on the upgrade
// path that happens.
//
// Nobody upgrades a host by carefully re-testing every installed plugin. They
// let a package manager or an image pull replace the binary while plugins
// built for the old one sit in the data directory. So every plugin template is
// executed against a zero value of the model it renders against, at install
// and again at every boot, and a template referencing a field that no longer
// exists disables its plugin BY NAME — naming the plugin, the template, the
// field, and what to write instead.
//
// This is the one check the JavaScript designs could not have an equivalent
// of: whether a compiled frontend plugin survives a new host is undecidable
// without rendering it, and rendering it means shipping the breakage.

// Models maps a template name to a zero value of the model it is rendered
// with. The host registers one per name it owns; that pair — the name and the
// model — is the API, and the markup underneath is not.
type Models map[string]any

// Failure is one template that cannot render. It fails its plugin, not the
// boot: one bad plugin must not take the product down with it.
type Failure struct {
	Layer string
	Name  string
	// Field is what the template asked for that no longer exists. Empty when
	// the failure was something else.
	Field string
	// Suggestion is the closest thing that does exist, when there is an
	// obvious one. This is the difference between an error an author can act
	// on and one they have to go read source to understand.
	Suggestion string
	Err        error
}

func (f Failure) Error() string { return f.String() }

func (f Failure) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "plugin %q: template %q", f.Layer, f.Name)
	if f.Field != "" {
		fmt.Fprintf(&b, ": no field %q in the model it renders against", f.Field)
		if f.Suggestion != "" {
			fmt.Fprintf(&b, " — did you mean %q?", f.Suggestion)
		}
	} else {
		fmt.Fprintf(&b, ": %v", f.Err)
	}
	return b.String()
}

// Report is the outcome of validating a composed set.
type Report struct {
	Failures []Failure
	// Unvalidated lists names that were executed against nothing because no
	// model is registered for them. Reported rather than silently skipped: a
	// check that quietly covers less than it appears to is worse than no
	// check, because it is trusted.
	Unvalidated []string
}

// FailedLayers is the set of plugins that must be disabled, with their reasons.
func (r Report) FailedLayers() map[string][]Failure {
	out := map[string][]Failure{}
	for _, f := range r.Failures {
		out[f.Layer] = append(out[f.Layer], f)
	}
	return out
}

// OK reports whether nothing failed.
func (r Report) OK() bool { return len(r.Failures) == 0 }

// Validate executes every public template against its registered model.
//
// The host's own templates are validated too. A core template that cannot
// render is a bug in this repository and it is better to learn that from a
// test than from a user, so it is reported the same way rather than exempted.
func Validate(s *Set, models Models) Report {
	var r Report

	owner := map[string]string{}
	for _, e := range s.ledger.Entries {
		// The last layer to define a name is the one whose body renders, so it
		// is the one a failure belongs to.
		if e.Action != Extends {
			owner[e.Name] = e.Layer
		}
	}
	// Every contributor to an append name renders, so each is on the hook.
	appendContributors := map[string][]string{}
	for _, e := range s.ledger.Entries {
		if e.Action == Extends {
			appendContributors[e.Name] = append(appendContributors[e.Name], e.Layer)
		}
	}

	for _, name := range s.Names() {
		model, ok := models[name]
		if !ok {
			r.Unvalidated = append(r.Unvalidated, name)
			continue
		}
		err := s.Execute(io.Discard, name, model)
		if err == nil {
			continue
		}
		field, suggestion := explain(err, model)

		layers := appendContributors[name]
		if len(layers) == 0 {
			layers = []string{owner[name]}
		}
		for _, l := range layers {
			r.Failures = append(r.Failures, Failure{
				Layer: l, Name: name, Field: field, Suggestion: suggestion, Err: err,
			})
		}
	}
	sort.Slice(r.Failures, func(i, j int) bool {
		if r.Failures[i].Layer != r.Failures[j].Layer {
			return r.Failures[i].Layer < r.Failures[j].Layer
		}
		return r.Failures[i].Name < r.Failures[j].Name
	})
	return r
}

// fieldErr pulls the field name out of the message html/template produces for
// a reference that no longer resolves. Matching on the message is unpleasant,
// and the alternative — reflecting over every template's parse tree to
// pre-check every field path — would reimplement the evaluator badly and
// still miss what a method returns.
var fieldErr = regexp.MustCompile(`can't evaluate field ([A-Za-z0-9_]+) in type`)

// nilMapErr is what missingkey=error produces. A map key is a different kind
// of absence from a struct field and worth naming differently.
var nilMapErr = regexp.MustCompile(`map has no entry for key "([^"]+)"`)

func explain(err error, model any) (field, suggestion string) {
	msg := err.Error()
	if m := fieldErr.FindStringSubmatch(msg); m != nil {
		field = m[1]
	} else if m := nilMapErr.FindStringSubmatch(msg); m != nil {
		field = m[1]
	}
	if field == "" {
		return "", ""
	}
	return field, nearest(field, fieldsOf(model))
}

// fieldsOf lists what the model actually offers, so a suggestion can be made
// from the real thing rather than from a guess.
func fieldsOf(model any) []string {
	seen := map[string]bool{}
	var out []string

	var walk func(reflect.Type, int)
	walk = func(t reflect.Type, depth int) {
		if t == nil || depth > 3 {
			return
		}
		for t.Kind() == reflect.Ptr || t.Kind() == reflect.Slice || t.Kind() == reflect.Array {
			t = t.Elem()
		}
		if t.Kind() != reflect.Struct {
			return
		}
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			if f.PkgPath != "" {
				continue // unexported: a template could never reach it
			}
			if !seen[f.Name] {
				seen[f.Name] = true
				out = append(out, f.Name)
			}
			walk(f.Type, depth+1)
		}
	}
	if model != nil {
		walk(reflect.TypeOf(model), 0)
		if t := reflect.TypeOf(model); t != nil {
			for i := 0; i < t.NumMethod(); i++ {
				m := t.Method(i)
				if !seen[m.Name] {
					seen[m.Name] = true
					out = append(out, m.Name)
				}
			}
		}
	}
	sort.Strings(out)
	return out
}

// nearest picks the closest available name, and only when it is close enough
// to be worth saying. A suggestion that is wrong is worse than none: it sends
// an author to change something that was already right.
func nearest(want string, have []string) string {
	best, bestDist := "", 1<<30
	for _, h := range have {
		d := distance(strings.ToLower(want), strings.ToLower(h))
		if d < bestDist {
			best, bestDist = h, d
		}
	}
	limit := len(want) / 3
	if limit < 1 {
		limit = 1
	}
	if bestDist > limit {
		return ""
	}
	return best
}

// distance counts a transposition as one edit rather than two.
//
// Plain Levenshtein would score "Nmae" against "Name" as 2 and fall outside
// the suggestion threshold — and swapping two adjacent letters is the single
// most common way a field name gets mistyped. Refusing to suggest on exactly
// that case would make the suggestion useless where it is needed most.
func distance(a, b string) int {
	d := make([][]int, len(a)+1)
	for i := range d {
		d[i] = make([]int, len(b)+1)
		d[i][0] = i
	}
	for j := 0; j <= len(b); j++ {
		d[0][j] = j
	}
	for i := 1; i <= len(a); i++ {
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			d[i][j] = min3(d[i][j-1]+1, d[i-1][j]+1, d[i-1][j-1]+cost)
			if i > 1 && j > 1 && a[i-1] == b[j-2] && a[i-2] == b[j-1] {
				if t := d[i-2][j-2] + 1; t < d[i][j] {
					d[i][j] = t
				}
			}
		}
	}
	return d[len(a)][len(b)]
}

func min3(a, b, c int) int {
	if b < a {
		a = b
	}
	if c < a {
		a = c
	}
	return a
}
