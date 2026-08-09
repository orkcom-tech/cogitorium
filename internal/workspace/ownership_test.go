package workspace

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"testing"

	"github.com/orkcom-tech/cogitorium/internal/catalog"
	"github.com/orkcom-tech/cogitorium/internal/gear"
	"github.com/orkcom-tech/cogitorium/internal/identity"
	"github.com/orkcom-tech/cogitorium/internal/store"
)

// Visible is the entire authorisation rule for a workspace: every handler
// reaches it, directly or through CanAccess. The role and the team ids are
// wire values that arrive from the database and the API, so these cases
// spell them out literally instead of reusing the package's own constants —
// redefining a constant must not quietly redefine who can read someone
// else's work.
func TestVisible(t *testing.T) {
	t.Parallel()

	const (
		ownerID    = int64(11)
		strangerID = int64(22)
		research   = int64(7)
		platform   = int64(9)
		design     = int64(8)
	)

	owned := func(teams ...int64) Workspace {
		id := ownerID
		return Workspace{ID: 1, Name: "atlas", OwnerID: &id, TeamIDs: teams}
	}

	cases := []struct {
		name string
		ws   Workspace
		user identity.User
		want bool
	}{
		{
			// The admin is the operator of the install. Hiding a workspace from
			// them would leave nobody able to clean up after a departed user.
			name: "admin sees a workspace they neither own nor share",
			ws:   owned(research),
			user: identity.User{ID: strangerID, Name: "root", Role: "admin"},
			want: true,
		},
		{
			// team-lead is a distinct role: it must not collapse into admin, or
			// every lead silently reads every workspace in the install.
			name: "team lead is not an admin",
			ws:   owned(research),
			user: identity.User{ID: strangerID, Name: "lead", Role: "team-lead", Teams: []int64{design}},
			want: false,
		},
		{
			name: "owner sees their own",
			ws:   owned(),
			user: identity.User{ID: ownerID, Name: "alice", Role: "member"},
			want: true,
		},
		{
			// The owner is matched by id, not by "somebody owns it": a workspace
			// owned by 11 must stay shut to 22.
			name: "a different user does not inherit the owner's access",
			ws:   owned(),
			user: identity.User{ID: strangerID, Name: "bob", Role: "member"},
			want: false,
		},
		{
			name: "member of the granted team sees it",
			ws:   owned(research),
			user: identity.User{ID: strangerID, Name: "bob", Role: "member", Teams: []int64{research}},
			want: true,
		},
		{
			// Additive sharing is only real if the second grant is consulted too;
			// checking just the first team is the single-column behaviour the
			// workspace_teams migration replaced.
			name: "member of the second granted team sees it",
			ws:   owned(research, platform),
			user: identity.User{ID: strangerID, Name: "carol", Role: "member", Teams: []int64{platform}},
			want: true,
		},
		{
			name: "member of an ungranted team does not",
			ws:   owned(research, platform),
			user: identity.User{ID: strangerID, Name: "dave", Role: "member", Teams: []int64{design}},
			want: false,
		},
		{
			// A workspace whose owner was deleted (owner_id ON DELETE SET NULL)
			// must not become readable by everyone.
			name: "an unowned, unshared workspace is not public",
			ws:   Workspace{ID: 1, Name: "orphan"},
			user: identity.User{ID: strangerID, Name: "bob", Role: "member", Teams: []int64{research}},
			want: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := Visible(tc.ws, tc.user); got != tc.want {
				t.Errorf("Visible(workspace owner=%v teams=%v, user id=%d role=%q teams=%v) = %v, want %v",
					tc.ws.OwnerID, tc.ws.TeamIDs, tc.user.ID, tc.user.Role, tc.user.Teams, got, tc.want)
			}
		})
	}
}

