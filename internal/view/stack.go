// Package view composes one template set out of ordered layers.
//
// This is the mechanism the whole plugin system rests on. Go's html/template
// resolves a name at execute time against whatever is currently defined under
// it, so a later layer defining the same name replaces an earlier one without
// the earlier one having anticipated it. That is late binding by name, and it
// is the difference between a plugin filling a hole somebody remembered to
// leave and a plugin replacing a screen nobody designated as extensible.
//
// Three things make it usable rather than merely possible:
//
//   - What a layer overrides is computed from the templates it ships, never
//     from what it claimed. A manifest can lie; parsed bytes cannot.
//   - An override can call the body it replaced, so adding a header does not
//     mean reproducing everything between. Two plugins wrapping the same name
//     both survive, and neither had to know the other existed.
//   - Addition and replacement are different operations. Names that append
//     concatenate every layer's body instead of picking a winner, because two
//     plugins each adding a rail entry must not erase each other.
package view

import (
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"path"
	"sort"
	"strings"
	"text/template/parse"

	"github.com/orkcom-tech/cogitorium/internal/plugin"
)

// Layer is one source of templates. Layer zero is always the host's own; every
// layer after it is a plugin, in the operator's enable order.
type Layer struct {
	// ID is the namespace this layer owns. The host's is "cog".
	ID string
	// FS holds the layer's templates. For the host this is the embedded set;
	// for a plugin it is its bundle rooted at the templates directory.
	FS fs.FS
}

// Set is a composed template set plus the record of how it got that way.
type Set struct {
	tmpl   *template.Template
	funcs  template.FuncMap
	ledger Ledger
	// bodies is the current body per name as composition proceeded. Kept
	// rather than read back out of html/template so this package never depends
	// on that package's internals.
	bodies map[string]*parse.Tree
	// definedBy records whose body sits under each installed name, including
	// the private alias names. It is what lets a failure be blamed on the
	// layer that actually wrote the broken markup rather than on whoever
	// happens to define the outermost name — a wrapper reached through an
	// alias is innocent of what it wrapped.
	definedBy map[string]string
}

// Ledger is what each layer actually did, computed from parsed bytes.
//
// It is the truth the approval screen shows and the manifest's `overrides:`
// is compared against — in that direction, never the reverse. A discrepancy
// is reported; it is not an error, because declaration is advisory by design.
type Ledger struct {
	Entries  []Entry
	Warnings []Warning
}

// Warning is something inert rather than broken: it renders, it just does not
// do what its author probably meant. Broken fails the plugin; inert warns,
// because taking a whole plugin away over a no-op would be a worse trade than
// the confusion it prevents.
type Warning struct {
	Layer   string
	Name    string
	Message string
}

// Action is what defining a name amounted to.
type Action string

const (
	// Adds is a layer defining a name in its own namespace.
	Adds Action = "adds"
	// Overrides is a layer defining a name owned by somebody else. This is the
	// Jenkins property and it is allowed whether or not it was declared.
	Overrides Action = "overrides"
	// Extends is a layer contributing to a name that concatenates. No winner,
	// no shadowing, no coordination between the contributors.
	Extends Action = "extends"
	// Dangling is a layer defining a name in a namespace nothing installed
	// owns. The definition is kept — it simply never renders — and the ledger
	// says so, because a silently inert override is the hardest kind of
	// plugin bug to find.
	Dangling Action = "dangling"
)

// Entry is one line of the ledger.
type Entry struct {
	Layer  string
	Name   string
	Action Action
	// Took names the layer whose body this one replaced. Empty unless the
	// action is Overrides and something was actually there.
	Took string
}

// Overridden lists the names a layer took over from somebody else.
func (l Ledger) Overridden(layer string) []string {
	var out []string
	for _, e := range l.Entries {
		if e.Layer == layer && e.Action == Overrides {
			out = append(out, e.Name)
		}
	}
	sort.Strings(out)
	return out
}

