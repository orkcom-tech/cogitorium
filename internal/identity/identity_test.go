// These tests live in identity_test, not identity, on purpose: reaching for
// hashToken, RoleAdmin or tokenPrefix would assert the code against its own
// constants and pass whatever the code happened to do. Everything below goes
// through the exported API and a real SQLite database, and compares against
// literals a person could read off the product's behaviour.
package identity_test

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"github.com/orkcom-tech/cogitorium/internal/identity"
	"github.com/orkcom-tech/cogitorium/internal/store"
)

func TestMain(m *testing.M) {
	// The package logs every migration, every login and every failed login.
	// None of it is under test and all of it buries the failure message.
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	os.Exit(m.Run())
}

// newStore opens a real database in a temp dir. There are no mocks anywhere
// in this codebase and identity is nothing but SQL, so a fake store would
// test the fake.
func newStore(t *testing.T) (*identity.Store, *sql.DB, string) {
	t.Helper()
	dir := t.TempDir()
	db, err := store.Open(dir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return identity.NewStore(db), db, dir
}

// assertNobody fails unless the call authenticated no one at all. Returning
// ErrUnauthorized matters beyond tidiness: server.authenticate turns exactly
// that error into 401 "invalid token" and turns anything else into a 500, so
// a caller with a bad token would get told the server is broken.
func assertNobody(t *testing.T, u identity.User, err error, what string) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s: authenticated as %+v, want rejection", what, u)
	}
	if !errors.Is(err, identity.ErrUnauthorized) {
		t.Errorf("%s: got %v, want ErrUnauthorized (server maps anything else to 500)", what, err)
	}
	if u.ID != 0 || u.Name != "" || u.Role != "" {
		t.Errorf("%s: rejected call still returned a user: %+v", what, u)
	}
}

func countRows(t *testing.T, db *sql.DB, query string, args ...any) int {
	t.Helper()
	var n int
	if err := db.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatalf("count (%s): %v", query, err)
	}
	return n
}

// mustNotAppearOnDisk is the point of hashing: a copied database file must be
// useless. It scans every file the store wrote, including the WAL, because a
// plaintext regression lands there first.
func mustNotAppearOnDisk(t *testing.T, dir, secret, what string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read data dir: %v", err)
	}
	scanned := 0
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		scanned++
		if bytes.Contains(b, []byte(secret)) {
			t.Errorf("%s is recoverable in plaintext from %s — stealing the database file hands over the account", what, e.Name())
		}
	}
	if scanned == 0 {
		t.Fatalf("no database files found in %s, so this check proved nothing", dir)
	}
}

// --- role rules -------------------------------------------------------------

// Every admin-only route in the server is gated by nothing but IsAdmin, so a
// role string that is merely admin-ish must not pass. Literals here, never
// identity.RoleAdmin: the constant is what the code compares against.
func TestIsAdminIsAnExactMatchOnTheAdminRole(t *testing.T) {
	t.Parallel()
	cases := []struct {
		role string
		want bool
	}{
		{"admin", true},
		{"member", false},
		{"team-lead", false},
		// A case-insensitive or prefix comparison would promote all of these.
		{"Admin", false},
		{"ADMIN", false},
		{"administrator", false},
		{"admin ", false},
		{" admin", false},
		{"not-admin", false},
		{"", false},
	}
	for _, c := range cases {
		t.Run("role="+c.role, func(t *testing.T) {
			if got := (identity.User{ID: 1, Name: "x", Role: c.role}).IsAdmin(); got != c.want {
				t.Errorf("User{Role: %q}.IsAdmin() = %v, want %v", c.role, got, c.want)
			}
		})
	}
}

// A workspace shared with a team is visible to exactly its members
// (workspace.Visible calls straight through to InTeam), so a wrong answer
// here either leaks someone else's workspace or hides your own.
func TestInTeamAnswersOnlyForTeamsTheUserBelongsTo(t *testing.T) {
	t.Parallel()
	member := identity.User{ID: 5, Name: "alice", Role: "member", Teams: []int64{7, 9}}
	loner := identity.User{ID: 6, Name: "bob", Role: "member"}

	cases := []struct {
		name string
		user identity.User
		team int64
		want bool
	}{
		{"first listed team", member, 7, true},
		{"second listed team", member, 9, true},
		{"neighbouring team id", member, 8, false},
		{"zero team id", member, 0, false},
		// The user's own id is not a team id; confusing the two would grant
		// every user the team that happens to share their number.
		{"own user id", member, 5, false},
		{"no teams at all", loner, 7, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.user.InTeam(c.team); got != c.want {
				t.Errorf("%+v.InTeam(%d) = %v, want %v", c.user, c.team, got, c.want)
			}
		})
	}
}

