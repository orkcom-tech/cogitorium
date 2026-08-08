package workspace

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// The standing grant and the audit trail for the internet gate. Every method
// here does one statement and returns the connection: SQLite runs with a
// single connection, and a paused turn must never be holding it.

// EgressGrant is one agent's standing permission to ASK to go out. It is not
// permission to go out — every search still stops the turn for a human.
type EgressGrant struct {
	ID          int64  `json:"id"`
	WorkspaceID int64  `json:"workspace_id"`
	AgentID     int64  `json:"agent_id"`
	AgentName   string `json:"agent_name"`
	GrantedBy   *int64 `json:"granted_by"`
	GrantedName string `json:"granted_by_name"`
	GrantedAuth string `json:"granted_auth"`
	CreatedAt   string `json:"created_at"`
	// Stale means the agent's role, model or bound context changed after the
	// grant was reviewed, so it no longer applies and an operator must look
	// again. Surfaced rather than silently repaired.
	Stale bool `json:"stale"`
}

// SearchRecord is one attempt, whatever became of it.
type SearchRecord struct {
	ID          int64    `json:"id"`
	WorkspaceID int64    `json:"workspace_id"`
	AgentID     *int64   `json:"agent_id"`
	AgentName   string   `json:"agent_name"`
	Chain       []string `json:"chain"`
	Query       string   `json:"query"`
	BlobFlag    bool     `json:"blob_flag"`
	Overlap     string   `json:"overlap"`
	State       string   `json:"state"`
	DecidedName string   `json:"decided_name"`
	DecidedAuth string   `json:"decided_auth"`
	DecidedAt   string   `json:"decided_at"`
	HTTPStatus  *int     `json:"http_status"`
	ResultBytes *int     `json:"result_bytes"`
	Error       string   `json:"error"`
	CreatedAt   string   `json:"created_at"`
}

// Fingerprint is what makes a grant apply to a REVIEWED agent rather than to
// an id wearing a name. It covers everything that decides what the agent will
// say on the operator's behalf: its instructions, the model that writes them,
// and the documents fed into its prompt.
func Fingerprint(role string, modelID *int64, boundPaths []string) string {
	paths := append([]string(nil), boundPaths...)
	sort.Strings(paths)
	model := "none"
	if modelID != nil {
		model = fmt.Sprintf("%d", *modelID)
	}
	h := sha256.New()
	h.Write([]byte(role))
	h.Write([]byte{0})
	h.Write([]byte(model))
	h.Write([]byte{0})
	h.Write([]byte(strings.Join(paths, "\n")))
	return hex.EncodeToString(h.Sum(nil))
}

