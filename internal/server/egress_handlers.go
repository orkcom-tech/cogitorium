package server

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/orkcom-tech/cogitorium/internal/egress"
	"github.com/orkcom-tech/cogitorium/internal/websearch"
	"github.com/orkcom-tech/cogitorium/internal/workspace"
)

// The HTTP surface of the internet gate.
//
// One rule shapes all of it: after authentication, the approval path touches
// no database at all. The turn it releases is parked on a channel while the
// single SQLite connection stays free, and an approval handler that wanted a
// write would be the thing that deadlocks it.

type egressStatus struct {
	Enabled     bool   `json:"enabled"`
	Reason      string `json:"reason"`
	Destination string `json:"destination"`
	Killed      bool   `json:"killed"`
	KilledBy    string `json:"killed_by,omitempty"`
	KilledAt    string `json:"killed_at,omitempty"`
	// Sandboxed is admin-only: whether gears run isolated is a fact about
	// this install that a non-admin does not get to learn from an open
	// endpoint.
	Sandboxed *bool `json:"sandboxed,omitempty"`
}

func (s *Server) handleEgressStatus(w http.ResponseWriter, r *http.Request) {
	st := egressStatus{Destination: websearch.Destination()}
	switch {
	case s.searcher == nil:
		st.Reason = "off — set egress: true in this server's configuration and restart"
	case s.egressOff.Load():
		st.Enabled = false
		st.Killed = true
		st.KilledBy = s.egressKilledBy
		st.KilledAt = s.egressKilledAt
		st.Reason = "switched off at runtime by " + s.egressKilledBy + " — restart to restore"
	default:
		st.Enabled = true
		st.Reason = "on — agents may ask to search the web, one query at a time, " +
			"and every one waits for you to approve it"
	}
	if u := callerFrom(r.Context()); u.IsAdmin() {
		sandboxed := s.gearExec.Sandboxed()
		st.Sandboxed = &sandboxed
	}
	writeJSON(w, http.StatusOK, st)
}

// handleEgressKill is close-only. There is no route that opens the gate: the
// master switch lives in a file on the operator's disk, so the worst this can
// do is refuse faster, and it adds no authority to anyone who calls it.
func (s *Server) handleEgressKill(w http.ResponseWriter, r *http.Request) {
	u, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	s.egressOff.Store(true)
	s.egressKilledBy = u.Name
	s.egressKilledAt = time.Now().UTC().Format(time.RFC3339)
	slog.Warn("egress switched off at runtime", "by", u.Name)
	writeJSON(w, http.StatusOK, map[string]any{
		"killed": true,
		"notice": "no further searches will be issued. Restart the server to restore the gate.",
	})
}

func (s *Server) handleListEgressGrants(w http.ResponseWriter, r *http.Request) {
	id, ok := s.workspaceScoped(w, r)
	if !ok {
		return
	}
	grants, err := s.workspaces.ListEgressGrants(r.Context(), id)
	if err != nil {
		fail(w, r, err)
		return
	}
	reach, err := s.egressReach(r.Context(), id, grants)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"enabled":     s.searcher != nil && !s.egressOff.Load(),
		"destination": websearch.Destination(),
		"grants":      grants,
		"reach":       reach,
	})
}

// egressReach answers the question a canvas with thirty agents cannot: who
// ELSE ends up with a path outward, by delegation. Asking an operator to read
// the transitive closure off a hairball of model-created wires is not a
// control, so the server computes it and the UI states it in a sentence.
func (s *Server) egressReach(ctx context.Context, wsID int64, grants []workspace.EgressGrant) (map[string][]string, error) {
	if len(grants) == 0 {
		return map[string][]string{}, nil
	}
	agents, err := s.workspaces.ListAgents(ctx, wsID)
	if err != nil {
		return nil, err
	}
	wires, err := s.workspaces.ListWires(ctx, wsID)
	if err != nil {
		return nil, err
	}
	name := map[int64]string{}
	for _, a := range agents {
		name[a.ID] = a.Name
	}
	// An edge from A to B means A may delegate to B, so reaching a granted
	// agent means reaching outward through it.
	callers := map[int64][]int64{}
	for _, wire := range wires {
		callers[wire.ToAgentID] = append(callers[wire.ToAgentID], wire.FromAgentID)
	}

	out := map[string][]string{}
	for _, g := range grants {
		seen := map[int64]bool{g.AgentID: true}
		queue := []int64{g.AgentID}
		indirect := []string{}
		for len(queue) > 0 {
			cur := queue[0]
			queue = queue[1:]
			for _, up := range callers[cur] {
				if seen[up] {
					continue
				}
				seen[up] = true
				queue = append(queue, up)
				indirect = append(indirect, name[up])
			}
		}
		out[g.AgentName] = indirect
	}
	return out, nil
}

