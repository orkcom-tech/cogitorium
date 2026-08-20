// Package workflow versions a workspace: what it was, in full, at a moment
// somebody chose to record.
//
// NOT the export bundle, and the difference is the whole reason this exists.
// A bundle crosses a machine boundary, so it drops approvals and credentials
// and names models by shape rather than by id — all correct there, and all
// wrong here. A version stays on this install, and a rollback that quietly
// un-approved every gear would not be a rollback.
//
// What a version covers is everything that decides what a run does: the
// agents, the wires between them, the gears they may call, what they read, and
// the clocks that start them. A version number that identified some of that
// would identify no behaviour at all — a blueprint versioned without the
// instruction an agent reads is a number that does not say how it will act.
//
// What it does NOT cover is the conversation. The transcript is work, not
// configuration: rolling a workflow back to last week must not delete what was
// said since. That is a judgement rather than a rule, and it is written here
// so the next person can disagree with it deliberately.
package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/orkcom-tech/cogitorium/internal/gear"
	"github.com/orkcom-tech/cogitorium/internal/planboard"
	"github.com/orkcom-tech/cogitorium/internal/schedule"
	"github.com/orkcom-tech/cogitorium/internal/workspace"
)

// Format is the snapshot generation. It moves when an old snapshot can no
// longer be restored by this code, which is the only thing a reader needs it
// for.
const Format = "cogitorium.workflow/1"

// Snapshot is a workflow as it stood.
type Snapshot struct {
	Format string `json:"format"`

	Name        string `json:"name"`
	Description string `json:"description"`

	Agents     []Agent     `json:"agents"`
	Wires      []Wire      `json:"wires"`
	Gears      []Gear      `json:"gears"`
	Context    []Context   `json:"context"`
	Schedules  []Schedule  `json:"schedules"`
	Planboards []Planboard `json:"planboards,omitempty"`
}

// Agent is one agent, by name.
//
// By name and not by id throughout this file: a rollback deletes and recreates,
// so every id in a workspace is different afterwards. A snapshot full of ids
// would restore once and be wrong the second time.
type Agent struct {
	Name           string   `json:"name"`
	Role           string   `json:"role"`
	Avoid          string   `json:"avoid"`
	IsOrchestrator bool     `json:"is_orchestrator"`
	ModelID        *int64   `json:"model_id,omitempty"`
	PosX           *float64 `json:"pos_x,omitempty"`
	PosY           *float64 `json:"pos_y,omitempty"`
}

// Wire is one delegation, by the names at its ends.
type Wire struct {
	From  string `json:"from"`
	To    string `json:"to"`
	Label string `json:"label"`
}

// Gear is a gear this workflow may call, pinned to the version it was pinned
// to.
//
// The VERSION is the point. A workflow holds what it held, so a gear edited in
// the library afterwards does not silently change what a saved version means —
// which is the whole reason a version is worth having. Restoring re-pins to
// this version if the library still has it, and says so plainly if it does
// not: a gear that was deleted cannot be conjured back by a rollback, and
// pretending otherwise would be worse than saying so.
type Gear struct {
	Name    string `json:"name"`
	Version int    `json:"version"`
	// Agent is who may call it, or empty for the whole workspace.
	Agent string `json:"agent,omitempty"`
}

// Context is one document an agent reads — an instruction pinned to it, or a
// file from the space. This is what the memory drawer calls memory, and it
// travels with the version because an agent's behaviour is mostly what it
// reads.
type Context struct {
	Path string `json:"path"`
	// Agent is who reads it, or empty for everybody in the workspace.
	Agent string `json:"agent,omitempty"`
}

// Schedule is one clock.
type Schedule struct {
	Name        string `json:"name"`
	Spec        string `json:"spec"`
	TZ          string `json:"tz"`
	Instruction string `json:"instruction"`
	OnMiss      string `json:"on_miss"`
	Enabled     bool   `json:"enabled"`
	// Agent is what it starts. A clock pointing at anything else is not
	// recorded, because nothing else can be restored by name.
	Agent string `json:"agent"`
}