// The role is decided when the account is made and never re-validated, so an
// unknown role must not reach the database, and the operator who typed it
// must be told what the legal roles are.
func TestCreateUserAcceptsOnlyTheThreeKnownRoles(t *testing.T) {
	t.Parallel()
	s, _, _ := newStore(t)
	ctx := context.Background()

	for _, role := range []string{"admin", "team-lead", "member"} {
		name := "ok-" + role
		u, token, err := s.CreateUser(ctx, name, role, "")
		if err != nil {
			t.Fatalf("CreateUser(%q, %q): %v", name, role, err)
		}
		// The role must survive the round trip through the database and come
		// back on the token, because that is the copy the server authorises
		// against.
		back, err := s.Authenticate(ctx, token)
		if err != nil {
			t.Fatalf("Authenticate(%q): %v", name, err)
		}
		if back.Role != role {
			t.Errorf("user %q came back with role %q, want %q", name, back.Role, role)
		}
		if want := role == "admin"; back.IsAdmin() != want {
			t.Errorf("user %q with role %q: IsAdmin() = %v, want %v", name, role, back.IsAdmin(), want)
		}
		if back.ID != u.ID {
			t.Errorf("user %q: created id %d, authenticated id %d", name, u.ID, back.ID)
		}
	}

	for _, role := range []string{"", "owner", "root", "superuser", "Admin", "ADMIN", "teamlead", "team_lead", " admin", "admin,member"} {
		name := "rejected-" + role
		_, token, err := s.CreateUser(ctx, name, role, "")
		if err == nil {
			t.Errorf("CreateUser with role %q succeeded; roles outside the three are not authorised anywhere", role)
			continue
		}
		if token != "" {
			t.Errorf("CreateUser with role %q failed but still handed out token %q", role, token)
		}
		// A rejected role must leave no account behind — a half-created user
		// would be an account nobody meant to exist.
		if _, err := s.GetUserByName(ctx, name); !errors.Is(err, identity.ErrNotFound) {
			t.Errorf("after rejected role %q, GetUserByName(%q) = %v, want ErrNotFound", role, name, err)
		}
		// The message goes straight into the 400 body the operator reads, so
		// it has to name the roles. The table's CHECK constraint happens to
		// list the same three words, so naming them is not enough on its own:
		// the answer must be this package's, not a schema dump forwarded to
		// whoever asked.
		msg := err.Error()
		for _, want := range []string{"admin", "team-lead", "member"} {
			if !strings.Contains(msg, want) {
				t.Errorf("error for role %q was %q; it must tell the operator that %q is a legal role", role, msg, want)
			}
		}
		if strings.Contains(strings.ToLower(msg), "constraint") {
			t.Errorf("error for role %q was %q — the role is being rejected by the database instead of by this package, and the schema is leaking to the caller", role, msg)
		}
	}
}

// --- tokens -----------------------------------------------------------------

// A token is the whole authorisation story: whoever holds it acts as its
// user, with that user's role. It must resolve to that user and no other.
func TestTokenAuthenticatesOnlyTheUserItWasIssuedTo(t *testing.T) {
	t.Parallel()
	s, _, _ := newStore(t)
	ctx := context.Background()

	alice, aliceToken, err := s.CreateUser(ctx, "alice", "member", "")
	if err != nil {
		t.Fatalf("create alice: %v", err)
	}
	root, rootToken, err := s.CreateUser(ctx, "root-user", "admin", "")
	if err != nil {
		t.Fatalf("create root-user: %v", err)
	}
	if aliceToken == rootToken {
		t.Fatal("two users were issued the same token")
	}

	got, err := s.Authenticate(ctx, aliceToken)
	if err != nil {
		t.Fatalf("authenticate alice: %v", err)
	}
	if got.ID != alice.ID || got.Name != "alice" {
		t.Errorf("alice's token resolved to %+v, want id %d name alice", got, alice.ID)
	}
	if got.Role != "member" || got.IsAdmin() {
		t.Errorf("alice's token resolved to role %q (IsAdmin=%v), want member and not admin", got.Role, got.IsAdmin())
	}

	got, err = s.Authenticate(ctx, rootToken)
	if err != nil {
		t.Fatalf("authenticate root-user: %v", err)
	}
	if got.ID != root.ID || got.Name != "root-user" || !got.IsAdmin() {
		t.Errorf("admin's token resolved to %+v, want id %d name root-user with admin rights", got, root.ID)
	}

	// The token text carries the owner's name for humans to recognise. It is
	// decoration; trusting it would let anyone rename their way into another
	// account.
	forged := strings.Replace(aliceToken, "-alice-", "-root-user-", 1)
	if forged == aliceToken {
		t.Fatalf("token %q does not contain the issuing user's name; adjust this forgery", aliceToken)
	}
	u, err := s.Authenticate(ctx, forged)
	assertNobody(t, u, err, "token with the name half swapped for an admin's")

	// The secret half of one token must not open another account either.
	secret := aliceToken[strings.LastIndex(aliceToken, "-")+1:]
	u, err = s.Authenticate(ctx, "cg-root-user-"+secret)
	assertNobody(t, u, err, "admin-shaped token carrying alice's secret")

	// Control: the genuine tokens still work, so the rejections above are not
	// a store that rejects everything.
	if _, err := s.Authenticate(ctx, aliceToken); err != nil {
		t.Fatalf("alice's real token stopped working: %v", err)
	}
}

// A bearer token is a password nobody ever types, so being unguessable is its
// only defence: there is no rate limit and no second factor in front of
// Authenticate. Sixteen random bytes — 32 hex characters — is the floor.
func TestIssuedTokensCarryEnoughRandomnessToBeUnguessable(t *testing.T) {
	t.Parallel()
	s, _, _ := newStore(t)
	ctx := context.Background()

	const issued = 8
	seen := map[string]string{}
	for i := range issued {
		name := "user" + string(rune('a'+i))
		_, token, err := s.CreateUser(ctx, name, "member", "")
		if err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
		secret := token[strings.LastIndex(token, "-")+1:]
		if len(secret) < 32 {
			t.Errorf("%s's token is %q: only %d characters of secret, which is small enough to walk through offline",
				name, token, len(secret))
		}
		if other, dup := seen[secret]; dup {
			t.Errorf("%s and %s were issued the same secret %q", name, other, secret)
		}
		seen[secret] = name
	}
	if len(seen) != issued {
		t.Errorf("%d distinct secrets across %d tokens", len(seen), issued)
	}
}

