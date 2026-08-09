package server

// These tests defend the access rules, and nothing else about the HTTP
// layer. Every case here describes a failure an operator would notice: a
// member reading someone else's work, a member reconfiguring the install, or
// a server on a routable address handing admin to whoever opens a socket.
//
// They run against a real SQLite database in a temp dir and drive the real
// handler chain (logging -> authenticate -> mux). Nothing is stubbed: the
// only dependencies deliberately pointed somewhere dead are contextd (a path
// that does not exist) and the provider base URL (a port nothing listens on),
// so a refusal can never be mistaken for an outage and no test can reach the
// network.

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/orkcom-tech/cogitorium/internal/catalog"
	"github.com/orkcom-tech/cogitorium/internal/config"
	"github.com/orkcom-tech/cogitorium/internal/identity"
	"github.com/orkcom-tech/cogitorium/internal/sandbox"
	"github.com/orkcom-tech/cogitorium/internal/store"
	"github.com/orkcom-tech/cogitorium/internal/workspace"
)

const (
	// TEST-NET-3: a client address that can never be loopback, whatever the
	// machine running the tests looks like.
	offBox = "203.0.113.9:41234"
	// A client on the same machine as the server.
	onBox = "127.0.0.1:52101"

	// The provider address the fixture registers. Port 1 refuses instantly,
	// so a mutation that lets a request through fails fast instead of
	// dialing anything real.
	deadProvider = "http://127.0.0.1:1"
)

// quiet keeps request logging out of the test output; the handlers under
// test log every refusal, and that noise buries the actual failure.
var quiet sync.Once

type install struct {
	srv      *Server
	db       *sql.DB
	dir      string
	users    *identity.Store
	spaces   *workspace.Store
	cat      *catalog.Store
	adminTok string
}

// newInstall builds a server the way cmd/ does: real config, real database,
// real stores. listen decides whether an unauthenticated local request is
// the admin, so it is the one thing every caller states explicitly.
func newInstall(t *testing.T, listen string, tweak func(*config.Config)) *install {
	t.Helper()
	quiet.Do(func() {
		slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	})

	dir := t.TempDir()
	db, err := store.Open(dir)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	cfg := config.Config{
		Listen:  listen,
		DataDir: dir,
		// A contextd that cannot exist: the global context routes must refuse
		// a non-admin BEFORE they reach the store, and pointing the store at
		// nothing is what proves the 403 came from the role check rather than
		// from a machine that happens to have no contextd either way.
		ContextdPath: filepath.Join(dir, "no-such-contextd"),
		// The terminal must be switched on, or every terminal request is
		// refused for being disabled and an admin-only test would pass
		// without an admin check existing at all.
		Terminal: true,
	}
	if tweak != nil {
		tweak(&cfg)
	}

	// A sandbox that can host a terminal, so terminalReady lets the request
	// reach the role check. Nothing in these tests ever starts a session, so
	// Docker is never invoked.
	srv := New(cfg, db, &sandbox.Docker{Image: "cogitorium-tests-never-run-this"}, nil)

	in := &install{
		srv:    srv,
		db:     db,
		dir:    dir,
		users:  identity.NewStore(db),
		spaces: workspace.NewStore(db),
		cat:    catalog.NewStore(db),
	}
	// Empty seed: the fixture wants a generated token back. A seeded one is
	// deliberately NOT returned by Bootstrap, so passing a literal here would
	// leave adminTok empty and every admin case would fail as unauthenticated.
	_, tok, err := in.users.Bootstrap(context.Background(), "")
	if err != nil {
		t.Fatalf("bootstrap admin: %v", err)
	}
	in.adminTok = tok
	return in
}

// team is a small multi-user install: three workspaces arranged so that
// every possible answer to "may this person reach it" is represented once.
type team struct {
	*install

	alice, bob, lead     identity.User
	aliceTok, bobTok     string
	leadTok              string
	reviewers            identity.Team
	providerID, modelID  int64
	alicePrivate, shared workspace.Workspace
	bobOwn               workspace.Workspace
}

const (
	alicePrivateName = "alice-only-plans"
	sharedName       = "shared-with-reviewers"
	bobOwnName       = "bob-only"
)