// Stores is what taking and restoring a snapshot needs.
//
// An interface-free struct of concrete stores, like everything else that
// crosses packages here: the alternative is five interfaces with one
// implementation each, which is indirection that buys nothing and costs a
// reader every time.
type Stores struct {
	Spaces     *workspace.Store
	Gears      *gear.Store
	Schedules  *schedule.Store
	Planboards *planboard.Store
}

// Planboard is a plan attached here, AND where it had got to.
//
// The position is the reason this is in a version at all. Restoring the plan
// without it would put the steps back and leave the marker wherever the run
// that went wrong had pushed it — a rollback that returns the map and keeps
// the wrong pin on it.
type Planboard struct {
	Name string `json:"name"`
	// Agent is empty for the attachment the whole workspace shares.
	Agent string `json:"agent,omitempty"`
	Step  int    `json:"step"`
	Cycle int    `json:"completed_passes"`
}

// Take reads a workflow as it stands.
func Take(ctx context.Context, st Stores, wsID int64) (Snapshot, error) {
	ws, err := st.Spaces.GetWorkspace(ctx, wsID)
	if err != nil {
		return Snapshot{}, err
	}
	snap := Snapshot{Format: Format, Name: ws.Name, Description: ws.Description}

	agents, err := st.Spaces.ListAgents(ctx, wsID)
	if err != nil {
		return Snapshot{}, err
	}
	byID := map[int64]string{}
	for _, a := range agents {
		byID[a.ID] = a.Name
		snap.Agents = append(snap.Agents, Agent{
			Name: a.Name, Role: a.Role, Avoid: a.Avoid,
			IsOrchestrator: a.IsOrchestrator, ModelID: a.ModelID,
			PosX: a.PosX, PosY: a.PosY,
		})
	}

	wires, err := st.Spaces.ListWires(ctx, wsID)
	if err != nil {
		return Snapshot{}, err
	}
	for _, w := range wires {
		from, to := byID[w.FromAgentID], byID[w.ToAgentID]
		if from == "" || to == "" {
			// An end that is not in this workspace's agents cannot be restored
			// by name, and recording it would produce a version that fails
			// half way through being applied.
			continue
		}
		snap.Wires = append(snap.Wires, Wire{From: from, To: to, Label: w.Label})
	}

	if st.Gears != nil {
		bindings, err := st.Gears.ListBindings(ctx, wsID)
		if err != nil {
			return Snapshot{}, err
		}
		for _, b := range bindings {
			g, err := st.Gears.Get(ctx, b.GearID)
			if err != nil {
				continue
			}
			entry := Gear{Name: g.Name, Version: g.Version}
			if b.AgentID != nil {
				entry.Agent = byID[*b.AgentID]
			}
			snap.Gears = append(snap.Gears, entry)
		}
	}

	bindings, err := st.Spaces.ListContextBindings(ctx, wsID)
	if err != nil {
		return Snapshot{}, err
	}
	for _, b := range bindings {
		entry := Context{Path: b.Path}
		if b.AgentID != nil {
			entry.Agent = byID[*b.AgentID]
		}
		snap.Context = append(snap.Context, entry)
	}

	if st.Schedules != nil {
		clocks, err := st.Schedules.List(ctx, wsID)
		if err != nil {
			return Snapshot{}, err
		}
		for _, sc := range clocks {
			if sc.TargetKind != schedule.TargetAgent || sc.TargetAgentID == nil {
				continue
			}
			snap.Schedules = append(snap.Schedules, Schedule{
				Name: sc.Name, Spec: sc.Spec, TZ: sc.TZ, Instruction: sc.Instruction,
				OnMiss: sc.OnMiss, Enabled: sc.Enabled, Agent: byID[*sc.TargetAgentID],
			})
		}
	}

	if st.Planboards != nil {
		bindings, err := st.Planboards.Bindings(ctx, wsID)
		if err != nil {
			return Snapshot{}, err
		}
		for _, b := range bindings {
			state, err := st.Planboards.State(ctx, b.ID)
			if err != nil {
				return Snapshot{}, err
			}
			entry := Planboard{Name: b.Planboard, Step: state.Step, Cycle: state.Cycle}
			if b.AgentID != nil {
				entry.Agent = byID[*b.AgentID]
			}
			snap.Planboards = append(snap.Planboards, entry)
		}
	}

	snap.sort()
	return snap, nil
}

