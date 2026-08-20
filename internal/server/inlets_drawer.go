package server

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/orkcom-tech/cogitorium/internal/view"
)

// The receivers drawer.
//
// A receiver is an address and a key: data arrives by HTTP, an agent works on
// it, and the result comes back on the same response. The payload is checked
// against a schema before any model is called, so a malformed request costs
// nothing.
//
// A key is shown once, in the response that issues it, and only its hash is
// kept. The panel says so before one appears rather than after — somebody who
// has already closed it cannot be told.

func (s *Server) inletsModel(r *http.Request, wsID int64, problem, notice string, justIssued map[int64]string) view.Inlets {
	model := view.Inlets{
		Ctx:          s.viewCtx(r, callerFrom(r.Context())),
		Error:        problem,
		Notice:       notice,
		CreateAction: "/workspaces/" + strconv.FormatInt(wsID, 10) + "/receivers",
	}
	if s.inlets == nil {
		model.Error = "this install serves no receivers"
		return model
	}

	// Who could do the work, for the task form. Names rather than ids: a task
	// names its agent, which is the pair somebody reads on the screen.
	if agents, err := s.workspaces.ListAgents(r.Context(), wsID); err == nil {
		for _, a := range agents {
			model.Agents = append(model.Agents, a.Name)
		}
	}

	list, err := s.inlets.ListInlets(r.Context(), wsID)
	if err != nil {
		model.Error = err.Error()
		return model
	}
	for _, in := range list {
		row := view.Inlet{
			ID: in.ID, Address: in.Address, Description: in.Description,
			// Whether a key exists, never the key.
			HasKey: in.HasKey, IssuedAt: in.KeyIssuedAt, LastUsedAt: in.KeyLastUsedAt,
			JustIssued: justIssued[in.ID],
		}
		for _, t := range in.Tasks {
			row.Tasks = append(row.Tasks, view.InletTask{
				Name: t.Name, Agent: t.AgentName, Accepts: t.Accepts,
				Instruction: t.Instruction, CallbackURL: t.CallbackURL,
			})
		}
		model.Items = append(model.Items, row)
	}

	// Every delivery is recorded before the work starts, so a run that never
	// came back still left a row — which is the whole reason this list is
	// worth showing beside the doors rather than on a page of its own.
	if runs, err := s.inlets.ListRuns(r.Context(), wsID, deliveriesShown); err == nil {
		for _, run := range runs {
			model.Runs = append(model.Runs, view.InletRun{
				Task: run.TaskName, State: run.State, Agent: run.AgentName,
				At: run.CreatedAt, Error: run.Error,
				Failed: strings.HasPrefix(run.State, "refused") ||
					run.State == "failed" || run.State == "interrupted",
			})
		}
	}
	return model
}

// deliveriesShown bounds the list. The fifty most recent are what somebody is
// looking for; anything older is still on the record and reachable through the
// API.
const deliveriesShown = 50