func seedTeam(t *testing.T, listen string) *team {
	t.Helper()
	ctx := context.Background()
	in := newInstall(t, listen, nil)
	tm := &team{install: in}

	var err error
	tm.alice, tm.aliceTok, err = in.users.CreateUser(ctx, "alice", "member", "")
	if err != nil {
		t.Fatalf("create alice: %v", err)
	}
	tm.bob, tm.bobTok, err = in.users.CreateUser(ctx, "bob", "member", "")
	if err != nil {
		t.Fatalf("create bob: %v", err)
	}
	tm.lead, tm.leadTok, err = in.users.CreateUser(ctx, "lead", "team-lead", "")
	if err != nil {
		t.Fatalf("create lead: %v", err)
	}
	tm.reviewers, err = in.users.CreateTeam(ctx, "reviewers")
	if err != nil {
		t.Fatalf("create team: %v", err)
	}
	if err := in.users.AddTeamMember(ctx, tm.reviewers.ID, tm.bob.ID); err != nil {
		t.Fatalf("add bob to reviewers: %v", err)
	}
	// Team membership is loaded at authentication time, but the fixture's
	// copy is used to build requests, so refresh it.
	if tm.bob, err = in.users.GetUser(ctx, tm.bob.ID); err != nil {
		t.Fatalf("reload bob: %v", err)
	}

	provider, err := in.cat.CreateProvider(ctx, "house", "openai-compatible", deadProvider, "sk-the-house-key")
	if err != nil {
		t.Fatalf("create provider: %v", err)
	}
	tm.providerID = provider.ID
	model, err := in.cat.CreateModel(ctx, provider.ID, "test-model", "house / test-model")
	if err != nil {
		t.Fatalf("create model: %v", err)
	}
	tm.modelID = model.ID

	if tm.alicePrivate, err = in.spaces.CreateWorkspace(ctx, alicePrivateName, "", model.ID, tm.alice.ID); err != nil {
		t.Fatalf("create alice's workspace: %v", err)
	}
	if tm.shared, err = in.spaces.CreateWorkspace(ctx, sharedName, "", model.ID, tm.alice.ID); err != nil {
		t.Fatalf("create shared workspace: %v", err)
	}
	if _, err = in.spaces.ShareWith(ctx, tm.shared.ID, tm.reviewers.ID); err != nil {
		t.Fatalf("share workspace with reviewers: %v", err)
	}
	if tm.bobOwn, err = in.spaces.CreateWorkspace(ctx, bobOwnName, "", model.ID, tm.bob.ID); err != nil {
		t.Fatalf("create bob's workspace: %v", err)
	}
	return tm
}

// request drives the whole handler chain, from a client that is not on this
// machine — so nothing is implicitly the admin and the token decides.
func (in *install) request(t *testing.T, method, path, token, body string) *httptest.ResponseRecorder {
	t.Helper()
	return in.requestFrom(t, offBox, method, path, token, body)
}

func (in *install) requestFrom(t *testing.T, client, method, path, token, body string) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reader)
	req.RemoteAddr = client
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	in.srv.http.Handler.ServeHTTP(rec, req)
	return rec
}

func id(v int64) string { return strconv.FormatInt(v, 10) }

func asMap(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("response is not a JSON object (%d): %s", rec.Code, rec.Body.String())
	}
	return out
}

// TestAdminOnlyRoutesRefuseNonAdmins covers every route that reconfigures the
// install or reaches across all workspaces. A member reaching any of these
// can take over the server: mint an admin account, repoint the provider that
// holds the API key, approve agent-written code, or read every workspace's
// context.
func TestAdminOnlyRoutesRefuseNonAdmins(t *testing.T) {
	t.Parallel()
	tm := seedTeam(t, "0.0.0.0:8688")

	routes := []struct{ name, method, path, body string }{
		{"list users", "GET", "/api/v1/users", ""},
		{"create user", "POST", "/api/v1/users", `{"name":"backdoor","role":"admin"}`},
		{"delete user", "DELETE", "/api/v1/users/" + id(tm.alice.ID), ""},
		{"create team", "POST", "/api/v1/teams", `{"name":"forged"}`},
		{"delete team", "DELETE", "/api/v1/teams/" + id(tm.reviewers.ID), ""},
		{"add team member", "POST", "/api/v1/teams/" + id(tm.reviewers.ID) + "/members", `{"user_id":` + id(tm.alice.ID) + `}`},
		{"remove team member", "DELETE", "/api/v1/teams/" + id(tm.reviewers.ID) + "/members/" + id(tm.bob.ID), ""},
		{"access map", "GET", "/api/v1/map", ""},
		{"create provider", "POST", "/api/v1/providers", `{"name":"theirs","type":"openai-compatible","base_url":"` + deadProvider + `","api_key":"k"}`},
		{"update provider", "PATCH", "/api/v1/providers/" + id(tm.providerID), `{"name":"renamed"}`},
		{"delete provider", "DELETE", "/api/v1/providers/" + id(tm.providerID), ""},
		{"dial provider", "POST", "/api/v1/providers/" + id(tm.providerID) + "/test", ""},
		{"list the whole context space", "GET", "/api/v1/context/files", ""},
		{"read any context file", "GET", "/api/v1/context/file?path=notes.md", ""},
		{"write any context file", "PUT", "/api/v1/context/file?path=notes.md", "pwned"},
		{"server-wide terminal", "GET", "/api/v1/terminal", ""},
		{"kill the internet gate", "POST", "/api/v1/egress/off", ""},
		{"approve a gear", "PATCH", "/api/v1/gears/1", `{"status":"approved"}`},
		{"delete a gear", "DELETE", "/api/v1/gears/1", ""},
	}

	callers := []struct {
		who, token string
	}{
		{"member", tm.bobTok},
		{"team-lead", tm.leadTok},
	}

	for _, route := range routes {
		for _, caller := range callers {
			t.Run(route.name+"/"+caller.who, func(t *testing.T) {
				t.Parallel()
				rec := tm.request(t, route.method, route.path, caller.token, route.body)
				if rec.Code != http.StatusForbidden {
					t.Fatalf("%s %s as %s: got %d, want 403\nbody: %s",
						route.method, route.path, caller.who, rec.Code, rec.Body.String())
				}
				// The refusal must be the role check. "The terminal is
				// disabled" and "contextd is not available" are also
				// refusals, and a test that accepted them would pass on an
				// install where the admin check had been deleted.
				if !strings.Contains(rec.Body.String(), "admin") {
					t.Fatalf("%s %s as %s: 403 but not for lacking the admin role\nbody: %s",
						route.method, route.path, caller.who, rec.Body.String())
				}
			})
		}
	}
}