// Anything that is not an issued token authenticates nobody. Without this the
// server would hand a session to whatever string arrived in the header.
func TestUnknownAndMalformedTokensAuthenticateNobody(t *testing.T) {
	t.Parallel()
	s, _, _ := newStore(t)
	ctx := context.Background()

	_, real, err := s.CreateUser(ctx, "alice", "member", "")
	if err != nil {
		t.Fatalf("create alice: %v", err)
	}

	cases := []struct {
		name  string
		token string
	}{
		{"empty", ""},
		{"whitespace", "   "},
		{"right shape, never issued", "cg-alice-0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"},
		{"no prefix", "0123456789abcdef"},
		{"just the username", "alice"},
		{"sql fragment", "' OR 1=1 --"},
		{"quoted sql fragment", "x' OR '1'='1"},
		{"like wildcard", "%"},
		{"single wildcard char", "_"},
		{"null byte", "cg-alice-\x00"},
		{"real token truncated", real[:len(real)-1]},
		{"real token with a trailing newline", real + "\n"},
		{"real token uppercased", strings.ToUpper(real)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			u, err := s.Authenticate(ctx, c.token)
			assertNobody(t, u, err, c.name)
		})
	}

	// Control: a store that failed every lookup would pass everything above.
	if _, err := s.Authenticate(ctx, real); err != nil {
		t.Fatalf("the genuine token no longer authenticates: %v", err)
	}
}

// Only the hash is stored, which is the promise the UI repeats to the
// operator when it shows a token once. A database copy must not be a set of
// working credentials.
func TestTokenIsStoredHashedAndItsUseIsRecorded(t *testing.T) {
	t.Parallel()
	s, db, dir := newStore(t)
	ctx := context.Background()

	user, token, err := s.CreateUser(ctx, "alice", "member", "")
	if err != nil {
		t.Fatalf("create alice: %v", err)
	}

	var stored string
	var lastUsed sql.NullString
	if err := db.QueryRow(`SELECT token_hash, last_used_at FROM tokens WHERE user_id = ?`, user.ID).
		Scan(&stored, &lastUsed); err != nil {
		t.Fatalf("read stored token: %v", err)
	}
	if stored == "" {
		t.Fatal("no token material stored at all")
	}
	if stored == token {
		t.Error("the token itself is in the database — anyone who reads the file can authenticate as this user")
	}
	secret := token[strings.LastIndex(token, "-")+1:]
	if strings.Contains(stored, secret) {
		t.Errorf("stored value %q contains the token's secret half", stored)
	}
	if n := countRows(t, db, `SELECT COUNT(*) FROM tokens WHERE token_hash = ? OR name = ?`, token, token); n != 0 {
		t.Errorf("%d row(s) hold the raw token", n)
	}
	mustNotAppearOnDisk(t, dir, token, "the bearer token")

	if lastUsed.Valid {
		t.Errorf("last_used_at was %q before the token was ever used", lastUsed.String)
	}
	if _, err := s.Authenticate(ctx, token); err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	// Operators audit which tokens are live; a token that never records a use
	// looks dormant and gets left in place.
	if err := db.QueryRow(`SELECT last_used_at FROM tokens WHERE user_id = ?`, user.ID).Scan(&lastUsed); err != nil {
		t.Fatalf("re-read token row: %v", err)
	}
	if !lastUsed.Valid || lastUsed.String == "" {
		t.Error("authenticating did not record the token's use")
	}
}

// Signing out of the web UI must not sign you out of the desktop app, and it
// must not sign out anybody else.
func TestLogoutRevokesOnlyTheTokenPresented(t *testing.T) {
	t.Parallel()
	s, _, _ := newStore(t)
	ctx := context.Background()

	alice, aliceInitial, err := s.CreateUser(ctx, "alice", "member", "hunter2hunter2")
	if err != nil {
		t.Fatalf("create alice: %v", err)
	}
	_, bobToken, err := s.CreateUser(ctx, "bob", "member", "")
	if err != nil {
		t.Fatalf("create bob: %v", err)
	}
	_, laptop, err := s.Login(ctx, "alice", "hunter2hunter2")
	if err != nil {
		t.Fatalf("login on laptop: %v", err)
	}
	_, phone, err := s.Login(ctx, "alice", "hunter2hunter2")
	if err != nil {
		t.Fatalf("login on phone: %v", err)
	}
	if laptop == phone {
		t.Fatal("two sessions of the same user share a token; logging out of one would end both")
	}

	if err := s.Logout(ctx, laptop); err != nil {
		t.Fatalf("logout: %v", err)
	}
	u, err := s.Authenticate(ctx, laptop)
	assertNobody(t, u, err, "the token that was logged out")

	for _, tc := range []struct {
		what  string
		token string
		id    int64
	}{
		{"alice's other session", phone, alice.ID},
		{"alice's original token", aliceInitial, alice.ID},
	} {
		got, err := s.Authenticate(ctx, tc.token)
		if err != nil {
			t.Errorf("%s stopped working after a different session logged out: %v", tc.what, err)
			continue
		}
		if got.ID != tc.id {
			t.Errorf("%s resolved to user %d, want %d", tc.what, got.ID, tc.id)
		}
	}
	if _, err := s.Authenticate(ctx, bobToken); err != nil {
		t.Errorf("bob was signed out by alice's logout: %v", err)
	}

	// Logging out with a token nobody was issued must not revoke anything.
	if err := s.Logout(ctx, "cg-alice-not-a-real-token"); err != nil {
		t.Fatalf("logout with an unknown token: %v", err)
	}
	if _, err := s.Authenticate(ctx, phone); err != nil {
		t.Errorf("an unknown token's logout revoked a live session: %v", err)
	}
}

// --- passwords --------------------------------------------------------------