// Sharing has to be additive at the store level too, not only in Visible:
// granting a second team must not cost the first its access, and withdrawing
// one must leave the rest alone. Both directions were impossible with the
// single team_id column this replaced.
func TestSharingIsAdditive(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()

	ws := f.createWorkspace(t, "Atlas", "chart the northern approaches", f.alice.ID)

	if _, err := f.ws.ShareWith(ctx, ws.ID, f.research.ID); err != nil {
		t.Fatalf("share with research: %v", err)
	}
	both, err := f.ws.ShareWith(ctx, ws.ID, f.platform.ID)
	if err != nil {
		t.Fatalf("share with platform: %v", err)
	}
	if !sameIDs(both.TeamIDs, []int64{f.research.ID, f.platform.ID}) {
		t.Fatalf("after sharing with both teams TeamIDs = %v, want both research (%d) and platform (%d)",
			both.TeamIDs, f.research.ID, f.platform.ID)
	}
	f.assertAccess(t, ws.ID, f.bob, true)   // research
	f.assertAccess(t, ws.ID, f.carol, true) // platform

	withdrawn, err := f.ws.Unshare(ctx, ws.ID, f.research.ID)
	if err != nil {
		t.Fatalf("unshare research: %v", err)
	}
	if !sameIDs(withdrawn.TeamIDs, []int64{f.platform.ID}) {
		t.Fatalf("after withdrawing research TeamIDs = %v, want only platform (%d)", withdrawn.TeamIDs, f.platform.ID)
	}
	f.assertAccess(t, ws.ID, f.bob, false)  // withdrawn
	f.assertAccess(t, ws.ID, f.carol, true) // untouched by someone else's withdrawal
}

// Sharing twice is a double click, not an error the operator has to think
// about — and it must not accumulate a second grant either, or withdrawing
// once would leave a ghost behind.
func TestShareWithIsIdempotent(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()

	ws := f.createWorkspace(t, "Atlas", "", f.alice.ID)
	if _, err := f.ws.ShareWith(ctx, ws.ID, f.research.ID); err != nil {
		t.Fatalf("first share: %v", err)
	}
	again, err := f.ws.ShareWith(ctx, ws.ID, f.research.ID)
	if err != nil {
		t.Fatalf("sharing with the same team twice failed: %v", err)
	}
	if !sameIDs(again.TeamIDs, []int64{f.research.ID}) {
		t.Fatalf("TeamIDs after sharing twice = %v, want exactly one grant to research (%d)", again.TeamIDs, f.research.ID)
	}

	if _, err := f.ws.Unshare(ctx, ws.ID, f.research.ID); err != nil {
		t.Fatalf("unshare: %v", err)
	}
	f.assertAccess(t, ws.ID, f.bob, false)
}

// CanAccess is the guard on every workspace-scoped route: a filtered list
// alone would leave those routes open to a direct id. It must refuse an id
// that does not exist rather than reporting access to nothing.
func TestCanAccessRejectsAnUnknownWorkspace(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()

	ok, err := f.ws.CanAccess(ctx, f.root, 4242)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("CanAccess on a missing workspace: err = %v, want ErrNotFound", err)
	}
	if ok {
		t.Error("CanAccess granted access to a workspace that does not exist")
	}
}