// TestAMemberCannotChangeTheAccounts checks the refusals actually refused,
// not merely answered 403: an account created or deleted behind a 403 is the
// same breach, and only the database can say which happened.
func TestAMemberCannotChangeTheAccounts(t *testing.T) {
	t.Parallel()
	tm := seedTeam(t, "0.0.0.0:8688")
	ctx := context.Background()

	rec := tm.request(t, "POST", "/api/v1/users", tm.bobTok, `{"name":"backdoor","role":"admin"}`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("member creating an admin: got %d, want 403", rec.Code)
	}
	users, err := tm.users.ListUsers(ctx)
	if err != nil {
		t.Fatalf("list users: %v", err)
	}
	for _, u := range users {
		if u.Name == "backdoor" {
			t.Fatal("a member minted an admin account")
		}
	}

	rec = tm.request(t, "DELETE", "/api/v1/users/"+id(tm.alice.ID), tm.bobTok, "")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("member deleting another account: got %d, want 403", rec.Code)
	}
	if _, err := tm.users.GetUser(ctx, tm.alice.ID); err != nil {
		t.Fatalf("a member deleted another user's account: %v", err)
	}
	// Alice's credential must still work — a deletion takes the token with it.
	rec = tm.request(t, "GET", "/api/v1/whoami", tm.aliceTok, "")
	if rec.Code != http.StatusOK || asMap(t, rec)["name"] != "alice" {
		t.Fatalf("alice can no longer authenticate: %d %s", rec.Code, rec.Body.String())
	}
}

