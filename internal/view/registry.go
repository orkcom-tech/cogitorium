package view

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"reflect"
	"sort"
	"strings"

	"github.com/orkcom-tech/cogitorium/internal/plugin"
)

// What an author needs to know before writing an override.
//
// Three questions, and until now none of them had an answer that did not
// involve reading this repository: what names exist, what model does each one
// render against, and what does the host's own body look like today. An author
// guessing at any of the three writes a template that installs, validates and
// renders something nobody intended — which is the most expensive way to learn
// how this works.
//
// Generated from the compiled-in templates and models rather than maintained
// beside them, so it cannot describe a name that does not exist or miss one
// that does.

// Named describes one addressable template.
type Named struct {
	Name string `json:"name"`
	// Model is the shape the template renders against, flattened to dotted
	// paths: "Ctx.Viewer.Name". Flattened rather than nested because what an
	// author writes is `{{.Ctx.Viewer.Name}}`, and a nested document makes
	// them assemble the path themselves from three levels of indentation.
	Model []Field `json:"model"`
	// Body is the host's current definition. Included because "add to what is
	// there" is the common case and `{{template "under:name" .}}` only helps
	// once you know what under: contains.
	Body string `json:"body"`
	// Appends marks a slot whose definitions concatenate rather than replace,
	// which changes what writing one means.
	Appends bool `json:"appends"`
	// Dormant marks a name that is registered and validated but that nothing
	// calls yet, so overriding it changes nothing on screen.
	Dormant bool `json:"dormant,omitempty"`
}

// Field is one path into a model.
type Field struct {
	Path string `json:"path"`
	Type string `json:"type"`
}

// Registry is the whole vocabulary, for a file an author can read offline.
type Registry struct {
	// Contract is the ABI generation these names belong to. A registry from a
	// different one describes a different product.
	Contract int     `json:"contract"`
	Entries  []Named `json:"entries"`
}

// BuildRegistry describes every name the host defines.
func BuildRegistry(core fs.FS, models Models) (Registry, error) {
	bodies, err := coreBodies(core)
	if err != nil {
		return Registry{}, err
	}

	reg := Registry{Contract: plugin.Contract}
	for name, model := range models {
		reg.Entries = append(reg.Entries, Named{
			Name:    name,
			Model:   flatten(reflect.TypeOf(model), "", 0),
			Body:    strings.TrimSpace(bodies[name]),
			Appends: appends(name),
			Dormant: plugin.Dormant(name),
		})
	}
	sort.Slice(reg.Entries, func(i, j int) bool { return reg.Entries[i].Name < reg.Entries[j].Name })
	return reg, nil
}

// RegistryJSON is the registry as it is published: indented, stable order,
// newline-terminated, so a diff is about content rather than formatting.
func RegistryJSON(core fs.FS, models Models) ([]byte, error) {
	reg, err := BuildRegistry(core, models)
	if err != nil {
		return nil, err
	}
	b, err := json.MarshalIndent(reg, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// flatten turns a model type into the dotted paths a template writes.
//
// Depth-bounded rather than cycle-detected: a model is a small struct written
// in this repository, and a bound is one line where a visited-set is a
// structure to get wrong. The bound is deep enough for every model that exists
// and shallow enough that a self-referential one stops.
func flatten(t reflect.Type, prefix string, depth int) []Field {
	if t == nil || depth > maxModelDepth {
		return nil
	}
	for t.Kind() == reflect.Ptr {
		t = t.Elem()
	}

	switch t.Kind() {
	case reflect.Struct:
		var out []Field
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			if !f.IsExported() {
				continue
			}
			path := f.Name
			if prefix != "" {
				path = prefix + "." + f.Name
			}
			ft := f.Type
			for ft.Kind() == reflect.Ptr {
				ft = ft.Elem()
			}
			if ft.Kind() == reflect.Struct && ft.String() != "time.Time" {
				out = append(out, flatten(ft, path, depth+1)...)
				continue
			}
			out = append(out, Field{Path: path, Type: typeName(ft)})
		}
		return out
	case reflect.Slice, reflect.Array:
		// A range target. Named as one, because "[]NavItem" tells an author to
		// write {{range .}} and "NavItem" does not.
		inner := flatten(t.Elem(), prefix, depth+1)
		if len(inner) == 0 {
			return []Field{{Path: prefix, Type: typeName(t)}}
		}
		return inner
	default:
		if prefix == "" {
			// The whole model is a scalar — cog.empty.default renders against
			// a string. Said explicitly rather than returning nothing, which
			// would read as "this template has no model".
			return []Field{{Path: ".", Type: typeName(t)}}
		}
		return []Field{{Path: prefix, Type: typeName(t)}}
	}
}

const maxModelDepth = 6

// appends reports whether definitions of this name concatenate rather than
// replace, which changes what writing one means.
func appends(name string) bool {
	n, err := plugin.ParseName(name)
	if err != nil {
		return false
	}
	return n.Appends()
}

func typeName(t reflect.Type) string {
	if t == nil {
		return "?"
	}
	switch t.Kind() {
	case reflect.Slice, reflect.Array:
		return "[]" + typeName(t.Elem())
	case reflect.Map:
		return fmt.Sprintf("map[%s]%s", typeName(t.Key()), typeName(t.Elem()))
	}
	if n := t.Name(); n != "" {
		return n
	}
	return t.Kind().String()
}

// coreBodies reads each {{define}} out of the host's own templates.
//
// Text rather than the parsed tree: what an author wants to see is what they
// would have to reproduce, and a parse tree printed back is not what anybody
// wrote.
func coreBodies(core fs.FS) (map[string]string, error) {
	out := map[string]string{}
	err := fs.WalkDir(core, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".html") {
			return err
		}
		b, err := fs.ReadFile(core, p)
		if err != nil {
			return err
		}
		for name, body := range definitions(string(b)) {
			out[name] = body
		}
		return nil
	})
	return out, err
}

// definitions pulls {{define "x"}}…{{end}} pairs out of a template file.
//
// Depth-counted rather than regex-matched on the first {{end}}: a body
// containing {{if}} or {{range}} has ends of its own, and taking the first one
// would truncate every interesting template at its first conditional.
func definitions(src string) map[string]string {
	out := map[string]string{}
	const open = `{{define "`
	for i := 0; ; {
		start := strings.Index(src[i:], open)
		if start < 0 {
			return out
		}
		start += i
		nameStart := start + len(open)
		nameEnd := strings.Index(src[nameStart:], `"`)
		if nameEnd < 0 {
			return out
		}
		name := src[nameStart : nameStart+nameEnd]

		bodyStart := strings.Index(src[nameStart:], "}}")
		if bodyStart < 0 {
			return out
		}
		bodyStart += nameStart + 2

		depth := 1
		j := bodyStart
		for depth > 0 {
			next := strings.Index(src[j:], "{{")
			if next < 0 {
				return out
			}
			j += next
			action := src[j:]
			switch {
			case strings.HasPrefix(action, "{{end}}"):
				depth--
				if depth == 0 {
					out[name] = src[bodyStart:j]
					i = j + len("{{end}}")
				} else {
					j += len("{{end}}")
				}
			case strings.HasPrefix(action, "{{if"), strings.HasPrefix(action, "{{range"),
				strings.HasPrefix(action, "{{with"), strings.HasPrefix(action, "{{block"),
				strings.HasPrefix(action, "{{define"):
				depth++
				j += 2
			default:
				j += 2
			}
		}
	}
}
