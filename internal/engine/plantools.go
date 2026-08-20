package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/orkcom-tech/cogitorium/internal/planboard"
	"github.com/orkcom-tech/cogitorium/internal/workspace"
)

// The plan tools are split in two, and the split is the point.
//
// A worker may close the step in front of it or say why it cannot. It may not
// reorder the plan, move the marker, or write a new plan — those are the
// orchestrator's, because a worker that can edit its own instructions is a
// worker with no instructions.
func (e *Engine) dispatchPlanTool(ctx context.Context, wsID int64, agent workspace.Agent, name string, args toolArgs) (string, error) {
	if e.plans == nil {
		return "", errors.New("this install has no planboards")
	}
	switch name {
	case "plan_step_done":
		a, err := e.assignment(ctx, wsID, agent, args)
		if err != nil {
			return "", err
		}
		note, err := args.str("note")
		if err != nil {
			return "", err
		}
		st, err := e.plans.Done(ctx, a.Binding.ID, note)
		if err != nil {
			return "", err
		}
		next, ok := stepTitle(a.Planboard, st.Step)
		if !ok {
			return fmt.Sprintf("step %d of %q is done", a.Current.Ordinal, a.Planboard.Name), nil
		}
		// Told, but not asked to start it. The next step belongs to the next
		// turn, which is what keeps one step one step.
		if st.Cycle > a.State.Cycle {
			return fmt.Sprintf("step %d of %q is done, and that was the last one — the plan is back at step 1 (%s) for pass %d. Do not start it now.",
				a.Current.Ordinal, a.Planboard.Name, next, st.Cycle+1), nil
		}
		return fmt.Sprintf("step %d of %q is done. Next is step %d (%s), which is not yours to start in this turn.",
			a.Current.Ordinal, a.Planboard.Name, st.Step, next), nil

	case "plan_step_blocked":
		a, err := e.assignment(ctx, wsID, agent, args)
		if err != nil {
			return "", err
		}
		reason, err := args.reqStr("reason")
		if err != nil {
			return "", err
		}
		if _, err := e.plans.Blocked(ctx, a.Binding.ID, reason); err != nil {
			return "", err
		}
		return fmt.Sprintf("recorded: step %d of %q is blocked. The plan stays where it is, and the next run is told why.",
			a.Current.Ordinal, a.Planboard.Name), nil
	}

	// Everything below is the orchestrator's. The tools are not offered to a
	// worker, and a worker that calls one anyway is told no rather than obeyed.
	if !agent.IsOrchestrator {
		return "", fmt.Errorf("%s belongs to the orchestrator: a worker follows a plan, it does not rewrite one", name)
	}

	switch name {
	case "planboard_list":
		return e.planboardList(ctx, wsID)

	case "planboard_create":
		return e.planboardCreate(ctx, wsID, agent, args)

	case "planboard_attach":
		p, agentID, err := e.planAndScope(ctx, wsID, args)
		if err != nil {
			return "", err
		}
		if _, err := e.plans.Bind(ctx, p.ID, wsID, agentID); err != nil {
			return "", err
		}
		if agentID == nil {
			return fmt.Sprintf("%q is attached to the workspace: every agent here shares one position, starting at step 1 (%s)",
				p.Name, p.Steps[0].Title), nil
		}
		return fmt.Sprintf("%q is attached, starting at step 1 (%s)", p.Name, p.Steps[0].Title), nil

	case "planboard_detach":
		p, agentID, err := e.planAndScope(ctx, wsID, args)
		if err != nil {
			return "", err
		}
		if err := e.plans.Unbind(ctx, p.ID, wsID, agentID); err != nil {
			return "", err
		}
		return fmt.Sprintf("%q is detached; where it had got to is forgotten", p.Name), nil

	case "planboard_move":
		p, agentID, err := e.planAndScope(ctx, wsID, args)
		if err != nil {
			return "", err
		}
		b, err := e.plans.Bind(ctx, p.ID, wsID, agentID)
		if err != nil {
			return "", err
		}
		if !args.has("step") {
			st, err := e.plans.Reset(ctx, b.ID)
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("%q is back at step %d (%s)", p.Name, st.Step, p.Steps[0].Title), nil
		}
		raw, err := args.str("step")
		if err != nil {
			return "", err
		}
		n, convErr := strconv.Atoi(strings.TrimSpace(raw))
		if convErr != nil {
			return "", fmt.Errorf("tool planboard_move: argument \"step\" must be a step number, got %q", raw)
		}
		st, err := e.plans.Seek(ctx, b.ID, n)
		if err != nil {
			return "", err
		}
		title, _ := stepTitle(p, st.Step)
		return fmt.Sprintf("%q is now at step %d (%s)", p.Name, st.Step, title), nil

	case "planboard_delete":
		name, err := args.reqStr("name")
		if err != nil {
			return "", err
		}
		p, err := e.plans.GetByName(ctx, name)
		if err != nil {
			return "", err
		}
		if err := e.plans.Delete(ctx, p.ID); err != nil {
			return "", err
		}
		return fmt.Sprintf("deleted the plan %q wherever it was attached", name), nil
	}
	return "", fmt.Errorf("unknown plan tool %q", name)
}

