package server

import (
	"net/http"
	"strings"
	"testing"

	"github.com/orkcom-tech/cogitorium/internal/config"
)

// The operator's side of external MCP servers, over HTTP.
//
// The claim under test is a boundary rather than a behaviour: nothing short of
// an administrator may install, approve or grant one, and with the capability
// off none of it exists at all. An external MCP server is a command this
// install never saw the source of, running on the host with this server's file
// access — so "who may do this" is the whole of the control.

// newDoorWithMCP is the ordinary fixture with external MCP servers switched on,
// which is the only way any of the routes below exist at all.
func newDoorWithMCP(t *testing.T) *door {
	t.Helper()
	return doorAround(t, newInstall(t, doorListen, func(c *config.Config) { c.MCPClients = true }))
}

// memberToken is somebody with a perfectly good token who is not an
// administrator — the caller every write below must refuse.
func (d *door) memberToken(t *testing.T) string {
	t.Helper()
	_, tok, err := d.users.CreateUser(t.Context(), "member-"+t.Name(), "member", "")
	if err != nil {
		t.Fatalf("create a member: %v", err)
	}
	return tok
}

// With the capability off, every route says so rather than half-working.
func TestWithMCPOffEveryRouteSaysSo(t *testing.T) {
	d := newDoor(t)
	for _, c := range []struct{ method, path, body string }{
		{http.MethodGet, "/api/v1/mcp-servers", ""},
		{http.MethodPost, "/api/v1/mcp-servers", `{"name":"x","command":"/bin/true"}`},
		{http.MethodPatch, "/api/v1/mcp-servers/1", `{"status":"approved"}`},
		{http.MethodDelete, "/api/v1/mcp-servers/1", ""},
		{http.MethodPost, "/api/v1/mcp-servers/1/probe", ""},
		{http.MethodGet, "/api/v1/mcp-servers/1/tools", ""},
		{http.MethodPatch, "/api/v1/mcp-tools/1", `{"approved":true}`},
		{http.MethodGet, "/api/v1/workspaces/" + id(d.wsID) + "/mcp-bindings", ""},
		{http.MethodPost, "/api/v1/workspaces/" + id(d.wsID) + "/mcp-bindings", `{"server_id":1}`},
		{http.MethodDelete, "/api/v1/mcp-bindings/1", ""},
	} {
		rec := d.request(t, c.method, c.path, d.adminTok, c.body)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s %s answered %d with the capability off; want 404 and a sentence: %s",
				c.method, c.path, rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), "mcp_clients") {
			t.Fatalf("%s %s does not say how to switch it on: %s", c.method, c.path, rec.Body.String())
		}
	}
}

// And that the routes exist at all, so the test above is not passing because
// the paths are unregistered.
func TestTheMCPRoutesAreRegistered(t *testing.T) {
	d := newDoor(t)
	want := map[string]bool{
		"/api/v1/mcp-servers":                  false,
		"/api/v1/mcp-servers/{id}":             false,
		"/api/v1/mcp-servers/{id}/probe":       false,
		"/api/v1/mcp-servers/{id}/tools":       false,
		"/api/v1/mcp-tools/{id}":               false,
		"/api/v1/workspaces/{id}/mcp-bindings": false,
		"/api/v1/mcp-bindings/{id}":            false,
	}
	for _, r := range d.srv.Routes() {
		if _, ok := want[r.Path]; ok {
			want[r.Path] = true
		}
	}
	for path, found := range want {
		if !found {
			t.Fatalf("%s is not registered, so the 404 above means 'no such route' rather than "+
				"'not switched on'", path)
		}
	}
}