// Clone is the documented replacement for versioned templates: it copies the
// definition so two people can run the same setup independently. Everything
// that makes the setup work — agent names, their roles, the model each is
// bound to, and the wires that authorise delegation — has to survive the
// copy, or the clone looks identical and behaves differently.
func TestCloneCopiesAgentsAndWiring(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	src := f.seedCloneSource(t)

	clone, err := f.ws.Clone(ctx, src.ws.ID, "Atlas Copy", f.bob.ID)
	if err != nil {
		t.Fatalf("clone: %v", err)
	}
	if clone.ID == src.ws.ID {
		t.Fatal("clone returned the source workspace itself")
	}
	if clone.Name != "Atlas Copy" {
		t.Errorf("clone name = %q, want %q", clone.Name, "Atlas Copy")
	}
	if clone.Description != "chart the northern approaches" {
		t.Errorf("clone description = %q, want the source's %q", clone.Description, "chart the northern approaches")
	}

	agents, err := f.ws.ListAgents(ctx, clone.ID)
	if err != nil {
		t.Fatalf("list clone agents: %v", err)
	}
	byName := map[string]Agent{}
	for _, a := range agents {
		if a.WorkspaceID != clone.ID {
			t.Errorf("cloned agent %q reports workspace %d, want the clone %d", a.Name, a.WorkspaceID, clone.ID)
		}
		byName[a.Name] = a
	}
	if got := sortedNames(agents); !slices.Equal(got, []string{"auditor", "orchestrator", "scribe"}) {
		t.Fatalf("clone agents = %v, want auditor, orchestrator and scribe", got)
	}

	// Role and model are what an agent *is*; a clone that drops either is a
	// row with the right name and none of the behaviour.
	wantRole := map[string]string{
		"orchestrator": "run the atlas survey",
		"scribe":       "write the field notes",
		"auditor":      "check the field notes",
	}
	wantModel := map[string]string{
		"orchestrator": "acme / big-model",
		"scribe":       "acme / small-model",
		"auditor":      "acme / tiny-model",
	}
	for name, a := range byName {
		if a.Role != wantRole[name] {
			t.Errorf("cloned agent %q role = %q, want %q", name, a.Role, wantRole[name])
		}
		if a.ModelLabel != wantModel[name] {
			t.Errorf("cloned agent %q model = %q, want %q", name, a.ModelLabel, wantModel[name])
		}
	}

	// Copies, not aliases: sharing a row would make an edit in one workspace
	// show up in the other.
	srcIDs := map[int64]bool{src.orchestrator.ID: true, src.scribe.ID: true, src.auditor.ID: true}
	for _, a := range agents {
		if srcIDs[a.ID] {
			t.Errorf("cloned agent %q reuses the source's row (id %d)", a.Name, a.ID)
		}
	}

	// The wires are the delegation capability: no edge, no delegation. They
	// must be rebuilt between the clone's own agents.
	wires, err := f.ws.ListWires(ctx, clone.ID)
	if err != nil {
		t.Fatalf("list clone wires: %v", err)
	}
	nameOf := map[int64]string{}
	for _, a := range agents {
		nameOf[a.ID] = a.Name
	}
	edges := []string{}
	for _, w := range wires {
		if w.WorkspaceID != clone.ID {
			t.Errorf("cloned wire %d reports workspace %d, want the clone %d", w.ID, w.WorkspaceID, clone.ID)
		}
		from, okF := nameOf[w.FromAgentID]
		to, okT := nameOf[w.ToAgentID]
		if !okF || !okT {
			t.Fatalf("cloned wire %d points at agents outside the clone (%d -> %d)", w.ID, w.FromAgentID, w.ToAgentID)
		}
		edges = append(edges, fmt.Sprintf("%s -> %s [%s]", from, to, w.Label))
	}
	slices.Sort(edges)
	if !slices.Equal(edges, []string{"orchestrator -> scribe [delegate survey]", "scribe -> auditor [review]"}) {
		t.Fatalf("cloned wiring = %v, want the source's two edges", edges)
	}

	// And the capability check agrees with the drawing.
	can, err := f.ws.CanDelegate(ctx, clone.ID, byName["orchestrator"].ID, byName["scribe"].ID)
	if err != nil {
		t.Fatalf("can delegate: %v", err)
	}
	if !can {
		t.Error("orchestrator cannot delegate to scribe in the clone, though the source wire says it may")
	}
	can, err = f.ws.CanDelegate(ctx, clone.ID, byName["auditor"].ID, byName["scribe"].ID)
	if err != nil {
		t.Fatalf("can delegate: %v", err)
	}
	if can {
		t.Error("the clone invented a wire auditor -> scribe that the source does not have")
	}
}

// The timeline is deliberately left behind: a clone is a fresh start with the
// same setup, and inheriting someone else's conversation would both mislead
// the new owner and hand them content they were never shown.
func TestCloneLeavesTheConversationBehind(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	src := f.seedCloneSource(t)

	clone, err := f.ws.Clone(ctx, src.ws.ID, "Atlas Copy", f.bob.ID)
	if err != nil {
		t.Fatalf("clone: %v", err)
	}

	cloned, err := f.ws.ListMessages(ctx, clone.ID, nil, 0)
	if err != nil {
		t.Fatalf("list clone messages: %v", err)
	}
	if len(cloned) != 0 {
		t.Fatalf("clone timeline has %d entries, want none; first is %q", len(cloned), cloned[0].Content)
	}

	// The source keeps its own history, unread by the copy.
	original, err := f.ws.ListMessages(ctx, src.ws.ID, nil, 0)
	if err != nil {
		t.Fatalf("list source messages: %v", err)
	}
	got := []string{}
	for _, m := range original {
		got = append(got, m.Content)
	}
	if !slices.Equal(got, []string{"survey the north", "on it", "the pass is snowed in"}) {
		t.Fatalf("source timeline after cloning = %v, want the three entries it started with", got)
	}
}