func (s *Server) handleGrantEgress(w http.ResponseWriter, r *http.Request) {
	id, ok := s.workspaceScoped(w, r)
	if !ok {
		return
	}
	u, ok := s.requireHuman(w, r)
	if !ok {
		return
	}
	var in struct {
		AgentID int64 `json:"agent_id"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	agent, err := s.workspaces.GetAgent(r.Context(), in.AgentID)
	if err != nil {
		fail(w, r, err)
		return
	}
	if agent.WorkspaceID != id {
		writeError(w, http.StatusBadRequest, "that agent belongs to another workspace")
		return
	}
	paths, err := s.workspaces.BoundContextPaths(r.Context(), id, agent.ID)
	if err != nil {
		fail(w, r, err)
		return
	}
	// The fingerprint is taken now, from the agent the human is looking at.
	// It is what makes this a grant to a REVIEWED agent rather than to an id.
	fp := workspace.Fingerprint(agent.Role, agent.ModelID, paths)
	g, err := s.workspaces.GrantEgress(r.Context(), id, agent.ID, fp, &u.user.ID, u.auth)
	if err != nil {
		fail(w, r, err)
		return
	}
	slog.Warn("internet gate granted", "workspace_id", id, "agent", agent.Name, "by", u.user.Name, "auth", u.auth)
	writeJSON(w, http.StatusCreated, g)
}

func (s *Server) handleRevokeEgress(w http.ResponseWriter, r *http.Request) {
	id, ok := s.nestedScoped(w, r, func(grantID int64) (int64, error) {
		return s.workspaces.WorkspaceOfEgressGrant(r.Context(), grantID)
	})
	if !ok {
		return
	}
	if _, ok := s.requireHuman(w, r); !ok {
		return
	}
	if err := s.workspaces.RevokeEgress(r.Context(), id); err != nil {
		fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleEgressPending is what the modal polls. The SSE event is a latency
// optimisation; this is the mechanism, so a buffering reverse proxy that
// swallows the stream costs a second of delay rather than the whole feature.
func (s *Server) handleEgressPending(w http.ResponseWriter, r *http.Request) {
	id, ok := s.workspaceScoped(w, r)
	if !ok {
		return
	}
	req := s.broker.Pending()
	if req == nil || req.WorkspaceID != id {
		writeJSON(w, http.StatusOK, nil)
		return
	}
	writeJSON(w, http.StatusOK, req)
}

// handleEgressApproval records one human decision.
//
// Order matters: resolve the pending request FIRST so the workspace it
// belongs to is known, then run the ordinary access check against that
// workspace, then require a human. A wrong principal gets 403, which is
// distinguishable from a stale token's 404 — the operator whose click lost a
// race deserves to know which happened.
func (s *Server) handleEgressApproval(w http.ResponseWriter, r *http.Request) {
	// PathValue, never pathID: the token is opaque hex and ParseInt would
	// reject every approval with a 400.
	tok := egress.Token(r.PathValue("token"))
	if tok == "" {
		writeError(w, http.StatusBadRequest, "an approval token is required")
		return
	}
	pending := s.broker.Pending()
	if pending == nil || pending.Token != tok {
		writeError(w, http.StatusNotFound, "that approval is no longer waiting — it may have timed out or been answered")
		return
	}

	// A saturated single connection must fail visibly rather than eat the
	// operator's sixty seconds and present as "I clicked and nothing happened".
	authCtx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	if !s.requireWorkspaceCtx(authCtx, w, r, pending.WorkspaceID) {
		return
	}
	u, ok := s.requireHuman(w, r)
	if !ok {
		return
	}

	var in struct {
		Allow bool `json:"allow"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}

	// From here on, no database work happens on this request at all. The
	// woken turn writes the single audit row, carrying this identity with it.
	_, err := s.broker.Resolve(tok, egress.Answer{
		Allow: in.Allow, UserID: u.user.ID, UserName: u.user.Name, Auth: u.auth, At: time.Now().UTC(),
	})
	switch {
	case errors.Is(err, egress.ErrUnknown):
		writeError(w, http.StatusNotFound, "that approval is no longer waiting")
	case errors.Is(err, egress.ErrAlreadyClosed):
		writeError(w, http.StatusConflict, "that approval was already answered")
	case errors.Is(err, egress.ErrTimedOut):
		writeError(w, http.StatusGone, "that approval expired before this answer arrived")
	case err != nil:
		fail(w, r, err)
	default:
		slog.Info("egress decision", "allow", in.Allow, "by", u.user.Name, "auth", u.auth,
			"workspace_id", pending.WorkspaceID, "agent", pending.AgentName)
		writeJSON(w, http.StatusOK, map[string]any{"allow": in.Allow})
	}
}

func (s *Server) handleEgressLog(w http.ResponseWriter, r *http.Request) {
	id, ok := s.workspaceScoped(w, r)
	if !ok {
		return
	}
	limit := 100
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := parsePositive(v); err == nil && n <= 500 {
			limit = n
		}
	}
	rows, err := s.workspaces.ListSearches(r.Context(), id, limit)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, rows)
}
