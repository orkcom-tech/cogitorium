package bundle

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/orkcom-tech/cogitorium/internal/gear"
)

// Export writes a workspace out as a bundle. Ids never leave: agents are
// named, wires name their endpoints, gear bindings name what they are bound
// to, and models name the provider type and model rather than the catalog row
// they happen to occupy here.
func Export(ctx context.Context, s Stores, wsID int64, opts Options) (Bundle, error) {
	ws, err := s.Workspaces.GetWorkspace(ctx, wsID)
	if err != nil {
		return Bundle{}, err
	}
	agents, err := s.Workspaces.ListAgents(ctx, wsID)
	if err != nil {
		return Bundle{}, err
	}
	wires, err := s.Workspaces.ListWires(ctx, wsID)
	if err != nil {
		return Bundle{}, err
	}

	b := Bundle{
		Format:     Format,
		ExportedAt: time.Now().UTC().Format(time.RFC3339),
		Workspace:  Workspace{Name: ws.Name, Description: ws.Description},
		Agents:     []Agent{},
		Wires:      []Wire{},
		Gears:      []Gear{},
		Context:    []ContextFile{},
	}

	nameOf := map[int64]string{}
	for _, a := range agents {
		nameOf[a.ID] = a.Name
		entry := Agent{
			Name:           a.Name,
			Role:           a.Role,
			Avoid:          a.Avoid,
			IsOrchestrator: a.IsOrchestrator,
			PosX:           a.PosX,
			PosY:           a.PosY,
		}
		if a.ModelID != nil {
			m, err := s.Catalog.GetModel(ctx, *a.ModelID)
			if err != nil {
				return Bundle{}, fmt.Errorf("export workspace %d: model of agent %q: %w", wsID, a.Name, err)
			}
			entry.Model = &ModelRef{ProviderType: m.ProviderType, ModelName: m.ModelName}
		}
		b.Agents = append(b.Agents, entry)
	}

	for _, w := range wires {
		from, okFrom := nameOf[w.FromAgentID]
		to, okTo := nameOf[w.ToAgentID]
		if !okFrom || !okTo {
			// Both endpoints are agents of this workspace and both were just
			// listed, so this cannot happen without the rows disagreeing with
			// each other. Say so rather than write a wire naming "".
			return Bundle{}, fmt.Errorf("export workspace %d: wire %d joins agents that are not in this workspace", wsID, w.ID)
		}
		b.Wires = append(b.Wires, Wire{From: from, To: to, Label: w.Label})
	}

	if opts.Gears {
		b.Gears, err = exportGears(ctx, s, wsID, nameOf)
		if err != nil {
			return Bundle{}, err
		}
	}
	if opts.Planboards && s.Planboards != nil {
		b.Planboards, err = exportPlanboards(ctx, s, wsID, nameOf)
		if err != nil {
			return Bundle{}, err
		}
	}
	if opts.MCP && s.MCP != nil {
		b.MCPServers, err = exportMCP(ctx, s, wsID, nameOf)
		if err != nil {
			return Bundle{}, err
		}
	}
	if opts.Inlets && s.Inlets != nil {
		b.Inlets, err = exportInlets(ctx, s, wsID)
		if err != nil {
			return Bundle{}, err
		}
	}
	// Who reads what, always. It is three fields of wiring rather than a
	// document, it is meaningless without the agents it names, and an agent
	// arriving without what it was told to read is a different agent.
	b.Reads, err = exportReads(ctx, s, wsID, ws.Branch, nameOf)
	if err != nil {
		return Bundle{}, err
	}

	if opts.Context {
		b.Context, err = exportContext(ctx, s, ws.Branch)
		if err != nil {
			return Bundle{}, err
		}
	}

	slog.Info("workspace exported as a bundle", "workspace_id", wsID, "name", ws.Name,
		"agents", len(b.Agents), "wires", len(b.Wires), "gears", len(b.Gears),
		"mcp_servers", len(b.MCPServers), "inlets", len(b.Inlets), "context_files", len(b.Context))
	return b, nil
}

