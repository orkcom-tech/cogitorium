package bundle

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/orkcom-tech/cogitorium/internal/contextstore"
	"github.com/orkcom-tech/cogitorium/internal/gear"
	"github.com/orkcom-tech/cogitorium/internal/workspace"
)

// ImportOptions is everything about an import that is not in the document.
type ImportOptions struct {
	// Name replaces the bundle's own workspace name. Workspace names are
	// unique per install, so importing a bundle twice — or importing one next
	// to the workspace it was exported from — needs a way to say what this
	// copy is called.
	Name string
	// OwnerID is the caller. An imported workspace belongs to whoever
	// imported it, never to whoever exported it: the bundle carries no owner
	// at all, which is what makes it safe to pass around.
	OwnerID        int64
	IncludeGears   bool
	IncludeContext bool
}

// Result is what an import did, in the operator's terms. It reports the parts
// that did not come across as loudly as the parts that did: a model this
// install does not have, or a gear name already taken, changes what the
// workspace can do, and finding that out by watching an agent fail later is
// how a missing piece turns into a mystery.
type Result struct {
	Workspace        workspace.Workspace `json:"workspace"`
	Agents           int                 `json:"agents"`
	Wires            int                 `json:"wires"`
	GearsImported    []string            `json:"gears_imported"`
	GearsSkipped     []SkippedGear       `json:"gears_skipped"`
	ContextFiles     int                 `json:"context_files"`
	UnresolvedModels []UnresolvedModel   `json:"unresolved_models"`
}

type SkippedGear struct {
	Name string `json:"name"`
	Why  string `json:"why"`
}

// UnresolvedModel names an agent whose model this install does not have. The
// agent is still created, with no model bound, because a workspace missing
// one model is repairable and a refused import is not.
type UnresolvedModel struct {
	Agent        string `json:"agent"`
	ProviderType string `json:"provider_type"`
	ModelName    string `json:"model_name"`
}