// Cloning is a copy, not a move. The person who took the copy must find the
// original exactly as they left it.
func TestCloneDoesNotDisturbTheSource(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	src := f.seedCloneSource(t)

	if _, err := f.ws.Clone(ctx, src.ws.ID, "Atlas Copy", f.bob.ID); err != nil {
		t.Fatalf("clone: %v", err)
	}

	after, err := f.ws.GetWorkspace(ctx, src.ws.ID)
	if err != nil {
		t.Fatalf("re-read source: %v", err)
	}
	if after.OwnerID == nil || *after.OwnerID != f.alice.ID {
		t.Errorf("source owner after cloning = %v, want alice (%d)", after.OwnerID, f.alice.ID)
	}
	if !sameIDs(after.TeamIDs, []int64{f.research.ID}) {
		t.Errorf("source team grants after cloning = %v, want research (%d)", after.TeamIDs, f.research.ID)
	}
	if after.Branch != src.ws.Branch {
		t.Errorf("source branch moved from %q to %q; its agents would lose their context", src.ws.Branch, after.Branch)
	}

	agents, err := f.ws.ListAgents(ctx, src.ws.ID)
	if err != nil {
		t.Fatalf("list source agents: %v", err)
	}
	if got := sortedNames(agents); !slices.Equal(got, []string{"auditor", "orchestrator", "scribe"}) {
		t.Errorf("source agents after cloning = %v, want the three it started with", got)
	}
	wires, err := f.ws.ListWires(ctx, src.ws.ID)
	if err != nil {
		t.Fatalf("list source wires: %v", err)
	}
	if len(wires) != 2 {
		t.Errorf("source has %d wires after cloning, want 2", len(wires))
	}
}

// A clone belongs to whoever took it, and to nobody else: it must not arrive
// pre-shared with the source owner's teams, and the source owner does not
// keep a key to it. "Nothing they do afterwards touches the other" is only
// true if the copy starts private.
func TestCloneBelongsToWhoeverTookIt(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	src := f.seedCloneSource(t)

	clone, err := f.ws.Clone(ctx, src.ws.ID, "Atlas Copy", f.bob.ID)
	if err != nil {
		t.Fatalf("clone: %v", err)
	}

	if clone.OwnerID == nil || *clone.OwnerID != f.bob.ID {
		t.Fatalf("clone owner = %v, want bob (%d) who took the copy", clone.OwnerID, f.bob.ID)
	}
	if len(clone.TeamIDs) != 0 {
		t.Errorf("clone arrived shared with teams %v; a copy must start private to its new owner", clone.TeamIDs)
	}

	f.assertAccess(t, clone.ID, f.bob, true)
	f.assertAccess(t, clone.ID, f.alice, false) // owner of the source, stranger to the copy
	f.assertAccess(t, clone.ID, f.carol, false)
	f.assertAccess(t, clone.ID, f.root, true) // admin still sees everything

	// The clone needs its own context subtree, frozen with its own id.
	// Reusing the source's branch would have the copy read and overwrite the
	// original's memory.
	want := fmt.Sprintf("workspaces/atlas-copy-%d", clone.ID)
	if clone.Branch != want {
		t.Errorf("clone branch = %q, want %q", clone.Branch, want)
	}
	if clone.SharedBranchPath != want+"/shared" {
		t.Errorf("clone shared branch = %q, want %q", clone.SharedBranchPath, want+"/shared")
	}
	if clone.Branch == src.ws.Branch {
		t.Error("clone shares the source's context branch")
	}
}

