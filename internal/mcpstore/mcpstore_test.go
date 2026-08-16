package mcpstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/orkcom-tech/cogitorium/internal/catalog"
	"github.com/orkcom-tech/cogitorium/internal/mcp/mcpwire"
	"github.com/orkcom-tech/cogitorium/internal/store"
)

// Every test here is about a gate an operator closed, against a real migrated
// database. The subject is not "does the SQL run" — it is whether a model can
// end up being offered a tool nobody agreed to.

type fixture struct {
	*Store
	db    *sql.DB
	wsID  int64
	agent int64
	other int64
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open a database: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	ctx := context.Background()

	// Real rows, because the bindings have foreign keys into them and a fixture
	// that skipped them would be testing a schema this product does not have.
	var wsID int64
	res, err := db.ExecContext(ctx,
		`INSERT INTO workspaces (name, description, created_at, updated_at)
		 VALUES ('w', '', datetime('now'), datetime('now'))`)
	if err != nil {
		t.Fatalf("workspace: %v", err)
	}
	wsID, _ = res.LastInsertId()

	agentID := insertAgent(t, db, wsID, "worker")
	otherID := insertAgent(t, db, wsID, "other")
	return &fixture{Store: NewStore(db), db: db, wsID: wsID, agent: agentID, other: otherID}
}

func insertAgent(t *testing.T, db *sql.DB, wsID int64, name string) int64 {
	t.Helper()
	res, err := db.Exec(
		`INSERT INTO agents (workspace_id, name, role, kind, is_orchestrator, created_at, updated_at)
		 VALUES (?, ?, '', 'model', 0, datetime('now'), datetime('now'))`, wsID, name)
	if err != nil {
		t.Fatalf("agent %s: %v", name, err)
	}
	id, _ := res.LastInsertId()
	return id
}

// installed puts a server in with one tool, at whatever status and approval the
// test is about.
func (f *fixture) installed(t *testing.T, name string, status string, toolApproved bool) (Server, Tool) {
	t.Helper()
	ctx := context.Background()
	srv, err := f.Install(ctx, Server{Name: name, Command: "/usr/bin/true", Args: []string{"--serve"}}, nil)
	if err != nil {
		t.Fatalf("install %s: %v", name, err)
	}
	if err := f.RecordTools(ctx, srv.ID, []mcpwire.Tool{
		{Name: "read_file", Description: "reads one", InputSchema: json.RawMessage(`{"type":"object"}`)},
	}); err != nil {
		t.Fatalf("record tools: %v", err)
	}
	tools, err := f.Tools(ctx, srv.ID)
	if err != nil || len(tools) != 1 {
		t.Fatalf("tools: %v %d", err, len(tools))
	}
	if toolApproved {
		if err := f.ApproveTool(ctx, tools[0].ID, true); err != nil {
			t.Fatal(err)
		}
	}
	if status != StatusPending {
		if srv, err = f.SetStatus(ctx, srv.ID, status); err != nil {
			t.Fatal(err)
		}
	}
	return srv, tools[0]
}

// A newly installed server is pending, and a newly seen tool is unapproved.
// Installing is not approving, and that is the whole shape of this feature.
func TestInstallingIsNotApproving(t *testing.T) {
	f := newFixture(t)
	srv, tool := f.installed(t, "files", StatusPending, false)
	if srv.Status != StatusPending {
		t.Fatalf("a freshly installed server is %q", srv.Status)
	}
	if tool.Approved {
		t.Fatal("a tool a server reported was approved by the act of reporting it")
	}
	if srv.Fingerprint != "" {
		t.Fatal("a server that was never approved carries a fingerprint")
	}
}

// The four gates on what a model may be offered, each broken in turn by the
// mutations in the changelog: bound, server approved, tool approved, and bound
// to THIS agent.
func TestOnlyABoundApprovedServersApprovedToolsAreOffered(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	srv, tool := f.installed(t, "files", StatusApproved, true)

	// Not bound yet.
	got, err := f.ToolsForAgent(ctx, f.wsID, f.agent)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("a server nobody granted is offered: %+v", got)
	}

	if _, err := f.Bind(ctx, srv.ID, f.wsID, &f.agent); err != nil {
		t.Fatalf("bind: %v", err)
	}
	if got, _ = f.ToolsForAgent(ctx, f.wsID, f.agent); len(got) != 1 || got[0].OfferedName != tool.OfferedName {
		t.Fatalf("a granted, approved tool is not offered: %+v", got)
	}

	// The other agent in the same workspace was not granted it.
	if got, _ = f.ToolsForAgent(ctx, f.wsID, f.other); len(got) != 0 {
		t.Fatalf("a per-agent grant reached another agent: %+v", got)
	}

	// Server disabled: nothing, however the tools stand.
	if _, err := f.SetStatus(ctx, srv.ID, StatusDisabled); err != nil {
		t.Fatal(err)
	}
	if got, _ = f.ToolsForAgent(ctx, f.wsID, f.agent); len(got) != 0 {
		t.Fatalf("a disabled server is still offered: %+v", got)
	}
	if _, err := f.SetStatus(ctx, srv.ID, StatusApproved); err != nil {
		t.Fatal(err)
	}

	// Tool unapproved: nothing, however the server stands. This is the gate a
	// server that grows a tool after approval runs into.
	if err := f.ApproveTool(ctx, tool.ID, false); err != nil {
		t.Fatal(err)
	}
	if got, _ = f.ToolsForAgent(ctx, f.wsID, f.agent); len(got) != 0 {
		t.Fatalf("an unapproved tool is offered because its server is approved: %+v", got)
	}
}

