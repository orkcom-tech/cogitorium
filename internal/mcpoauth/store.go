package mcpoauth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/orkcom-tech/cogitorium/internal/secrets"
)

// Where the grants live, sealed.
//
// Every value that could be replayed — the access token, the refresh token, the
// client secret, the PKCE verifier of a flow in progress — goes through the
// same AEAD the secrets table uses, with the key id stored beside it so a
// rotated key produces an honest refusal rather than a corrupt decrypt.
//
// A Store with no key REFUSES to write. That is the whole of the position: an
// install without COGITORIUM_SECRET_KEY can still name a token by hand, and
// what it cannot do is have this server hold somebody's refresh token in the
// clear.
type Store struct {
	db  *sql.DB
	key *secrets.Key
	// client is used for discovery, exchange and refresh. One with a timeout,
	// because all three talk to somebody else's host.
	client *http.Client
}

func NewStore(db *sql.DB, key *secrets.Key) *Store {
	return &Store{db: db, key: key, client: &http.Client{Timeout: discoveryTimeout}}
}

// HTTPClient is the one used for discovery, exchange and refresh, so a caller
// doing its own probe uses the same timeouts rather than inventing them.
func (s *Store) HTTPClient() *http.Client { return s.client }

// Available reports whether this install can hold a grant at all.
func (s *Store) Available() bool { return s != nil && s.key != nil }

// Grant is one signed-in server.
type Grant struct {
	ServerID     int64
	Issuer       string
	AuthorizeURL string
	TokenURL     string
	ClientID     string
	ClientSecret string
	AccessToken  string
	RefreshToken string
	ExpiresAt    time.Time
	Scopes       []string
	Resource     string
	UpdatedAt    string
}

// Expired reports a token past its stated life, with a minute of margin.
//
// The margin is not politeness: a token that expires between the check and the
// request arrives as a 401 the caller has to recover from, and recovering costs
// a round trip that refreshing early does not.
func (g Grant) Expired() bool {
	if g.ExpiresAt.IsZero() {
		// The server did not say. That is NOT "never expires" — it means the
		// only way to find out is a 401, and the caller treats it that way.
		return false
	}
	return time.Now().UTC().After(g.ExpiresAt.Add(-time.Minute))
}

func now() string { return time.Now().UTC().Format(time.RFC3339) }