// exportGears carries the source of every gear this workspace can reach.
//
// A gear bound both to the workspace and to one agent appears once, as
// workspace-wide: that is the broader grant, so reproducing it reproduces the
// same reach. The status is not carried at all — see Import, where approval
// deliberately does not travel.
func exportGears(ctx context.Context, s Stores, wsID int64, nameOf map[int64]string) ([]Gear, error) {
	bindings, err := s.Gears.ListBindings(ctx, wsID)
	if err != nil {
		return nil, err
	}

	out := []Gear{}
	at := map[string]int{}
	for _, bind := range bindings {
		boundTo := BoundToWorkspace
		if bind.AgentID != nil {
			name, ok := nameOf[*bind.AgentID]
			if !ok {
				return nil, fmt.Errorf("export workspace %d: gear %q is bound to an agent of another workspace", wsID, bind.GearName)
			}
			boundTo = name
		}

		if i, seen := at[bind.GearName]; seen {
			if boundTo == BoundToWorkspace {
				out[i].BoundTo = BoundToWorkspace
			}
			continue
		}

		g, err := s.Gears.Get(ctx, bind.GearID)
		if err != nil {
			return nil, fmt.Errorf("export workspace %d: gear %q: %w", wsID, bind.GearName, err)
		}
		files, err := s.Gears.Files(ctx, g.ID, g.Version)
		if err != nil {
			return nil, fmt.Errorf("export workspace %d: files of gear %q: %w", wsID, g.Name, err)
		}
		entry := Gear{
			Name:           g.Name,
			Description:    g.Description,
			Tags:           g.Tags,
			Runtime:        g.Runtime,
			Entrypoint:     g.Entrypoint,
			ArgsSchema:     g.ArgsSchema,
			TimeoutSeconds: g.TimeoutSeconds,
			// The names travel so the receiving operator knows what this gear
			// will want before it runs and fails. Nothing here reads a value:
			// the values live in env_values and the mounted directories, and
			// neither is reachable from this function.
			EnvNames: g.EnvNames,
			Files:    make([]GearFile, 0, len(files)),
			BoundTo:  boundTo,
		}
		for _, f := range files {
			entry.Files = append(entry.Files, GearFile{
				Path:       f.Path,
				Content:    f.Content,
				Encoding:   bundleEncoding(f.Encoding),
				Executable: g.Runtime == gear.RuntimeBinary && f.Path == g.Entrypoint,
			})
		}
		at[g.Name] = len(out)
		out = append(out, entry)
	}
	return out, nil
}

// exportContext carries the workspace's own documents — its shared branch and
// its agents' private branches — as paths relative to the branch root, so an
// import can re-root them under the workspace it creates.
//
// An unreachable context store is an error here rather than an empty list:
// the operator asked for the documents, and a bundle that silently arrives
// without them is one they will hand to somebody else believing it complete.
func exportContext(ctx context.Context, s Stores, branch string) ([]ContextFile, error) {
	if branch == "" {
		return nil, fmt.Errorf("this workspace has no context branch, so there is nothing to export")
	}
	files, err := s.Context.List(ctx)
	if err != nil {
		return nil, err
	}

	prefix := branch + "/"
	out := []ContextFile{}
	for _, f := range files {
		if !strings.HasPrefix(f.Path, prefix) {
			continue
		}
		content, err := s.Context.Get(ctx, f.Path)
		if err != nil {
			return nil, fmt.Errorf("read context document %q: %w", f.Path, err)
		}
		out = append(out, ContextFile{Path: strings.TrimPrefix(f.Path, prefix), Content: content})
	}
	return out, nil
}