// TestPasswordChangeIsSelfOrAdmin: setting someone else's password is taking
// their account. The route is not admin-only — everyone must be able to set
// their own — so the "or your own" clause is the whole gate.
func TestPasswordChangeIsSelfOrAdmin(t *testing.T) {
	t.Parallel()
	tm := seedTeam(t, "0.0.0.0:8688")
	ctx := context.Background()

	const stolen = "bob-picked-this-1"
	rec := tm.request(t, "PUT", "/api/v1/users/"+id(tm.alice.ID)+"/password", tm.bobTok, `{"password":"`+stolen+`"}`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("member setting another user's password: got %d, want 403\nbody: %s", rec.Code, rec.Body.String())
	}
	// The observable consequence: bob must not be able to sign in as alice.
	rec = tm.request(t, "POST", "/api/v1/login", "", `{"name":"alice","password":"`+stolen+`"}`)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("bob's password took over alice's account: login answered %d\nbody: %s", rec.Code, rec.Body.String())
	}

	// Control: the same route on his own account works, so the refusal above
	// is about whose account it is and not about the route being closed.
	const own = "bobs-own-secret-1"
	rec = tm.request(t, "PUT", "/api/v1/users/"+id(tm.bob.ID)+"/password", tm.bobTok, `{"password":"`+own+`"}`)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("member setting their own password: got %d, want 204\nbody: %s", rec.Code, rec.Body.String())
	}
	rec = tm.request(t, "POST", "/api/v1/login", "", `{"name":"bob","password":"`+own+`"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("bob cannot sign in with the password he just set: %d %s", rec.Code, rec.Body.String())
	}

	// And an admin may set anyone's, which is how a seeded install gets its
	// first password before it is exposed beyond loopback.
	const byAdmin = "admin-issued-pass-1"
	rec = tm.request(t, "PUT", "/api/v1/users/"+id(tm.alice.ID)+"/password", tm.adminTok, `{"password":"`+byAdmin+`"}`)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("admin setting a user's password: got %d, want 204\nbody: %s", rec.Code, rec.Body.String())
	}
	if _, _, err := tm.users.Login(ctx, "alice", byAdmin); err != nil {
		t.Fatalf("admin-set password does not work: %v", err)
	}
}

// TestProviderKeyDoesNotFollowANewAddress guards the hole this project was
// actually bitten by: repointing a provider at an attacker's URL makes the
// server deliver its stored API key there on the next call.
func TestProviderKeyDoesNotFollowANewAddress(t *testing.T) {
	t.Parallel()
	tm := seedTeam(t, "0.0.0.0:8688")
	ctx := context.Background()

	const theirAddr = "http://192.0.2.66:8080"

	rec := tm.request(t, "PATCH", "/api/v1/providers/"+id(tm.providerID), tm.bobTok, `{"base_url":"`+theirAddr+`"}`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("member repointing a provider: got %d, want 403\nbody: %s", rec.Code, rec.Body.String())
	}
	if p, err := tm.cat.GetProvider(ctx, tm.providerID); err != nil || p.BaseURL != deadProvider {
		t.Fatalf("a member moved the provider's address: base_url=%q err=%v", p.BaseURL, err)
	}

	// Even an admin must re-supply the key: the stored one is not carried to
	// an address its owner did not name in the same request.
	rec = tm.request(t, "PATCH", "/api/v1/providers/"+id(tm.providerID), tm.adminTok, `{"base_url":"`+theirAddr+`"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("admin repointing without a key: got %d, want 400\nbody: %s", rec.Code, rec.Body.String())
	}
	if p, err := tm.cat.GetProvider(ctx, tm.providerID); err != nil || p.BaseURL != deadProvider {
		t.Fatalf("the address moved anyway: base_url=%q err=%v", p.BaseURL, err)
	}

	// Control: with the key supplied it is an ordinary edit, so the rule is
	// about the key travelling and not about base_url being frozen.
	rec = tm.request(t, "PATCH", "/api/v1/providers/"+id(tm.providerID), tm.adminTok,
		`{"base_url":"`+theirAddr+`","api_key":"sk-new-key"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("admin repointing with a key: got %d, want 200\nbody: %s", rec.Code, rec.Body.String())
	}
	if p, err := tm.cat.GetProvider(ctx, tm.providerID); err != nil || p.BaseURL != theirAddr {
		t.Fatalf("the address did not move: base_url=%q err=%v", p.BaseURL, err)
	}

	// The key itself never leaves the server, for anyone.
	for who, token := range map[string]string{"admin": tm.adminTok, "member": tm.bobTok} {
		rec = tm.request(t, "GET", "/api/v1/providers", token, "")
		if strings.Contains(rec.Body.String(), "sk-new-key") {
			t.Fatalf("the provider API key was served to a %s: %s", who, rec.Body.String())
		}
	}
}

// TestServerWideTerminalIsAdminOnly: the global shell belongs to no
// workspace, so it is bounded by nothing but the sandbox. A member opening
// one gets a shell next to every workspace's files.
func TestServerWideTerminalIsAdminOnly(t *testing.T) {
	t.Parallel()
	tm := seedTeam(t, "0.0.0.0:8688")

	rec := tm.request(t, "GET", "/api/v1/terminal", tm.bobTok, "")
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "admin") {
		t.Fatalf("member opening the server-wide terminal: got %d, want 403 for lacking the admin role\nbody: %s",
			rec.Code, rec.Body.String())
	}

	// Control: the same member may open one inside their own workspace, so
	// the refusal above is the role check and not the terminal being off.
	// (This answers 400: a plain GET is not a WebSocket handshake.)
	rec = tm.request(t, "GET", "/api/v1/workspaces/"+id(tm.bobOwn.ID)+"/terminal", tm.bobTok, "")
	if rec.Code == http.StatusForbidden || rec.Code == http.StatusNotFound {
		t.Fatalf("member refused a terminal in their own workspace: %d %s", rec.Code, rec.Body.String())
	}

	// But not in a workspace they cannot reach.
	rec = tm.request(t, "GET", "/api/v1/workspaces/"+id(tm.alicePrivate.ID)+"/terminal", tm.bobTok, "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("member opening a terminal in someone else's workspace: got %d, want 404\nbody: %s",
			rec.Code, rec.Body.String())
	}
}

// TestGlobalContextSpaceIsAdminOnly: these three routes reach every
// workspace's shared branch and every agent's private memory at once. Until
// per-branch access exists, a non-admin reader is a hole.
func TestGlobalContextSpaceIsAdminOnly(t *testing.T) {
	t.Parallel()
	tm := seedTeam(t, "0.0.0.0:8688")

	// Any path in the space: these routes take one string and do not care
	// whose branch it names, which is exactly why they are admin-only.
	const someonesBranch = "workspaces/someone-elses/shared/notes.md"

	for _, c := range []struct{ name, method, path, body string }{
		{"list", "GET", "/api/v1/context/files", ""},
		{"read", "GET", "/api/v1/context/file?path=" + someonesBranch, ""},
		{"write", "PUT", "/api/v1/context/file?path=" + someonesBranch, "pwned"},
	} {
		rec := tm.request(t, c.method, c.path, tm.bobTok, c.body)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("member %s of the global context space: got %d, want 403\nbody: %s",
				c.name, rec.Code, rec.Body.String())
		}
	}

	// Control: the admin is not refused for the same reason. This install's
	// contextd does not exist, so an admin gets 503 — proving the member's
	// 403 came from the role check and not from the store being dead.
	rec := tm.request(t, "GET", "/api/v1/context/files", tm.adminTok, "")
	if rec.Code == http.StatusForbidden {
		t.Fatalf("admin refused the context space: %d %s", rec.Code, rec.Body.String())
	}
}

// TestMemberCannotReachAWorkspaceTheyDoNotShare is the core tenancy rule.
// Filtering the list is not enough: every route that does work with a
// workspace id has to check, or a stranger reads and writes by guessing an
// integer.
func TestMemberCannotReachAWorkspaceTheyDoNotShare(t *testing.T) {
	t.Parallel()
	tm := seedTeam(t, "0.0.0.0:8688")
	ctx := context.Background()

	aliceAgents, err := tm.spaces.ListAgents(ctx, tm.alicePrivate.ID)
	if err != nil || len(aliceAgents) == 0 {
		t.Fatalf("alice's workspace has no agents to protect: %v", err)
	}
	orchestrator := aliceAgents[0]

	// The list shows exactly what bob may reach: his own, and the one shared
	// with his team. Nothing else, whoever owns it.
	rec := tm.request(t, "GET", "/api/v1/workspaces", tm.bobTok, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list workspaces: %d %s", rec.Code, rec.Body.String())
	}
	var listed []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatalf("workspace list is not JSON: %s", rec.Body.String())
	}
	names := []string{}
	for _, w := range listed {
		names = append(names, w["name"].(string))
	}
	sort.Strings(names)
	if got, want := strings.Join(names, ","), bobOwnName+","+sharedName; got != want {
		t.Fatalf("bob's workspace list = %q, want %q", got, want)
	}

	// Every route keyed by the workspace id, reached directly.
	byID := []struct{ name, method, path, body string }{
		{"read it", "GET", "/api/v1/workspaces/" + id(tm.alicePrivate.ID), ""},
		{"delete it", "DELETE", "/api/v1/workspaces/" + id(tm.alicePrivate.ID), ""},
		{"list its agents", "GET", "/api/v1/workspaces/" + id(tm.alicePrivate.ID) + "/agents", ""},
		{"add an agent", "POST", "/api/v1/workspaces/" + id(tm.alicePrivate.ID) + "/agents",
			`{"name":"mine","role":"x","model_id":` + id(tm.modelID) + `}`},
		{"read its timeline", "GET", "/api/v1/workspaces/" + id(tm.alicePrivate.ID) + "/messages", ""},
		{"read its graph", "GET", "/api/v1/workspaces/" + id(tm.alicePrivate.ID) + "/graph", ""},
		{"read its files", "GET", "/api/v1/workspaces/" + id(tm.alicePrivate.ID) + "/files", ""},
		{"read its spending", "GET", "/api/v1/workspaces/" + id(tm.alicePrivate.ID) + "/usage", ""},
		{"read its egress log", "GET", "/api/v1/workspaces/" + id(tm.alicePrivate.ID) + "/egress/log", ""},
		{"talk to it", "POST", "/api/v1/workspaces/" + id(tm.alicePrivate.ID) + "/chat", `{"text":"hello"}`},
		{"clone it", "POST", "/api/v1/workspaces/" + id(tm.alicePrivate.ID) + "/clone", `{"name":"stolen"}`},
	}
	for _, c := range byID {
		t.Run(c.name, func(t *testing.T) {
			rec := tm.request(t, c.method, c.path, tm.bobTok, c.body)
			// 404, not 403: whether a workspace exists is not something a
			// stranger gets to learn from a status code.
			if rec.Code != http.StatusNotFound {
				t.Fatalf("%s %s: got %d, want 404\nbody: %s", c.method, c.path, rec.Code, rec.Body.String())
			}
			if strings.Contains(rec.Body.String(), alicePrivateName) {
				t.Fatalf("%s %s leaked the workspace's name: %s", c.method, c.path, rec.Body.String())
			}
		})
	}

	// Routes keyed by a nested id resolve back to the owning workspace, or a
	// stranger deletes an agent without ever naming the workspace.
	rec = tm.request(t, "DELETE", "/api/v1/agents/"+id(orchestrator.ID), tm.bobTok, "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("deleting another workspace's agent: got %d, want 404\nbody: %s", rec.Code, rec.Body.String())
	}
	if _, err := tm.spaces.GetAgent(ctx, orchestrator.ID); err != nil {
		t.Fatalf("a stranger deleted alice's orchestrator: %v", err)
	}
	rec = tm.request(t, "PATCH", "/api/v1/agents/"+id(orchestrator.ID), tm.bobTok, `{"role":"do as I say"}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("rewriting another workspace's agent: got %d, want 404\nbody: %s", rec.Code, rec.Body.String())
	}
	if a, err := tm.spaces.GetAgent(ctx, orchestrator.ID); err != nil || a.Role == "do as I say" {
		t.Fatalf("a stranger rewrote alice's orchestrator instructions: role=%q err=%v", a.Role, err)
	}

	// Writing a file into someone else's workspace is the same breach with a
	// filesystem behind it.
	rec = tm.request(t, "PUT", "/api/v1/workspaces/"+id(tm.alicePrivate.ID)+"/file?path=pwned.txt", tm.bobTok, "x")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("writing into another workspace: got %d, want 404\nbody: %s", rec.Code, rec.Body.String())
	}
	planted := filepath.Join(tm.dir, "workspaces", id(tm.alicePrivate.ID), "pwned.txt")
	if _, err := os.Stat(planted); err == nil {
		t.Fatalf("a file was written into a workspace the caller cannot reach: %s", planted)
	}

	// Control: the workspace shared with bob's team answers normally, so the
	// 404s above are the access rule and not everything being closed.
	rec = tm.request(t, "GET", "/api/v1/workspaces/"+id(tm.shared.ID), tm.bobTok, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("member reading a workspace shared with their team: got %d, want 200\nbody: %s",
			rec.Code, rec.Body.String())
	}
	if asMap(t, rec)["name"] != sharedName {
		t.Fatalf("shared workspace came back as %s", rec.Body.String())
	}
}