// Import rebuilds a workspace from a bundle, owned by the caller.
func Import(ctx context.Context, s Stores, b Bundle, opts ImportOptions) (Result, error) {
	// Normalize BEFORE validating, so the names Validate approves are the exact
	// strings the rest of this function compares and stores. Validating first
	// and trimming later is what let a padded name mean one thing to the guard
	// and another to the store — see Normalized.
	b = b.Normalized()
	if err := b.Validate(); err != nil {
		return Result{}, err
	}

	name := strings.TrimSpace(opts.Name)
	if name == "" {
		name = strings.TrimSpace(b.Workspace.Name)
	}
	if name == "" {
		return Result{}, fmt.Errorf("%w: it has no workspace name — send \"name\" with the import to give this one a name", ErrMalformed)
	}

	// The context store is checked before anything is written, because it is
	// the one dependency that lives outside this process: finding out it is
	// unreachable halfway through leaves a workspace whose documents are
	// partly there, and partly there is the hardest state to recover from.
	if opts.IncludeContext && len(b.Context) > 0 {
		if st := s.Context.CheckStatus(ctx); !st.Available {
			return Result{}, fmt.Errorf("this bundle carries %d context documents, but Contextverse is not reachable (%s): %w",
				len(b.Context), st.Error, contextstore.ErrUnavailable)
		}
	}

	models, err := localModels(ctx, s)
	if err != nil {
		return Result{}, err
	}

	res := Result{GearsImported: []string{}, GearsSkipped: []SkippedGear{}, UnresolvedModels: []UnresolvedModel{}}

	// The orchestrator is created together with the workspace, so its
	// definition has to be resolved first. A bundle that describes no
	// orchestrator still produces a working workspace: every workspace has
	// one by construction, so it gets the standard role rather than a
	// refusal over a section the document simply did not fill in.
	orch := workspace.AgentSpec{Name: workspace.OrchestratorName, Role: workspace.DefaultOrchestratorRole}
	for _, a := range b.Agents {
		if !a.IsOrchestrator {
			continue
		}
		orch = workspace.AgentSpec{Name: a.Name, Role: a.Role, Avoid: a.Avoid, PosX: a.PosX, PosY: a.PosY}
		orch.ModelID = resolve(a, models, &res)
	}

	ws, err := s.Workspaces.CreateWorkspaceSpec(ctx, name, b.Workspace.Description, orch, opts.OwnerID)
	if err != nil {
		if errors.Is(err, workspace.ErrConflict) {
			return Result{}, fmt.Errorf("a workspace named %q already exists here — send \"name\" with the import to call this copy something else: %w", name, err)
		}
		return Result{}, err
	}
	res.Workspace = ws
	res.Agents = 1

	// Agents are keyed by the name the bundle used, which is also what its
	// wires and gear bindings point at. The orchestrator was created by
	// CreateWorkspaceSpec, so it is read back rather than created here.
	created := map[string]workspace.Agent{}
	agents, err := s.Workspaces.ListAgents(ctx, ws.ID)
	if err != nil {
		return res, err
	}
	for _, a := range agents {
		if a.IsOrchestrator {
			created[orch.Name] = a
		}
	}
	if _, ok := created[orch.Name]; !ok {
		return res, fmt.Errorf("workspace %q was created (id %d) but has no orchestrator, so nothing can be wired to it", ws.Name, ws.ID)
	}

	for _, a := range b.Agents {
		if a.IsOrchestrator {
			continue
		}
		spec := workspace.AgentSpec{Name: a.Name, Role: a.Role, Avoid: a.Avoid, PosX: a.PosX, PosY: a.PosY}
		spec.ModelID = resolve(a, models, &res)
		agent, err := s.Workspaces.CreateAgentSpec(ctx, ws.ID, spec)
		if err != nil {
			return res, fmt.Errorf("workspace %q was created (id %d) but agent %q could not be: %w — delete it and import the corrected bundle",
				ws.Name, ws.ID, a.Name, err)
		}
		created[a.Name] = agent
		res.Agents++
	}

	for _, w := range b.Wires {
		if _, err := s.Workspaces.CreateWire(ctx, ws.ID, created[w.From].ID, created[w.To].ID, w.Label); err != nil {
			return res, fmt.Errorf("workspace %q was created (id %d) but the wire %s -> %s could not be: %w — delete it and import the corrected bundle",
				ws.Name, ws.ID, w.From, w.To, err)
		}
		res.Wires++
	}

	if opts.IncludeGears {
		if err := importGears(ctx, s, b, ws, created, &res); err != nil {
			return res, err
		}
	}
	if opts.IncludeContext {
		for _, f := range b.Context {
			path, err := ContextPath(ws.Branch, f.Path)
			if err != nil {
				return res, fmt.Errorf("workspace %q was created (id %d) but its context could not be: %w", ws.Name, ws.ID, err)
			}
			if err := s.Context.Put(ctx, path, f.Content); err != nil {
				return res, fmt.Errorf("workspace %q was created (id %d) but the context document %q could not be written: %w",
					ws.Name, ws.ID, path, err)
			}
			res.ContextFiles++
		}
	}

	slog.Info("workspace imported from a bundle", "workspace_id", ws.ID, "name", ws.Name, "owner_id", opts.OwnerID,
		"agents", res.Agents, "wires", res.Wires, "gears_imported", len(res.GearsImported),
		"gears_skipped", len(res.GearsSkipped), "context_files", res.ContextFiles,
		"unresolved_models", len(res.UnresolvedModels))
	return res, nil
}

