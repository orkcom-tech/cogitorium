package bundle

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/orkcom-tech/cogitorium/internal/contextstore"
	"github.com/orkcom-tech/cogitorium/internal/gear"
	"github.com/orkcom-tech/cogitorium/internal/mcpstore"
	"github.com/orkcom-tech/cogitorium/internal/planboard"
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
	// IncludePlanboards recreates the plans the bundle carried and attaches
	// them as it says. Each begins at step one: where a plan had got to is a
	// fact about a run on the exporting install.
	IncludePlanboards bool
	// IncludeMCP recreates the external MCP servers the bundle carried. Every
	// one arrives PENDING with no credential resolved — see importMCP.
	IncludeMCP bool
}

// Result is what an import did, in the operator's terms. It reports the parts
// that did not come across as loudly as the parts that did: a model this
// install does not have, or a gear name already taken, changes what the
// workspace can do, and finding that out by watching an agent fail later is
// how a missing piece turns into a mystery.
type Result struct {
	Workspace     workspace.Workspace `json:"workspace"`
	Agents        int                 `json:"agents"`
	Wires         int                 `json:"wires"`
	GearsImported []string            `json:"gears_imported"`
	GearsSkipped  []SkippedGear       `json:"gears_skipped"`
	MCPImported   []string            `json:"mcp_imported"`
	MCPSkipped    []SkippedGear       `json:"mcp_skipped"`
	ContextFiles  int                 `json:"context_files"`
	// Reads is how many bindings were made, and ReadsSkipped the ones that
	// named an agent this bundle does not have.
	// PlanboardsImported is what was written and attached, and
	// PlanboardsSkipped the ones a name collision or a missing agent stopped.
	PlanboardsImported []string          `json:"planboards_imported,omitempty"`
	PlanboardsSkipped  []SkippedGear     `json:"planboards_skipped,omitempty"`
	Reads              int               `json:"reads"`
	ReadsSkipped       []SkippedGear     `json:"reads_skipped,omitempty"`
	UnresolvedModels   []UnresolvedModel `json:"unresolved_models"`
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
	if opts.IncludeMCP && s.MCP != nil {
		if err := importMCP(ctx, s, b, ws, created, &res); err != nil {
			return res, err
		}
	}
	if opts.IncludePlanboards && s.Planboards != nil {
		if err := importPlanboards(ctx, s, b, ws, created, &res); err != nil {
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

	// Who reads what. Always, and after the documents: a binding is three
	// fields of wiring, and an agent that arrived without what it was told to
	// read is a different agent behaving differently for no stated reason.
	//
	// A path that does not resolve on this install is still bound. The binding
	// says what the workflow asks for; whether this install can supply it is a
	// separate question, answered where context is read and answered there
	// already. Dropping it would turn a missing document into a missing
	// instruction nobody can see was ever intended.
	for _, rd := range b.Reads {
		var agentID *int64
		if rd.Agent != "" {
			agent, ok := created[rd.Agent]
			if !ok {
				res.ReadsSkipped = append(res.ReadsSkipped, SkippedGear{
					Name: rd.Path,
					Why:  fmt.Sprintf("it was read by %q, and this bundle has no such agent", rd.Agent),
				})
				continue
			}
			agentID = &agent.ID
		}
		path := rd.Path
		// A document of the exporting workspace's own arrives under THIS
		// workspace's branch, so the binding has to follow it. A binding
		// carrying the old branch would point at nothing here, or at somebody
		// else's workspace.
		if rd.Own {
			p, err := ContextPath(ws.Branch, rd.Path)
			if err != nil {
				return res, fmt.Errorf("workspace %q was created (id %d) but %q could not be bound: %w",
					ws.Name, ws.ID, rd.Path, err)
			}
			path = p
		}
		if _, err := s.Workspaces.CreateContextBinding(ctx, ws.ID, path, agentID); err != nil {
			return res, fmt.Errorf("workspace %q was created (id %d) but %q could not be bound: %w",
				ws.Name, ws.ID, rd.Path, err)
		}
		res.Reads++
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

// importMCP recreates the SHAPE of the external MCP servers a bundle carried,
// and nothing else.
//
// EVERY ONE ARRIVES PENDING, with no fingerprint, and there is no path here
// that could make it otherwise — the bundle has no field for a status and this
// function has no branch that reads one. That is the point rather than an
// implementation detail: an MCP server is a command line or a hostname, so the
// operator on this side cannot read what they are agreeing to the way they can
// read a gear's source. A bundle that arrived pre-approved would be a way to
// hand somebody a process on their own host by email.
//
// The names of the values it wants come across; the values do not, and there is
// nowhere in the bundle they could have been. What an imported server needs is
// therefore visible immediately — it names JIRA_TOKEN and this install has no
// such value — which is the honest failure rather than a silent one.
func importMCP(ctx context.Context, s Stores, b Bundle, ws workspace.Workspace,
	created map[string]workspace.Agent, res *Result) error {
	for _, m := range b.MCPServers {
		existing, err := s.MCP.List(ctx)
		if err != nil {
			return fmt.Errorf("workspace %q was created (id %d) but the MCP catalog could not be read: %w",
				ws.Name, ws.ID, err)
		}
		clash := false
		for _, e := range existing {
			if e.Name == m.Name {
				clash = true
				break
			}
		}
		if clash {
			// Left alone rather than bound: an install's existing server may be
			// a different thing wearing the same name, and quietly granting it
			// to an imported workspace would be handing an agent something
			// nobody here chose.
			res.MCPSkipped = append(res.MCPSkipped, SkippedGear{
				Name: m.Name,
				Why: "an MCP server with this name already exists in this install; it was left untouched " +
					"and not granted to the imported workspace",
			})
			continue
		}

		srv, err := s.MCP.Install(ctx, mcpstore.Server{
			Name: m.Name, Description: m.Description, Transport: m.Transport,
			Command: m.Command, Args: m.Args, Dir: m.Dir, EnvNames: m.EnvNames,
			URL: m.URL, HeaderNames: m.HeaderNames,
		}, nil)
		if err != nil {
			// A bundle from a newer install may carry a transport this one does
			// not speak, or a shape this one refuses. Skipped with the reason
			// rather than failing an import that is otherwise fine.
			res.MCPSkipped = append(res.MCPSkipped, SkippedGear{Name: m.Name, Why: err.Error()})
			continue
		}

		var agentID *int64
		if m.BoundTo != "" && m.BoundTo != "workspace" {
			a, ok := created[m.BoundTo]
			if !ok {
				res.MCPSkipped = append(res.MCPSkipped, SkippedGear{
					Name: m.Name,
					Why:  "it was granted to agent " + m.BoundTo + ", which this bundle does not contain",
				})
				continue
			}
			agentID = &a.ID
		}
		if _, err := s.MCP.Bind(ctx, srv.ID, ws.ID, agentID); err != nil {
			return fmt.Errorf("workspace %q was created (id %d) but the MCP server %q could not be granted: %w",
				ws.Name, ws.ID, m.Name, err)
		}
		res.MCPImported = append(res.MCPImported, m.Name)
	}
	if len(res.MCPImported) > 0 {
		slog.Warn("a bundle brought external MCP servers; every one is PENDING and does nothing",
			"workspace_id", ws.ID, "servers", res.MCPImported,
			"note", "an approval never travels in a bundle: read what each one runs or calls before approving it")
	}
	return nil
}

// importPlanboards writes the plans a bundle carried and attaches them.
//
// A name already in the catalogue is left alone, exactly as a gear's is:
// overwriting it would change the order some other workflow on this install
// runs in, and attaching the local one instead would let a bundle adopt
// whatever this install already has by naming it.
//
// Every imported plan starts at step one. The bundle carries no position, and
// inventing one would start a workflow halfway through a plan it has never
// run.
func importPlanboards(ctx context.Context, s Stores, b Bundle, ws workspace.Workspace, created map[string]workspace.Agent, res *Result) error {
	for _, p := range b.Planboards {
		if _, err := s.Planboards.GetByName(ctx, p.Name); err == nil {
			res.PlanboardsSkipped = append(res.PlanboardsSkipped, SkippedGear{
				Name: p.Name,
				Why:  "a plan with this name already exists in this install; it was left untouched and not attached to the imported workspace",
			})
			continue
		} else if !errors.Is(err, planboard.ErrNotFound) {
			return fmt.Errorf("workspace %q was created (id %d) but the plan catalogue could not be read: %w", ws.Name, ws.ID, err)
		}

		steps := make([]planboard.Step, 0, len(p.Steps))
		for _, st := range p.Steps {
			steps = append(steps, planboard.Step{Title: st.Title, Body: st.Body})
		}
		saved, err := s.Planboards.Save(ctx, p.Name, p.Description, nil, planboard.Mode(p.Mode), steps, ws.ID, 0)
		if err != nil {
			return fmt.Errorf("workspace %q was created (id %d) but its plan %q could not be: %w — delete the workspace and fix the bundle, or import it again without plans",
				ws.Name, ws.ID, p.Name, err)
		}

		var agentID *int64
		if p.BoundTo != "" {
			agent, ok := created[p.BoundTo]
			if !ok {
				// Written, but attached to nothing: the plan is worth keeping
				// and guessing which agent was meant is not.
				res.PlanboardsSkipped = append(res.PlanboardsSkipped, SkippedGear{
					Name: p.Name,
					Why:  "it was attached to the agent " + p.BoundTo + ", which this bundle does not have; the plan was written but attached to nothing",
				})
				res.PlanboardsImported = append(res.PlanboardsImported, saved.Name)
				continue
			}
			agentID = &agent.ID
		}
		if _, err := s.Planboards.Bind(ctx, saved.ID, ws.ID, agentID); err != nil {
			return fmt.Errorf("workspace %q was created (id %d) but its plan %q could not be attached: %w", ws.Name, ws.ID, p.Name, err)
		}
		res.PlanboardsImported = append(res.PlanboardsImported, saved.Name)
	}
	return nil
}