// TestWorkspaceAccessFollowsTheTeamGrant: the grant is the only thing giving
// a non-owner access, so withdrawing it must take the access with it — and
// only an admin may hand it out, owner or not.
func TestWorkspaceAccessFollowsTheTeamGrant(t *testing.T) {
	t.Parallel()
	tm := seedTeam(t, "0.0.0.0:8688")

	// Alice owns it, so she reaches it, but sharing it is an admin's call.
	rec := tm.request(t, "POST", "/api/v1/workspaces/"+id(tm.alicePrivate.ID)+"/teams", tm.aliceTok,
		`{"team_id":`+id(tm.reviewers.ID)+`}`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("owner sharing their own workspace: got %d, want 403\nbody: %s", rec.Code, rec.Body.String())
	}
	rec = tm.request(t, "GET", "/api/v1/workspaces/"+id(tm.alicePrivate.ID), tm.bobTok, "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("a refused share still let the team in: got %d, want 404", rec.Code)
	}

	// A stranger asking to share it must not even learn it exists.
	rec = tm.request(t, "POST", "/api/v1/workspaces/"+id(tm.alicePrivate.ID)+"/teams", tm.bobTok,
		`{"team_id":`+id(tm.reviewers.ID)+`}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("stranger sharing someone else's workspace: got %d, want 404\nbody: %s", rec.Code, rec.Body.String())
	}

	// The admin grants it, and bob — a reviewer — is in.
	rec = tm.request(t, "POST", "/api/v1/workspaces/"+id(tm.alicePrivate.ID)+"/teams", tm.adminTok,
		`{"team_id":`+id(tm.reviewers.ID)+`}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("admin sharing a workspace: got %d, want 200\nbody: %s", rec.Code, rec.Body.String())
	}
	rec = tm.request(t, "GET", "/api/v1/workspaces/"+id(tm.alicePrivate.ID), tm.bobTok, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("member of a granted team: got %d, want 200\nbody: %s", rec.Code, rec.Body.String())
	}

	// Withdrawing it puts him back outside. Access that survives a revoke is
	// not access control.
	rec = tm.request(t, "DELETE", "/api/v1/workspaces/"+id(tm.alicePrivate.ID)+"/teams/"+id(tm.reviewers.ID), tm.adminTok, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("admin unsharing a workspace: got %d, want 200\nbody: %s", rec.Code, rec.Body.String())
	}
	rec = tm.request(t, "GET", "/api/v1/workspaces/"+id(tm.alicePrivate.ID), tm.bobTok, "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("access outlived the team grant: got %d, want 404\nbody: %s", rec.Code, rec.Body.String())
	}

	// The lead is in no team and owns nothing, so a team install's other
	// roles do not carry access on their own.
	rec = tm.request(t, "GET", "/api/v1/workspaces/"+id(tm.shared.ID), tm.leadTok, "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("a team-lead outside the team reached the workspace: got %d, want 404", rec.Code)
	}
}