// importGears forges the bundle's gears into the local catalog and binds them
// to the new workspace.
//
// Two rules hold here, and both are about somebody else's code arriving on
// this machine. A forged gear is always pending, whatever the bundle says —
// the format has nowhere to record an approval precisely so that approval
// cannot travel, and the operator of this install decides what may run on it.
// And a name already in the catalog is left exactly as it is: overwriting it
// would change a tool other workspaces already depend on, and binding the
// local one instead would let a bundle hand itself whatever this install has
// already approved by simply naming it.
func importGears(ctx context.Context, s Stores, b Bundle, ws workspace.Workspace, created map[string]workspace.Agent, res *Result) error {
	for _, g := range b.Gears {
		if _, err := s.Gears.GetByName(ctx, g.Name); err == nil {
			res.GearsSkipped = append(res.GearsSkipped, SkippedGear{
				Name: g.Name,
				Why:  "a gear with this name already exists in this install; it was left untouched and not bound to the imported workspace",
			})
			continue
		} else if !errors.Is(err, gear.ErrNotFound) {
			return fmt.Errorf("workspace %q was created (id %d) but the gear catalog could not be read: %w", ws.Name, ws.ID, err)
		}

		files := make([]gear.File, 0, len(g.Files))
		for _, f := range g.Files {
			encoding, err := storeEncoding(f.Encoding)
			if err != nil {
				return fmt.Errorf("gear %q file %q: %w: %s", g.Name, f.Path, ErrMalformed, err)
			}
			files = append(files, gear.File{Path: f.Path, Content: f.Content, Encoding: encoding})
		}

		// The new workspace is recorded as the gear's origin, and no agent as
		// its author: nobody here forged it, and the workspace it arrived
		// with is the honest answer to "where did this come from".
		// The declared names come across; what they mean does not. The gear
		// arrives pending, as every imported gear does, so the operator reads
		// both halves — this source, and these named credentials — before
		// anything can run.
		forged, err := s.Gears.Forge(ctx, g.Name, g.Description, g.Tags, g.Runtime, g.Entrypoint, g.ArgsSchema, g.EnvNames, files, ws.ID, 0)
		if err != nil {
			return fmt.Errorf("workspace %q was created (id %d) but its gear %q could not be: %w — delete the workspace and fix the bundle, or import it again without gears",
				ws.Name, ws.ID, g.Name, err)
		}
		// The bundle's timeout is deliberately NOT applied. Raising a gear's
		// timeout is an administrator's decision everywhere else — PATCH
		// /api/v1/gears/{id} says so and refuses a member — and importing is
		// not. Honouring it here let any signed-in caller bring a gear with a
		// 3600 second timeout and then run it, holding a sandbox container for
		// an hour per request. The catalog default stands, and an admin can
		// raise it afterwards by the route that was built for the decision.

		var agentID *int64
		if g.BoundTo != BoundToWorkspace {
			id := created[g.BoundTo].ID
			agentID = &id
		}
		if _, err := s.Gears.Bind(ctx, forged.ID, ws.ID, agentID); err != nil {
			return fmt.Errorf("gear %q was imported but could not be bound to %q: %w", g.Name, g.BoundTo, err)
		}
		res.GearsImported = append(res.GearsImported, g.Name)
	}
	return nil
}

// localModels indexes this install's catalog by what a bundle names a model
// with. Where two providers of the same type offer the same model, the first
// in the catalog's own order wins — an arbitrary choice made the same way
// every time beats an arbitrary choice that moves between imports.
func localModels(ctx context.Context, s Stores) (map[string]int64, error) {
	all, err := s.Catalog.ListModels(ctx)
	if err != nil {
		return nil, err
	}
	out := map[string]int64{}
	for _, m := range all {
		key := modelKey(m.ProviderType, m.ModelName)
		if _, taken := out[key]; !taken {
			out[key] = m.ID
		}
	}
	return out, nil
}

func modelKey(providerType, modelName string) string {
	return strings.ToLower(strings.TrimSpace(providerType)) + "\x00" + strings.TrimSpace(modelName)
}

// resolve turns a bundle's model reference into a local catalog id, and
// records the miss when there is none. A miss is never silent: the agent is
// created with no model, and the import result names the agent and the model
// it wanted so the operator knows exactly what to add and to whom.
func resolve(a Agent, models map[string]int64, res *Result) *int64 {
	if a.Model == nil {
		return nil
	}
	if id, ok := models[modelKey(a.Model.ProviderType, a.Model.ModelName)]; ok {
		return &id
	}
	res.UnresolvedModels = append(res.UnresolvedModels, UnresolvedModel{
		Agent: a.Name, ProviderType: a.Model.ProviderType, ModelName: a.Model.ModelName,
	})
	return nil
}