// sort puts everything in a fixed order.
//
// So that two snapshots of an unchanged workflow are byte-identical, which is
// what makes "nothing changed since v3" answerable by comparing them rather
// than by walking two graphs.
func (s *Snapshot) sort() {
	sort.Slice(s.Agents, func(i, j int) bool { return s.Agents[i].Name < s.Agents[j].Name })
	sort.Slice(s.Wires, func(i, j int) bool {
		if s.Wires[i].From != s.Wires[j].From {
			return s.Wires[i].From < s.Wires[j].From
		}
		return s.Wires[i].To < s.Wires[j].To
	})
	sort.Slice(s.Gears, func(i, j int) bool {
		if s.Gears[i].Name != s.Gears[j].Name {
			return s.Gears[i].Name < s.Gears[j].Name
		}
		return s.Gears[i].Agent < s.Gears[j].Agent
	})
	sort.Slice(s.Context, func(i, j int) bool {
		if s.Context[i].Path != s.Context[j].Path {
			return s.Context[i].Path < s.Context[j].Path
		}
		return s.Context[i].Agent < s.Context[j].Agent
	})
	sort.Slice(s.Schedules, func(i, j int) bool { return s.Schedules[i].Name < s.Schedules[j].Name })
	sort.Slice(s.Planboards, func(i, j int) bool {
		if s.Planboards[i].Name != s.Planboards[j].Name {
			return s.Planboards[i].Name < s.Planboards[j].Name
		}
		return s.Planboards[i].Agent < s.Planboards[j].Agent
	})
}

// Same reports whether two snapshots describe the same workflow.
//
// Used to refuse a save that would record nothing: a version list where half
// the entries are identical is a list nobody reads.
func Same(a, b Snapshot) bool {
	x, err1 := json.Marshal(a)
	y, err2 := json.Marshal(b)
	return err1 == nil && err2 == nil && string(x) == string(y)
}