// For returns every entry belonging to one layer.
func (l Ledger) For(layer string) []Entry {
	var out []Entry
	for _, e := range l.Entries {
		if e.Layer == layer {
			out = append(out, e)
		}
	}
	return out
}

// Compose builds the set. Layers are applied in order; layer zero is the host.
//
// The whole set is parsed at once, at boot. Restart-to-activate is the accepted
// model, so there is no atomic swap here and no machinery to keep a live set
// consistent while it changes — which removes the single most intricate part of
// a design that tried to avoid a one-second restart.
func Compose(funcs template.FuncMap, layers ...Layer) (*Set, error) {
	if len(layers) == 0 {
		return nil, fmt.Errorf("view: composing needs at least the host's own layer")
	}

	root := template.New("").Funcs(funcs).Option("missingkey=error")
	s := &Set{tmpl: root, funcs: funcs, bodies: map[string]*parse.Tree{}, definedBy: map[string]string{}}

	owner := map[string]string{}           // name -> layer id currently rendering it
	coreBodies := map[string]*parse.Tree{} // layer zero's bodies, for the core: alias
	appendParts := map[string][]string{}   // name -> synthesized segment names, in order
	installed := map[string]bool{}
	for _, l := range layers {
		installed[l.ID] = true
	}

	for i, layer := range layers {
		trees, err := parseLayer(layer, funcs)
		if err != nil {
			return nil, err
		}

		for _, name := range sortedKeys(trees) {
			tree := trees[name]

			n, err := plugin.ParseName(name)
			if err != nil {
				return nil, fmt.Errorf("view: %s defines %w", layer.ID, err)
			}

			// An override reaches the body beneath it through an alias, and the
			// alias has to be private to this layer or the second plugin
			// wrapping a name would reach its own wrapper and recurse forever.
			refs := rewriteAliases(tree.Root, i)

			if n.Appends() {
				// An append segment replaces nothing, so there is nothing
				// beneath it. The alias is still installed, empty, because a
				// reference that fails to resolve would take down a plugin over
				// a misunderstanding — and the misunderstanding is said out loud
				// instead.
				if err := s.own(underName(name, i), emptyTree(name), ""); err != nil {
					return nil, err
				}
				if refs > 0 {
					s.ledger.Warnings = append(s.ledger.Warnings, Warning{
						Layer: layer.ID, Name: name,
						Message: "uses under: on a name that appends rather than replaces, " +
							"so there is nothing beneath it and the reference renders nothing. " +
							"Contributions to this name are concatenated in enable order — " +
							"just write your own body.",
					})
				}
				seg := fmt.Sprintf("%s\x00seg\x00%d", name, i)
				if err := s.own(seg, tree, layer.ID); err != nil {
					return nil, err
				}
				appendParts[name] = append(appendParts[name], seg)
				s.ledger.Entries = append(s.ledger.Entries, Entry{
					Layer: layer.ID, Name: name, Action: Extends,
				})
				continue
			}

			// under: for this layer is whatever is currently installed. Empty
			// when nothing is, so an override that wraps a name the host never
			// defined renders its own body and nothing else rather than failing.
			if err := s.own(underName(name, i), orEmpty(s.bodies[name], name), owner[name]); err != nil {
				return nil, err
			}

			if i == 0 {
				coreBodies[name] = tree
			}

			action := Adds
			took := ""
			switch {
			case n.OwnedBy(layer.ID):
				action = Adds
			case !installed[n.Namespace]:
				action = Dangling
			default:
				action = Overrides
				took = owner[name]
			}
			s.ledger.Entries = append(s.ledger.Entries, Entry{
				Layer: layer.ID, Name: name, Action: action, Took: took,
			})

			if err := s.own(name, tree, layer.ID); err != nil {
				return nil, err
			}
			owner[name] = layer.ID
		}
	}

	// core: reaches past every plugin to the host's own body. Installed after
	// the loop so it is never confused with a layer's own definition.
	for name, tree := range coreBodies {
		if err := s.own(plugin.AliasCore+name, tree, plugin.CoreNamespace); err != nil {
			return nil, err
		}
	}
	// A core: reference to a name the host never defined must still resolve,
	// or an override that reaches for it fails at render rather than rendering
	// nothing — which is the wrong failure for something that is legitimately
	// absent.
	for _, name := range sortedKeys(s.bodies) {
		if _, ok := coreBodies[name]; !ok && !strings.Contains(name, "\x00") {
			if _, exists := s.bodies[plugin.AliasCore+name]; !exists {
				if err := s.add(plugin.AliasCore+name, emptyTree(plugin.AliasCore+name)); err != nil {
					return nil, err
				}
			}
		}
	}

	if err := s.installAppendSlots(appendParts); err != nil {
		return nil, err
	}
	return s, nil
}