// Every write is an administrator's. A member with a perfectly good token gets
// 403 from all of them.
func TestOnlyAnAdminMayInstallApproveOrGrantAnMCPServer(t *testing.T) {
	d := newDoorWithMCP(t)
	member := d.memberToken(t)

	for _, c := range []struct{ method, path, body string }{
		{http.MethodPost, "/api/v1/mcp-servers", `{"name":"x","command":"/bin/true"}`},
		{http.MethodPatch, "/api/v1/mcp-servers/1", `{"status":"approved"}`},
		{http.MethodDelete, "/api/v1/mcp-servers/1", ""},
		{http.MethodPost, "/api/v1/mcp-servers/1/probe", ""},
		{http.MethodPatch, "/api/v1/mcp-tools/1", `{"approved":true}`},
		{http.MethodPost, "/api/v1/workspaces/" + id(d.wsID) + "/mcp-bindings", `{"server_id":1}`},
	} {
		rec := d.request(t, c.method, c.path, member, c.body)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("%s %s answered %d for a non-admin; want 403. Installing, approving or granting an "+
				"external MCP server is the one act that must stay an administrator's: %s",
				c.method, c.path, rec.Code, rec.Body.String())
		}
	}
}

// Installing is not approving, and the response says so rather than leaving it
// to be inferred.
func TestInstallingLeavesTheServerPending(t *testing.T) {
	d := newDoorWithMCP(t)
	rec := d.request(t, http.MethodPost, "/api/v1/mcp-servers", d.adminTok,
		`{"name":"files","command":"/usr/bin/true","args":["--serve"]}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("install answered %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"status":"pending"`) {
		t.Fatalf("a freshly installed server is not pending: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"approved_fingerprint":""`) {
		t.Fatalf("a server nobody approved carries a fingerprint: %s", rec.Body.String())
	}
}

// An edit and an approval cannot arrive in one request. Approving what you have
// just changed is approving something you have not seen.
func TestAnEditAndAnApprovalCannotBeOneAct(t *testing.T) {
	d := newDoorWithMCP(t)
	rec := d.request(t, http.MethodPost, "/api/v1/mcp-servers", d.adminTok,
		`{"name":"files","command":"/usr/bin/true"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("install: %s", rec.Body.String())
	}
	rec = d.request(t, http.MethodPatch, "/api/v1/mcp-servers/1", d.adminTok,
		`{"status":"approved"}`)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"status":"approved"`) {
		t.Fatalf("approving: %d %s", rec.Code, rec.Body.String())
	}
	// Now an edit, in its own request: it must take the approval with it.
	rec = d.request(t, http.MethodPatch, "/api/v1/mcp-servers/1", d.adminTok,
		`{"command":"/bin/somethingelse"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("editing: %d %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"status":"pending"`) {
		t.Fatalf("editing an approved server's command left it approved: %s", rec.Body.String())
	}
	// And an attempt to do both at once takes the status branch only, never
	// applying the edit under cover of the approval.
	rec = d.request(t, http.MethodPatch, "/api/v1/mcp-servers/1", d.adminTok,
		`{"status":"approved","command":"/bin/yetanother"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "yetanother") {
		t.Fatal("an edit rode along with an approval, so the operator approved a command they were " +
			"changing in the same breath")
	}
}

// A member of one workspace cannot remove another workspace's grant by naming
// its id.
func TestAGrantCannotBeRemovedFromOutsideItsWorkspace(t *testing.T) {
	d := newDoorWithMCP(t)
	if rec := d.request(t, http.MethodPost, "/api/v1/mcp-servers", d.adminTok,
		`{"name":"files","command":"/usr/bin/true"}`); rec.Code != http.StatusCreated {
		t.Fatalf("install: %s", rec.Body.String())
	}
	if rec := d.request(t, http.MethodPost, "/api/v1/workspaces/"+id(d.wsID)+"/mcp-bindings",
		d.adminTok, `{"server_id":1}`); rec.Code != http.StatusCreated {
		t.Fatalf("bind: %s", rec.Body.String())
	}
	// Somebody with a token who is not in that workspace at all.
	rec := d.request(t, http.MethodDelete, "/api/v1/mcp-bindings/1", d.memberToken(t), "")
	if rec.Code == http.StatusNoContent {
		t.Fatal("a caller outside the binding's workspace removed the grant by naming its id")
	}
	// And the administrator, who is in it, can.
	if rec := d.request(t, http.MethodDelete, "/api/v1/mcp-bindings/1", d.adminTok, ""); rec.Code != http.StatusNoContent {
		t.Fatalf("the grant could not be removed by somebody entitled to: %d %s", rec.Code, rec.Body.String())
	}
}

// ── the read surface, once it is on a screen ──────────────────────────────

// A member may see THAT a server exists and whether it is approved — that is
// why their agent has a tool. They may not see how it is spawned.
//
// This route was open and unredacted while nothing called it, which is
// survivable when an operator has to know the endpoint. Putting it in a drawer
// turns "reachable by curl" into "on everybody's screen", and what it carries
// is a full command line and the NAMES of every credential the server is
// handed — a map of this install's integrations and internal hostnames.
func TestAMemberSeesThatAServerExistsAndNotHowItIsSpawned(t *testing.T) {
	d := newDoorWithMCP(t)
	member := d.memberToken(t)

	rec := d.request(t, http.MethodPost, "/api/v1/mcp-servers", d.adminTok,
		`{"name":"jira","description":"our tickets","command":"npx",`+
			`"args":["-y","@acme/jira-mcp","--site","acme.internal.example"],"env_names":["JIRA_TOKEN"]}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("install: %d %s", rec.Code, rec.Body.String())
	}

	// The admin sees everything.
	rec = d.request(t, http.MethodGet, "/api/v1/mcp-servers", d.adminTok, "")
	for _, want := range []string{"npx", "acme.internal.example", "JIRA_TOKEN"} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Fatalf("an administrator cannot see %q, which they need in order to approve it: %s",
				want, rec.Body.String())
		}
	}

	// The member sees the name and the status, and nothing else.
	rec = d.request(t, http.MethodGet, "/api/v1/mcp-servers", member, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("a member could not list servers at all: %d %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, leak := range []string{"npx", "acme.internal.example", "JIRA_TOKEN"} {
		if strings.Contains(body, leak) {
			t.Fatalf("a member can read %q from the server listing: %s", leak, body)
		}
	}
	// And still enough to understand why an agent has a tool.
	for _, keep := range []string{"jira", "our tickets", "pending"} {
		if !strings.Contains(body, keep) {
			t.Fatalf("the redacted listing dropped %q, which a member needs to make sense of it: %s", keep, body)
		}
	}
}

