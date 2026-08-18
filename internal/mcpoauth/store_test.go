package mcpoauth

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/orkcom-tech/cogitorium/internal/secrets"
	"github.com/orkcom-tech/cogitorium/internal/store"
)

// A grant is the one live credential this schema holds, so these tests are
// about it not being readable and not being replayable.

func newStore(t *testing.T, withKey bool) (*Store, *sql.DB, int64) {
	t.Helper()
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	res, err := db.Exec(
		`INSERT INTO mcp_servers (name, transport, url, status, created_at, updated_at)
		 VALUES ('remote', 'streamable-http', 'https://mcp.example.com/mcp', 'approved', datetime('now'), datetime('now'))`)
	if err != nil {
		t.Fatalf("seed a server: %v", err)
	}
	serverID, _ := res.LastInsertId()

	var key *secrets.Key
	if withKey {
		if key, err = secrets.NewKey("a-key-long-enough-to-be-accepted-here"); err != nil {
			t.Fatalf("key: %v", err)
		}
	}
	return NewStore(db, key), db, serverID
}

func aStart() Start {
	return Start{
		State: "st-1", Verifier: "the-verifier", Issuer: "https://as.example.com",
		AuthorizeBase: "https://as.example.com/a", TokenURL: "https://as.example.com/t",
		ClientID: "cid", ClientSecret: "csecret", Scopes: []string{"files:read"},
		Resource: "https://mcp.example.com/mcp", RedirectURI: "https://c.example.com/cb",
	}
}

// THE POSITION. An install with no key cannot hold a grant at all, rather than
// holding one in the clear.
func TestWithoutAKeyAGrantIsRefusedRatherThanStoredInTheClear(t *testing.T) {
	s, _, serverID := newStore(t, false)
	if s.Available() {
		t.Fatal("a store with no key reported itself available")
	}
	if err := s.SavePending(context.Background(), serverID, aStart()); err == nil {
		t.Fatal("a pending flow was recorded without a key")
	}
	if err := s.Save(context.Background(), serverID, aStart(), Token{AccessToken: "at"}); err == nil {
		t.Fatal("a grant was stored without a key")
	}
}

// Nothing replayable may be readable in the database. Somebody with the file
// and no key has ciphertext.
func TestNothingReplayableIsStoredInTheClear(t *testing.T) {
	s, db, serverID := newStore(t, true)
	ctx := context.Background()

	if err := s.SavePending(ctx, serverID, aStart()); err != nil {
		t.Fatalf("save pending: %v", err)
	}
	if err := s.Save(ctx, serverID, aStart(), Token{
		AccessToken: "the-access-token", RefreshToken: "the-refresh-token",
		ExpiresAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatalf("save: %v", err)
	}

	for _, q := range []string{
		`SELECT access_token || refresh_token || client_secret FROM mcp_oauth`,
		`SELECT verifier || client_secret FROM mcp_oauth_pending`,
	} {
		var blob string
		if err := db.QueryRow(q).Scan(&blob); err != nil {
			// The pending row is consumed below in another test; here it exists.
			continue
		}
		for _, secret := range []string{"the-access-token", "the-refresh-token", "csecret", "the-verifier"} {
			if strings.Contains(blob, secret) {
				t.Fatalf("%q is readable in the database", secret)
			}
		}
	}

	// And it comes back out.
	g, err := s.Get(ctx, serverID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if g.AccessToken != "the-access-token" || g.RefreshToken != "the-refresh-token" || g.ClientSecret != "csecret" {
		t.Fatalf("a sealed value did not survive the round trip: %+v", g)
	}
}

// The state is the only thing tying a callback to the request that began it.
// One that could be used twice is a replayable callback.
func TestAPendingFlowIsConsumedExactlyOnce(t *testing.T) {
	s, _, serverID := newStore(t, true)
	ctx := context.Background()
	if err := s.SavePending(ctx, serverID, aStart()); err != nil {
		t.Fatalf("save pending: %v", err)
	}

	got, gotServer, err := s.TakePending(ctx, "st-1")
	if err != nil {
		t.Fatalf("take: %v", err)
	}
	if got.Verifier != "the-verifier" || gotServer != serverID {
		t.Fatalf("the flow came back wrong: %+v (server %d)", got, gotServer)
	}
	if _, _, err := s.TakePending(ctx, "st-1"); err == nil {
		t.Fatal("the same state was consumed twice")
	}
}

// An expiry the server never stated is not "never expires" — it means the only
// way to find out is a 401, and pretending otherwise would make this refresh
// on every call.
func TestAnUnstatedExpiryIsNotTreatedAsExpired(t *testing.T) {
	if (Grant{}).Expired() {
		t.Fatal("a grant with no stated expiry reads as expired")
	}
	if !(Grant{ExpiresAt: time.Now().Add(-time.Hour)}).Expired() {
		t.Fatal("a token an hour past its life does not read as expired")
	}
	// The margin: a token expiring in thirty seconds would otherwise be used
	// and come back a 401.
	if !(Grant{ExpiresAt: time.Now().Add(30 * time.Second)}).Expired() {
		t.Fatal("a token expiring within the margin was treated as usable")
	}
}

// A rotating authorization server issues a NEW refresh token and kills the old
// one; one that does not, omits the field. Overwriting with an empty string
// would throw the grant away and force a fresh sign-in.
func TestARefreshThatOmitsANewRefreshTokenKeepsTheOldOne(t *testing.T) {
	s, _, serverID := newStore(t, true)
	ctx := context.Background()

	as := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "fresh", "expires_in": 3600})
	}))
	t.Cleanup(as.Close)
	s.client = as.Client()

	st := aStart()
	st.TokenURL = as.URL
	if err := s.Save(ctx, serverID, st, Token{
		AccessToken: "stale", RefreshToken: "keep-me", ExpiresAt: time.Now().Add(-time.Hour),
	}); err != nil {
		t.Fatalf("save: %v", err)
	}

	tok, err := s.Bearer(ctx, serverID)
	if err != nil {
		t.Fatalf("bearer: %v", err)
	}
	if tok != "fresh" {
		t.Fatalf("the expired token was handed out: %q", tok)
	}
	g, err := s.Get(ctx, serverID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if g.RefreshToken != "keep-me" {
		t.Fatalf("the refresh token was lost: %q", g.RefreshToken)
	}
}