// exportMCP carries the SHAPE of every external MCP server this workspace
// granted, and nothing else.
//
// The same three rules the gears follow, for the same reasons. A server bound
// both workspace-wide and to one agent appears once as workspace-wide, because
// that is the broader grant. The status is not carried, so an import arrives
// pending. And the names of the values it wants travel while the values do not.
//
// The difference from a gear is worth stating rather than assuming: a gear's
// complete source is in the bundle, so the receiving operator can read what
// they are approving. An MCP server is a command line or a hostname, and they
// cannot. That is an argument for carrying it — they need the shape in order to
// decide at all — and a much stronger argument for never carrying the approval.
func exportMCP(ctx context.Context, s Stores, wsID int64, nameOf map[int64]string) ([]MCPServer, error) {
	bindings, err := s.MCP.Bindings(ctx, wsID)
	if err != nil {
		return nil, fmt.Errorf("read this workspace's MCP grants: %w", err)
	}
	broadest := map[int64]string{}
	for _, b := range bindings {
		if b.AgentID == nil {
			broadest[b.ServerID] = "workspace"
			continue
		}
		if _, already := broadest[b.ServerID]; already {
			continue
		}
		if name, ok := nameOf[*b.AgentID]; ok {
			broadest[b.ServerID] = name
		}
	}

	out := []MCPServer{}
	for serverID, boundTo := range broadest {
		srv, err := s.MCP.Get(ctx, serverID)
		if err != nil {
			// A grant whose server has gone is not a reason to fail the whole
			// export: the workspace is still exportable and the missing row is
			// already invisible to everything else.
			slog.Warn("an MCP grant names a server that is gone; it is left out of the bundle",
				"workspace_id", wsID, "server_id", serverID, "err", err)
			continue
		}
		out = append(out, MCPServer{
			Name: srv.Name, Description: srv.Description, Transport: srv.Transport,
			Command: srv.Command, Args: srv.Args, Dir: srv.Dir, EnvNames: srv.EnvNames,
			URL: srv.URL, HeaderNames: srv.HeaderNames,
			BoundTo: boundTo,
		})
	}
	// Sorted, so two exports of the same workspace are the same bytes: a map
	// has no order, and a bundle people diff must not churn.
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// exportReads carries the bindings: which document each agent was told to read.
func exportReads(ctx context.Context, s Stores, wsID int64, branch string, nameOf map[int64]string) ([]Reading, error) {
	bindings, err := s.Workspaces.ListContextBindings(ctx, wsID)
	if err != nil {
		return nil, err
	}
	out := []Reading{}
	for _, b := range bindings {
		r := Reading{Path: b.Path}
		if prefix := branch + "/"; branch != "" && strings.HasPrefix(b.Path, prefix) {
			r.Path, r.Own = strings.TrimPrefix(b.Path, prefix), true
		}
		if b.AgentID != nil {
			// An agent that is not in this bundle cannot be named on the far
			// side, and a binding to a name nobody has is worse than none.
			name, ok := nameOf[*b.AgentID]
			if !ok {
				continue
			}
			r.Agent = name
		}
		out = append(out, r)
	}
	return out, nil
}

// exportPlanboards carries the plans this workspace follows.
//
// The steps and who follows them; never the position. A plan is global in the
// catalogue and attached per workspace, so what belongs in a workspace's
// bundle is the attachment — and the plan itself, because on the far side
// there is no catalogue entry to attach to.
func exportPlanboards(ctx context.Context, s Stores, wsID int64, nameOf map[int64]string) ([]Planboard, error) {
	bindings, err := s.Planboards.Bindings(ctx, wsID)
	if err != nil {
		return nil, fmt.Errorf("export workspace %d: %w", wsID, err)
	}
	out := make([]Planboard, 0, len(bindings))
	for _, b := range bindings {
		plan, err := s.Planboards.Get(ctx, b.PlanboardID)
		if err != nil {
			return nil, fmt.Errorf("export workspace %d: %w", wsID, err)
		}
		entry := Planboard{
			Name: plan.Name, Description: plan.Description, Mode: string(plan.Mode),
		}
		for _, st := range plan.Steps {
			entry.Steps = append(entry.Steps, PlanStep{Title: st.Title, Body: st.Body})
		}
		if b.AgentID != nil {
			entry.BoundTo = nameOf[*b.AgentID]
		}
		out = append(out, entry)
	}
	return out, nil
}

// exportInlets carries the doors into this workspace and the tasks behind
// them: the address, the name a caller selects, what it accepts, who does the
// work, what they are told, and what success is.
//
// The KEY is not read here and there is nowhere in the document to put it. The
// store does not hand it out either — the hash is unexported and never leaves
// the inlet package — so this is not a rule this function has to keep so much
// as one it could not break.
//
// What travels is the shape a caller depends on. Everything in a task is part
// of the contract with whatever calls it: the schema is checked before a model
// is reached, the instruction is what the agent is actually told, and `expect`
// is what makes a wrong answer a refusal instead of a 200. A door whose shape
// did not travel is a door with the same name and different behaviour.
func exportInlets(ctx context.Context, s Stores, wsID int64) ([]Inlet, error) {
	doors, err := s.Inlets.ListInlets(ctx, wsID)
	if err != nil {
		return nil, fmt.Errorf("read this workspace's inlets: %w", err)
	}
	out := []Inlet{}
	for _, d := range doors {
		entry := Inlet{Address: d.Address, Description: d.Description, Tasks: []InletTask{}}
		for _, t := range d.Tasks {
			task := InletTask{
				Name: t.Name, Accepts: t.Accepts, Schema: t.Schema,
				ContentType: t.ContentType, Agent: t.AgentName,
				Instruction: t.Instruction, CallbackURL: t.CallbackURL,
			}
			if t.Expect.Declared() {
				task.Expect = &InletExpect{
					ProducesFiles: t.Expect.ProducesFiles,
					RunsGear:      t.Expect.RunsGear,
					Schema:        string(t.Expect.Schema),
					AnswerFrom:    t.Expect.AnswerFrom,
				}
			}
			entry.Tasks = append(entry.Tasks, task)
		}
		out = append(out, entry)
	}
	return out, nil
}