// The library is installed FROM, so it is an administrator's, like every other
// act that spawns a subprocess on this server.
func TestOnlyAnAdministratorMayReadTheLibrary(t *testing.T) {
	d := newDoorWithMCP(t)

	rec := d.request(t, http.MethodGet, "/api/v1/mcp-catalog", d.memberToken(t), "")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("a member read the library: %d %s", rec.Code, rec.Body.String())
	}

	rec = d.request(t, http.MethodGet, "/api/v1/mcp-catalog", d.adminTok, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("an administrator could not read the library: %d %s", rec.Code, rec.Body.String())
	}
	// The sentence that has to reach the review screen verbatim rather than
	// being softened on the way.
	if !strings.Contains(rec.Body.String(), "downloaded when it starts") {
		t.Fatalf("the library does not carry the fetched-at-spawn warning: %s", rec.Body.String())
	}
}

// With the capability off the library does not exist either, and says how to
// switch it on rather than answering an empty list.
func TestTheLibraryIsAbsentWhenMCPIsOff(t *testing.T) {
	d := newDoor(t)
	rec := d.request(t, http.MethodGet, "/api/v1/mcp-catalog", d.adminTok, "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("the library answered %d with the capability off: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "mcp_clients") {
		t.Fatalf("it does not say how to switch it on: %s", rec.Body.String())
	}
}
