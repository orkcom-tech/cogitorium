package engine

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/orkcom-tech/cogitorium/internal/mcp/mcpwire"
	"github.com/orkcom-tech/cogitorium/internal/mcpstore"
	"github.com/orkcom-tech/cogitorium/internal/secrets"
	"github.com/orkcom-tech/cogitorium/internal/workspace"
)

// The engine's half of external MCP servers.
//
// The claim that carries the most weight here is negative: a model naming a
// tool it was never granted must not cause somebody else's binary to START on
// this host. Everywhere else in this product an unauthorised call is refused
// and that is the end of it; here the spawn IS the dangerous act, so the check
// has to come first and the test has to prove nothing ran.
//
// The MCP server used below records the fact that it was started, in a file, so
// "nothing ran" is checked against the filesystem rather than against a counter
// this package could have got wrong.
const recordingServer = `package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
)

func main() {
	// Written before anything is read: being started at all is the thing under
	// test, whatever the client then does.
	os.WriteFile(os.Getenv("SPAWN_RECORD"), []byte("started"), 0o644)
	in := bufio.NewReader(os.Stdin)
	for {
		line, err := in.ReadString('\n')
		if err != nil {
			return
		}
		var m map[string]any
		if json.Unmarshal([]byte(line), &m) != nil {
			continue
		}
		method, _ := m["method"].(string)
		id := m["id"]
		send := func(result any) {
			b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
			fmt.Println(string(b))
		}
		switch method {
		case "initialize":
			send(map[string]any{"protocolVersion": "2024-11-05",
				"serverInfo": map[string]any{"name": "recorder", "version": "1"}})
		case "tools/list":
			send(map[string]any{"tools": []any{map[string]any{
				"name": "lookup", "description": "looks something up",
				"inputSchema": map[string]any{"type": "object"},
			}}})
		case "tools/call":
			p, _ := m["params"].(map[string]any)
			args, _ := p["arguments"].(map[string]any)
			send(map[string]any{"content": []any{map[string]any{
				"type": "text",
				"text": fmt.Sprintf("looked up %v with SECRET=%s", args["q"], os.Getenv("LOOKUP_TOKEN")),
			}}})
		}
	}
}
`

type mcpFixture struct {
	*filesFixture
	store  *mcpstore.Store
	server mcpstore.Server
	tool   mcpstore.Tool
	record string
	worker workspace.Agent
}