// A grant with no refresh token simply runs out. Said plainly rather than
// retried forever.
func TestAnExpiredGrantWithNoRefreshTokenSaysSo(t *testing.T) {
	s, _, serverID := newStore(t, true)
	ctx := context.Background()
	if err := s.Save(ctx, serverID, aStart(), Token{
		AccessToken: "stale", ExpiresAt: time.Now().Add(-time.Hour),
	}); err != nil {
		t.Fatalf("save: %v", err)
	}
	_, err := s.Bearer(ctx, serverID)
	if err == nil || !strings.Contains(err.Error(), "signed in to again") {
		t.Fatalf("got %v", err)
	}
}

// A server nobody signed in to is an ordinary state, not a failure: most use a
// header by name instead.
func TestNoGrantIsAnOrdinaryState(t *testing.T) {
	s, _, serverID := newStore(t, true)
	if _, err := s.Get(context.Background(), serverID); err != ErrNoGrant {
		t.Fatalf("got %v, want ErrNoGrant", err)
	}
}

// A pending row holds a PKCE verifier. One that lived forever would be a table
// of credentials for sign-ins that never happened.
func TestAbandonedFlowsAreSwept(t *testing.T) {
	s, db, serverID := newStore(t, true)
	ctx := context.Background()
	if err := s.SavePending(ctx, serverID, aStart()); err != nil {
		t.Fatalf("save pending: %v", err)
	}
	if _, err := db.Exec(`UPDATE mcp_oauth_pending SET created_at = ?`,
		time.Now().UTC().Add(-2*pendingLife).Format(time.RFC3339)); err != nil {
		t.Fatalf("age it: %v", err)
	}

	s.SweepPending(ctx)

	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM mcp_oauth_pending`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Fatalf("%d abandoned flows survived the sweep", n)
	}
}

// Deleting a server takes its grant with it: a token for something that is gone
// is a credential nobody is watching.
func TestDeletingAServerTakesItsGrant(t *testing.T) {
	s, db, serverID := newStore(t, true)
	ctx := context.Background()
	if err := s.Save(ctx, serverID, aStart(), Token{AccessToken: "at"}); err != nil {
		t.Fatalf("save: %v", err)
	}
	if _, err := db.Exec(`DELETE FROM mcp_servers WHERE id = ?`, serverID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	var n int
	_ = db.QueryRow(`SELECT COUNT(*) FROM mcp_oauth`).Scan(&n)
	if n != 0 {
		t.Fatal("a grant outlived the server it belonged to")
	}
}