// installAppendSlots makes the canonical name render every contribution in
// enable order. This is the mechanism that lets strangers add to the same
// place without coordinating.
func (s *Set) installAppendSlots(parts map[string][]string) error {
	for _, name := range sortedKeys(parts) {
		var b strings.Builder
		fmt.Fprintf(&b, `{{define %q}}`, name)
		for _, seg := range parts[name] {
			fmt.Fprintf(&b, `{{template %q .}}`, seg)
		}
		b.WriteString(`{{end}}`)

		trees, err := parse.Parse(name, b.String(), "{{", "}}", parseFuncs(s.funcs))
		if err != nil {
			return fmt.Errorf("view: assembling append slot %s: %w", name, err)
		}
		if err := s.add(name, trees[name]); err != nil {
			return err
		}
	}
	return nil
}

func (s *Set) add(name string, tree *parse.Tree) error {
	if _, err := s.tmpl.AddParseTree(name, tree); err != nil {
		return fmt.Errorf("view: installing %s: %w", name, err)
	}
	s.bodies[name] = tree
	return nil
}

// own installs a body and records which layer wrote it.
func (s *Set) own(name string, tree *parse.Tree, layer string) error {
	if err := s.add(name, tree); err != nil {
		return err
	}
	s.definedBy[name] = layer
	return nil
}

// DefinedBy reports which layer wrote the body currently installed under a
// name, including the private alias names a failure can surface from.
func (s *Set) DefinedBy(name string) (string, bool) {
	l, ok := s.definedBy[name]
	return l, ok
}

// Ledger reports what each layer did.
func (s *Set) Ledger() Ledger { return s.ledger }