// KNOWN GAP, pinned deliberately. Clone's own doc comment says it copies
// "agents, wires and gear grants", and the HTTP handler promises "agents,
// wires and their arrangement" — but the implementation copies neither the
// gear bindings nor the canvas positions. This test states what the code
// actually does so the gap is visible instead of assumed. If it starts
// failing because someone implemented the copy, delete it and assert the
// copy instead: that is the doc coming true, not a regression.
func TestCloneDropsGearGrantsAndCanvasPositions(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	src := f.seedCloneSource(t)

	gears := gear.NewStore(f.db)
	g, err := gears.Forge(ctx, "field_notes", "writes notes", nil, "bash", "main.sh", "{}",
		[]gear.File{{Path: "main.sh", Content: "echo noted"}}, src.ws.ID, src.scribe.ID)
	if err != nil {
		t.Fatalf("forge gear: %v", err)
	}
	if _, err := gears.Bind(ctx, g.ID, src.ws.ID, nil); err != nil {
		t.Fatalf("bind gear workspace-wide: %v", err)
	}
	srcBindings, err := gears.ListBindings(ctx, src.ws.ID)
	if err != nil {
		t.Fatalf("list source gear bindings: %v", err)
	}
	if len(srcBindings) != 2 {
		t.Fatalf("source has %d gear bindings, want the agent one and the workspace-wide one", len(srcBindings))
	}

	clone, err := f.ws.Clone(ctx, src.ws.ID, "Atlas Copy", f.bob.ID)
	if err != nil {
		t.Fatalf("clone: %v", err)
	}

	cloneBindings, err := gears.ListBindings(ctx, clone.ID)
	if err != nil {
		t.Fatalf("list clone gear bindings: %v", err)
	}
	if len(cloneBindings) != 0 {
		t.Errorf("clone has %d gear bindings — Clone's doc promise about gear grants is now kept; "+
			"replace this test with one asserting the copy", len(cloneBindings))
	}

	scribe, err := f.ws.GetAgentByName(ctx, clone.ID, "scribe")
	if err != nil {
		t.Fatalf("get cloned scribe: %v", err)
	}
	if scribe.PosX != nil || scribe.PosY != nil {
		t.Errorf("cloned scribe has canvas position (%v, %v) — the arrangement is now copied; "+
			"replace this test with one asserting it matches the source", scribe.PosX, scribe.PosY)
	}
	original, err := f.ws.GetAgent(ctx, src.scribe.ID)
	if err != nil {
		t.Fatalf("get source scribe: %v", err)
	}
	if original.PosX == nil || *original.PosX != 12 || original.PosY == nil || *original.PosY != 34 {
		t.Fatalf("source scribe position = (%v, %v), want the (12, 34) it was placed at", original.PosX, original.PosY)
	}
}

// --- fixture -------------------------------------------------------------

// fixture is a real install in a temp directory: internal/store owns the
// schema, and only the real database enforces the foreign keys and unique
// indexes these rules lean on.
type fixture struct {
	db   *sql.DB
	ws   *Store
	cat  *catalog.Store
	auth *identity.Store

	alice, bob, carol, root identity.User
	research, platform      identity.Team
	bigModel                int64
	smallModel              int64
	tinyModel               int64
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	ctx := context.Background()

	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	f := &fixture{db: db, ws: NewStore(db), cat: catalog.NewStore(db), auth: identity.NewStore(db)}

	p, err := f.cat.CreateProvider(ctx, "acme", "openai-compatible", "http://127.0.0.1:1", "")
	if err != nil {
		t.Fatalf("create provider: %v", err)
	}
	for _, m := range []struct {
		name string
		into *int64
	}{{"big-model", &f.bigModel}, {"small-model", &f.smallModel}, {"tiny-model", &f.tinyModel}} {
		model, err := f.cat.CreateModel(ctx, p.ID, m.name, "")
		if err != nil {
			t.Fatalf("create model %q: %v", m.name, err)
		}
		*m.into = model.ID
	}

	f.alice = f.user(t, "alice", "member")
	f.bob = f.user(t, "bob", "member")
	f.carol = f.user(t, "carol", "member")
	f.root = f.user(t, "root", "admin")

	f.research = f.team(t, "research", f.bob.ID)
	f.platform = f.team(t, "platform", f.carol.ID)
	// Reload so the in-memory users carry the memberships the store now holds.
	f.bob = f.reload(t, f.bob.ID)
	f.carol = f.reload(t, f.carol.ID)

	return f
}

func (f *fixture) user(t *testing.T, name, role string) identity.User {
	t.Helper()
	u, _, err := f.auth.CreateUser(context.Background(), name, role, "")
	if err != nil {
		t.Fatalf("create user %q: %v", name, err)
	}
	return u
}

func (f *fixture) team(t *testing.T, name string, members ...int64) identity.Team {
	t.Helper()
	ctx := context.Background()
	team, err := f.auth.CreateTeam(ctx, name)
	if err != nil {
		t.Fatalf("create team %q: %v", name, err)
	}
	for _, m := range members {
		if err := f.auth.AddTeamMember(ctx, team.ID, m); err != nil {
			t.Fatalf("add user %d to team %q: %v", m, name, err)
		}
	}
	return team
}

func (f *fixture) reload(t *testing.T, id int64) identity.User {
	t.Helper()
	u, err := f.auth.GetUser(context.Background(), id)
	if err != nil {
		t.Fatalf("reload user %d: %v", id, err)
	}
	return u
}

func (f *fixture) createWorkspace(t *testing.T, name, description string, owner int64) Workspace {
	t.Helper()
	ws, err := f.ws.CreateWorkspace(context.Background(), name, description, f.bigModel, owner)
	if err != nil {
		t.Fatalf("create workspace %q: %v", name, err)
	}
	return ws
}