// A workspace-wide grant reaches every agent in it, and is one row.
func TestAWorkspaceWideGrantIsOneRowAndReachesEveryAgent(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	srv, _ := f.installed(t, "files", StatusApproved, true)

	if _, err := f.Bind(ctx, srv.ID, f.wsID, nil); err != nil {
		t.Fatalf("bind: %v", err)
	}
	for _, agent := range []int64{f.agent, f.other} {
		if got, _ := f.ToolsForAgent(ctx, f.wsID, agent); len(got) != 1 {
			t.Fatalf("a workspace-wide grant did not reach agent %d", agent)
		}
	}
	// Twice is a conflict rather than a second row. Without COALESCE in the
	// index, two NULL agent_ids are distinct and this silently succeeds.
	if _, err := f.Bind(ctx, srv.ID, f.wsID, nil); !errors.Is(err, catalog.ErrConflict) {
		t.Fatalf("binding a server to the same workspace twice gave %v, want a conflict", err)
	}
	// And a per-agent row may coexist with the workspace-wide one.
	if _, err := f.Bind(ctx, srv.ID, f.wsID, &f.agent); err != nil {
		t.Fatalf("a per-agent grant beside a workspace-wide one: %v", err)
	}
	// Still offered once, not twice: a model given the same tool twice is a
	// model that may call either copy, and the second is not a second tool.
	if got, _ := f.ToolsForAgent(ctx, f.wsID, f.agent); len(got) != 1 {
		t.Fatalf("the tool is offered %d times when two grants overlap", len(got))
	}
}

// Approval covers the command line. Editing it puts the server back to pending,
// and a spawn attempt in between is refused.
func TestChangingTheCommandTakesTheApprovalWithIt(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	srv, _ := f.installed(t, "files", StatusApproved, true)

	if _, err := f.Spawnable(ctx, srv.ID); err != nil {
		t.Fatalf("an approved, unchanged server would not spawn: %v", err)
	}

	// Edit it the way the API does.
	edited := srv
	edited.Args = []string{"--serve", "--and-something-else"}
	if _, err := f.Update(ctx, srv.ID, edited); err != nil {
		t.Fatalf("update: %v", err)
	}
	after, _ := f.Get(ctx, srv.ID)
	if after.Status != StatusPending {
		t.Fatalf("editing a server's command left it %q", after.Status)
	}

	// And the harder case: the row edited behind the store's back, which is
	// what the fingerprint is actually for.
	if _, err := f.SetStatus(ctx, srv.ID, StatusApproved); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.ExecContext(ctx,
		`UPDATE mcp_servers SET command = '/bin/somethingelse' WHERE id = ?`, srv.ID); err != nil {
		t.Fatal(err)
	}
	_, err := f.Spawnable(ctx, srv.ID)
	if !errors.Is(err, ErrChanged) {
		t.Fatalf("a server whose command changed after approval spawned anyway: %v", err)
	}
	back, _ := f.Get(ctx, srv.ID)
	if back.Status != StatusPending {
		t.Fatalf("a changed server was refused but left %q, so the next spawn would try again", back.Status)
	}
}

// A pending or disabled server is never spawned, whatever else is true.
func TestOnlyAnApprovedServerSpawns(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	srv, _ := f.installed(t, "files", StatusPending, true)
	if _, err := f.Spawnable(ctx, srv.ID); err == nil {
		t.Fatal("a pending server was cleared to spawn — an operator has not read it yet")
	}
	if _, err := f.SetStatus(ctx, srv.ID, StatusDisabled); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Spawnable(ctx, srv.ID); err == nil {
		t.Fatal("a disabled server was cleared to spawn")
	}
}

