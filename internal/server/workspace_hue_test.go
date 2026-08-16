package server

import (
	"net/http"
	"strings"
	"testing"
)

// A workspace's colour.
//
// It is cosmetic, and that is exactly why the tests below are about the shape
// of the field rather than the value: a colour nobody can clear, or one that
// silently reappears after an unrelated edit, is worse than no colour at all.
// The one claim that is not cosmetic is the last: setting a colour must not be
// a way to touch a workspace you cannot otherwise reach.

// A fresh workspace has no colour. Not grey — none.
//
// The distinction carries weight downstream: only a workspace nobody has
// coloured may be given one derived from its id, so if this ever returns a
// number the interface stops being able to tell "unset" from "chosen".
func TestAFreshWorkspaceHasNoColour(t *testing.T) {
	d := newDoor(t)
	rec := d.request(t, http.MethodGet, "/api/v1/workspaces/"+id(d.wsID), d.adminTok, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("get workspace: %d %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"hue":null`) {
		t.Fatalf("a workspace nobody has coloured came back with one: %s", rec.Body.String())
	}
}

func TestAColourIsKeptAndCanBeTakenAway(t *testing.T) {
	d := newDoor(t)
	path := "/api/v1/workspaces/" + id(d.wsID)

	rec := d.request(t, http.MethodPatch, path, d.adminTok, `{"hue":264}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("set a colour: %d %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"hue":264`) {
		t.Fatalf("the response does not carry the colour just set: %s", rec.Body.String())
	}
	// And it is stored, rather than merely echoed back.
	rec = d.request(t, http.MethodGet, path, d.adminTok, "")
	if !strings.Contains(rec.Body.String(), `"hue":264`) {
		t.Fatalf("the colour did not survive being written: %s", rec.Body.String())
	}

	// An explicit null takes it away and returns it to unset.
	rec = d.request(t, http.MethodPatch, path, d.adminTok, `{"hue":null}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("clear the colour: %d %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"hue":null`) {
		t.Fatalf("a cleared colour is not unset: %s", rec.Body.String())
	}
}

// An absent field and an explicit null are DIFFERENT.
//
// This is the whole reason the handler decodes into **int. Collapse the two and
// every future field on this route erases somebody's colour as a side effect of
// editing something else — the kind of bug that is found months later and never
// traced back to the request that caused it.
func TestOmittingTheFieldIsNotTheSameAsClearingIt(t *testing.T) {
	d := newDoor(t)
	path := "/api/v1/workspaces/" + id(d.wsID)

	if rec := d.request(t, http.MethodPatch, path, d.adminTok, `{"hue":18}`); rec.Code != http.StatusOK {
		t.Fatalf("set a colour: %s", rec.Body.String())
	}
	rec := d.request(t, http.MethodPatch, path, d.adminTok, `{}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("a body naming no field answered %d; want 400 rather than a silent no-op: %s",
			rec.Code, rec.Body.String())
	}
	// And the colour is still there, which is the part that actually matters.
	rec = d.request(t, http.MethodGet, path, d.adminTok, "")
	if !strings.Contains(rec.Body.String(), `"hue":18`) {
		t.Fatalf("a request that named no field still changed the colour: %s", rec.Body.String())
	}
}

// A hue is an angle, so 420 and 60 are the same colour. Refusing one of them
// would be pedantry at an API nobody would enjoy using.
func TestAHueWrapsRatherThanBeingRefused(t *testing.T) {
	d := newDoor(t)
	path := "/api/v1/workspaces/" + id(d.wsID)
	for _, c := range []struct {
		send string
		want string
	}{
		{`{"hue":420}`, `"hue":60`},
		{`{"hue":-30}`, `"hue":330`},
		{`{"hue":360}`, `"hue":0`},
	} {
		rec := d.request(t, http.MethodPatch, path, d.adminTok, c.send)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s answered %d: %s", c.send, rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), c.want) {
			t.Fatalf("%s did not wrap to %s: %s", c.send, c.want, rec.Body.String())
		}
	}
}

// The only claim here that is not cosmetic.
//
// Colouring is deliberately NOT an administrator's — the person who works in a
// room every day should be able to fix a colour they cannot tell apart from the
// one beside it. But it must still go through the same access check as every
// other workspace-scoped route, or it becomes a way to confirm that a workspace
// exists by watching which id answers differently.
func TestColouringAWorkspaceYouCannotReachIsRefused(t *testing.T) {
	d := newDoor(t)
	_, member, err := d.users.CreateUser(t.Context(), "outsider", "member", "")
	if err != nil {
		t.Fatalf("create a member: %v", err)
	}
	rec := d.request(t, http.MethodPatch, "/api/v1/workspaces/"+id(d.wsID), member, `{"hue":264}`)
	if rec.Code == http.StatusOK {
		t.Fatal("somebody outside the workspace coloured it")
	}
	if rec.Code != http.StatusForbidden && rec.Code != http.StatusNotFound {
		t.Fatalf("answered %d; want 403 or 404: %s", rec.Code, rec.Body.String())
	}
}