// newMCPFixture builds a real engine with a real MCP server compiled for it.
func newMCPFixture(t *testing.T) *mcpFixture {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("no go toolchain to build an MCP server with")
	}
	f := newFilesFixture(t)
	ctx := context.Background()

	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "main.go"), []byte(recordingServer), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "go.mod"), []byte("module recorder\n\ngo 1.25\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(src, "recorder")
	build := exec.Command("go", "build", "-o", bin, ".")
	build.Dir = src
	if out, err := build.CombinedOutput(); err != nil {
		t.Skipf("cannot build the MCP server: %v: %s", err, out)
	}

	record := filepath.Join(t.TempDir(), "spawned")
	db := f.db
	ms := mcpstore.NewStore(db)
	resolver, err := secrets.NewResolver(secrets.NewStore(db, nil), "", "")
	if err != nil {
		t.Fatal(err)
	}
	f.e.SetMCP(ms, resolver)

	srv, err := ms.Install(ctx, mcpstore.Server{
		Name: "recorder", Command: bin, EnvNames: []string{"SPAWN_RECORD"},
	}, nil)
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	// SPAWN_RECORD is set as a named VALUE, so the path the server writes to
	// travels the same way a credential would — which also means this fixture
	// exercises the resolver rather than stepping around it.
	if _, err := secrets.NewStore(db, nil).Set(ctx, nil, "SPAWN_RECORD", secrets.KindVariable, record, ""); err != nil {
		t.Fatal(err)
	}
	if err := ms.RecordTools(ctx, srv.ID, []mcpwire.Tool{{
		Name: "lookup", Description: "looks something up",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"q":{"type":"string"}}}`),
	}}); err != nil {
		t.Fatal(err)
	}
	tools, _ := ms.Tools(ctx, srv.ID)
	if len(tools) != 1 {
		t.Fatalf("expected one tool, got %d", len(tools))
	}

	// A second agent, so a per-agent grant has somebody to exclude. It shares
	// the orchestrator's model, which is the one this fixture put in the
	// catalog.
	worker, err := f.ws.CreateAgent(ctx, f.wsID, "worker", "does the work", *f.orch.ModelID)
	if err != nil {
		t.Fatalf("hire a worker: %v", err)
	}
	return &mcpFixture{filesFixture: f, store: ms, server: srv, tool: tools[0], record: record, worker: worker}
}

func (m *mcpFixture) spawned() bool {
	_, err := os.Stat(m.record)
	return err == nil
}

// approveAndGrant is the operator's three acts: approve the server, approve the
// tool, grant it to the workspace.
func (m *mcpFixture) approveAndGrant(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	if _, err := m.store.SetStatus(ctx, m.server.ID, mcpstore.StatusApproved); err != nil {
		t.Fatal(err)
	}
	if err := m.store.ApproveTool(ctx, m.tool.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := m.store.Bind(ctx, m.server.ID, m.wsID, nil); err != nil {
		t.Fatal(err)
	}
}

// Nothing is spawned for a tool this agent was never granted.
func TestAnUngrantedMCPToolStartsNothing(t *testing.T) {
	m := newMCPFixture(t)
	// Approved and its tool approved, but never bound to anything: the model
	// naming it is the case where a check that ran after the spawn would
	// already have lost.
	ctx := context.Background()
	if _, err := m.store.SetStatus(ctx, m.server.ID, mcpstore.StatusApproved); err != nil {
		t.Fatal(err)
	}
	if err := m.store.ApproveTool(ctx, m.tool.ID, true); err != nil {
		t.Fatal(err)
	}

	_, err := m.e.runMCPTool(ctx, m.wsID, m.orch, m.tool.OfferedName, `{"q":"x"}`)
	if err == nil {
		t.Fatal("a tool nobody granted was called")
	}
	if !strings.Contains(err.Error(), "not a tool this agent was granted") {
		t.Fatalf("the refusal does not say why: %v", err)
	}
	if m.spawned() {
		t.Fatal("the MCP server was STARTED before the grant was checked — the spawn is the dangerous " +
			"act here, so refusing afterwards is refusing too late")
	}
}

// Nor for a server the operator has not approved.
func TestAnUnapprovedMCPServerStartsNothing(t *testing.T) {
	m := newMCPFixture(t)
	ctx := context.Background()
	// Granted and its tool approved, but the SERVER left pending.
	if err := m.store.ApproveTool(ctx, m.tool.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := m.store.Bind(ctx, m.server.ID, m.wsID, nil); err != nil {
		t.Fatal(err)
	}
	_, err := m.e.runMCPTool(ctx, m.wsID, m.orch, m.tool.OfferedName, `{"q":"x"}`)
	if err == nil {
		t.Fatal("a pending server was run")
	}
	if m.spawned() {
		t.Fatal("a pending MCP server was started")
	}
}

// A command changed after approval starts nothing, and the server goes back to
// pending.
func TestAChangedCommandStartsNothing(t *testing.T) {
	m := newMCPFixture(t)
	ctx := context.Background()
	m.approveAndGrant(t)

	// Edited behind the store's back, which is what the fingerprint is for.
	if _, err := m.db.ExecContext(ctx,
		`UPDATE mcp_servers SET command = '/bin/echo' WHERE id = ?`, m.server.ID); err != nil {
		t.Fatal(err)
	}
	_, err := m.e.runMCPTool(ctx, m.wsID, m.orch, m.tool.OfferedName, `{"q":"x"}`)
	if err == nil {
		t.Fatal("a server whose command changed after approval was run")
	}
	if m.spawned() {
		t.Fatal("a changed command was started before the fingerprint was checked")
	}
	after, _ := m.store.Get(ctx, m.server.ID)
	if after.Status != mcpstore.StatusApproved {
		return // returned to pending, which is the intended outcome
	}
	t.Fatal("a changed server stayed approved, so the next call would try it again")
}

// The whole path, once every gate is open: the server starts, the tool runs,
// and what it was granted reached it.
func TestAGrantedMCPToolRunsAndIsGivenWhatItWasGranted(t *testing.T) {
	m := newMCPFixture(t)
	ctx := context.Background()
	m.approveAndGrant(t)

	out, err := m.e.runMCPTool(ctx, m.wsID, m.orch, m.tool.OfferedName, `{"q":"a question"}`)
	if err != nil {
		t.Fatalf("a granted tool would not run: %v", err)
	}
	if !strings.Contains(out, "looked up a question") {
		t.Fatalf("the tool's answer did not come back: %q", out)
	}
	if !m.spawned() {
		t.Fatal("the tool answered without the server ever being started, which cannot be true")
	}
}

// A per-agent grant does not reach another agent, and starts nothing for it.
func TestAPerAgentGrantDoesNotStartAServerForAnotherAgent(t *testing.T) {
	m := newMCPFixture(t)
	ctx := context.Background()
	if _, err := m.store.SetStatus(ctx, m.server.ID, mcpstore.StatusApproved); err != nil {
		t.Fatal(err)
	}
	if err := m.store.ApproveTool(ctx, m.tool.ID, true); err != nil {
		t.Fatal(err)
	}
	// Granted to the worker only.
	if _, err := m.store.Bind(ctx, m.server.ID, m.wsID, &m.worker.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := m.e.runMCPTool(ctx, m.wsID, m.orch, m.tool.OfferedName, `{"q":"x"}`); err == nil {
		t.Fatal("an agent used another agent's grant")
	}
	if m.spawned() {
		t.Fatal("another agent's grant started the server")
	}
	// And the agent that WAS granted it can.
	if _, err := m.e.runMCPTool(ctx, m.wsID, m.worker, m.tool.OfferedName, `{"q":"x"}`); err != nil {
		t.Fatalf("the agent the grant was for could not use it: %v", err)
	}
}

// The tools a model is offered are the granted ones, named so they cannot
// collide with a gear's.
func TestMCPToolsAreOfferedBesideGearsWithADisjointPrefix(t *testing.T) {
	m := newMCPFixture(t)
	m.approveAndGrant(t)

	tools, err := m.e.mcpToolsFor(context.Background(), m.wsID, m.orch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 1 {
		t.Fatalf("the granted tool is not offered: %+v", tools)
	}
	offered := m.e.toolsFor(m.orch, nil, nil, tools, false, false, true)
	var found bool
	for _, tool := range offered {
		if tool.Name == m.tool.OfferedName {
			found = true
			if strings.HasPrefix(tool.Name, gearToolPrefix) {
				t.Fatal("an MCP tool is named like a gear, so one would shadow the other")
			}
			if !strings.Contains(tool.Description, "recorder") {
				t.Fatalf("the description does not say where the tool comes from: %q", tool.Description)
			}
			// The server's own schema, carried through rather than rewritten.
			if tool.InputSchema["type"] != "object" {
				t.Fatalf("the remote schema did not survive: %+v", tool.InputSchema)
			}
		}
	}
	if !found {
		t.Fatal("the granted MCP tool was not in the list handed to the model")
	}
}

// With the capability off, nothing is offered and nothing can be called — the
// path is unreachable rather than merely unused.
func TestWithTheCapabilityOffNothingIsOfferedOrCallable(t *testing.T) {
	f := newFilesFixture(t)
	tools, err := f.e.mcpToolsFor(context.Background(), f.wsID, f.orch.ID)
	if err != nil || len(tools) != 0 {
		t.Fatalf("an install that never switched MCP on offered %d tools (%v)", len(tools), err)
	}
	_, err = f.e.runMCPTool(context.Background(), f.wsID, f.orch, "mcp_x__y", `{}`)
	if err == nil || !strings.Contains(err.Error(), "switched on") {
		t.Fatalf("calling an MCP tool with the capability off gave %v", err)
	}
}
