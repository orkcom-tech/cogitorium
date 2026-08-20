package engine

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/orkcom-tech/cogitorium/internal/catalog"
	"github.com/orkcom-tech/cogitorium/internal/contextstore"
	"github.com/orkcom-tech/cogitorium/internal/gear"
	"github.com/orkcom-tech/cogitorium/internal/identity"
	"github.com/orkcom-tech/cogitorium/internal/llm"
	"github.com/orkcom-tech/cogitorium/internal/store"
	"github.com/orkcom-tech/cogitorium/internal/work"
	"github.com/orkcom-tech/cogitorium/internal/workspace"
)

// A fake contextd that COUNTS how many times it is spawned.
//
// Counting the process is the point. The cost this cache exists to remove is
// not a function call, it is a fork-exec of a CLI that opens a space and reads
// a file, and a test that mocked a Go interface would prove nothing about it.
// Every invocation appends a line to a file; the test reads the file.
func fakeContextd(t *testing.T, dir string, files map[string]string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake contextd is a shell script")
	}
	bin := filepath.Join(dir, "contextd")
	log := filepath.Join(dir, "calls.log")
	space := filepath.Join(dir, "space")
	if err := os.MkdirAll(space, 0o755); err != nil {
		t.Fatal(err)
	}
	var listing []string
	for path, body := range files {
		full := filepath.Join(space, strings.ReplaceAll(path, "/", "__"))
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		listing = append(listing, `{"path":"`+path+`","version":"v7"}`)
	}
	script := `#!/bin/sh
echo "$@" >> ` + log + `
if [ "$1" = "file" ] && [ "$2" = "list" ]; then
  echo '[` + strings.Join(listing, ",") + `]'
  exit 0
fi
if [ "$1" = "file" ] && [ "$2" = "get" ]; then
  name=$(echo "$3" | sed 's|/|__|g')
  cat ` + space + `/$name
  exit 0
fi
exit 1
`
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin
}

func countCalls(t *testing.T, dir, prefix string) int {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "calls.log"))
	if err != nil {
		return 0
	}
	n := 0
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(line, prefix) {
			n++
		}
	}
	return n
}

type cacheFixture struct {
	engine *Engine
	dir    string
	wsID   int64
	agents []workspace.Agent
}

func newCacheFixture(t *testing.T, docs map[string]string, agentNames ...string) cacheFixture {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()

	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	ws := workspace.NewStore(db)
	cat := catalog.NewStore(db)
	cs := contextstore.New(fakeContextd(t, dir, docs))

	p, _ := cat.CreateProvider(ctx, "anthropic-live", llm.TypeAnthropic, "", "")
	m, _ := cat.CreateModel(ctx, p.ID, "claude-sonnet-4-6", "")
	owner, _, _ := identity.NewStore(db).CreateUser(ctx, "operator", "member", "")
	space, err := ws.CreateWorkspace(ctx, "atlas", "", m.ID, owner.ID)
	if err != nil {
		t.Fatal(err)
	}

	e := New(ws, cat, cs, gear.NewStore(db), nil, nil, nil, nil, work.NewStore(db), Budgets{}, "")
	f := cacheFixture{engine: e, dir: dir, wsID: space.ID}
	for _, name := range agentNames {
		a, err := ws.CreateAgentSpec(ctx, space.ID, workspace.AgentSpec{
			Name: name, Role: "You work.", ModelID: &m.ID,
		})
		if err != nil {
			t.Fatal(err)
		}
		f.agents = append(f.agents, a)
	}
	for path := range docs {
		if _, err := ws.CreateContextBinding(ctx, space.ID, path, nil); err != nil {
			t.Fatal(err)
		}
	}
	return f
}

func TestOneReadPerDocumentPerRun(t *testing.T) {
	docs := map[string]string{"team/style.md": "Write plainly.", "team/rules.md": "Ship on Friday."}
	f := newCacheFixture(t, docs, "orchestrator-a", "worker-b", "worker-c")
	ctx := context.Background()

	f.engine.beginTurn(f.wsID)
	// Every agent in the tree assembles its prompt, several turns each — which
	// is what actually happens: systemPrompt runs before every model call.
	for iteration := 0; iteration < 4; iteration++ {
		for _, a := range f.agents {
			if _, err := f.engine.systemPrompt(ctx, f.wsID, a, ""); err != nil {
				t.Fatalf("assemble prompt: %v", err)
			}
		}
	}

	gets := countCalls(t, f.dir, "file get")
	if gets != len(docs) {
		t.Fatalf("12 prompt assemblies over %d documents spawned contextd %d times; want %d — one per document",
			len(docs), gets, len(docs))
	}
	if lists := countCalls(t, f.dir, "file list"); lists != 1 {
		t.Fatalf("the space was listed %d times in one run; want 1", lists)
	}
}