// Login is the only place a password is ever checked. If it accepts the wrong
// one, every account on the install is open.
func TestLoginRejectsAnythingButTheRightPassword(t *testing.T) {
	t.Parallel()
	s, db, _ := newStore(t)
	ctx := context.Background()

	const password = "correct-horse-battery"
	alice, initialToken, err := s.CreateUser(ctx, "alice", "member", password)
	if err != nil {
		t.Fatalf("create alice: %v", err)
	}

	user, token, err := s.Login(ctx, "alice", password)
	if err != nil {
		t.Fatalf("login with the right password: %v", err)
	}
	if user.ID != alice.ID || user.Name != "alice" || user.Role != "member" {
		t.Errorf("login returned %+v, want alice (id %d, member)", user, alice.ID)
	}
	if token == "" || token == initialToken {
		t.Errorf("login returned token %q, want a fresh one (initial was %q)", token, initialToken)
	}
	if got, err := s.Authenticate(ctx, token); err != nil || got.ID != alice.ID {
		t.Errorf("the token login handed out does not authenticate alice: %+v, %v", got, err)
	}

	wrong := []struct {
		name     string
		password string
	}{
		{"one character short", password[:len(password)-1]},
		{"different case", "Correct-Horse-Battery"},
		{"trailing space", password + " "},
		{"empty", ""},
		{"someone else's guess", "letmein12345"},
		{"the stored hash pasted as a password", "$2a$10$notarealhashbutlookslikeone"},
	}
	for _, c := range wrong {
		t.Run(c.name, func(t *testing.T) {
			u, tok, err := s.Login(ctx, "alice", c.password)
			assertNobody(t, u, err, "login with "+c.name)
			if tok != "" {
				t.Errorf("a rejected login still issued token %q", tok)
			}
		})
	}

	// Exactly two tokens exist: the one CreateUser handed out and the one the
	// successful login did. A failed login that mints a token first and
	// checks the password afterwards would show up here.
	if n := countRows(t, db, `SELECT COUNT(*) FROM tokens WHERE user_id = ?`, alice.ID); n != 2 {
		t.Errorf("alice has %d tokens, want 2 (initial + one successful login)", n)
	}
}

// An unauthenticated caller must not be able to enumerate accounts by
// comparing the two rejections.
func TestLoginTellsNobodyWhetherTheAccountExists(t *testing.T) {
	t.Parallel()
	s, _, _ := newStore(t)
	ctx := context.Background()

	if _, _, err := s.CreateUser(ctx, "alice", "member", "hunter2hunter2"); err != nil {
		t.Fatalf("create alice: %v", err)
	}

	_, _, missing := s.Login(ctx, "nobody-here", "hunter2hunter2")
	_, _, wrongPassword := s.Login(ctx, "alice", "not-the-password")
	if missing == nil || wrongPassword == nil {
		t.Fatalf("expected both logins to fail; got %v and %v", missing, wrongPassword)
	}
	if !errors.Is(missing, identity.ErrUnauthorized) {
		t.Errorf("login as a nonexistent user returned %v, want ErrUnauthorized", missing)
	}
	if !errors.Is(wrongPassword, identity.ErrUnauthorized) {
		t.Errorf("login with a wrong password returned %v, want ErrUnauthorized", wrongPassword)
	}
	if missing.Error() != wrongPassword.Error() {
		t.Errorf("the two failures are distinguishable: %q vs %q — that is an account-name oracle",
			missing.Error(), wrongPassword.Error())
	}
}

// A token-only account has no password, and "no password" must not mean
// "any password", least of all the empty one.
func TestAccountWithNoPasswordCannotBeLoggedInto(t *testing.T) {
	t.Parallel()
	s, _, _ := newStore(t)
	ctx := context.Background()

	carol, carolToken, err := s.CreateUser(ctx, "carol", "member", "")
	if err != nil {
		t.Fatalf("create carol: %v", err)
	}

	for _, attempt := range []string{"", " ", "carol", "password", "\x00"} {
		u, tok, err := s.Login(ctx, "carol", attempt)
		assertNobody(t, u, err, "login to a passwordless account with "+strconv.Quote(attempt))
		if tok != "" {
			t.Errorf("a passwordless account issued token %q for password %q", tok, attempt)
		}
	}

	// The account is still reachable the way it was meant to be.
	got, err := s.Authenticate(ctx, carolToken)
	if err != nil || got.ID != carol.ID {
		t.Fatalf("carol's token stopped working: %+v, %v", got, err)
	}
}

// Passwords are stored hashed and salted. Plaintext would turn one stolen
// database into every user's password, and an unsalted digest would turn it
// into a rainbow-table lookup.
func TestPasswordIsStoredHashedAndSalted(t *testing.T) {
	t.Parallel()
	s, db, dir := newStore(t)
	ctx := context.Background()

	const shared = "same-password-for-both"
	if _, _, err := s.CreateUser(ctx, "alice", "member", shared); err != nil {
		t.Fatalf("create alice: %v", err)
	}
	if _, _, err := s.CreateUser(ctx, "bob", "member", shared); err != nil {
		t.Fatalf("create bob: %v", err)
	}

	read := func(name string) string {
		t.Helper()
		var h string
		if err := db.QueryRow(`SELECT password_hash FROM users WHERE name = ?`, name).Scan(&h); err != nil {
			t.Fatalf("read password_hash for %s: %v", name, err)
		}
		return h
	}
	aliceHash, bobHash := read("alice"), read("bob")

	for name, h := range map[string]string{"alice": aliceHash, "bob": bobHash} {
		if h == "" {
			t.Fatalf("%s: no password material stored, yet a password was given", name)
		}
		if h == shared {
			t.Errorf("%s: the password is stored verbatim", name)
		}
		if strings.Contains(h, shared) {
			t.Errorf("%s: stored value %q contains the password", name, h)
		}
	}
	if aliceHash == bobHash {
		t.Error("two users with the same password have identical stored values — the hash is unsalted, so one crack opens both accounts")
	}
	mustNotAppearOnDisk(t, dir, shared, "the password")

	// Control: hashing did not break logging in.
	for _, name := range []string{"alice", "bob"} {
		if _, _, err := s.Login(ctx, name, shared); err != nil {
			t.Errorf("%s cannot log in with the password that was set: %v", name, err)
		}
	}
}

