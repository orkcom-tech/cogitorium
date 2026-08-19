package server

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/orkcom-tech/cogitorium/internal/view"
	"github.com/orkcom-tech/cogitorium/internal/workspace"
)

// The roster: every agent in this workspace, what it is doing and what it
// cost.
//
// This is the drawer that was hardest to move, and the reason is worth
// writing down: it is the only one whose rows drive the client. Selecting an
// agent opens a different panel, and which panel is open is the client's
// state. So the seam is drawn there — the server renders the markup, the rows
// carry their id, and the client keeps the behaviour it already owns. The
// roster becomes a template without the workspace around it having to be one.
//
// The live half moves too. The application polled the status endpoint on a
// timer; the panel polls itself now, which is the same request on the same
// interval with the assembly happening where the data already is.

func (s *Server) agentsModel(r *http.Request, wsID int64, selected int64) view.Agents {
	model := view.Agents{Ctx: s.viewCtx(r, callerFrom(r.Context()))}
	model.PollURL = fmt.Sprintf("/workspaces/%d/drawers/agents", wsID)
	if selected != 0 {
		model.PollURL += fmt.Sprintf("?selected=%d", selected)
	}

	agents, err := s.workspaces.ListAgents(r.Context(), wsID)
	if err != nil {
		model.Error = err.Error()
		return model
	}

	// One query for spend rather than one per agent, so drawing a roster does
	// not become N round trips.
	usage := map[int64]workspace.Usage{}
	if byAgent, err := s.workspaces.UsageForWorkspace(r.Context(), wsID); err == nil {
		for _, u := range byAgent {
			usage[u.AgentID] = u
		}
	}
	var total int64
	for _, u := range usage {
		total += u.Total()
	}

	state := map[int64]string{}
	detail := map[int64]string{}
	if statuses, err := s.engine.Statuses(r.Context(), wsID); err == nil {
		for _, st := range statuses {
			state[st.AgentID], detail[st.AgentID] = st.State, st.Detail
		}
	}

	for _, a := range agents {
		u := usage[a.ID]
		card := view.AgentCard{
			ID: a.ID, Name: a.Name, Orchestrator: a.IsOrchestrator,
			Model: a.ModelLabel, State: stateOf(state[a.ID]), Detail: detail[a.ID],
			Spend: spendLabel(u), SpendDetail: spendDetail(u),
			Selected: a.ID == selected,
		}
		if card.Model == "" {
			card.Model = "no model"
		}
		if total > 0 {
			card.HasSpend = true
			card.Share = int(u.Total() * 100 / total)
		}
		model.Items = append(model.Items, card)
	}
	return model
}

func stateOf(s string) string {
	if s == "" {
		return "idle"
	}
	return s
}

// spendLabel is what the card shows, in the words it showed before.
//
// Nothing rather than a dash before an agent has run: set at the size of a
// number, a dash reads as a broken element — five of them down a fresh roster
// looked like five failed fields — and the line under it already says "no
// spend yet".
func spendLabel(u workspace.Usage) string {
	if u.Turns == 0 {
		return ""
	}
	// A provider that reports nothing would otherwise show a confident 0.
	if u.Total() == 0 && u.Unreported == u.Turns {
		return "n/a"
	}
	return compactTokens(u.Total())
}

func spendDetail(u workspace.Usage) string {
	if u.Turns == 0 {
		return "has not run yet"
	}
	calls := "calls"
	if u.Turns == 1 {
		calls = "call"
	}
	lines := []string{
		fmt.Sprintf("%s in + %s out", withThousands(u.InputTokens), withThousands(u.OutputTokens)),
		fmt.Sprintf("%d model %s", u.Turns, calls),
	}
	if u.Unreported > 0 {
		lines = append(lines,
			fmt.Sprintf("%d of them reported no usage — the real spend is higher", u.Unreported))
	}
	return strings.Join(lines, "\n")
}

// compactTokens is 12.4k rather than 12,431: the card has room for a shape,
// and the exact number is in the title for whoever wants it.
func compactTokens(n int64) string {
	switch {
	case n >= 1_000_000:
		return strconv.FormatFloat(float64(n)/1e6, 'f', 1, 64) + "M"
	case n >= 1_000:
		return strconv.FormatFloat(float64(n)/1e3, 'f', 1, 64) + "k"
	}
	return strconv.FormatInt(n, 10)
}

func withThousands(n int64) string {
	s := strconv.FormatInt(n, 10)
	if len(s) <= 3 {
		return s
	}
	var out []byte
	for i, c := range []byte(s) {
		if i > 0 && (len(s)-i)%3 == 0 {
			out = append(out, ',')
		}
		out = append(out, c)
	}
	return string(out)
}