// Names lists every publicly addressable template — the ones an author could
// override. The private alias and segment names are not included: they are
// mechanism, not contract.
func (s *Set) Names() []string {
	var out []string
	for name := range s.bodies {
		if strings.Contains(name, "\x00") {
			continue
		}
		if _, _, isAlias := plugin.SplitAlias(name); isAlias {
			continue
		}
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// Has reports whether a name is defined.
func (s *Set) Has(name string) bool {
	_, ok := s.bodies[name]
	return ok
}

// Execute renders one named template.
func (s *Set) Execute(w io.Writer, name string, data any) error {
	t := s.tmpl.Lookup(name)
	if t == nil {
		return fmt.Errorf("view: no template named %q", name)
	}
	return t.Execute(w, data)
}

// ── parsing a layer ───────────────────────────────────────────────────────

// parseLayer reads every .html file in a layer into its own scratch parse, so
// one layer's definitions can be examined before they are merged. Parsing
// straight into the accumulating set would overwrite the evidence of what was
// there before, which is the thing the ledger is made of.
func parseLayer(l Layer, funcs template.FuncMap) (map[string]*parse.Tree, error) {
	out := map[string]*parse.Tree{}
	known := parseFuncs(funcs)

	err := fs.WalkDir(l.FS, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || path.Ext(p) != ".html" {
			return nil
		}
		b, err := fs.ReadFile(l.FS, p)
		if err != nil {
			return fmt.Errorf("view: %s: reading %s: %w", l.ID, p, err)
		}
		trees, err := parse.Parse(p, string(b), "{{", "}}", known)
		if err != nil {
			return fmt.Errorf("view: %s: %s: %w", l.ID, p, err)
		}
		for name, tree := range trees {
			// parse.Parse returns the file itself under its own path as well as
			// every {{define}} inside it. A file whose whole content is defines
			// has an empty body, and installing that under a path-shaped name
			// would put a name in the set that no naming rule allows.
			if name == p {
				if !isEffectivelyEmpty(tree.Root) {
					return fmt.Errorf("view: %s: %s has markup outside a {{define}}; "+
						"every template must be inside one so it has a name to be "+
						"overridden by", l.ID, p)
				}
				continue
			}
			if prev, dup := out[name]; dup && prev != nil {
				return fmt.Errorf("view: %s defines %q twice in its own layer", l.ID, name)
			}
			out[name] = tree
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// parseFuncs gives the parser the set of function names that exist, so a
// misspelled function is a parse error naming it and the plugin's own file,
// rather than a render-time surprise in front of a user.
//
// The parser only needs the names to be present; it never calls them.
func parseFuncs(funcs template.FuncMap) map[string]any {
	m := make(map[string]any, len(funcs))
	for name, fn := range funcs {
		m[name] = fn
	}
	return m
}

// ── alias rewriting ───────────────────────────────────────────────────────

// underName is the private name a layer's under: alias resolves to. The layer
// index is in it because two plugins wrapping the same template each need
// their own view of what was beneath them — without that, the second would
// reach its own wrapper and recurse until the stack ran out.
func underName(name string, layer int) string {
	return fmt.Sprintf("%s\x00under\x00%d\x00%s", plugin.AliasUnder, layer, name)
}

// rewriteAliases walks a parsed body and points every under: reference at this
// layer's private alias.
//
// Done on the tree rather than by rewriting source text, because the same
// reference can be written {{template "under:x"}} or {{ template "under:x" . }}
// and a text substitution would also hit the string inside a paragraph of prose
// that happened to mention it.
// It returns how many under: references it rewrote, so the caller can say
// something when one was used where it can never resolve to anything.
func rewriteAliases(n parse.Node, layer int) int {
	switch v := n.(type) {
	case nil:
		return 0
	case *parse.ListNode:
		if v == nil {
			return 0
		}
		var count int
		for _, c := range v.Nodes {
			count += rewriteAliases(c, layer)
		}
		return count
	case *parse.TemplateNode:
		if alias, name, ok := plugin.SplitAlias(v.Name); ok && alias == plugin.AliasUnder {
			v.Name = underName(name, layer)
			return 1
		}
	case *parse.IfNode:
		return rewriteBranch(&v.BranchNode, layer)
	case *parse.RangeNode:
		return rewriteBranch(&v.BranchNode, layer)
	case *parse.WithNode:
		return rewriteBranch(&v.BranchNode, layer)
	}
	return 0
}

func rewriteBranch(b *parse.BranchNode, layer int) int {
	return rewriteAliases(b.List, layer) + rewriteAliases(b.ElseList, layer)
}

// ── small helpers ─────────────────────────────────────────────────────────

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func orEmpty(t *parse.Tree, name string) *parse.Tree {
	if t != nil {
		return t
	}
	return emptyTree(name)
}

func emptyTree(name string) *parse.Tree {
	trees, err := parse.Parse(name, fmt.Sprintf(`{{define %q}}{{end}}`, name), "{{", "}}")
	if err != nil {
		panic("view: the empty template does not parse: " + err.Error())
	}
	return trees[name]
}

func isEffectivelyEmpty(n *parse.ListNode) bool {
	if n == nil {
		return true
	}
	for _, c := range n.Nodes {
		if t, ok := c.(*parse.TextNode); ok {
			if strings.TrimSpace(string(t.Text)) == "" {
				continue
			}
		}
		return false
	}
	return true
}