// TestLoopbackAdminOnlyOnALoopbackListenAddress is the rule that makes a
// single-operator install feel accountless without opening a team install to
// the network: an unauthenticated request is the admin ONLY when the server
// cannot be reached from anywhere but this machine.
func TestLoopbackAdminOnlyOnALoopbackListenAddress(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		listen    string
		client    string
		wantAdmin bool
	}{
		{"loopback listen, local client", "127.0.0.1:8688", onBox, true},
		{"localhost listen", "localhost:8688", onBox, true},
		{"ipv6 loopback listen", "[::1]:8688", "[::1]:52101", true},
		// Everything below is reachable from off this machine, so an
		// anonymous caller must never be the admin.
		{"all interfaces", "0.0.0.0:8688", onBox, false},
		{"all interfaces, bare port", ":8688", onBox, false},
		{"all interfaces, ipv6", "[::]:8688", onBox, false},
		{"routable address", "192.168.1.10:8688", onBox, false},
		{"public address", "203.0.113.9:8688", onBox, false},
		// A loopback listener cannot receive this, but a proxy in front of
		// one can forward it, and then the connection is the only evidence
		// left about where the request came from.
		{"loopback listen, off-box client", "127.0.0.1:8688", offBox, false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			in := newInstall(t, c.listen, nil)
			rec := in.requestFrom(t, c.client, "GET", "/api/v1/whoami", "", "")

			if !c.wantAdmin {
				if rec.Code != http.StatusUnauthorized {
					t.Fatalf("listen %q, client %s: got %d, want 401\nbody: %s",
						c.listen, c.client, rec.Code, rec.Body.String())
				}
				if strings.Contains(rec.Body.String(), `"role"`) {
					t.Fatalf("listen %q: an anonymous caller was handed an identity: %s", c.listen, rec.Body.String())
				}
				// And it is not just whoami: nothing behind the gate opens.
				rec = in.requestFrom(t, c.client, "GET", "/api/v1/users", "", "")
				if rec.Code != http.StatusUnauthorized {
					t.Fatalf("listen %q: anonymous user list: got %d, want 401\nbody: %s",
						c.listen, rec.Code, rec.Body.String())
				}
				return
			}

			if rec.Code != http.StatusOK {
				t.Fatalf("listen %q, client %s: got %d, want 200\nbody: %s",
					c.listen, c.client, rec.Code, rec.Body.String())
			}
			who := asMap(t, rec)
			if who["name"] != "admin" || who["role"] != "admin" {
				t.Fatalf("listen %q: local caller is %v, want the admin", c.listen, who)
			}
			// The identity is real, not cosmetic: it opens admin routes.
			rec = in.requestFrom(t, c.client, "GET", "/api/v1/users", "", "")
			if rec.Code != http.StatusOK {
				t.Fatalf("listen %q: implicit admin refused an admin route: %d %s",
					c.listen, rec.Code, rec.Body.String())
			}
		})
	}
}

