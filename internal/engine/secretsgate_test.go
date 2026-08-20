package engine

import (
	"context"
	"strings"
	"testing"

	"github.com/orkcom-tech/cogitorium/internal/workspace"
)

// Reading a secret puts its plaintext into a model's context. That is the cost
// of the capability, so the switch that turns it off has to actually turn it
// off — and "off" has to mean the tools are not offered, rather than offered
// and refused. A tool a model can see is a tool it will try, and a refusal
// mid-turn is a turn spent on nothing.
func TestTheSecretsSwitchWithholdsTheTools(t *testing.T) {
	orchestrator := workspace.Agent{IsOrchestrator: true}
	e := &Engine{}

	names := func(granted bool) map[string]bool {
		out := map[string]bool{}
		for _, tool := range e.toolsFor(orchestrator, nil, nil, nil, false, granted, true, false) {
			out[tool.Name] = true
		}
		return out
	}

	off, on := names(false), names(true)

	for _, tool := range []string{"env_get", "env_set", "env_delete"} {
		if off[tool] {
			t.Errorf("%s is offered with the switch off; it must not be there at all", tool)
		}
		if !on[tool] {
			t.Errorf("%s is missing with the switch on", tool)
		}
	}

	// Names are not gated either way, and that is deliberate: an orchestrator
	// that cannot see WHICH names exist cannot tell an agent which one to
	// declare, and a name is not a secret.
	if !off["env_list"] || !on["env_list"] {
		t.Error("env_list is gated; it lists names, and a name is not a secret")
	}
}

// A worker agent never gets them, switch or no switch. It receives values the
// way a gear does — declared by name, supplied by the host, unseen.
func TestOnlyTheOrchestratorReachesNamedValues(t *testing.T) {
	e := &Engine{}
	for _, tool := range e.toolsFor(workspace.Agent{Name: "worker"}, nil, nil, nil, false, true, true, false) {
		if strings.HasPrefix(tool.Name, "env_") {
			t.Errorf("a worker agent is offered %q", tool.Name)
		}
	}
}

// Nobody told the engine, so nobody granted it.
func TestAnEngineNobodyToldGrantsNothing(t *testing.T) {
	e := &Engine{}
	if e.secretsAvailable(context.Background()) {
		t.Error("an engine with no access function handed out the operator's credentials")
	}
}