// GrantEgress records a human's decision to let one agent ask to go out.
func (s *Store) GrantEgress(ctx context.Context, wsID, agentID int64, fingerprint string, by *int64, auth string) (EgressGrant, error) {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO agent_egress (workspace_id, agent_id, fingerprint, granted_by, granted_auth, created_at)
		 VALUES (?, ?, ?, ?, ?, ?)
		 ON CONFLICT (workspace_id, agent_id)
		 DO UPDATE SET fingerprint = excluded.fingerprint, granted_by = excluded.granted_by,
		               granted_auth = excluded.granted_auth, created_at = excluded.created_at`,
		wsID, agentID, fingerprint, by, auth, now())
	if err != nil {
		return EgressGrant{}, fmt.Errorf("grant egress: %w", err)
	}
	g, err := s.EgressGrantFor(ctx, wsID, agentID)
	if err != nil {
		return EgressGrant{}, err
	}
	return g, nil
}

// EgressGrantFor returns one agent's grant, or ErrNotFound.
func (s *Store) EgressGrantFor(ctx context.Context, wsID, agentID int64) (EgressGrant, error) {
	var g EgressGrant
	var by sql.NullInt64
	var name sql.NullString
	err := s.db.QueryRowContext(ctx,
		`SELECT e.id, e.workspace_id, e.agent_id, a.name, e.granted_by, u.name, e.granted_auth, e.created_at
		   FROM agent_egress e
		   JOIN agents a ON a.id = e.agent_id
		   LEFT JOIN users u ON u.id = e.granted_by
		  WHERE e.workspace_id = ? AND e.agent_id = ?`, wsID, agentID).
		Scan(&g.ID, &g.WorkspaceID, &g.AgentID, &g.AgentName, &by, &name, &g.GrantedAuth, &g.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return EgressGrant{}, ErrNotFound
	}
	if err != nil {
		return EgressGrant{}, fmt.Errorf("egress grant: %w", err)
	}
	if by.Valid {
		g.GrantedBy = &by.Int64
	}
	g.GrantedName = name.String
	return g, nil
}

// EgressFingerprint returns the fingerprint stored when the grant was made,
// for comparison against the agent as it is now. Separate from the grant
// lookup because the hot path wants only this, in one statement.
func (s *Store) EgressFingerprint(ctx context.Context, wsID, agentID int64) (string, error) {
	var fp string
	err := s.db.QueryRowContext(ctx,
		`SELECT fingerprint FROM agent_egress WHERE workspace_id = ? AND agent_id = ?`, wsID, agentID).Scan(&fp)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("egress fingerprint: %w", err)
	}
	return fp, nil
}

// ListEgressGrants returns every grant in a workspace, marking any whose
// agent has changed since the human reviewed it.
func (s *Store) ListEgressGrants(ctx context.Context, wsID int64) ([]EgressGrant, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT e.id, e.workspace_id, e.agent_id, a.name, e.granted_by, u.name, e.granted_auth,
		        e.created_at, e.fingerprint, a.role, a.model_id
		   FROM agent_egress e
		   JOIN agents a ON a.id = e.agent_id
		   LEFT JOIN users u ON u.id = e.granted_by
		  WHERE e.workspace_id = ?
		  ORDER BY a.name`, wsID)
	if err != nil {
		return nil, fmt.Errorf("list egress grants: %w", err)
	}
	defer rows.Close()

	type pending struct {
		g       EgressGrant
		stored  string
		role    string
		modelID *int64
	}
	var found []pending
	for rows.Next() {
		var p pending
		var by, modelID sql.NullInt64
		var name sql.NullString
		if err := rows.Scan(&p.g.ID, &p.g.WorkspaceID, &p.g.AgentID, &p.g.AgentName, &by, &name,
			&p.g.GrantedAuth, &p.g.CreatedAt, &p.stored, &p.role, &modelID); err != nil {
			return nil, fmt.Errorf("scan egress grant: %w", err)
		}
		if by.Valid {
			p.g.GrantedBy = &by.Int64
		}
		if modelID.Valid {
			p.modelID = &modelID.Int64
		}
		p.g.GrantedName = name.String
		found = append(found, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// The bound-path lookup runs after the rows are closed: holding a result
	// set open while issuing another query would want a second connection.
	out := make([]EgressGrant, 0, len(found))
	for _, p := range found {
		paths, err := s.BoundContextPaths(ctx, wsID, p.g.AgentID)
		if err != nil {
			return nil, err
		}
		p.g.Stale = Fingerprint(p.role, p.modelID, paths) != p.stored
		out = append(out, p.g)
	}
	return out, nil
}

// BoundContextPaths lists the documents that reach one agent's prompt: its
// own bindings plus the workspace-wide ones.
func (s *Store) BoundContextPaths(ctx context.Context, wsID, agentID int64) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT path FROM context_bindings
		  WHERE workspace_id = ? AND (agent_id IS NULL OR agent_id = ?)
		  ORDER BY path`, wsID, agentID)
	if err != nil {
		return nil, fmt.Errorf("bound context paths: %w", err)
	}
	defer rows.Close()

	out := []string{}
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// RevokeEgress removes a grant. Deleting the wire on the canvas is the
// operator's kill switch for one agent.
func (s *Store) RevokeEgress(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM agent_egress WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("revoke egress: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// WorkspaceOfEgressGrant resolves a grant back to its workspace so the HTTP
// layer can run the same access check it runs on everything else.
func (s *Store) WorkspaceOfEgressGrant(ctx context.Context, id int64) (int64, error) {
	var wsID int64
	err := s.db.QueryRowContext(ctx, `SELECT workspace_id FROM agent_egress WHERE id = ?`, id).Scan(&wsID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	}
	return wsID, err
}

// RecordSearchAttempt writes the attempt BEFORE anything is asked of a human
// or of the network, so a crash, a refusal or a timeout still leaves the
// attempt on record. There is no path that searches without a row first.
func (s *Store) RecordSearchAttempt(ctx context.Context, wsID int64, agentID int64, agentName string,
	chain []string, token, query string, blob bool, overlap any, state string) (int64, error) {

	chainJSON, _ := json.Marshal(chain)
	overlapJSON, err := json.Marshal(overlap)
	if err != nil {
		overlapJSON = []byte("[]")
	}
	b := 0
	if blob {
		b = 1
	}
	ts := now()
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO search_queries
		   (workspace_id, agent_id, agent_name, chain, request_token, query, blob_flag, overlap, state, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		wsID, agentID, agentName, string(chainJSON), token, query, b, string(overlapJSON), state, ts, ts)
	if err != nil {
		return 0, fmt.Errorf("record search attempt: %w", err)
	}
	return res.LastInsertId()
}

// SettleSearch writes the outcome exactly once. The approval HTTP handler
// writes nothing here — the woken turn performs the single settle, carrying
// the decider's identity with it. That is what stops the two-writer failure
// where a row claims bytes left with nobody recorded as approving, or a human
// is recorded approving a search that was never issued.
func (s *Store) SettleSearch(ctx context.Context, token, state string,
	decidedBy *int64, decidedName, decidedAuth, decidedAt string,
	httpStatus, resultBytes *int, errText string) error {

	res, err := s.db.ExecContext(ctx,
		`UPDATE search_queries
		    SET state = ?, decided_by = ?, decided_name = ?, decided_auth = ?, decided_at = ?,
		        http_status = ?, result_bytes = ?, error = ?, updated_at = ?
		  WHERE request_token = ? AND state = 'awaiting'`,
		state, decidedBy, nullIfEmpty(decidedName), nullIfEmpty(decidedAuth), nullIfEmpty(decidedAt),
		httpStatus, resultBytes, errText, now(), token)
	if err != nil {
		return fmt.Errorf("settle search: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		// Already terminal. Not an error the caller can act on, but the
		// caller logs it: it means two things tried to finish one request.
		return ErrNotFound
	}
	return nil
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// SearchesSince counts what one agent actually sent in a window, for the
// rolling per-agent cap and for the figure shown in the approval dialog.
func (s *Store) SearchesSince(ctx context.Context, agentID int64, since time.Time) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT count(*) FROM search_queries
		  WHERE agent_id = ? AND state = 'sent' AND created_at > ?`,
		agentID, since.UTC().Format(time.RFC3339)).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("recent searches: %w", err)
	}
	return n, nil
}