func TestTheRunRecordsWhichVersionsFedIt(t *testing.T) {
	f := newCacheFixture(t, map[string]string{"team/style.md": "Write plainly."}, "worker")
	ctx := context.Background()

	f.engine.beginTurn(f.wsID)
	if _, err := f.engine.systemPrompt(ctx, f.wsID, f.agents[0], ""); err != nil {
		t.Fatal(err)
	}
	out := f.engine.outcome(f.wsID, "done")

	if len(out.Did.Context) != 1 {
		t.Fatalf("the record names %d documents; want 1: %+v", len(out.Did.Context), out.Did.Context)
	}
	if got := out.Did.Context[0]; got.Path != "team/style.md" || got.Version != "v7" {
		t.Fatalf("the record must name the document AND its version, got %+v", got)
	}
}

func TestTheNextRunReadsAgain(t *testing.T) {
	// A cache that outlived its run would serve a prompt from a document the
	// operator has since rewritten, which is a worse bug than a slow one.
	f := newCacheFixture(t, map[string]string{"team/style.md": "Write plainly."}, "worker")
	ctx := context.Background()

	for run := 0; run < 3; run++ {
		f.engine.beginTurn(f.wsID)
		if _, err := f.engine.systemPrompt(ctx, f.wsID, f.agents[0], ""); err != nil {
			t.Fatal(err)
		}
		f.engine.endTurn(f.wsID)
	}
	if gets := countCalls(t, f.dir, "file get"); gets != 3 {
		t.Fatalf("three separate runs read the document %d times; want 3 — the cache must die with its run", gets)
	}
}

func TestOutsideARunThereIsNoCache(t *testing.T) {
	// A UI panel asking what an agent can see is a person waiting on one
	// request, and it must see the space as it is now.
	f := newCacheFixture(t, map[string]string{"team/style.md": "Write plainly."}, "worker")
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		if _, err := f.engine.systemPrompt(ctx, f.wsID, f.agents[0], ""); err != nil {
			t.Fatal(err)
		}
	}
	if gets := countCalls(t, f.dir, "file get"); gets != 3 {
		t.Fatalf("outside a run the document was read %d times; want 3 — no caching without a turn to hang it on", gets)
	}
}

// The wiring, not the struct: nothing above proved that execToolAs — the one
// funnel every tool call goes through — actually hands the call's arguments to
// the record. A test that builds a ToolRun by hand passes with that wire cut.
func TestTheFunnelRecordsTheArgumentsItWasCalledWith(t *testing.T) {
	f := newCacheFixture(t, map[string]string{}, "worker")
	ctx := context.Background()
	f.engine.beginTurn(f.wsID)

	// An unknown tool: it is refused, and a refusal is still a call the model
	// made — with arguments worth reading at 3am.
	f.engine.execToolAs(ctx, f.wsID, f.agents[0], nil,
		llm.ToolCall{Name: "no_such_tool", InputJSON: `{"target":"production"}`},
		func(Event) {})

	out := f.engine.outcome(f.wsID, "")
	if len(out.Did.Tools) != 1 {
		t.Fatalf("the record holds %d tool calls; want 1", len(out.Did.Tools))
	}
	got := out.Did.Tools[0]
	if got.OK {
		t.Fatalf("a refused call must be recorded as refused")
	}
	if !strings.Contains(string(got.Args), `"target":"production"`) {
		t.Fatalf("the arguments did not travel from the call to the record: %s", got.Args)
	}
}

// context_search is offered under exactly the conditions dispatchTool accepts
// it under. A tool that is offered and always refused costs a paid round-trip
// on every iteration of every run.
func TestSearchIsOfferedOnlyWhereItWillBeAccepted(t *testing.T) {
	f := newCacheFixture(t, map[string]string{}, "worker")
	orch, err := f.engine.ws.GetAgentByName(context.Background(), f.wsID, "orchestrator")
	if err != nil {
		t.Fatal(err)
	}
	worker := f.agents[0]

	offered := func(a workspace.Agent, unattended bool) bool {
		for _, tl := range f.engine.toolsFor(a, nil, nil, nil, false, false, unattended, false) {
			if tl.Name == "context_search" {
				return true
			}
		}
		return false
	}
	if !offered(orch, false) {
		t.Error("the orchestrator on an ordinary turn cannot search its own memory")
	}
	if offered(worker, false) {
		t.Error("a worker is offered a tool it will be refused on every iteration")
	}
	if offered(orch, true) {
		t.Error("an inlet keyholder is offered a grep of the whole context space")
	}
}
