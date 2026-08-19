package plugin

import (
	"fmt"
	"sort"
	"strings"
)

// Deciding what layers after what.
//
// plugins.order is the operator's list and stays that way — it is a file
// somebody edits, and a program that silently rewrote it would be arguing with
// them through a text file. What this does is compute the order actually used
// to compose, repairing it where a plugin would otherwise land ahead of one it
// wraps, and saying what it moved and why.
//
// Position is precedence: a plugin later in the list renders instead of one
// earlier when both define the same name. So "A requires B" means A must come
// AFTER B — otherwise A's wrapper is the thing being wrapped, which renders
// without error and is nearly impossible to diagnose from the screen.

// Move is one repair, in the words an operator needs.
type Move struct {
	ID string
	// After is what it had to follow.
	After string
	// Why distinguishes a declared edge from a derived one, because an
	// operator who did not write the edge should be told where it came from.
	Why string
}

// CycleError is a requires: loop. Refused rather than broken arbitrarily: any
// tie-break here would pick a winner nobody chose, and the plugin that lost
// would render its wrapped body in the wrong place with nothing to read.
type CycleError struct{ IDs []string }

func (e CycleError) Error() string {
	return fmt.Sprintf("these plugins require each other in a loop, so no layer order satisfies them: %s",
		strings.Join(e.IDs, " → "))
}

// LayerOrder returns the order to compose in, and what it had to move.
//
// The operator's list is the tie-break, not the rule: everything they wrote
// keeps its relative position except where an edge forces otherwise, so a
// hand-written order stays recognisably theirs.
func LayerOrder(enabled []Installed, saved []string) ([]string, []Move, error) {
	present := map[string]Installed{}
	for _, in := range enabled {
		present[in.ID] = in
	}

	pos := map[string]int{}
	for i, id := range saved {
		pos[id] = i
	}
	rank := func(id string) int {
		if p, ok := pos[id]; ok {
			return p
		}
		return len(saved) // not in the file: after everything that is
	}

	// Edges: dependency -> dependents.
	after := map[string][]string{}
	indegree := map[string]int{}
	reason := map[string]Move{}
	for _, in := range enabled {
		indegree[in.ID] += 0
	}

	edge := func(dependent, dependency, why string) {
		if dependent == dependency {
			return
		}
		if _, ok := present[dependency]; !ok {
			// A companion that is not installed. Ordinary, and not an error:
			// the plugin simply has nothing to layer after.
			return
		}
		for _, existing := range after[dependency] {
			if existing == dependent {
				return
			}
		}
		after[dependency] = append(after[dependency], dependent)
		indegree[dependent]++
		if _, seen := reason[dependent]; !seen {
			reason[dependent] = Move{ID: dependent, After: dependency, Why: why}
		}
	}

	for _, in := range enabled {
		if in.Broken != nil {
			continue
		}
		for _, dep := range in.Manifest.Requires {
			edge(in.ID, dep, "its manifest requires it")
		}
		// Derived, from what the manifest says it overrides. A name belongs to
		// the namespace it starts with, so overriding somebody else's name is
		// a statement that they have to be there first. This is the one thing
		// `overrides:` earns: it is otherwise advisory, since the host
		// computes what a plugin actually overrode from what it shipped.
		for _, name := range in.Manifest.Overrides {
			ns, _, ok := strings.Cut(name, ".")
			if !ok || ns == in.ID || ns == CoreNamespace {
				continue
			}
			edge(in.ID, ns, fmt.Sprintf("it overrides %s, which %s owns", name, ns))
		}
	}

	// Kahn, with the operator's own order as the tie-break so the result is
	// still recognisably the list they wrote.
	var ready []string
	for id, d := range indegree {
		if d == 0 {
			ready = append(ready, id)
		}
	}
	sortByRank := func(ids []string) {
		sort.SliceStable(ids, func(a, b int) bool {
			ra, rb := rank(ids[a]), rank(ids[b])
			if ra != rb {
				return ra < rb
			}
			return ids[a] < ids[b]
		})
	}
	sortByRank(ready)

	out := make([]string, 0, len(enabled))
	for len(ready) > 0 {
		id := ready[0]
		ready = ready[1:]
		out = append(out, id)

		var freed []string
		for _, dependent := range after[id] {
			indegree[dependent]--
			if indegree[dependent] == 0 {
				freed = append(freed, dependent)
			}
		}
		ready = append(ready, freed...)
		sortByRank(ready)
	}

	if len(out) != len(indegree) {
		var stuck []string
		for id, d := range indegree {
			if d > 0 {
				stuck = append(stuck, id)
			}
		}
		sort.Strings(stuck)
		return nil, nil, CycleError{IDs: stuck}
	}

	// Report only what actually moved. An edge that was already satisfied by
	// the operator's own list is not news.
	var moves []Move
	final := map[string]int{}
	for i, id := range out {
		final[id] = i
	}
	for id, m := range reason {
		if rank(id) < rank(m.After) {
			moves = append(moves, m)
		}
	}
	sort.Slice(moves, func(a, b int) bool { return final[moves[a].ID] < final[moves[b].ID] })
	return out, moves, nil
}