// Restore puts a workflow back to what a snapshot says it was.
//
// Destructive by design: agents, wires, gear grants, context bindings and
// clocks that are not in the snapshot go. A "restore" that left things behind
// would be a merge, and a merge is not what somebody pressing rollback is
// asking for.
//
// The orchestrator is never deleted — it is the workspace's entry point and
// the store refuses — so it is updated in place and its name is kept.
//
// Returns what could not be put back, in words. A gear deleted from the
// library since cannot be conjured back by a rollback, and the honest answer
// is to restore everything else and say which one is missing.
func Restore(ctx context.Context, st Stores, wsID int64, snap Snapshot) ([]string, error) {
	if snap.Format != Format {
		return nil, fmt.Errorf("this version is in format %q and this build reads %q", snap.Format, Format)
	}
	var missing []string

	want := map[string]Agent{}
	for _, a := range snap.Agents {
		want[a.Name] = a
	}

	existing, err := st.Spaces.ListAgents(ctx, wsID)
	if err != nil {
		return nil, err
	}
	have := map[string]workspace.Agent{}
	for _, a := range existing {
		have[a.Name] = a
	}

	// Gone first, so a name freed here can be taken by a restored agent below.
	for _, a := range existing {
		if _, keep := want[a.Name]; keep || a.IsOrchestrator {
			continue
		}
		if err := st.Spaces.DeleteAgent(ctx, a.ID); err != nil {
			return nil, fmt.Errorf("removing %q: %w", a.Name, err)
		}
		delete(have, a.Name)
	}

	byName := map[string]int64{}
	for _, a := range snap.Agents {
		if cur, ok := have[a.Name]; ok {
			if _, err := st.Spaces.UpdateAgent(ctx, cur.ID, nil, &a.Role, a.ModelID); err != nil {
				return nil, fmt.Errorf("restoring %q: %w", a.Name, err)
			}
			if _, err := st.Spaces.SetAgentAvoid(ctx, cur.ID, a.Avoid); err != nil {
				return nil, fmt.Errorf("restoring %q: %w", a.Name, err)
			}
			byName[a.Name] = cur.ID
		} else {
			var modelID int64
			if a.ModelID != nil {
				modelID = *a.ModelID
			}
			made, err := st.Spaces.CreateAgent(ctx, wsID, a.Name, a.Role, modelID)
			if err != nil {
				return nil, fmt.Errorf("restoring %q: %w", a.Name, err)
			}
			byName[a.Name] = made.ID
			if a.Avoid != "" {
				if _, err := st.Spaces.SetAgentAvoid(ctx, made.ID, a.Avoid); err != nil {
					return nil, fmt.Errorf("restoring %q: %w", a.Name, err)
				}
			}
		}
		if a.PosX != nil && a.PosY != nil {
			_ = st.Spaces.SetAgentPosition(ctx, byName[a.Name], *a.PosX, *a.PosY)
		}
	}

	// Wires: all of them go, then the snapshot's are drawn. Drawing the
	// difference would need the same comparison in two directions and gets one
	// of them wrong eventually.
	wires, err := st.Spaces.ListWires(ctx, wsID)
	if err != nil {
		return nil, err
	}
	for _, w := range wires {
		if err := st.Spaces.DeleteWire(ctx, w.ID); err != nil {
			return nil, err
		}
	}
	for _, w := range snap.Wires {
		from, to := byName[w.From], byName[w.To]
		if from == 0 || to == 0 {
			missing = append(missing, fmt.Sprintf("the wire %s → %s: one end is not here", w.From, w.To))
			continue
		}
		if _, err := st.Spaces.CreateWire(ctx, wsID, from, to, w.Label); err != nil {
			return nil, err
		}
	}

	if st.Gears != nil {
		bindings, err := st.Gears.ListBindings(ctx, wsID)
		if err != nil {
			return nil, err
		}
		for _, b := range bindings {
			if err := st.Gears.Unbind(ctx, b.ID); err != nil {
				return nil, err
			}
		}
		for _, g := range snap.Gears {
			found, err := st.Gears.GetByName(ctx, g.Name)
			if err != nil {
				missing = append(missing, fmt.Sprintf("the gear %q is no longer in the catalog", g.Name))
				continue
			}
			if found.Version != g.Version {
				// Said, not silently accepted. The grant is restored because
				// the workflow had one; what it points at is not what it
				// pointed at, and that is exactly the thing a version exists
				// to make visible.
				missing = append(missing, fmt.Sprintf(
					"the gear %q is at v%d and this version held v%d — the grant is back, the code is not",
					g.Name, found.Version, g.Version))
			}
			var agentID *int64
			if g.Agent != "" {
				id, ok := byName[g.Agent]
				if !ok {
					missing = append(missing, fmt.Sprintf("%q could not be given %q: no such agent", g.Agent, g.Name))
					continue
				}
				agentID = &id
			}
			if _, err := st.Gears.Bind(ctx, found.ID, wsID, agentID); err != nil {
				return nil, err
			}
		}
	}

	current, err := st.Spaces.ListContextBindings(ctx, wsID)
	if err != nil {
		return nil, err
	}
	for _, b := range current {
		if err := st.Spaces.DeleteContextBinding(ctx, b.ID); err != nil {
			return nil, err
		}
	}
	for _, c := range snap.Context {
		var agentID *int64
		if c.Agent != "" {
			id, ok := byName[c.Agent]
			if !ok {
				missing = append(missing, fmt.Sprintf("%q could not be given %q: no such agent", c.Agent, c.Path))
				continue
			}
			agentID = &id
		}
		if _, err := st.Spaces.CreateContextBinding(ctx, wsID, c.Path, agentID); err != nil {
			return nil, err
		}
	}

	if st.Schedules != nil {
		clocks, err := st.Schedules.List(ctx, wsID)
		if err != nil {
			return nil, err
		}
		for _, sc := range clocks {
			if err := st.Schedules.Delete(ctx, sc.ID); err != nil {
				return nil, err
			}
		}
		for _, sc := range snap.Schedules {
			id, ok := byName[sc.Agent]
			if !ok {
				missing = append(missing, fmt.Sprintf("the clock %q has nothing to start: no agent called %q", sc.Name, sc.Agent))
				continue
			}
			if _, err := st.Schedules.Create(ctx, schedule.Schedule{
				WorkspaceID: wsID, TargetKind: schedule.TargetAgent, TargetAgentID: &id,
				Name: sc.Name, Spec: sc.Spec, TZ: sc.TZ, Instruction: sc.Instruction,
				OnMiss: sc.OnMiss, Enabled: sc.Enabled,
			}); err != nil {
				missing = append(missing, fmt.Sprintf("the clock %q: %v", sc.Name, err))
			}
		}
	}

	if st.Planboards != nil {
		// Detach everything first, so a plan attached since the version was
		// taken goes away with the rollback. Unbinding drops the position with
		// the attachment, which is what makes the restore below authoritative
		// rather than merged with whatever was there.
		bindings, err := st.Planboards.Bindings(ctx, wsID)
		if err != nil {
			return nil, err
		}
		for _, b := range bindings {
			if err := st.Planboards.Unbind(ctx, b.PlanboardID, wsID, b.AgentID); err != nil {
				return nil, err
			}
		}
		for _, pb := range snap.Planboards {
			plan, err := st.Planboards.GetByName(ctx, pb.Name)
			if err != nil {
				// The plan itself is global, like a gear: a version records
				// that it was attached here, not the plan's own text. Deleted
				// from the catalogue, it cannot come back by rolling a
				// workflow back, and saying so beats restoring silently
				// without it.
				missing = append(missing, fmt.Sprintf("the plan %q is no longer in the catalogue, so it was not re-attached", pb.Name))
				continue
			}
			var agentID *int64
			if pb.Agent != "" {
				id, ok := byName[pb.Agent]
				if !ok {
					missing = append(missing, fmt.Sprintf("the plan %q was attached to %q, and there is no such agent now", pb.Name, pb.Agent))
					continue
				}
				agentID = &id
			}
			b, err := st.Planboards.Bind(ctx, plan.ID, wsID, agentID)
			if err != nil {
				missing = append(missing, fmt.Sprintf("the plan %q: %v", pb.Name, err))
				continue
			}
			// The marker, not only the plan. SetState rather than Seek: a
			// position that was legal when it was recorded must go back even
			// if the plan has been shortened since, and the pass count is part
			// of what happened.
			if err := st.Planboards.SetState(ctx, b.ID, pb.Step, pb.Cycle); err != nil {
				missing = append(missing, fmt.Sprintf("the plan %q could not be put back on step %d: %v", pb.Name, pb.Step, err))
			}
		}
	}

	return missing, nil
}

// Summary is one line describing what a snapshot holds, for a list somebody
// scans rather than reads.
func (s Snapshot) Summary() string {
	parts := []string{plural(len(s.Agents), "agent", "agents")}
	if n := len(s.Wires); n > 0 {
		parts = append(parts, plural(n, "wire", "wires"))
	}
	if n := len(s.Gears); n > 0 {
		parts = append(parts, plural(n, "gear", "gears"))
	}
	if n := len(s.Context); n > 0 {
		parts = append(parts, plural(n, "document", "documents"))
	}
	if n := len(s.Schedules); n > 0 {
		parts = append(parts, plural(n, "clock", "clocks"))
	}
	return strings.Join(parts, ", ")
}

func plural(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, many)
}
