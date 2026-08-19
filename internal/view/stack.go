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

// LayerError is a failure that belongs to one layer.
//
// Typed rather than a formatted string, because Boot has to decide WHOSE
// plugin to drop and reading that back out of a message would be guessing at
// its own error format. A plugin whose templates will not parse is that
// plugin's problem, not the product's.
type LayerError struct {
	Layer string
	Err   error
}

func (e *LayerError) Error() string { return "plugin " + e.Layer + ": " + e.Err.Error() }
func (e *LayerError) Unwrap() error { return e.Err }

func layerErr(layer string, format string, a ...any) *LayerError {
	return &LayerError{Layer: layer, Err: fmt.Errorf(format, a...)}
}

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
	Entries []Entry
}

// LedgerAction is what defining a name amounted to.
//
// Named LedgerAction rather than Action because Action is the author-facing
// model type for a control, and that one is part of the published contract
// while this is internal bookkeeping. The contract keeps the short name.
type LedgerAction string

const (
	// Adds is a layer defining a name in its own namespace.
	Adds LedgerAction = "adds"
	// Overrides is a layer defining a name owned by somebody else. This is the
	// Jenkins property and it is allowed whether or not it was declared.
	Overrides LedgerAction = "overrides"
	// Extends is a layer contributing to a name that concatenates. No winner,
	// no shadowing, no coordination between the contributors.
	Extends LedgerAction = "extends"
	// Dangling is a layer defining a name in a namespace nothing installed
	// owns. The definition is kept — it simply never renders — and the ledger
	// says so, because a silently inert override is the hardest kind of
	// plugin bug to find.
	Dangling LedgerAction = "dangling"
)

// Entry is one line of the ledger.
type Entry struct {
	Layer  string
	Name   string
	Action LedgerAction
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
	coreRefs := map[string][]string{}      // core: name -> layers that reached for it
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
				return nil, layerErr(layer.ID, "defines %w", err)
			}

			// An override reaches the body beneath it through an alias, and the
			// alias has to be private to this layer or the second plugin
			// wrapping a name would reach its own wrapper and recurse forever.
			refs := rewriteAliases(tree.Root, i)
			for _, ref := range collectCoreRefs(tree.Root) {
				coreRefs[ref] = append(coreRefs[ref], layer.ID)
			}

			if n.Appends() {
				// An append segment replaces nothing, so there is nothing
				// beneath it. Refused rather than rendered empty: a reference
				// that can never resolve to anything is a mistake about how
				// this name works, and quietly producing nothing would leave
				// the author looking for a gap in their markup.
				if refs > 0 {
					return nil, layerErr(layer.ID, "uses under: inside %q, which appends rather "+
						"than replaces, so there is nothing beneath it. Contributions to this "+
						"name are concatenated in enable order — write your own body and drop "+
						"the under: reference", name)
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

			// under: for this layer is whatever is currently installed. There
			// being nothing installed is a refusal, not an empty render: the
			// author asked to wrap something, and wrapping nothing is not a
			// smaller version of that — it is a different thing they did not
			// ask for, and they would go looking for the missing content
			// rather than for the missing template.
			beneath := s.bodies[name]
			if beneath == nil {
				if refs > 0 {
					return nil, layerErr(layer.ID, "uses under: inside %q, but nothing defines "+
						"that name yet, so there is nothing to wrap. Define it, or drop the "+
						"under: reference and write the body directly", name)
				}
				beneath = emptyTree(name)
			}
			if err := s.own(underName(name, i), beneath, owner[name]); err != nil {
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
	// A core: reference to a name the host never defined is refused rather
	// than resolved to nothing. Reaching past every plugin to the product's
	// own body is a deliberate act, and doing it for a body that does not
	// exist is a mistake about what the host ships — one that would otherwise
	// render an empty region the author goes hunting for.
	if err := checkCoreRefs(coreRefs, coreBodies); err != nil {
		return nil, err
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
			return layerErr(l.ID, "reading %s: %w", p, err)
		}
		trees, err := parse.Parse(p, string(b), "{{", "}}", known)
		if err != nil {
			return layerErr(l.ID, "%s: %w", p, err)
		}
		for name, tree := range trees {
			// parse.Parse returns the file itself under its own path as well as
			// every {{define}} inside it. A file whose whole content is defines
			// has an empty body, and installing that under a path-shaped name
			// would put a name in the set that no naming rule allows.
			if name == p {
				if !isEffectivelyEmpty(tree.Root) {
					return layerErr(l.ID, "%s has markup outside a {{define}}; every template "+
						"must be inside one so it has a name to be overridden by", p)
				}
				continue
			}
			if prev, dup := out[name]; dup && prev != nil {
				return layerErr(l.ID, "defines %q twice in its own layer", name)
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

// collectCoreRefs lists the core: names a body reaches for, so a reference to
// something the product does not define can be refused by name.
func collectCoreRefs(n parse.Node) []string {
	var out []string
	var walk func(parse.Node)
	walk = func(n parse.Node) {
		switch v := n.(type) {
		case nil:
			return
		case *parse.ListNode:
			if v == nil {
				return
			}
			for _, c := range v.Nodes {
				walk(c)
			}
		case *parse.TemplateNode:
			if alias, name, ok := plugin.SplitAlias(v.Name); ok && alias == plugin.AliasCore {
				out = append(out, name)
			}
		case *parse.IfNode:
			walk(v.List)
			walk(v.ElseList)
		case *parse.RangeNode:
			walk(v.List)
			walk(v.ElseList)
		case *parse.WithNode:
			walk(v.List)
			walk(v.ElseList)
		}
	}
	walk(n)
	return out
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

// checkCoreRefs refuses a core: reference to a name the host never defined.
func checkCoreRefs(refs map[string][]string, coreBodies map[string]*parse.Tree) error {
	for _, name := range sortedKeys(refs) {
		if _, ok := coreBodies[name]; ok {
			continue
		}
		return layerErr(refs[name][0], "uses core:%s, but the product does not define that "+
			"name, so there is no original body to reach. Check the name, or use under: to "+
			"wrap whatever is beneath you", name)
	}
	return nil
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
