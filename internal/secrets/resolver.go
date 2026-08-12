package secrets

import (
	"context"
	"fmt"
	"sort"
)

// Resolver answers "what does this name mean here", and is the only thing that
// ever holds a plaintext value outside the store.
//
// THE ORDER, later winning:
//
//  1. the install's own store, global scope — set in the interface, held in
//     the database, encrypted at rest;
//  2. the variables directory on disk — one file per name, contents are the
//     value (a Kubernetes ConfigMap mounted as a directory);
//  3. the secrets directory on disk — the same shape (a Kubernetes Secret);
//  4. the install's own store, this workspace's scope.
//
// That is the plan's three sources, with the directory counted once per kind
// because the chart mounts a ConfigMap and a Secret separately and the two say
// different things about whether a value may be shown.
//
// Global first and workspace last is the whole of "global, overridden per
// workspace": the same name means one thing install-wide and another inside a
// particular workspace, which is how one gear serves staging and production
// without being edited.
//
// The mounted directories sit BETWEEN them on purpose. A directory is the
// cluster's own environment and outranks a default somebody typed into this
// install's interface; a workspace override is the narrowest statement anyone
// made and outranks everything.
type Resolver struct {
	store     *Store
	variables dirSource
	secrets   dirSource
}

// NewResolver builds the lookup. Either directory may be empty, which means
// this install has no such source — the ordinary case on a laptop.
func NewResolver(store *Store, variablesDir, secretsDir string) (*Resolver, error) {
	r := &Resolver{
		store:     store,
		variables: dirSource{path: variablesDir, kind: KindVariable, label: "the variables directory"},
		secrets:   dirSource{path: secretsDir, kind: KindSecret, label: "the secrets directory"},
	}
	if err := r.variables.Check(); err != nil {
		return nil, err
	}
	if err := r.secrets.Check(); err != nil {
		return nil, err
	}
	return r, nil
}

// Store exposes the writable half, so a caller that has the resolver does not
// also have to be handed the store and keep the two in step.
func (r *Resolver) Store() *Store { return r.store }

// Sources names the directories in use, for the interface to show. An operator
// looking at a name that is not what they set needs to know a directory exists
// at all before they can suspect it.
func (r *Resolver) Sources() (variablesDir, secretsDir string) {
	return r.variables.path, r.secrets.path
}

// layer is one source's contribution, in the order they are applied.
type layer struct {
	kind   string
	source string
	// stored is set for the two database layers; files for the two directories.
	stored map[string]sealed
	files  map[string]Value
}

// layers assembles the sources in resolution order for one workspace. wsID nil
// is a run with no workspace — an operator's dry run from the catalog — which
// sees the global scope and the directories and no override.
func (r *Resolver) layers(ctx context.Context, wsID *int64) ([]layer, error) {
	global, err := r.store.load(ctx, nil)
	if err != nil {
		return nil, err
	}
	out := []layer{
		{source: "this install's store", stored: global},
		{source: r.variables.label, kind: KindVariable, files: r.variables.read()},
		{source: r.secrets.label, kind: KindSecret, files: r.secrets.read()},
	}
	if wsID != nil {
		ws, err := r.store.load(ctx, wsID)
		if err != nil {
			return nil, err
		}
		out = append(out, layer{source: fmt.Sprintf("workspace %d's own overrides", *wsID), stored: ws})
	}
	return out, nil
}

// Resolve turns a gear's declared names into the values to inject.
//
// A name nothing supplies is a refusal naming it, never an empty string: an
// empty credential fails somewhere far away, in a stranger's library, with a
// message about nothing.
func (r *Resolver) Resolve(ctx context.Context, wsID *int64, names []string) ([]Value, error) {
	if len(names) == 0 {
		return nil, nil
	}
	layers, err := r.layers(ctx, wsID)
	if err != nil {
		return nil, err
	}

	out := make([]Value, 0, len(names))
	var missing []string
	for _, name := range names {
		v, found, err := resolveOne(r.store, layers, name)
		if err != nil {
			return nil, err
		}
		if !found {
			missing = append(missing, name)
			continue
		}
		out = append(out, v)
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return nil, fmt.Errorf("%w: nothing here supplies %v — set %s under Variables & Secrets (for this install or for this workspace), "+
			"or mount a file of that name in a variables or secrets directory",
			ErrUnresolved, missing, plural(missing))
	}
	return out, nil
}

func plural(missing []string) string {
	if len(missing) == 1 {
		return "it"
	}
	return "them"
}

// resolveOne walks the layers in order and keeps the last hit.
//
// The kind is sticky towards secret: once a lower layer said a name is a
// secret, a higher layer's override does not turn it back into something that
// gets printed. Values are still overridden normally — this decides only
// whether the winning value is redacted — and it exists because the accident it
// prevents is silent and permanent. Setting a workspace override for a name
// that is a secret install-wide is exactly the moment somebody would pick the
// wrong radio button and publish a production key in a chat transcript.
func resolveOne(store *Store, layers []layer, name string) (Value, bool, error) {
	var out Value
	found := false
	wasSecret := false
	for _, l := range layers {
		if raw, ok := l.stored[name]; ok {
			plain, err := store.open(name, raw)
			if err != nil {
				return Value{}, false, err
			}
			out = Value{Name: name, Value: plain, Kind: raw.kind, Source: l.source}
			found = true
			wasSecret = wasSecret || raw.kind == KindSecret
			continue
		}
		if v, ok := l.files[name]; ok {
			out = v
			found = true
			wasSecret = wasSecret || v.Kind == KindSecret
		}
	}
	if found && wasSecret {
		out.Kind = KindSecret
	}
	return out, found, nil
}

// Describe reports what would happen to each declared name, without disclosing
// any value. It is what the approval screen reads: an operator approving a gear
// that names something nothing supplies is approving a gear that cannot run,
// and finding that out at the first call is finding it out in the wrong place.
func (r *Resolver) Describe(ctx context.Context, wsID *int64, names []string) ([]Status, error) {
	out := make([]Status, 0, len(names))
	if len(names) == 0 {
		return out, nil
	}
	layers, err := r.layers(ctx, wsID)
	if err != nil {
		return nil, err
	}
	for _, name := range names {
		st := Status{Name: name}
		// Deliberately not resolveOne: describing must not decrypt, so a secret
		// sealed under a key this server no longer has still describes itself
		// as present rather than failing the whole screen. The sticky-secret
		// rule is repeated here rather than shared, because the screen has to
		// say the same word the run will act on.
		wasSecret := false
		for _, l := range layers {
			if raw, ok := l.stored[name]; ok {
				st.Found, st.Kind, st.Source = true, raw.kind, l.source
				wasSecret = wasSecret || raw.kind == KindSecret
				continue
			}
			if v, ok := l.files[name]; ok {
				st.Found, st.Kind, st.Source = true, v.Kind, v.Source
				wasSecret = wasSecret || v.Kind == KindSecret
			}
		}
		if st.Found && wasSecret {
			st.Kind = KindSecret
		}
		out = append(out, st)
	}
	return out, nil
}
