package server

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// The install map, and who is allowed to see how much of it.
//
// Every assertion below greps the RAW RESPONSE BODY rather than a parsed graph.
// That is deliberate and it is the whole point: a client that filters what it
// draws still received the names, and one HTTP response is all it takes to
// learn the shape of an install somebody has no grant on. A test that walks the
// node list would pass on a server that leaked and a client that hid it.

// mapFor asks for the map with one caller's token and hands back the raw body.
func mapFor(t *testing.T, d *door, token string) string {
	t.Helper()
	rec := d.request(t, http.MethodGet, "/api/v1/map", token, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("map answered %d: %s", rec.Code, rec.Body.String())
	}
	return rec.Body.String()
}

// An administrator sees the install. This is the control: without it, every
// assertion below would also pass on a handler that returned an empty graph.
func TestAnAdminSeesTheWholeInstall(t *testing.T) {
	d := newDoor(t)
	if _, _, err := d.users.CreateUser(t.Context(), "mira", "member", ""); err != nil {
		t.Fatalf("create a member: %v", err)
	}
	body := mapFor(t, d, d.adminTok)
	for _, want := range []string{`"kind":"workspace"`, `"kind":"user"`, "mira"} {
		if !strings.Contains(body, want) {
			t.Fatalf("an administrator's map is missing %s — the scoping below would pass on an empty graph too: %s",
				want, body)
		}
	}
}

// The one that matters.
//
// A member with a perfectly good token, in no team and on no workspace, must
// not receive the name or the id of the workspace they cannot reach.
func TestAMembersMapDoesNotNameAWorkspaceTheyCannotReach(t *testing.T) {
	d := newDoor(t)
	_, member, err := d.users.CreateUser(t.Context(), "outsider", "member", "")
	if err != nil {
		t.Fatalf("create a member: %v", err)
	}

	ws, err := d.spaces.GetWorkspace(t.Context(), d.wsID)
	if err != nil {
		t.Fatalf("read the workspace under test: %v", err)
	}

	body := mapFor(t, d, member)
	if strings.Contains(body, ws.Name) {
		t.Fatalf("the map named %q to somebody with no grant on it: %s", ws.Name, body)
	}
	if strings.Contains(body, workspaceNodeID(d.wsID)) {
		t.Fatalf("the map carried the id of a workspace the caller cannot reach: %s", body)
	}
}

// And it does not enumerate the user table either. A member sharing no team
// with anybody learns about themselves and nobody else.
func TestAMembersMapDoesNotEnumerateOtherPeople(t *testing.T) {
	d := newDoor(t)
	_, member, err := d.users.CreateUser(t.Context(), "outsider", "member", "")
	if err != nil {
		t.Fatalf("create a member: %v", err)
	}
	if _, _, err := d.users.CreateUser(t.Context(), "someone-else", "member", ""); err != nil {
		t.Fatalf("create a second member: %v", err)
	}

	body := mapFor(t, d, member)
	if strings.Contains(body, "someone-else") {
		t.Fatalf("the map named an unrelated person to a member: %s", body)
	}
	if !strings.Contains(body, "outsider") {
		t.Fatalf("a member cannot see themselves on their own map: %s", body)
	}
}

// A team the caller is not in is somebody else's — its name and its size are
// not theirs to read.
func TestAMembersMapDoesNotNameTeamsTheyAreNotIn(t *testing.T) {
	d := newDoor(t)
	if _, err := d.users.CreateTeam(t.Context(), "platform"); err != nil {
		t.Fatalf("create a team: %v", err)
	}
	_, member, err := d.users.CreateUser(t.Context(), "outsider", "member", "")
	if err != nil {
		t.Fatalf("create a member: %v", err)
	}

	if body := mapFor(t, d, member); strings.Contains(body, "platform") {
		t.Fatalf("the map named a team the caller is not in: %s", body)
	}
	// The administrator still sees it, so the assertion above is scoping
	// rather than the team having failed to be created.
	if body := mapFor(t, d, d.adminTok); !strings.Contains(body, "platform") {
		t.Fatalf("the team was never created, so the check above proved nothing: %s", body)
	}
}

// No edge may point at a node that was filtered away.
//
// An edge to a missing node is worse than no edge at all: it carries the id of
// the thing it points at, which is exactly what the filtering just removed.
//
// THE FIXTURE IS THE TEST. The first version of this put a member with no
// grants in front of the map and asserted no edge dangled — which it could not,
// because with no visible workspace the loop that emits those edges never runs
// at all. It passed with the guard deleted. What is needed is a caller who CAN
// see a workspace that is ALSO granted to a team they are not in, so a dangling
// edge is physically possible and its absence means something.
func TestNoEdgeSurvivesItsOwnNode(t *testing.T) {
	d := newDoor(t)
	ctx := t.Context()

	mine, err := d.users.CreateTeam(ctx, "mine")
	if err != nil {
		t.Fatalf("create a team: %v", err)
	}
	theirs, err := d.users.CreateTeam(ctx, "theirs")
	if err != nil {
		t.Fatalf("create the other team: %v", err)
	}
	u, member, err := d.users.CreateUser(ctx, "insider", "member", "")
	if err != nil {
		t.Fatalf("create a member: %v", err)
	}
	if err := d.users.AddTeamMember(ctx, mine.ID, u.ID); err != nil {
		t.Fatalf("add to a team: %v", err)
	}
	// Granted to both, so the caller reaches it through one team while the
	// other is filtered out from under an edge that points at it.
	if _, err := d.spaces.ShareWith(ctx, d.wsID, mine.ID); err != nil {
		t.Fatalf("share with the caller's team: %v", err)
	}
	if _, err := d.spaces.ShareWith(ctx, d.wsID, theirs.ID); err != nil {
		t.Fatalf("share with the other team: %v", err)
	}

	rec := d.request(t, http.MethodGet, "/api/v1/map", member, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("map: %d %s", rec.Code, rec.Body.String())
	}
	var g struct {
		Nodes []struct {
			ID string `json:"id"`
		} `json:"nodes"`
		Edges []struct {
			From string `json:"from"`
			To   string `json:"to"`
		} `json:"edges"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &g); err != nil {
		t.Fatalf("the map is not the shape it claims: %v", err)
	}

	// The fixture has to have produced the situation, or the assertion below
	// is checking nothing — the same mistake this test was rewritten to fix.
	if !strings.Contains(rec.Body.String(), workspaceNodeID(d.wsID)) {
		t.Fatal("the caller cannot see the shared workspace, so no edge could dangle and this proves nothing")
	}
	if len(g.Edges) == 0 {
		t.Fatal("no edges at all, so a dangling one could not appear")
	}

	have := map[string]bool{}
	for _, n := range g.Nodes {
		have[n.ID] = true
	}
	for _, e := range g.Edges {
		if !have[e.From] || !have[e.To] {
			t.Fatalf("edge %s -> %s points outside the filtered graph, naming a node the caller may not see: %s",
				e.From, e.To, rec.Body.String())
		}
	}
	// And the other team's name never appears, by any route.
	if strings.Contains(rec.Body.String(), "theirs") {
		t.Fatalf("a team the caller is not in was named through a grant: %s", rec.Body.String())
	}
}
