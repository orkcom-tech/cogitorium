package server

import (
	"context"
	"database/sql"
	"net/http"
	"time"
)

// A workspace's own numbers, as JSON.
//
// The Prometheus surface this server already exposes is for an operator's
// monitoring, on its own address, and it says nothing about one workspace. This
// is the other question — what has THIS workspace spent and what is it doing
// right now — and it is answered per workspace because that is the only scope
// in which the numbers mean anything to the person looking at them.
//
// Read through the caller's own access, so a plugin drawing a chart from this
// shows exactly what the person looking at it could have seen anyway.

// MetricsSummary is one workspace's numbers.
type MetricsSummary struct {
	WorkspaceID int64 `json:"workspace_id"`
	// Tokens is spend per day, oldest first. Days with no spend are present
	// with zeroes rather than absent: a chart drawn from a series with gaps
	// draws a shape that did not happen.
	Tokens []TokenDay `json:"tokens"`
	// Agents is how many are configured and how many are working now.
	Agents AgentCount `json:"agents"`
	// Network is what left this workspace through the gate.
	Network NetworkCount `json:"network"`
	// Reported says whether the provider actually told us what turns cost. A
	// total that silently reads zero for a provider that reports nothing is a
	// lie the operator finds out about from a bill.
	Reported bool `json:"reported"`
}

type TokenDay struct {
	Day    string `json:"day"`
	Input  int64  `json:"input"`
	Output int64  `json:"output"`
}

type AgentCount struct {
	Total   int `json:"total"`
	Running int `json:"running"`
}

type NetworkCount struct {
	Requests int64 `json:"requests"`
	Bytes    int64 `json:"bytes"`
}

func (s *Server) handleWorkspaceMetrics(w http.ResponseWriter, r *http.Request) {
	wsID, ok := s.workspaceScoped(w, r)
	if !ok {
		return
	}
	ctx := r.Context()

	out := MetricsSummary{WorkspaceID: wsID}
	var err error
	if out.Tokens, out.Reported, err = tokenDays(ctx, s.db, wsID, 14); err != nil {
		writeError(w, http.StatusInternalServerError, "the token history could not be read")
		return
	}
	if out.Agents, err = agentCounts(ctx, s.db, wsID); err != nil {
		writeError(w, http.StatusInternalServerError, "the agent counts could not be read")
		return
	}
	// Network is best-effort: an install with the gate switched off has no
	// table to read, and that is an absence rather than a failure.
	out.Network, _ = networkCounts(ctx, s.db, wsID)

	writeJSON(w, http.StatusOK, out)
}

// tokenDays reads spend per day, filling the days nobody spent anything.
func tokenDays(ctx context.Context, db *sql.DB, wsID int64, days int) ([]TokenDay, bool, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT substr(created_at, 1, 10) AS day,
		       COALESCE(SUM(input_tokens), 0),
		       COALESCE(SUM(output_tokens), 0),
		       MAX(reported)
		FROM agent_usage
		WHERE workspace_id = ? AND created_at >= ?
		GROUP BY day`,
		wsID, time.Now().UTC().AddDate(0, 0, -days).Format(time.RFC3339))
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()

	seen := map[string]TokenDay{}
	reported := false
	for rows.Next() {
		var d TokenDay
		var rep int
		if err := rows.Scan(&d.Day, &d.Input, &d.Output, &rep); err != nil {
			return nil, false, err
		}
		if rep != 0 {
			reported = true
		}
		seen[d.Day] = d
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}

	out := make([]TokenDay, 0, days)
	for i := days - 1; i >= 0; i-- {
		day := time.Now().UTC().AddDate(0, 0, -i).Format("2006-01-02")
		if d, ok := seen[day]; ok {
			d.Day = day
			out = append(out, d)
			continue
		}
		out = append(out, TokenDay{Day: day})
	}
	return out, reported, nil
}

func agentCounts(ctx context.Context, db *sql.DB, wsID int64) (AgentCount, error) {
	var c AgentCount
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM agents WHERE workspace_id = ?`, wsID).Scan(&c.Total); err != nil {
		return c, err
	}
	// Running is what the queue says is in flight for this workspace. A count
	// taken from the engine's own map would be the same number one layer
	// further from anything an operator can inspect afterwards. "claimed" is
	// the queue's word for a unit somebody is working on.
	_ = db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM work WHERE workspace_id = ? AND state = 'claimed'`,
		wsID).Scan(&c.Running)
	return c, nil
}

func networkCounts(ctx context.Context, db *sql.DB, wsID int64) (NetworkCount, error) {
	var n NetworkCount
	err := db.QueryRowContext(ctx, `
		SELECT COUNT(*), COALESCE(SUM(bytes_sent + bytes_received), 0)
		FROM gear_connections WHERE workspace_id = ?`, wsID).Scan(&n.Requests, &n.Bytes)
	return n, err
}