// SetPassword replaces the credential rather than adding one, and it must
// apply the same minimum as account creation.
func TestSetPasswordReplacesTheOldOneAndKeepsTheMinimumLength(t *testing.T) {
	t.Parallel()
	s, db, _ := newStore(t)
	ctx := context.Background()

	alice, _, err := s.CreateUser(ctx, "alice", "member", "first-password")
	if err != nil {
		t.Fatalf("create alice: %v", err)
	}

	if err := s.SetPassword(ctx, alice.ID, "second-password"); err != nil {
		t.Fatalf("set password: %v", err)
	}
	if _, _, err := s.Login(ctx, "alice", "second-password"); err != nil {
		t.Errorf("the new password does not work: %v", err)
	}
	u, _, err := s.Login(ctx, "alice", "first-password")
	assertNobody(t, u, err, "login with the replaced password")

	// Seven characters is refused, eight is not; this is the literal boundary
	// the operator is held to.
	if err := s.SetPassword(ctx, alice.ID, "1234567"); err == nil {
		t.Error("a seven-character password was accepted")
	}
	if _, _, err := s.Login(ctx, "alice", "second-password"); err != nil {
		t.Errorf("a rejected password change took effect anyway: %v", err)
	}
	if err := s.SetPassword(ctx, alice.ID, "12345678"); err != nil {
		t.Errorf("an eight-character password was refused: %v", err)
	}
	if _, _, err := s.Login(ctx, "alice", "12345678"); err != nil {
		t.Errorf("the eight-character password does not work: %v", err)
	}

	// Setting a password on an account that is not there must say so rather
	// than silently succeed.
	if err := s.SetPassword(ctx, alice.ID+9999, "12345678"); !errors.Is(err, identity.ErrNotFound) {
		t.Errorf("SetPassword for a missing user = %v, want ErrNotFound", err)
	}
	if n := countRows(t, db, `SELECT COUNT(*) FROM users`); n != 1 {
		t.Errorf("%d users exist, want 1 — SetPassword must not create accounts", n)
	}
}

// Creating an account with a too-short password must not leave the account
// behind without one: that account would be a login nobody can complete and a
// name nobody can reuse.
func TestCreateUserRejectsAShortPasswordAndCreatesNothing(t *testing.T) {
	t.Parallel()
	s, db, _ := newStore(t)
	ctx := context.Background()

	if _, token, err := s.CreateUser(ctx, "shorty", "member", "1234567"); err == nil {
		t.Errorf("a seven-character password was accepted (token %q)", token)
	}
	if _, err := s.GetUserByName(ctx, "shorty"); !errors.Is(err, identity.ErrNotFound) {
		t.Errorf("GetUserByName(shorty) = %v, want ErrNotFound — the rejected account was still created", err)
	}
	if n := countRows(t, db, `SELECT COUNT(*) FROM users`); n != 0 {
		t.Errorf("%d users exist after a rejected creation, want 0", n)
	}
	if n := countRows(t, db, `SELECT COUNT(*) FROM tokens`); n != 0 {
		t.Errorf("%d tokens exist after a rejected creation, want 0", n)
	}

	if _, _, err := s.CreateUser(ctx, "shorty", "member", "12345678"); err != nil {
		t.Fatalf("an eight-character password was refused: %v", err)
	}
	if _, _, err := s.Login(ctx, "shorty", "12345678"); err != nil {
		t.Errorf("login with the eight-character password: %v", err)
	}
}

// bcrypt refuses inputs over 72 bytes. The tempting way to make that error go
// away is to shorten the password before hashing it — which would make every
// passphrase sharing a long common prefix interchangeable. Whichever way the
// package answers, two different passwords must never open the same account.
func TestALongPassphraseIsNeverQuietlyShortened(t *testing.T) {
	t.Parallel()
	s, _, _ := newStore(t)
	ctx := context.Background()

	prefix := strings.Repeat("correct-horse-", 8) // comfortably past 72 bytes
	chosen := prefix + "ending-one"
	lookalike := prefix + "ending-two"

	_, _, err := s.CreateUser(ctx, "longy", "member", chosen)
	if err != nil {
		// Refused outright: nothing may be left behind to log in to.
		if _, e := s.GetUserByName(ctx, "longy"); !errors.Is(e, identity.ErrNotFound) {
			t.Errorf("the long password was refused (%v) but an account was created anyway: %v", err, e)
		}
		return
	}
	if _, _, err := s.Login(ctx, "longy", chosen); err != nil {
		t.Errorf("the long password was accepted but does not work: %v", err)
	}
	u, _, err := s.Login(ctx, "longy", lookalike)
	assertNobody(t, u, err, "login with a different passphrase sharing the first 72 bytes")
}

// Clearing a password is how the seeded admin stays token-only. It must
// disable password login entirely rather than leaving an empty one that
// works.
func TestClearingAPasswordBlocksLoginButLeavesTokensAlone(t *testing.T) {
	t.Parallel()
	s, _, _ := newStore(t)
	ctx := context.Background()

	alice, aliceToken, err := s.CreateUser(ctx, "alice", "member", "hunter2hunter2")
	if err != nil {
		t.Fatalf("create alice: %v", err)
	}
	if _, _, err := s.Login(ctx, "alice", "hunter2hunter2"); err != nil {
		t.Fatalf("login before clearing: %v", err)
	}

	if err := s.SetPassword(ctx, alice.ID, ""); err != nil {
		t.Fatalf("clear password: %v", err)
	}
	for _, attempt := range []string{"hunter2hunter2", "", " "} {
		u, tok, err := s.Login(ctx, "alice", attempt)
		assertNobody(t, u, err, "login after the password was cleared, with "+strconv.Quote(attempt))
		if tok != "" {
			t.Errorf("a cleared account issued token %q", tok)
		}
	}

	got, err := s.Authenticate(ctx, aliceToken)
	if err != nil || got.ID != alice.ID {
		t.Fatalf("clearing the password locked the account out of its token too: %+v, %v", got, err)
	}
}

