package workspace

import (
	"context"
	"database/sql"
	"fmt"
)

// Usage is what one agent has spent. Turns counts model calls; Unreported
// counts the ones whose provider sent no usage figures, so the operator can
// tell a genuinely cheap agent from one whose provider simply stays quiet.
type Usage struct {
	AgentID      int64  `json:"agent_id"`
	InputTokens  int64  `json:"input_tokens"`
	OutputTokens int64  `json:"output_tokens"`
	Turns        int64  `json:"turns"`
	Unreported   int64  `json:"unreported_turns"`
	LastAt       string `json:"last_at,omitempty"`
}

func (u Usage) Total() int64 { return u.InputTokens + u.OutputTokens }

// RecordTurn stores what one model call cost. Recording happens after the
// call has already succeeded, so a failure here must not lose the answer —
// callers log and carry on rather than propagating.
func (s *Store) RecordTurn(ctx context.Context, wsID, agentID int64, modelLabel string, in, out int, reported bool) error {
	r := 0
	if reported {
		r = 1
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO agent_usage (workspace_id, agent_id, model_label, input_tokens, output_tokens, reported, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		wsID, agentID, modelLabel, in, out, r, now())
	if err != nil {
		return fmt.Errorf("record token usage: %w", err)
	}
	return nil
}

// UsageForWorkspace totals every agent's spend in one query, so the agent
// list can show it without N round trips. Agents that have never run are
// absent; the caller shows them as zero.
func (s *Store) UsageForWorkspace(ctx context.Context, wsID int64) (map[int64]Usage, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT agent_id,
		        COALESCE(SUM(input_tokens), 0),
		        COALESCE(SUM(output_tokens), 0),
		        COUNT(*),
		        COALESCE(SUM(CASE WHEN reported = 0 THEN 1 ELSE 0 END), 0),
		        COALESCE(MAX(created_at), '')
		   FROM agent_usage
		  WHERE workspace_id = ?
		  GROUP BY agent_id`, wsID)
	if err != nil {
		return nil, fmt.Errorf("workspace usage: %w", err)
	}
	defer rows.Close()

	out := map[int64]Usage{}
	for rows.Next() {
		var u Usage
		if err := rows.Scan(&u.AgentID, &u.InputTokens, &u.OutputTokens, &u.Turns, &u.Unreported, &u.LastAt); err != nil {
			return nil, fmt.Errorf("scan usage: %w", err)
		}
		out[u.AgentID] = u
	}
	return out, rows.Err()
}

// UsageForAgent is the same figures for one agent, zeroed if it has never run.
func (s *Store) UsageForAgent(ctx context.Context, agentID int64) (Usage, error) {
	u := Usage{AgentID: agentID}
	err := s.db.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(input_tokens), 0),
		        COALESCE(SUM(output_tokens), 0),
		        COUNT(*),
		        COALESCE(SUM(CASE WHEN reported = 0 THEN 1 ELSE 0 END), 0),
		        COALESCE(MAX(created_at), '')
		   FROM agent_usage
		  WHERE agent_id = ?`, agentID).
		Scan(&u.InputTokens, &u.OutputTokens, &u.Turns, &u.Unreported, &u.LastAt)
	if err != nil && err != sql.ErrNoRows {
		return Usage{}, fmt.Errorf("agent usage: %w", err)
	}
	return u, nil
}
