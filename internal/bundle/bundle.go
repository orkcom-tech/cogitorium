// Package bundle is the portable form of a workspace: one JSON document that
// describes agents, wiring, gears and context well enough to rebuild the same
// setup on another install — and deliberately not well enough to carry
// anything private with it.
//
// The format has one home here, rather than in the handlers, because every
// rule that makes a bundle safe to accept from a stranger is a property of
// the document and not of the route it arrived on: references are by name
// because database ids mean nothing elsewhere, models are resolved against
// the local catalog, imported gears are never approved, and context paths are
// proved to stay inside the workspace they are creating.
package bundle

import (
	"errors"
	"fmt"
	"path"
	"regexp"
	"strings"

	"github.com/orkcom-tech/cogitorium/internal/catalog"
	"github.com/orkcom-tech/cogitorium/internal/contextstore"
	"github.com/orkcom-tech/cogitorium/internal/gear"
	"github.com/orkcom-tech/cogitorium/internal/workspace"
)

// Format identifies the document and its version. An import checks it before
// reading anything else: a document that is not this is not a bundle, and
// guessing at one would be reading a stranger's file as if it were ours.
const Format = "cogitorium.workspace/v1"

// ErrMalformed marks every refusal that is the bundle's fault rather than the
// server's, so the API can answer 400 and the operator knows to fix the
// document instead of retrying.
var ErrMalformed = errors.New("this bundle cannot be imported")

// BoundToWorkspace is the gear binding that reaches every agent in the
// workspace; any other value names one agent.
const BoundToWorkspace = "workspace"

// Encodings as written in a bundle. The gear store spells UTF-8 without the
// hyphen; the document format uses the spelling people expect to read, and
// import accepts both so a bundle written by hand still loads.
const (
	EncodingUTF8   = "utf-8"
	EncodingBase64 = "base64"
)

// Bundle is the whole document.
//
// What is absent is as deliberate as what is present: there is no provider
// key, no user, no owner, no team, no timeline and no gear approval anywhere
// in these types. A bundle is a template someone hands to someone else, so
// the way to guarantee it never leaks a secret or a conversation is that it
// has nowhere to put one.
type Bundle struct {
	Format     string        `json:"format"`
	ExportedAt string        `json:"exported_at"`
	Workspace  Workspace     `json:"workspace"`
	Agents     []Agent       `json:"agents"`
	Wires      []Wire        `json:"wires"`
	Gears      []Gear        `json:"gears"`
	Context    []ContextFile `json:"context"`
}

type Workspace struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

type Agent struct {
	Name           string   `json:"name"`
	Role           string   `json:"role"`
	Avoid          string   `json:"avoid"`
	IsOrchestrator bool     `json:"is_orchestrator"`
	PosX           *float64 `json:"pos_x"`
	PosY           *float64 `json:"pos_y"`
	// Model is nil when the agent has none bound. Otherwise it names the
	// model the way another install can understand it — never by id.
	Model *ModelRef `json:"model"`
}

// ModelRef is how a bundle points at a model: by what it is, not by which row
// it happened to be. The pair is resolved against the importing install's own
// catalog, and an install that has no such model still gets the agent.
type ModelRef struct {
	ProviderType string `json:"provider_type"`
	ModelName    string `json:"model_name"`
}

// Wire references its endpoints by agent name for the same reason: the ids on
// the exporting install belong to that install's rows and to nothing else.
type Wire struct {
	From  string `json:"from"`
	To    string `json:"to"`
	Label string `json:"label"`
}

type Gear struct {
	Name           string     `json:"name"`
	Description    string     `json:"description"`
	Tags           []string   `json:"tags"`
	Runtime        string     `json:"runtime"`
	Entrypoint     string     `json:"entrypoint"`
	ArgsSchema     string     `json:"args_schema"`
	TimeoutSeconds int        `json:"timeout_seconds"`
	Files          []GearFile `json:"files"`
	// BoundTo is "workspace" or the name of an agent in this bundle.
	BoundTo string `json:"bound_to"`
}

type GearFile struct {
	Path    string `json:"path"`
	Content string `json:"content"`
	// Encoding is "utf-8" for source and "base64" for uploaded binaries.
	Encoding string `json:"encoding"`
	// Executable says which file will be run directly rather than handed to
	// an interpreter. It is derived from the runtime and the entrypoint, not
	// stored, so it is here to be read and not to be obeyed: an import
	// decides executability the same way locally.
	Executable bool `json:"executable"`
}

// ContextFile is one document, at a path relative to the workspace branch it
// came from. Relative is the whole point — on import the path is re-rooted
// under the new workspace's own branch, so a bundle can never name where its
// documents land.
type ContextFile struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// Stores is the set of stores the format works through. Export and Import
// take it as one value so a caller cannot pass the gear store where the
// catalog belongs.
type Stores struct {
	Workspaces *workspace.Store
	Catalog    *catalog.Store
	Gears      *gear.Store
	Context    *contextstore.Store
}

