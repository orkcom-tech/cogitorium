package engine

import (
	"strings"
	"testing"

	"github.com/orkcom-tech/cogitorium/internal/workspace"
)

// A built-in tool must never be named gear_something.
//
// The prefix belongs to forged gears, so a built-in that takes it is not
// refused — it is silently routed to the gear runner, which looks for a gear
// by the rest of the name and answers "not available to you, it is unbound or
// awaiting approval". That sentence is true of a gear and meaningless about a
// built-in, and it is what a tool named gear_revoke actually produced: the
// call did nothing and the model was told something that could not be acted
// on.
//
// Built-ins are verb-first — forge_gear, grant_gear, revoke_gear — and this is
// what keeps them that way.
func TestNoBuiltInTakesTheGearPrefix(t *testing.T) {
	e := &Engine{}
	for _, tool := range e.toolsFor(workspace.Agent{Kind: "orchestrator"}, nil, nil, nil, true, true) {
		if strings.HasPrefix(tool.Name, gearToolPrefix) {
			t.Errorf("built-in tool %q takes the %q prefix, which belongs to forged gears.\n"+
				"It will be routed to the gear runner and answer a sentence about a gear that does not exist.\n"+
				"Name it verb-first instead: %s.",
				tool.Name, gearToolPrefix, verbFirst(tool.Name))
		}
	}
}

func verbFirst(name string) string {
	rest := strings.TrimPrefix(name, gearToolPrefix)
	return rest + "_gear"
}