// TestBearerIdentityWinsOverLoopbackAdmin: on a solo install every request is
// local, so if the loopback shortcut ran first — or ran as a fallback for a
// bad token — every member on that machine would silently be the admin.
func TestBearerIdentityWinsOverLoopbackAdmin(t *testing.T) {
	t.Parallel()
	tm := seedTeam(t, "127.0.0.1:8688")

	// Precondition: this install really does hand admin to anonymous local
	// requests, so the assertions below are about the token, not the listen
	// address.
	rec := tm.requestFrom(t, onBox, "GET", "/api/v1/whoami", "", "")
	if rec.Code != http.StatusOK || asMap(t, rec)["name"] != "admin" {
		t.Fatalf("loopback install does not grant implicit admin: %d %s", rec.Code, rec.Body.String())
	}

	rec = tm.requestFrom(t, onBox, "GET", "/api/v1/whoami", tm.bobTok, "")
	who := asMap(t, rec)
	if rec.Code != http.StatusOK || who["name"] != "bob" || who["role"] != "member" {
		t.Fatalf("a member's token on the server's own machine resolved to %v", who)
	}
	rec = tm.requestFrom(t, onBox, "GET", "/api/v1/users", tm.bobTok, "")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("a member on the server's machine reached an admin route: got %d, want 403\nbody: %s",
			rec.Code, rec.Body.String())
	}

	// A revoked or forged token is refused outright. Falling back to the
	// implicit admin here would turn every expired session into a promotion.
	rec = tm.requestFrom(t, onBox, "GET", "/api/v1/users", "cg-admin-not-a-real-token", "")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("an invalid token on a loopback install: got %d, want 401\nbody: %s", rec.Code, rec.Body.String())
	}
	rec = tm.requestFrom(t, onBox, "GET", "/api/v1/whoami", "cg-admin-not-a-real-token", "")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("an invalid token was resolved to an identity: %d %s", rec.Code, rec.Body.String())
	}
}

// TestOnlyHealthAndLoginSkipAuthentication: login is the one API route that
// must answer without credentials, because it is where credentials come
// from. Anything else that skips the middleware is an open door.
func TestOnlyHealthAndLoginSkipAuthentication(t *testing.T) {
	t.Parallel()
	tm := seedTeam(t, "0.0.0.0:8688")

	if rec := tm.request(t, "GET", "/health", "", ""); rec.Code != http.StatusOK {
		t.Fatalf("health without credentials: got %d, want 200\nbody: %s", rec.Code, rec.Body.String())
	}

	// Login reaches its handler and answers about the credentials, not about
	// the lack of a token.
	rec := tm.request(t, "POST", "/api/v1/login", "", `{"name":"bob","password":"wrong"}`)
	if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), "invalid username or password") {
		t.Fatalf("login without a token: got %d %s, want 401 about the credentials", rec.Code, rec.Body.String())
	}

	for _, path := range []string{
		"/api/v1/whoami",
		"/api/v1/workspaces",
		"/api/v1/providers",
		"/api/v1/models",
		"/api/v1/gears",
		"/api/v1/teams",
		"/api/v1/instructions",
		"/api/v1/egress/status",
		"/api/v1/context/files",
	} {
		rec := tm.request(t, "GET", path, "", "")
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("GET %s without credentials: got %d, want 401\nbody: %s", path, rec.Code, rec.Body.String())
		}
		if strings.Contains(rec.Body.String(), sharedName) || strings.Contains(rec.Body.String(), "house") {
			t.Fatalf("GET %s answered an anonymous caller with data: %s", path, rec.Body.String())
		}
	}
}

