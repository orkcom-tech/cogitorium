package server

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/orkcom-tech/cogitorium/internal/library"
)

// A graph is nodes and edges with a `kind` on each, so the canvas can draw
// what something is without guessing from its shape. It is assembled here
// rather than by the client making a dozen calls and stitching them: the
// relationships are the point, and they are known here.
type graphNode struct {
	ID      string `json:"id"`
	Kind    string `json:"kind"`
	Label   string `json:"label"`
	Detail  string `json:"detail,omitempty"`
	Status  string `json:"status,omitempty"`
	AgentID *int64 `json:"agent_id,omitempty"`
}

type graphEdge struct {
	From  string `json:"from"`
	To    string `json:"to"`
	Kind  string `json:"kind"`
	Label string `json:"label,omitempty"`
	// ID identifies the underlying binding or wire, so an edge can be cut
	// from the canvas rather than only looked at.
	ID *int64 `json:"id,omitempty"`
}

type graph struct {
	Nodes []graphNode `json:"nodes"`
	Edges []graphEdge `json:"edges"`
}

// There is exactly one shared branch per workspace, so it needs no numbering.
const sharedNodeID = "s-shared"

// handleWorkspaceGraph returns one workspace as a graph: who delegates to
// whom, which tools each agent may call, and what each one knows. Three
// relationships that are easy to hold separately in the head and hard to
// hold together — which is why they belong on one canvas as layers.
func (s *Server) handleWorkspaceGraph(w http.ResponseWriter, r *http.Request) {
	id, ok := s.workspaceScoped(w, r)
	if !ok {
		return
	}
	ctx := r.Context()

	ws, err := s.workspaces.GetWorkspace(ctx, id)
	if err != nil {
		fail(w, r, err)
		return
	}
	agents, err := s.workspaces.ListAgents(ctx, id)
	if err != nil {
		fail(w, r, err)
		return
	}
	wires, err := s.workspaces.ListWires(ctx, id)
	if err != nil {
		fail(w, r, err)
		return
	}
	gearBindings, err := s.gears.ListBindings(ctx, id)
	if err != nil {
		fail(w, r, err)
		return
	}
	gears, err := s.gears.List(ctx, "", "")
	if err != nil {
		fail(w, r, err)
		return
	}
	ctxBindings, err := s.workspaces.ListContextBindings(ctx, id)
	if err != nil {
		fail(w, r, err)
		return
	}

	g := graph{Nodes: []graphNode{}, Edges: []graphEdge{}}
	for _, a := range agents {
		agentID := a.ID
		detail := a.ModelLabel
		if detail == "" {
			detail = "no model"
		}
		g.Nodes = append(g.Nodes, graphNode{
			ID: agentNodeID(a.ID), Kind: "agent", Label: a.Name, Detail: detail, AgentID: &agentID,
		})
	}
	for _, wire := range wires {
		wireID := wire.ID
		g.Edges = append(g.Edges, graphEdge{
			From: agentNodeID(wire.FromAgentID), To: agentNodeID(wire.ToAgentID),
			Kind: "delegates", Label: wire.Label, ID: &wireID,
		})
	}

	gearByID := map[int64]string{}
	gearStatus := map[int64]string{}
	for _, gr := range gears {
		gearByID[gr.ID] = gr.Name
		gearStatus[gr.ID] = gr.Status
	}
	placedGear := map[int64]bool{}
	for _, b := range gearBindings {
		if !placedGear[b.GearID] {
			placedGear[b.GearID] = true
			g.Nodes = append(g.Nodes, graphNode{
				ID: gearNodeID(b.GearID), Kind: "gear",
				Label: gearByID[b.GearID], Status: gearStatus[b.GearID],
			})
		}
		bindingID := b.ID
		if b.AgentID == nil {
			// Workspace-wide: it reaches every agent, so say so once rather
			// than drawing an edge to each and drowning the canvas.
			continue
		}
		g.Edges = append(g.Edges, graphEdge{
			From: gearNodeID(b.GearID), To: agentNodeID(*b.AgentID), Kind: "tool", ID: &bindingID,
		})
	}

	// Memory: the branches every agent reads implicitly, plus documents
	// bound explicitly. Branches are drawn as one node each rather than one
	// per file — the question a reader has is "what feeds this agent", not
	// "how many files are in it".
	if ws.Branch != "" {
		g.Nodes = append(g.Nodes, graphNode{
			ID: sharedNodeID, Kind: "shared", Label: "workspace memory", Detail: ws.SharedBranch(),
		})
		for _, a := range agents {
			g.Edges = append(g.Edges, graphEdge{From: sharedNodeID, To: agentNodeID(a.ID), Kind: "knows"})
		}
	}
	for _, a := range agents {
		if a.Branch == "" {
			continue
		}
		g.Nodes = append(g.Nodes, graphNode{
			ID: branchNodeID(a.ID), Kind: "private", Label: a.Name + "'s memory", Detail: a.Branch,
		})
		g.Edges = append(g.Edges, graphEdge{From: branchNodeID(a.ID), To: agentNodeID(a.ID), Kind: "knows"})
	}
	for _, b := range ctxBindings {
		kind := "document"
		if strings.HasPrefix(b.Path, library.Root+"/") {
			kind = "instruction"
		}
		nodeID := "d-" + b.Path
		if !containsNode(g.Nodes, nodeID) {
			g.Nodes = append(g.Nodes, graphNode{
				ID: nodeID, Kind: kind, Label: shortPath(b.Path), Detail: b.Path,
			})
		}
		bindingID := b.ID
		if b.AgentID == nil {
			for _, a := range agents {
				g.Edges = append(g.Edges, graphEdge{From: nodeID, To: agentNodeID(a.ID), Kind: "knows", ID: &bindingID})
			}
			continue
		}
		g.Edges = append(g.Edges, graphEdge{From: nodeID, To: agentNodeID(*b.AgentID), Kind: "knows", ID: &bindingID})
	}

	writeJSON(w, http.StatusOK, g)
}