// RecentQueriesFor returns an agent's last queries so the dialog can show
// what its normal traffic looks like — a query that breaks the pattern should
// look different, not merely be present.
func (s *Store) RecentQueriesFor(ctx context.Context, agentID int64, limit int) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT query FROM search_queries WHERE agent_id = ? AND state = 'sent'
		  ORDER BY id DESC LIMIT ?`, agentID, limit)
	if err != nil {
		return nil, fmt.Errorf("recent queries: %w", err)
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var q string
		if err := rows.Scan(&q); err != nil {
			return nil, err
		}
		out = append(out, q)
	}
	return out, rows.Err()
}

// ListSearches is the operator's log. Chunked exfiltration across turns is an
// accepted residual risk and it is only DETECTABLE if this has a screen — an
// audit table nobody can read is not an audit, it is a table.
func (s *Store) ListSearches(ctx context.Context, wsID int64, limit int) ([]SearchRecord, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, workspace_id, agent_id, agent_name, chain, query, blob_flag, overlap, state,
		        decided_name, decided_auth, decided_at, http_status, result_bytes, error, created_at
		   FROM search_queries WHERE workspace_id = ? ORDER BY id DESC LIMIT ?`, wsID, limit)
	if err != nil {
		return nil, fmt.Errorf("list searches: %w", err)
	}
	defer rows.Close()

	out := []SearchRecord{}
	for rows.Next() {
		var r SearchRecord
		var agentID sql.NullInt64
		var chainJSON string
		var blob int
		var decidedName, decidedAuth, decidedAt, errText sql.NullString
		var status, bytes sql.NullInt64
		if err := rows.Scan(&r.ID, &r.WorkspaceID, &agentID, &r.AgentName, &chainJSON, &r.Query, &blob,
			&r.Overlap, &r.State, &decidedName, &decidedAuth, &decidedAt, &status, &bytes, &errText, &r.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan search: %w", err)
		}
		if agentID.Valid {
			r.AgentID = &agentID.Int64
		}
		r.BlobFlag = blob == 1
		r.DecidedName, r.DecidedAuth, r.DecidedAt, r.Error = decidedName.String, decidedAuth.String, decidedAt.String, errText.String
		if status.Valid {
			v := int(status.Int64)
			r.HTTPStatus = &v
		}
		if bytes.Valid {
			v := int(bytes.Int64)
			r.ResultBytes = &v
		}
		_ = json.Unmarshal([]byte(chainJSON), &r.Chain)
		out = append(out, r)
	}
	return out, rows.Err()
}

// ReconcileSearches runs at startup. A pause is process-local by definition,
// so nothing left awaiting can ever be answered after a restart; saying so in
// the row is what keeps the log honest.
func (s *Store) ReconcileSearches(ctx context.Context) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE search_queries
		    SET state = 'interrupted', updated_at = ?,
		        error = 'the server restarted while this request was awaiting approval'
		  WHERE state = 'awaiting'`, now())
	if err != nil {
		return 0, fmt.Errorf("reconcile searches: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}