// assertAccess checks both routes a user can reach a workspace by: the
// filtered list on the landing page and the per-request guard every
// workspace-scoped handler calls. They must agree — a workspace hidden from
// the list but reachable by id is still leaked.
func (f *fixture) assertAccess(t *testing.T, wsID int64, u identity.User, want bool) {
	t.Helper()
	ctx := context.Background()

	got, err := f.ws.CanAccess(ctx, u, wsID)
	if err != nil {
		t.Fatalf("CanAccess(%s, workspace %d): %v", u.Name, wsID, err)
	}
	if got != want {
		t.Errorf("CanAccess(%s, workspace %d) = %v, want %v", u.Name, wsID, got, want)
	}

	list, err := f.ws.ListWorkspacesFor(ctx, u)
	if err != nil {
		t.Fatalf("ListWorkspacesFor(%s): %v", u.Name, err)
	}
	listed := false
	for _, w := range list {
		if w.ID == wsID {
			listed = true
		}
	}
	if listed != want {
		t.Errorf("ListWorkspacesFor(%s) lists workspace %d = %v, want %v", u.Name, wsID, listed, want)
	}
}

type cloneSource struct {
	ws                            Workspace
	orchestrator, scribe, auditor Agent
}

// seedCloneSource builds a workspace worth copying: three agents on three
// different models, two wires, a placed agent, a conversation, and a team
// grant. Every one of those is something Clone either must or must not carry.
func (f *fixture) seedCloneSource(t *testing.T) cloneSource {
	t.Helper()
	ctx := context.Background()

	ws := f.createWorkspace(t, "Atlas", "chart the northern approaches", f.alice.ID)

	// The seeded entry point is found by its literal name — that is the handle
	// the orchestrator's tools and the UI use.
	orch, err := f.ws.GetAgentByName(ctx, ws.ID, "orchestrator")
	if err != nil {
		t.Fatalf("get orchestrator: %v", err)
	}
	role := "run the atlas survey"
	orch, err = f.ws.UpdateAgent(ctx, orch.ID, nil, &role, nil)
	if err != nil {
		t.Fatalf("set orchestrator role: %v", err)
	}

	scribe, err := f.ws.CreateAgent(ctx, ws.ID, "scribe", "write the field notes", f.smallModel)
	if err != nil {
		t.Fatalf("create scribe: %v", err)
	}
	auditor, err := f.ws.CreateAgent(ctx, ws.ID, "auditor", "check the field notes", f.tinyModel)
	if err != nil {
		t.Fatalf("create auditor: %v", err)
	}

	if _, err := f.ws.CreateWire(ctx, ws.ID, orch.ID, scribe.ID, "delegate survey"); err != nil {
		t.Fatalf("wire orchestrator -> scribe: %v", err)
	}
	if _, err := f.ws.CreateWire(ctx, ws.ID, scribe.ID, auditor.ID, "review"); err != nil {
		t.Fatalf("wire scribe -> auditor: %v", err)
	}
	if err := f.ws.SetAgentPosition(ctx, scribe.ID, 12, 34); err != nil {
		t.Fatalf("place scribe: %v", err)
	}

	for _, m := range []struct {
		agent   *int64
		kind    string
		content string
	}{
		{nil, "user", "survey the north"},
		{&orch.ID, "assistant", "on it"},
		{&scribe.ID, "assistant", "the pass is snowed in"},
	} {
		if _, err := f.ws.AppendMessage(ctx, ws.ID, m.agent, m.kind, m.content, ""); err != nil {
			t.Fatalf("append message %q: %v", m.content, err)
		}
	}

	if _, err := f.ws.ShareWith(ctx, ws.ID, f.research.ID); err != nil {
		t.Fatalf("share source with research: %v", err)
	}
	ws, err = f.ws.GetWorkspace(ctx, ws.ID)
	if err != nil {
		t.Fatalf("re-read source workspace: %v", err)
	}

	return cloneSource{ws: ws, orchestrator: orch, scribe: scribe, auditor: auditor}
}

func sortedNames(agents []Agent) []string {
	out := make([]string, 0, len(agents))
	for _, a := range agents {
		out = append(out, a.Name)
	}
	slices.Sort(out)
	return out
}

// sameIDs compares grant sets without pinning the order they come back in.
func sameIDs(a, b []int64) bool {
	return slices.Equal(slices.Sorted(slices.Values(a)), slices.Sorted(slices.Values(b)))
}