// handleAccessMap is the install one level up: who belongs to which team, and
// which workspaces each can reach. The same relationships the permission
// checks use, drawn instead of inferred from three screens.
//
// SCOPE IS THE WHOLE SECURITY MODEL OF THIS ROUTE, and it is enforced here
// rather than in the browser. An administrator sees the install. Anybody else
// sees only what they can already reach: their own teams, the people they
// share a team with, and the workspaces visible to them.
//
// Filtering this in the client would not be a smaller version of the same
// thing — the payload would still name every workspace on the server, and a
// member reading one HTTP response would learn the shape of rooms they have no
// grant on. The test that matters greps the raw JSON for a name, not the
// rendered graph.
func (s *Server) handleAccessMap(w http.ResponseWriter, r *http.Request) {
	caller := callerFrom(r.Context())
	admin := caller.Role == "admin"
	ctx := r.Context()

	users, err := s.identity.ListUsers(ctx)
	if err != nil {
		fail(w, r, err)
		return
	}
	teams, err := s.identity.ListTeams(ctx)
	if err != nil {
		fail(w, r, err)
		return
	}
	// The one call that already answers "which of these may this person use",
	// so the map and the workspace list can never disagree about it.
	workspaces, err := s.workspaces.ListWorkspacesFor(ctx, caller)
	if err != nil {
		fail(w, r, err)
		return
	}

	if !admin {
		// Teams the caller is in, and nothing else. A team they are not in is
		// not merely uninteresting — its name and size are somebody else's.
		mine := map[int64]bool{}
		for _, t := range caller.Teams {
			mine[t] = true
		}
		kept := teams[:0]
		for _, t := range teams {
			if mine[t.ID] {
				kept = append(kept, t)
			}
		}
		teams = kept

		// People are kept when the caller shares a team with them, or owns
		// nothing in common but is looking at themselves. Everyone else is
		// dropped: a member has no business enumerating the user table.
		visible := users[:0]
		for _, u := range users {
			keep := u.ID == caller.ID
			for _, t := range u.Teams {
				if mine[t] {
					keep = true
				}
			}
			if keep {
				visible = append(visible, u)
			}
		}
		users = visible
	}

	// After filtering, an edge may point at something that is no longer here —
	// a workspace owned by somebody the caller cannot see, a membership in a
	// team that was dropped. An edge to a missing node is worse than no edge:
	// it names the thing it points at.
	shownUser := map[int64]bool{}
	for _, u := range users {
		shownUser[u.ID] = true
	}
	shownTeam := map[int64]bool{}
	for _, t := range teams {
		shownTeam[t.ID] = true
	}

	g := graph{Nodes: []graphNode{}, Edges: []graphEdge{}}
	for _, u := range users {
		g.Nodes = append(g.Nodes, graphNode{ID: userNodeID(u.ID), Kind: "user", Label: u.Name, Detail: u.Role})
	}
	for _, t := range teams {
		g.Nodes = append(g.Nodes, graphNode{ID: teamNodeID(t.ID), Kind: "team", Label: t.Name})
	}
	for _, ws := range workspaces {
		// `users` is already the filtered set, so an owner the caller cannot
		// see simply never matches and the workspace reads as unowned rather
		// than naming a stranger.
		owner := "unowned"
		for _, u := range users {
			if ws.OwnerID != nil && u.ID == *ws.OwnerID {
				owner = "owner: " + u.Name
			}
		}
		g.Nodes = append(g.Nodes, graphNode{
			ID: workspaceNodeID(ws.ID), Kind: "workspace", Label: ws.Name, Detail: owner,
		})
		if ws.OwnerID != nil && shownUser[*ws.OwnerID] {
			g.Edges = append(g.Edges, graphEdge{From: userNodeID(*ws.OwnerID), To: workspaceNodeID(ws.ID), Kind: "owns"})
		}
		// One edge per team, so the map shows every path in rather than only
		// the first one granted.
		for _, t := range ws.TeamIDs {
			if !shownTeam[t] {
				continue
			}
			g.Edges = append(g.Edges, graphEdge{From: teamNodeID(t), To: workspaceNodeID(ws.ID), Kind: "shared"})
		}
	}
	for _, u := range users {
		for _, teamID := range u.Teams {
			if !shownTeam[teamID] {
				continue
			}
			g.Edges = append(g.Edges, graphEdge{From: userNodeID(u.ID), To: teamNodeID(teamID), Kind: "member"})
		}
	}

	writeJSON(w, http.StatusOK, g)
}

// Node ids share the blueprint editor's namespacing, so the memory layer can
// be dropped straight onto the existing canvas and its edges land on the
// agent nodes already drawn there.
func agentNodeID(id int64) string     { return "a-" + itoa(id) }
func gearNodeID(id int64) string      { return "g-" + itoa(id) }
func branchNodeID(id int64) string    { return "m-" + itoa(id) }
func userNodeID(id int64) string      { return "u-" + itoa(id) }
func teamNodeID(id int64) string      { return "t-" + itoa(id) }
func workspaceNodeID(id int64) string { return "w-" + itoa(id) }

func itoa(id int64) string { return strconv.FormatInt(id, 10) }

func containsNode(nodes []graphNode, id string) bool {
	for _, n := range nodes {
		if n.ID == id {
			return true
		}
	}
	return false
}

// shortPath keeps a node label readable; the full path stays in Detail.
func shortPath(p string) string {
	parts := strings.Split(p, "/")
	return strings.TrimSuffix(parts[len(parts)-1], ".md")
}