// --- bootstrap, deletion, teams ---------------------------------------------

// Bootstrap is how an install gets its first credential. Seeding twice would
// mean a restart mints a fresh admin token for whoever watched the logs.
func TestBootstrapSeedsOneAdminAndShowsItsTokenOnce(t *testing.T) {
	t.Parallel()
	s, _, _ := newStore(t)
	ctx := context.Background()

	admin, token, err := s.Bootstrap(ctx, "")
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	if admin.Name != "admin" || admin.Role != "admin" || !admin.IsAdmin() {
		t.Errorf("seeded user is %+v, want name admin with the admin role", admin)
	}
	if token == "" {
		t.Fatal("the first bootstrap returned no token; there would be no way into the install")
	}
	got, err := s.Authenticate(ctx, token)
	if err != nil || got.ID != admin.ID || !got.IsAdmin() {
		t.Fatalf("the bootstrap token does not authenticate the admin: %+v, %v", got, err)
	}

	again, secondToken, err := s.Bootstrap(ctx, "")
	if err != nil {
		t.Fatalf("second bootstrap: %v", err)
	}
	if again.ID != admin.ID {
		t.Errorf("second bootstrap returned user %d, want the existing admin %d", again.ID, admin.ID)
	}
	if secondToken != "" {
		t.Errorf("a restart handed out a fresh admin token %q; the credential is meant to be shown once", secondToken)
	}
	users, err := s.ListUsers(ctx)
	if err != nil {
		t.Fatalf("list users: %v", err)
	}
	if len(users) != 1 {
		t.Errorf("%d users after two bootstraps, want 1", len(users))
	}
	if _, err := s.Authenticate(ctx, token); err != nil {
		t.Errorf("the original bootstrap token stopped working after a restart: %v", err)
	}
}

// On Kubernetes the generated token is worse than useless: "printed once" there
// means "in the pod log, for anyone who can read logs". So the operator can
// supply the token instead, and then Bootstrap must hand nothing back — a
// returned token is a token the caller will print.
//
// The seeded token still has to work, or the install has no front door and the
// only way in is to delete the database.
func TestASeededAdminTokenWorksAndIsNeverHandedBack(t *testing.T) {
	t.Parallel()
	s, _, dir := newStore(t)
	ctx := context.Background()

	const seed = "an-operator-supplied-admin-token-long-enough"
	admin, token, err := s.Bootstrap(ctx, seed)
	if err != nil {
		t.Fatalf("bootstrap with a seed: %v", err)
	}
	if token != "" {
		t.Errorf("Bootstrap returned %q for a token the operator already has; the caller prints what it is given, which is the one place this must not appear", token)
	}
	got, err := s.Authenticate(ctx, seed)
	if err != nil || got.ID != admin.ID || !got.IsAdmin() {
		t.Fatalf("the seeded token does not authenticate the admin — there is no way into this install: %+v, %v", got, err)
	}
	// Same rule as any other credential: stored as a hash, and absent from
	// every file the server wrote.
	mustNotAppearOnDisk(t, dir, seed, "the seeded admin token")

	// And the seed does not silently become a second account or a second
	// admin on the next start.
	if _, second, err := s.Bootstrap(ctx, seed); err != nil || second != "" {
		t.Errorf("second bootstrap: token %q, err %v; want no token and no error", second, err)
	}
	users, err := s.ListUsers(ctx)
	if err != nil {
		t.Fatalf("list users: %v", err)
	}
	if len(users) != 1 {
		t.Errorf("%d users after two seeded bootstraps, want 1", len(users))
	}
}

// A password hash is only as good as the work factor it was computed with.
// bcrypt.MinCost is 4 rounds — a one-word change from DefaultCost that leaves
// every test of "can this user log in" passing while making the stored hashes
// roughly sixty times cheaper to attack. Nothing observes the cost except the
// hash itself, so the hash is what is read.
func TestPasswordHashesUseAWorkFactorWorthHaving(t *testing.T) {
	t.Parallel()
	s, db, _ := newStore(t)
	ctx := context.Background()

	if _, _, err := s.CreateUser(ctx, "alice", "member", "a-password-set-at-creation"); err != nil {
		t.Fatalf("create alice: %v", err)
	}
	if _, _, err := s.CreateUser(ctx, "bob", "member", ""); err != nil {
		t.Fatalf("create bob: %v", err)
	}
	// Both routes to a stored password, because they call bcrypt separately
	// and only one of them being weakened is the likelier mistake.
	if err := s.SetPassword(ctx, 2, "a-password-set-afterwards"); err != nil {
		t.Fatalf("set bob's password: %v", err)
	}

	for _, name := range []string{"alice", "bob"} {
		var h string
		if err := db.QueryRow(`SELECT password_hash FROM users WHERE name = ?`, name).Scan(&h); err != nil {
			t.Fatalf("read password_hash for %s: %v", name, err)
		}
		cost, err := bcrypt.Cost([]byte(h))
		if err != nil {
			t.Fatalf("%s: stored value is not a bcrypt hash: %v", name, err)
		}
		if cost < bcrypt.DefaultCost {
			t.Errorf("%s: password hashed at cost %d, want at least %d — anything lower is a stolen database that can be cracked offline in a fraction of the time",
				name, cost, bcrypt.DefaultCost)
		}
	}
}