// assignment finds which plan a worker means.
//
// Naming the plan is optional because one plan is the ordinary case and making
// an agent repeat a name it was just told is ceremony. With two in front of it,
// the name is required — guessing which one the model closed would advance the
// wrong workflow, silently.
func (e *Engine) assignment(ctx context.Context, wsID int64, agent workspace.Agent, args toolArgs) (planboard.Assignment, error) {
	active, err := e.plans.Active(ctx, wsID, agent.ID)
	if err != nil {
		return planboard.Assignment{}, err
	}
	if len(active) == 0 {
		return planboard.Assignment{}, errors.New("no plan is in front of you")
	}
	name, err := args.str("plan")
	if err != nil {
		return planboard.Assignment{}, err
	}
	if name == "" {
		if len(active) == 1 {
			return active[0], nil
		}
		names := make([]string, 0, len(active))
		for _, a := range active {
			names = append(names, a.Planboard.Name)
		}
		return planboard.Assignment{}, fmt.Errorf("say which plan: %s", strings.Join(names, ", "))
	}
	for _, a := range active {
		if strings.EqualFold(a.Planboard.Name, name) {
			return a, nil
		}
	}
	return planboard.Assignment{}, fmt.Errorf("no plan named %q is in front of you", name)
}

// planAndScope resolves the plan and whose position is meant — one agent's, or
// the workspace's shared one.
func (e *Engine) planAndScope(ctx context.Context, wsID int64, args toolArgs) (planboard.Planboard, *int64, error) {
	name, err := args.reqStr("plan")
	if err != nil {
		return planboard.Planboard{}, nil, err
	}
	p, err := e.plans.GetByName(ctx, name)
	if err != nil {
		return planboard.Planboard{}, nil, err
	}
	scope, err := args.str("agent")
	if err != nil {
		return planboard.Planboard{}, nil, err
	}
	agentID, err := e.bindScope(ctx, wsID, scope)
	if err != nil {
		return planboard.Planboard{}, nil, err
	}
	return p, agentID, nil
}

func (e *Engine) planboardCreate(ctx context.Context, wsID int64, agent workspace.Agent, args toolArgs) (string, error) {
	name, err := args.reqStr("name")
	if err != nil {
		return "", err
	}
	desc, err := args.str("description")
	if err != nil {
		return "", err
	}
	mode, err := args.str("mode")
	if err != nil {
		return "", err
	}

	// Steps arrive as objects, and models also send a plain list of strings
	// when the plan is simple. Both are a list of steps in order, so both are
	// read rather than one being an error about JSON shape.
	var raw []json.RawMessage
	if err := args.decode("steps", &raw); err != nil {
		return "", err
	}
	steps := make([]planboard.Step, 0, len(raw))
	for i, item := range raw {
		var asString string
		if err := json.Unmarshal(item, &asString); err == nil {
			steps = append(steps, planboard.Step{Title: asString})
			continue
		}
		var asObject struct {
			Title string `json:"title"`
			Body  string `json:"body"`
		}
		if err := json.Unmarshal(item, &asObject); err != nil {
			return "", fmt.Errorf("tool planboard_create: step %d must be a string or an object with a title, got %s", i+1, string(item))
		}
		steps = append(steps, planboard.Step{Title: asObject.Title, Body: asObject.Body})
	}

	p, err := e.plans.Save(ctx, name, desc, nil, planboard.Mode(mode), steps, wsID, agent.ID)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("wrote the plan %q: %d steps, %s. Attach it to an agent or to the workspace before it does anything.",
		p.Name, len(p.Steps), p.Mode), nil
}

func (e *Engine) planboardList(ctx context.Context, wsID int64) (string, error) {
	all, err := e.plans.List(ctx, "", "")
	if err != nil {
		return "", err
	}
	bound, err := e.plans.Bindings(ctx, wsID)
	if err != nil {
		return "", err
	}
	type where struct {
		Agent string `json:"agent,omitempty"`
		Step  int    `json:"step"`
		Cycle int    `json:"completed_passes"`
	}
	type row struct {
		Name        string   `json:"name"`
		Description string   `json:"description,omitempty"`
		Mode        string   `json:"mode"`
		Steps       []string `json:"steps"`
		AttachedAs  []where  `json:"attached_here,omitempty"`
	}
	out := make([]row, 0, len(all))
	for _, p := range all {
		r := row{Name: p.Name, Description: p.Description, Mode: string(p.Mode)}
		for _, st := range p.Steps {
			r.Steps = append(r.Steps, st.Title)
		}
		for _, b := range bound {
			if b.PlanboardID != p.ID {
				continue
			}
			st, err := e.plans.State(ctx, b.ID)
			if err != nil {
				return "", err
			}
			w := where{Step: st.Step, Cycle: st.Cycle}
			if b.AgentID != nil {
				w.Agent = b.Agent
			} else {
				w.Agent = "(whole workspace)"
			}
			r.AttachedAs = append(r.AttachedAs, w)
		}
		out = append(out, r)
	}
	b, err := json.Marshal(out)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func stepTitle(p planboard.Planboard, ordinal int) (string, bool) {
	for _, st := range p.Steps {
		if st.Ordinal == ordinal {
			return st.Title, true
		}
	}
	return "", false
}

// hasPlan reports whether a step is actually in front of this agent, which is
// what decides whether it is offered the tools for closing one.
//
// A read failure is not a plan. The alternative — offering the tools and
// failing when they are called — spends a model turn to deliver an error.
func (e *Engine) hasPlan(ctx context.Context, wsID int64, agent workspace.Agent) bool {
	if e.plans == nil {
		return false
	}
	active, err := e.plans.Active(ctx, wsID, agent.ID)
	if err != nil {
		return false
	}
	return len(active) > 0
}