// A tool already known keeps its approval when the server lists again; a new
// one arrives unapproved. This is the mechanism, so it is checked directly.
func TestRelistingKeepsApprovalsAndNewToolsArriveUnapproved(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	srv, tool := f.installed(t, "files", StatusApproved, true)

	if err := f.RecordTools(ctx, srv.ID, []mcpwire.Tool{
		{Name: "read_file", Description: "reads one, now with a better description"},
		{Name: "run_shell", Description: "runs anything at all"},
	}); err != nil {
		t.Fatal(err)
	}
	tools, err := f.Tools(ctx, srv.ID)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]Tool{}
	for _, tl := range tools {
		byName[tl.RemoteName] = tl
	}
	if !byName["read_file"].Approved {
		t.Fatal("a tool lost its approval because the server listed again")
	}
	if byName["read_file"].ID != tool.ID {
		t.Fatal("relisting replaced the row rather than updating it, which loses its history")
	}
	if !strings.Contains(byName["read_file"].Description, "better description") {
		t.Fatal("the description did not update")
	}
	if byName["run_shell"].Approved {
		t.Fatal("a tool that appeared AFTER approval arrived approved — that is the whole attack " +
			"per-tool approval exists to stop")
	}
	// And it is not offered, which is what actually matters.
	if _, err := f.Bind(ctx, srv.ID, f.wsID, nil); err != nil {
		t.Fatal(err)
	}
	got, _ := f.ToolsForAgent(ctx, f.wsID, f.agent)
	if len(got) != 1 || got[0].RemoteName != "read_file" {
		t.Fatalf("the new tool is being offered: %+v", got)
	}
}

// The name a model is offered has to be unique, within the provider's limit,
// and never collide with a gear's.
func TestOfferedNamesAreDisjointFromGearsAndSurviveLongNames(t *testing.T) {
	if got := OfferedName("github", "github"); got != "mcp_github__github" {
		t.Fatalf("offered name is %q", got)
	}
	if strings.HasPrefix(OfferedName("x", "y"), "gear_") {
		t.Fatal("an MCP tool would shadow a gear")
	}
	long := strings.Repeat("verylongtoolname", 6)
	a, b := OfferedName("server", long+"a"), OfferedName("server", long+"b")
	for _, n := range []string{a, b} {
		if len(n) > maxToolName {
			t.Fatalf("%q is %d characters, over what providers accept", n, len(n))
		}
	}
	if a == b {
		t.Fatal("two different long tool names collapsed into one, so the second silently shadows " +
			"the first")
	}
	// Stable: dispatch looks up what was offered, so the same input must give
	// the same name every time.
	if OfferedName("server", long+"a") != a {
		t.Fatal("the offered name is not stable across calls")
	}
	// And unsafe characters are gone, because providers reject them.
	if got := OfferedName("my server!", "read/file"); got != "mcp_my_server__read_file" {
		t.Fatalf("unsafe characters survived: %q", got)
	}
}

// Two servers cannot offer the same name, because dispatch is by name.
func TestTwoServersCannotOfferOneName(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	a, err := f.Install(ctx, Server{Name: "one", Command: "/usr/bin/true"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	b, err := f.Install(ctx, Server{Name: "two", Command: "/usr/bin/true"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.RecordTools(ctx, a.ID, []mcpwire.Tool{{Name: "read"}}); err != nil {
		t.Fatal(err)
	}
	// Different server, same remote name: the offered names differ, so both fit.
	if err := f.RecordTools(ctx, b.ID, []mcpwire.Tool{{Name: "read"}}); err != nil {
		t.Fatalf("two servers with a same-named tool collided: %v", err)
	}
	one, _, err := f.ByOfferedName(ctx, OfferedName("one", "read"))
	if err != nil {
		t.Fatal(err)
	}
	if one.ServerID != a.ID {
		t.Fatal("a tool resolved to the wrong server, so a model calling one would run the other")
	}
}

// A binding belongs to a workspace, so the HTTP layer can refuse a member of
// another one.
func TestABindingKnowsWhichWorkspaceItBelongsTo(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	srv, _ := f.installed(t, "files", StatusApproved, true)
	b, err := f.Bind(ctx, srv.ID, f.wsID, nil)
	if err != nil {
		t.Fatal(err)
	}
	got, err := f.WorkspaceOfBinding(ctx, b.ID)
	if err != nil || got != f.wsID {
		t.Fatalf("WorkspaceOfBinding gave (%d, %v)", got, err)
	}
	if _, err := f.WorkspaceOfBinding(ctx, 9999); !errors.Is(err, catalog.ErrNotFound) {
		t.Fatalf("a binding that does not exist gave %v", err)
	}
}

// Deleting a server takes its tools and its grants with it: a grant to
// something that is gone is a row that means nothing and would outlive the
// decision behind it.
func TestDeletingAServerTakesItsToolsAndGrants(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	srv, _ := f.installed(t, "files", StatusApproved, true)
	if _, err := f.Bind(ctx, srv.ID, f.wsID, nil); err != nil {
		t.Fatal(err)
	}
	if err := f.Delete(ctx, srv.ID); err != nil {
		t.Fatal(err)
	}
	if tools, _ := f.Tools(ctx, srv.ID); len(tools) != 0 {
		t.Fatalf("%d tools outlived their server", len(tools))
	}
	if bs, _ := f.Bindings(ctx, f.wsID); len(bs) != 0 {
		t.Fatalf("%d grants outlived the server they granted", len(bs))
	}
}