// TestInternetGateIsAnAdminsCall: reaching a workspace is not authority to
// point it at the internet. A member who could grant it to their own agent
// would be handing a model an outbound channel nobody else reviewed.
func TestInternetGateIsAnAdminsCall(t *testing.T) {
	t.Parallel()
	tm := seedTeam(t, "0.0.0.0:8688")
	ctx := context.Background()

	agents, err := tm.spaces.ListAgents(ctx, tm.bobOwn.ID)
	if err != nil || len(agents) == 0 {
		t.Fatalf("bob's workspace has no agent: %v", err)
	}
	body := `{"agent_id":` + id(agents[0].ID) + `}`

	// Bob owns this workspace, so the workspace check passes and the role
	// check is the only thing left standing between him and the gate.
	rec := tm.request(t, "POST", "/api/v1/workspaces/"+id(tm.bobOwn.ID)+"/egress", tm.bobTok, body)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("member granting egress in their own workspace: got %d, want 403\nbody: %s",
			rec.Code, rec.Body.String())
	}
	grants, err := tm.spaces.ListEgressGrants(ctx, tm.bobOwn.ID)
	if err != nil {
		t.Fatalf("list grants: %v", err)
	}
	if len(grants) != 0 {
		t.Fatalf("a member opened the internet gate: %+v", grants)
	}

	// Control: the admin may, in the very same workspace.
	rec = tm.request(t, "POST", "/api/v1/workspaces/"+id(tm.bobOwn.ID)+"/egress", tm.adminTok, body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("admin granting egress: got %d, want 201\nbody: %s", rec.Code, rec.Body.String())
	}
	grantID, _ := asMap(t, rec)["id"].(float64)
	if grantID == 0 {
		t.Fatalf("grant came back without an id: %s", rec.Body.String())
	}

	// Withdrawing one is the same decision, so it is refused the same way —
	// and the grant must survive the refusal.
	rec = tm.request(t, "DELETE", "/api/v1/egress-grants/"+id(int64(grantID)), tm.bobTok, "")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("member revoking a grant: got %d, want 403\nbody: %s", rec.Code, rec.Body.String())
	}
	if grants, err := tm.spaces.ListEgressGrants(ctx, tm.bobOwn.ID); err != nil || len(grants) != 1 {
		t.Fatalf("the grant did not survive a refused revoke: %+v err=%v", grants, err)
	}

	// Whether gears run isolated is a fact about the install. It tells a
	// member whether unapproved code they can already dry-run has this
	// server's files, which is reconnaissance, not their business.
	rec = tm.request(t, "GET", "/api/v1/egress/status", tm.bobTok, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("member reading the gate status: got %d, want 200", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "sandboxed") {
		t.Fatalf("the gate status told a member how gears are executed: %s", rec.Body.String())
	}
	rec = tm.request(t, "GET", "/api/v1/egress/status", tm.adminTok, "")
	if !strings.Contains(rec.Body.String(), "sandboxed") {
		t.Fatalf("the admin cannot see how gears are executed: %s", rec.Body.String())
	}
}

// TestEgressGrantNeedsASignedInOperator: with egress_approval_bearer on, the
// install has said the internet gate needs a person who typed something. The
// implicit loopback admin is exactly what that setting refuses, so a script
// running on the server's own machine must not be able to open the gate.
func TestEgressGrantNeedsASignedInOperator(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	// A single-operator install: local requests are the admin, and the
	// operator has asked for a real sign-in before the gate moves.
	in := newInstall(t, "127.0.0.1:8688", func(c *config.Config) { c.EgressApprovalBearer = true })
	spaces := in.spaces
	cat := in.cat
	provider, err := cat.CreateProvider(ctx, "house", "openai-compatible", deadProvider, "sk-key")
	if err != nil {
		t.Fatalf("create provider: %v", err)
	}
	model, err := cat.CreateModel(ctx, provider.ID, "test-model", "house / test-model")
	if err != nil {
		t.Fatalf("create model: %v", err)
	}
	admin, err := in.users.GetUserByName(ctx, "admin")
	if err != nil {
		t.Fatalf("get admin: %v", err)
	}
	ws, err := spaces.CreateWorkspace(ctx, "gate-test", "", model.ID, admin.ID)
	if err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	agents, err := spaces.ListAgents(ctx, ws.ID)
	if err != nil || len(agents) == 0 {
		t.Fatalf("workspace has no agent: %v", err)
	}
	body := `{"agent_id":` + id(agents[0].ID) + `}`

	// Implicit local admin: authenticated, admin, and still refused.
	rec := in.requestFrom(t, onBox, "POST", "/api/v1/workspaces/"+id(ws.ID)+"/egress", "", body)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("implicit loopback admin granting egress: got %d, want 403\nbody: %s", rec.Code, rec.Body.String())
	}
	grants, err := spaces.ListEgressGrants(ctx, ws.ID)
	if err != nil {
		t.Fatalf("list grants: %v", err)
	}
	if len(grants) != 0 {
		t.Fatalf("the gate was opened behind a 403: %+v", grants)
	}

	// The same request with a real token is allowed, and the record says
	// which kind of authentication actually made the decision.
	rec = in.requestFrom(t, onBox, "POST", "/api/v1/workspaces/"+id(ws.ID)+"/egress", in.adminTok, body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("signed-in admin granting egress: got %d, want 201\nbody: %s", rec.Code, rec.Body.String())
	}
	if got := asMap(t, rec)["granted_auth"]; got != "bearer" {
		t.Fatalf("grant recorded granted_auth=%v, want \"bearer\"", got)
	}
	grants, err = spaces.ListEgressGrants(ctx, ws.ID)
	if err != nil || len(grants) != 1 {
		t.Fatalf("grant was not stored: %+v err=%v", grants, err)
	}
}