// Upgrading an install that predates users must not make its work disappear:
// an unowned workspace is invisible to everyone once ownership starts being
// checked, so Bootstrap hands those to the admin — and only those.
func TestBootstrapAdoptsOnlyTheUnownedWorkspaces(t *testing.T) {
	t.Parallel()
	s, db, _ := newStore(t)
	ctx := context.Background()

	// A user and a workspace that already exist before the admin is seeded,
	// written directly because that is the shape an old database has.
	res, err := db.ExecContext(ctx,
		`INSERT INTO users (name, role, created_at, updated_at) VALUES ('early', 'member', 't', 't')`)
	if err != nil {
		t.Fatalf("seed early user: %v", err)
	}
	earlyID, _ := res.LastInsertId()
	if _, err := db.ExecContext(ctx,
		`INSERT INTO workspaces (name, description, created_at, updated_at, owner_id) VALUES ('owned', '', 't', 't', ?)`,
		earlyID); err != nil {
		t.Fatalf("seed owned workspace: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO workspaces (name, description, created_at, updated_at) VALUES ('orphan', '', 't', 't')`); err != nil {
		t.Fatalf("seed orphan workspace: %v", err)
	}

	admin, _, err := s.Bootstrap(ctx, "")
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}

	ownerOf := func(name string) (int64, bool) {
		t.Helper()
		var owner sql.NullInt64
		if err := db.QueryRow(`SELECT owner_id FROM workspaces WHERE name = ?`, name).Scan(&owner); err != nil {
			t.Fatalf("read owner of %s: %v", name, err)
		}
		return owner.Int64, owner.Valid
	}

	if id, ok := ownerOf("orphan"); !ok || id != admin.ID {
		t.Errorf("the unowned workspace ended up with owner %d (set=%v), want the admin %d — it would be visible to nobody", id, ok, admin.ID)
	}
	if id, _ := ownerOf("owned"); id != earlyID {
		t.Errorf("an already-owned workspace was reassigned to %d, want its owner %d", id, earlyID)
	}
}

// Deleting the seeded admin would lock the operator out of their own install
// with no way back in.
func TestBootstrapAdminCannotBeDeleted(t *testing.T) {
	t.Parallel()
	s, _, _ := newStore(t)
	ctx := context.Background()

	admin, token, err := s.Bootstrap(ctx, "")
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	if err := s.DeleteUser(ctx, admin.ID); err == nil {
		t.Error("the bootstrap admin was deleted")
	}
	got, err := s.Authenticate(ctx, token)
	if err != nil || got.ID != admin.ID || !got.IsAdmin() {
		t.Fatalf("the admin is gone after a delete attempt: %+v, %v", got, err)
	}
}

// Revoking someone's access means revoking it everywhere; a deleted user's
// token must stop opening the door on the next request.
func TestDeletingAUserRevokesTheirTokens(t *testing.T) {
	t.Parallel()
	s, _, _ := newStore(t)
	ctx := context.Background()

	alice, aliceToken, err := s.CreateUser(ctx, "alice", "member", "")
	if err != nil {
		t.Fatalf("create alice: %v", err)
	}
	bob, bobToken, err := s.CreateUser(ctx, "bob", "member", "")
	if err != nil {
		t.Fatalf("create bob: %v", err)
	}
	if _, err := s.Authenticate(ctx, aliceToken); err != nil {
		t.Fatalf("alice's token does not work to begin with: %v", err)
	}

	if err := s.DeleteUser(ctx, alice.ID); err != nil {
		t.Fatalf("delete alice: %v", err)
	}
	// Any error is acceptable here; authenticating as the deleted user is not.
	if got, err := s.Authenticate(ctx, aliceToken); err == nil {
		t.Errorf("a deleted user's token still authenticates as %+v", got)
	} else if got.ID == alice.ID {
		t.Errorf("authentication failed but still returned the deleted user: %+v", got)
	}
	if _, err := s.GetUser(ctx, alice.ID); !errors.Is(err, identity.ErrNotFound) {
		t.Errorf("GetUser after delete = %v, want ErrNotFound", err)
	}

	got, err := s.Authenticate(ctx, bobToken)
	if err != nil || got.ID != bob.ID {
		t.Errorf("deleting alice took bob's access with it: %+v, %v", got, err)
	}
}

// Team membership is what makes a shared workspace visible, and the copy the
// server authorises against is the one Authenticate returns.
func TestTeamMembershipTravelsWithTheAuthenticatedUser(t *testing.T) {
	t.Parallel()
	s, _, _ := newStore(t)
	ctx := context.Background()

	alice, aliceToken, err := s.CreateUser(ctx, "alice", "member", "")
	if err != nil {
		t.Fatalf("create alice: %v", err)
	}
	_, bobToken, err := s.CreateUser(ctx, "bob", "member", "")
	if err != nil {
		t.Fatalf("create bob: %v", err)
	}
	research, err := s.CreateTeam(ctx, "research")
	if err != nil {
		t.Fatalf("create team: %v", err)
	}
	if err := s.AddTeamMember(ctx, research.ID, alice.ID); err != nil {
		t.Fatalf("add alice to research: %v", err)
	}

	a, err := s.Authenticate(ctx, aliceToken)
	if err != nil {
		t.Fatalf("authenticate alice: %v", err)
	}
	if !a.InTeam(research.ID) {
		t.Errorf("alice authenticated with teams %v; she is a member of %d and would not see its workspaces", a.Teams, research.ID)
	}
	b, err := s.Authenticate(ctx, bobToken)
	if err != nil {
		t.Fatalf("authenticate bob: %v", err)
	}
	if b.InTeam(research.ID) {
		t.Errorf("bob authenticated with teams %v; he is in no team and would see the research workspaces", b.Teams)
	}
	if len(b.Teams) != 0 {
		t.Errorf("bob has teams %v, want none", b.Teams)
	}

	// The admin's user list is where memberships get reviewed; a list that
	// reports everyone as belonging to nothing hides who can reach what.
	listed, err := s.ListUsers(ctx)
	if err != nil {
		t.Fatalf("list users: %v", err)
	}
	found := false
	for _, u := range listed {
		switch u.Name {
		case "alice":
			found = true
			if !u.InTeam(research.ID) {
				t.Errorf("the user list shows alice with teams %v, but she is a member of %d", u.Teams, research.ID)
			}
		case "bob":
			if u.InTeam(research.ID) {
				t.Errorf("the user list shows bob in team %d, which he never joined", research.ID)
			}
		}
	}
	if !found {
		t.Fatalf("alice is missing from the user list %+v", listed)
	}

	if err := s.RemoveTeamMember(ctx, research.ID, alice.ID); err != nil {
		t.Fatalf("remove alice from research: %v", err)
	}
	a, err = s.Authenticate(ctx, aliceToken)
	if err != nil {
		t.Fatalf("re-authenticate alice: %v", err)
	}
	if a.InTeam(research.ID) {
		t.Errorf("alice still carries team %d after being removed: %v", research.ID, a.Teams)
	}

	// Removing a membership that is not there must report it rather than
	// pretend the removal happened.
	if err := s.RemoveTeamMember(ctx, research.ID, alice.ID); !errors.Is(err, identity.ErrNotFound) {
		t.Errorf("removing an absent membership = %v, want ErrNotFound", err)
	}
	// A team cannot gain a member who does not exist, and the admin who
	// mistyped an id gets told the user is missing (404) rather than a
	// foreign-key failure forwarded from the database.
	if err := s.AddTeamMember(ctx, research.ID, alice.ID+9999); !errors.Is(err, identity.ErrNotFound) {
		t.Errorf("adding a nonexistent user to a team = %v, want ErrNotFound", err)
	}
}