// SavePending records a flow before the browser is sent anywhere.
func (s *Store) SavePending(ctx context.Context, serverID int64, st Start) error {
	if !s.Available() {
		return ErrNoSecretKey
	}
	// The verifier is a credential for the length of the flow: whoever holds it
	// and the code can complete the exchange.
	sealedVerifier, err := s.key.Seal(st.Verifier)
	if err != nil {
		return err
	}
	sealedSecret := ""
	if st.ClientSecret != "" {
		if sealedSecret, err = s.key.Seal(st.ClientSecret); err != nil {
			return err
		}
	}
	advertised := 0
	if st.IssAdvertised {
		advertised = 1
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO mcp_oauth_pending (state, server_id, verifier, key_id, issuer, authorize_url,
		                                token_url, client_id, client_secret, scopes, resource,
		                                redirect_uri, iss_advertised, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		st.State, serverID, sealedVerifier, s.key.ID(), st.Issuer, st.AuthorizeBase,
		st.TokenURL, st.ClientID, sealedSecret, strings.Join(st.Scopes, " "), st.Resource,
		st.RedirectURI, advertised, now())
	if err != nil {
		return fmt.Errorf("record the sign-in in progress: %w", err)
	}
	return nil
}

// TakePending consumes a flow by its state, exactly once.
//
// A DELETE that returns the row rather than a read followed by a delete: the
// state is the only thing tying a callback to the request that began it, and a
// state that could be used twice is a replayable callback.
func (s *Store) TakePending(ctx context.Context, state string) (Start, int64, error) {
	if !s.Available() {
		return Start{}, 0, ErrNoSecretKey
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Start{}, 0, err
	}
	defer tx.Rollback()

	var (
		st         Start
		serverID   int64
		sealedV    string
		sealedSec  string
		keyID      string
		scopes     string
		advertised int
	)
	err = tx.QueryRowContext(ctx,
		`SELECT server_id, verifier, key_id, issuer, authorize_url, token_url, client_id,
		        client_secret, scopes, resource, redirect_uri, iss_advertised
		   FROM mcp_oauth_pending WHERE state = ?`, state).
		Scan(&serverID, &sealedV, &keyID, &st.Issuer, &st.AuthorizeBase, &st.TokenURL,
			&st.ClientID, &sealedSec, &scopes, &st.Resource, &st.RedirectURI, &advertised)
	if errors.Is(err, sql.ErrNoRows) {
		return Start{}, 0, errors.New("this sign-in was not started here, or it has already been completed")
	}
	if err != nil {
		return Start{}, 0, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM mcp_oauth_pending WHERE state = ?`, state); err != nil {
		return Start{}, 0, err
	}
	if st.Verifier, err = s.key.Open(sealedV, keyID); err != nil {
		return Start{}, 0, fmt.Errorf("this sign-in was sealed with a key this install no longer has: %w", err)
	}
	if sealedSec != "" {
		if st.ClientSecret, err = s.key.Open(sealedSec, keyID); err != nil {
			return Start{}, 0, err
		}
	}
	st.State = state
	st.Scopes = strings.Fields(scopes)
	st.IssAdvertised = advertised != 0
	return st, serverID, tx.Commit()
}

// Save writes a grant, replacing whatever was there.
func (s *Store) Save(ctx context.Context, serverID int64, st Start, t Token) error {
	if !s.Available() {
		return ErrNoSecretKey
	}
	sealedAccess, err := s.key.Seal(t.AccessToken)
	if err != nil {
		return err
	}
	sealedRefresh := ""
	if t.RefreshToken != "" {
		if sealedRefresh, err = s.key.Seal(t.RefreshToken); err != nil {
			return err
		}
	}
	sealedSecret := ""
	if st.ClientSecret != "" {
		if sealedSecret, err = s.key.Seal(st.ClientSecret); err != nil {
			return err
		}
	}
	scopes := t.Scopes
	if len(scopes) == 0 {
		// A server that did not echo the scope granted what was asked for.
		scopes = st.Scopes
	}
	expires := ""
	if !t.ExpiresAt.IsZero() {
		expires = t.ExpiresAt.UTC().Format(time.RFC3339)
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO mcp_oauth (server_id, issuer, authorize_url, token_url, client_id, client_secret,
		                        access_token, refresh_token, key_id, expires_at, scopes, resource,
		                        created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT (server_id) DO UPDATE SET
		   issuer = excluded.issuer, authorize_url = excluded.authorize_url,
		   token_url = excluded.token_url, client_id = excluded.client_id,
		   client_secret = excluded.client_secret, access_token = excluded.access_token,
		   refresh_token = excluded.refresh_token, key_id = excluded.key_id,
		   expires_at = excluded.expires_at, scopes = excluded.scopes,
		   resource = excluded.resource, updated_at = excluded.updated_at`,
		serverID, st.Issuer, st.AuthorizeBase, st.TokenURL, st.ClientID, sealedSecret,
		sealedAccess, sealedRefresh, s.key.ID(), expires, strings.Join(scopes, " "), st.Resource,
		now(), now())
	if err != nil {
		return fmt.Errorf("store the grant: %w", err)
	}
	// The tokens themselves are never logged, here or anywhere.
	slog.Info("an MCP server was signed in to", "server_id", serverID, "issuer", st.Issuer,
		"scopes", strings.Join(scopes, " "), "resource", st.Resource)
	return nil
}

// Get returns the grant for one server, or ErrNoGrant.
func (s *Store) Get(ctx context.Context, serverID int64) (Grant, error) {
	if !s.Available() {
		return Grant{}, ErrNoGrant
	}
	var (
		g                         Grant
		sealedA, sealedR, sealedS string
		keyID, expires, scopes    string
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT issuer, authorize_url, token_url, client_id, client_secret, access_token,
		        refresh_token, key_id, expires_at, scopes, resource, updated_at
		   FROM mcp_oauth WHERE server_id = ?`, serverID).
		Scan(&g.Issuer, &g.AuthorizeURL, &g.TokenURL, &g.ClientID, &sealedS, &sealedA,
			&sealedR, &keyID, &expires, &scopes, &g.Resource, &g.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Grant{}, ErrNoGrant
	}
	if err != nil {
		return Grant{}, err
	}
	g.ServerID = serverID
	g.Scopes = strings.Fields(scopes)
	if expires != "" {
		g.ExpiresAt, _ = time.Parse(time.RFC3339, expires)
	}
	if g.AccessToken, err = s.key.Open(sealedA, keyID); err != nil {
		return Grant{}, fmt.Errorf("this grant was sealed with a key this install no longer has: %w", err)
	}
	if sealedR != "" {
		if g.RefreshToken, err = s.key.Open(sealedR, keyID); err != nil {
			return Grant{}, err
		}
	}
	if sealedS != "" {
		if g.ClientSecret, err = s.key.Open(sealedS, keyID); err != nil {
			return Grant{}, err
		}
	}
	return g, nil
}

// Forget removes a grant. Used when an operator disconnects, and when a refresh
// is refused for good — a dead grant that stayed would be retried forever.
func (s *Store) Forget(ctx context.Context, serverID int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM mcp_oauth WHERE server_id = ?`, serverID)
	if err == nil {
		slog.Info("an MCP server's sign-in was removed", "server_id", serverID)
	}
	return err
}

// Bearer returns a usable access token, refreshing first when it has expired.
//
// This is what the connect path calls. A refresh is attempted only when there
// is a refresh token: an authorization server is under no obligation to issue
// one, and a grant without it simply runs out and has to be signed in again.
func (s *Store) Bearer(ctx context.Context, serverID int64) (string, error) {
	g, err := s.Get(ctx, serverID)
	if err != nil {
		return "", err
	}
	if !g.Expired() {
		return g.AccessToken, nil
	}
	if g.RefreshToken == "" {
		return "", fmt.Errorf("this server's access token expired and the authorization server issued no "+
			"refresh token, so it has to be signed in to again (%s)", g.Issuer)
	}
	t, err := Refresh(ctx, s.client, g.TokenURL, g.ClientID, g.ClientSecret, g.RefreshToken, g.Resource, g.Scopes)
	if err != nil {
		return "", fmt.Errorf("refreshing this server's access token failed, so it has to be signed in to "+
			"again: %w", err)
	}
	// A rotating authorization server issues a NEW refresh token and invalidates
	// the old one. Keeping the previous one on an answer that omitted it is
	// correct; overwriting it with an empty string would throw the grant away.
	if t.RefreshToken == "" {
		t.RefreshToken = g.RefreshToken
	}
	if err := s.Save(ctx, serverID, Start{
		Issuer: g.Issuer, AuthorizeBase: g.AuthorizeURL, TokenURL: g.TokenURL,
		ClientID: g.ClientID, ClientSecret: g.ClientSecret, Scopes: g.Scopes, Resource: g.Resource,
	}, t); err != nil {
		// The token works; only storing it failed. Handing it back is better
		// than failing a call that would have succeeded — the cost is one more
		// refresh next time.
		slog.Error("a refreshed MCP token could not be stored", "server_id", serverID, "err", err)
	}
	return t.AccessToken, nil
}

// SweepPending removes flows nobody finished.
//
// A pending row holds a PKCE verifier, so one that lived forever would be an
// ever-growing table of credentials for sign-ins that never happened.
func (s *Store) SweepPending(ctx context.Context) {
	cutoff := time.Now().UTC().Add(-pendingLife).Format(time.RFC3339)
	res, err := s.db.ExecContext(ctx, `DELETE FROM mcp_oauth_pending WHERE created_at < ?`, cutoff)
	if err != nil {
		slog.Debug("could not sweep abandoned MCP sign-ins", "err", err)
		return
	}
	if n, _ := res.RowsAffected(); n > 0 {
		slog.Info("abandoned MCP sign-ins were swept", "count", n)
	}
}

// pendingLife is how long an unfinished sign-in is kept. Long enough that
// somebody can read an authorization screen and think about it; short enough
// that an abandoned one is not a verifier sitting on disk for a day.
const pendingLife = 30 * time.Minute

// ErrNoGrant is "this server has not been signed in to", which is an ordinary
// state rather than a failure: most servers use a header by name instead.
var ErrNoGrant = errors.New("this MCP server has no OAuth grant")