// Options says which optional parts of a workspace travel. Agents and wires
// always do — they are the workspace. Gear source and context documents are
// bulk, and both are somebody's work rather than only a shape, so they are
// asked for explicitly.
type Options struct {
	Gears   bool
	Context bool
}

// filenameUnsafe matches everything a download filename must not carry. A
// workspace name is operator-supplied text: it must not be able to close the
// quoted filename in the Content-Disposition header, name a directory, or
// carry a parent reference. Keeping only letters and digits — the same shape
// branch slugs use — is what makes all three impossible at once rather than
// one exclusion at a time.
var filenameUnsafe = regexp.MustCompile(`[^A-Za-z0-9]+`)

// Filename is what a downloaded bundle is called.
func Filename(workspaceName string) string {
	name := strings.Trim(filenameUnsafe.ReplaceAllString(workspaceName, "-"), "-")
	if name == "" {
		name = "workspace"
	}
	if len(name) > 60 {
		name = strings.Trim(name[:60], "-")
	}
	return name + ".cogitorium.json"
}

// Validate reads the whole document before anything is created from it. It
// runs first, and in full, because a bundle comes from outside: a refusal
// halfway through an import would leave a half-built workspace behind, and
// the operator would then have to work out which half.
func (b Bundle) Validate() error {
	if b.Format != Format {
		return fmt.Errorf("%w: it says format %q, and this build reads %q — export it again from the install that made it",
			ErrMalformed, b.Format, Format)
	}
	// Judge the same strings the import will store. This is exported, so it is
	// called on documents nobody has touched yet, and an agent named "   " has
	// to be "has no name" here rather than a confusing wire error further down.
	b = b.Normalized()

	names := map[string]bool{}
	orchestrators := 0
	for i, a := range b.Agents {
		name := a.Name
		if name == "" {
			return fmt.Errorf("%w: agent %d has no name, and everything else in a bundle points at agents by name", ErrMalformed, i+1)
		}
		if names[name] {
			return fmt.Errorf("%w: two agents are called %q, so a wire naming it would be ambiguous", ErrMalformed, name)
		}
		names[name] = true
		if a.IsOrchestrator {
			orchestrators++
		}
		if a.Model != nil {
			if strings.TrimSpace(a.Model.ProviderType) == "" || strings.TrimSpace(a.Model.ModelName) == "" {
				return fmt.Errorf("%w: agent %q names a model without both provider_type and model_name", ErrMalformed, name)
			}
		}
	}
	if orchestrators > 1 {
		return fmt.Errorf("%w: it names %d orchestrators, and a workspace has exactly one", ErrMalformed, orchestrators)
	}

	for _, w := range b.Wires {
		if !names[w.From] {
			return fmt.Errorf("%w: a wire starts at %q, which is not an agent in this bundle", ErrMalformed, w.From)
		}
		if !names[w.To] {
			return fmt.Errorf("%w: a wire ends at %q, which is not an agent in this bundle", ErrMalformed, w.To)
		}
		if w.From == w.To {
			return fmt.Errorf("%w: %q is wired to itself", ErrMalformed, w.From)
		}
	}

	gearNames := map[string]bool{}
	for i, g := range b.Gears {
		name := strings.TrimSpace(g.Name)
		if name == "" {
			return fmt.Errorf("%w: gear %d has no name", ErrMalformed, i+1)
		}
		if gearNames[name] {
			return fmt.Errorf("%w: it carries the gear %q twice", ErrMalformed, name)
		}
		gearNames[name] = true
		if len(g.Files) == 0 {
			return fmt.Errorf("%w: gear %q carries no files, so there is nothing to run", ErrMalformed, name)
		}
		for _, f := range g.Files {
			if _, err := storeEncoding(f.Encoding); err != nil {
				return fmt.Errorf("%w: gear %q file %q: %s", ErrMalformed, name, f.Path, err)
			}
		}
		if g.BoundTo != BoundToWorkspace && !names[g.BoundTo] {
			return fmt.Errorf("%w: gear %q says bound_to %q — use %q or the name of an agent in this bundle",
				ErrMalformed, name, g.BoundTo, BoundToWorkspace)
		}
		// 0 means the bundle does not say, and the local default stands.
		if g.TimeoutSeconds < 0 || g.TimeoutSeconds > 3600 {
			return fmt.Errorf("%w: gear %q asks for a %d second timeout; the limit is 3600", ErrMalformed, name, g.TimeoutSeconds)
		}
	}

	for _, f := range b.Context {
		if err := checkContextPath(f.Path); err != nil {
			return fmt.Errorf("%w: %s", ErrMalformed, err)
		}
	}
	return nil
}