// The name is the login identity, so two accounts must never share one — and
// the conflict has to be reported as a conflict, not as a server fault.
func TestUserNamesAreUniqueAndTrimmed(t *testing.T) {
	t.Parallel()
	s, _, _ := newStore(t)
	ctx := context.Background()

	alice, aliceToken, err := s.CreateUser(ctx, "alice", "member", "")
	if err != nil {
		t.Fatalf("create alice: %v", err)
	}

	for _, name := range []string{"alice", "  alice", "alice  ", "  alice  "} {
		_, token, err := s.CreateUser(ctx, name, "admin", "")
		if err == nil {
			t.Errorf("a second account named %q was created; two users would answer to the same login name", name)
			continue
		}
		if !errors.Is(err, identity.ErrConflict) {
			t.Errorf("creating %q returned %v, want ErrConflict (anything else is reported as a server fault)", name, err)
		}
		if token != "" {
			t.Errorf("a rejected creation of %q handed out token %q", name, token)
		}
	}

	for _, name := range []string{"", "   ", "\t"} {
		if _, _, err := s.CreateUser(ctx, name, "member", ""); err == nil {
			t.Errorf("an account with the blank name %q was created", name)
		}
	}

	got, err := s.Authenticate(ctx, aliceToken)
	if err != nil || got.ID != alice.ID || got.Role != "member" {
		t.Fatalf("the original alice was disturbed by the rejected duplicates: %+v, %v", got, err)
	}
	users, err := s.ListUsers(ctx)
	if err != nil {
		t.Fatalf("list users: %v", err)
	}
	if len(users) != 1 {
		t.Errorf("%d users exist, want 1", len(users))
	}
}

// The unique index on names is case-sensitive, so "alice" and "ALICE" really
// are two accounts. Every lookup therefore has to be case-sensitive too:
// folding case in one place and not the other resolves a name to the wrong
// person, and the seeded admin is looked up by name on every loopback request.
func TestNamesThatDifferOnlyInCaseAreDifferentAccounts(t *testing.T) {
	t.Parallel()
	s, _, _ := newStore(t)
	ctx := context.Background()

	const lowerPassword, upperPassword = "lower-case-password", "upper-case-password"
	lower, lowerToken, err := s.CreateUser(ctx, "alice", "member", lowerPassword)
	if err != nil {
		t.Fatalf("create alice: %v", err)
	}
	upper, upperToken, err := s.CreateUser(ctx, "ALICE", "admin", upperPassword)
	if err != nil {
		t.Fatalf("create ALICE: %v", err)
	}
	if lower.ID == upper.ID {
		t.Fatal("alice and ALICE collapsed into one account")
	}

	for _, c := range []struct {
		name  string
		id    int64
		role  string
		token string
	}{
		{"alice", lower.ID, "member", lowerToken},
		{"ALICE", upper.ID, "admin", upperToken},
	} {
		got, err := s.GetUserByName(ctx, c.name)
		if err != nil {
			t.Fatalf("GetUserByName(%q): %v", c.name, err)
		}
		if got.ID != c.id || got.Name != c.name || got.Role != c.role {
			t.Errorf("GetUserByName(%q) = %+v, want id %d name %q role %q", c.name, got, c.id, c.name, c.role)
		}
		auth, err := s.Authenticate(ctx, c.token)
		if err != nil || auth.ID != c.id {
			t.Errorf("%q's token resolved to %+v, want id %d", c.name, auth, c.id)
		}
	}

	// Signing in has to land on the account that was named. One of these two
	// is an admin, so getting it wrong is a privilege change, not a typo.
	for _, c := range []struct {
		name     string
		password string
		id       int64
		admin    bool
	}{
		{"alice", lowerPassword, lower.ID, false},
		{"ALICE", upperPassword, upper.ID, true},
	} {
		got, _, err := s.Login(ctx, c.name, c.password)
		if err != nil {
			t.Errorf("login as %q: %v", c.name, err)
			continue
		}
		if got.ID != c.id || got.Name != c.name || got.IsAdmin() != c.admin {
			t.Errorf("login as %q signed in %+v, want id %d with admin=%v", c.name, got, c.id, c.admin)
		}
	}
	// Neither account's password opens the other.
	u, _, err := s.Login(ctx, "alice", upperPassword)
	assertNobody(t, u, err, "logging in as alice with ALICE's password")
	u, _, err = s.Login(ctx, "ALICE", lowerPassword)
	assertNobody(t, u, err, "logging in as ALICE with alice's password")
}