// Normalized returns a copy of the bundle with every name trimmed.
//
// This exists because the names in a bundle are compared in more than one
// place, by code that did not agree on what a name was. `Forge` trims before
// it looks a gear up; the guard that decides whether a gear already exists
// here did not — so a bundle naming a gear "wordcount " with a trailing space
// slipped past the guard, reached Forge, and superseded the real "wordcount":
// version raised, source replaced with the bundle's, status dropped to
// pending. Every workspace bound to that approved gear lost it at once, the
// attacker's source sat under a name an admin recognises, and the report said
// nothing was skipped. The same split let a padded AGENT name pass validation
// and then fail mid-import, leaving a half-built workspace behind.
//
// So normalization happens once, at the doorway, and everything downstream
// compares the same strings. Trimming rather than rejecting is deliberate: a
// stray space is what a hand-edited JSON file has, not an attack in itself —
// the attack was the disagreement about it.
func (b Bundle) Normalized() Bundle {
	out := b
	out.Workspace.Name = strings.TrimSpace(b.Workspace.Name)

	out.Agents = make([]Agent, len(b.Agents))
	for i, a := range b.Agents {
		a.Name = strings.TrimSpace(a.Name)
		if a.Model != nil {
			m := *a.Model
			m.ProviderType = strings.TrimSpace(m.ProviderType)
			m.ModelName = strings.TrimSpace(m.ModelName)
			a.Model = &m
		}
		out.Agents[i] = a
	}

	out.Wires = make([]Wire, len(b.Wires))
	for i, w := range b.Wires {
		w.From, w.To = strings.TrimSpace(w.From), strings.TrimSpace(w.To)
		out.Wires[i] = w
	}

	out.Gears = make([]Gear, len(b.Gears))
	for i, g := range b.Gears {
		g.Name = strings.TrimSpace(g.Name)
		g.BoundTo = strings.TrimSpace(g.BoundTo)
		out.Gears[i] = g
	}

	out.Context = make([]ContextFile, len(b.Context))
	for i, f := range b.Context {
		f.Path = strings.TrimSpace(f.Path)
		out.Context[i] = f
	}
	return out
}

// checkContextPath is the one rule in this file that is a security boundary
// rather than a data-shape check. Everything else in a bundle can only make a
// bad workspace; a context path is the single field that could reach outside
// the workspace being created — over another workspace's memory, or over the
// shared instruction library — so a path is required to be relative, is
// refused for any parent reference, and is refused by name rather than
// repaired into something acceptable.
func checkContextPath(p string) error {
	if strings.TrimSpace(p) == "" {
		return errors.New("a context entry has an empty path")
	}
	if strings.HasPrefix(p, "/") {
		return fmt.Errorf("context path %q is absolute; paths in a bundle are relative to the workspace branch", p)
	}
	if strings.Contains(p, `\`) {
		return fmt.Errorf(`context path %q contains a backslash; use / between path segments`, p)
	}
	// Rejecting the whole ".." sequence, not only a "descend one level"
	// segment, is what the context store itself does — accepting a path here
	// that it will refuse on write would fail the import halfway through.
	if strings.Contains(p, "..") {
		return fmt.Errorf("context path %q contains \"..\"; a bundle cannot write outside the workspace it creates", p)
	}
	if strings.HasPrefix(p, "-") {
		return fmt.Errorf("context path %q starts with a dash, which the context store reads as a command flag", p)
	}
	if strings.ContainsRune(p, 0) {
		return fmt.Errorf("context path %q contains a null byte", p)
	}
	// A path that collapses to nothing — "." , "./", "a/.." is already gone
	// above — names no file. This check has to be HERE rather than only in
	// ContextPath: validation runs before the workspace is created and
	// ContextPath runs after, so a "." reached the write, was refused there,
	// and left a workspace behind for a document that was never accepted.
	// Refusing halfway is worse than refusing, because it leaves the operator
	// to work out which half happened.
	if cleaned := path.Clean(p); cleaned == "." || cleaned == "/" {
		return fmt.Errorf("context path %q names no file", p)
	}
	return nil
}

// ContextPath places one bundle path under a workspace's own branch and
// proves it stayed there. Cleaning happens after the checks above, not
// instead of them, so a path that only looks safe once collapsed is still
// refused: this is the last gate before a write, and it is deliberately
// paranoid.
func ContextPath(branch, rel string) (string, error) {
	if branch == "" {
		return "", errors.New("this workspace has no context branch to write into")
	}
	if err := checkContextPath(rel); err != nil {
		return "", err
	}
	cleaned := path.Clean(rel)
	if cleaned == "." || cleaned == "/" {
		return "", fmt.Errorf("context path %q names no file", rel)
	}
	full := path.Join(branch, cleaned)
	if !strings.HasPrefix(full, branch+"/") {
		return "", fmt.Errorf("context path %q escapes the workspace branch", rel)
	}
	return full, nil
}

// storeEncoding maps a bundle's encoding onto the gear store's own spelling,
// and refuses anything else: a file whose encoding nobody recognises would be
// written to disk as its own base64 text and fail at run time, far from here.
func storeEncoding(enc string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(enc)) {
	case "", EncodingUTF8, gear.EncodingUTF8:
		return gear.EncodingUTF8, nil
	case EncodingBase64:
		return gear.EncodingBase64, nil
	default:
		return "", fmt.Errorf("encoding %q is not one of %q or %q", enc, EncodingUTF8, EncodingBase64)
	}
}

// bundleEncoding is the reverse, for export.
func bundleEncoding(enc string) string {
	if enc == gear.EncodingBase64 {
		return EncodingBase64
	}
	return EncodingUTF8
}
