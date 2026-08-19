package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

const (
	firstPassword = "the-first-password-42"
	nextPassword  = "a-brand-new-password-9"
)

// Changing a password asks for the current one.
//
// The client's dialog did not, and that made an unlocked screen enough to take
// an account over permanently: a session proves somebody sat down at this
// machine, not that they are who the account belongs to.
func TestChangingAPasswordNeedsTheCurrentOne(t *testing.T) {
	in := newInstall(t, "", nil)
	ctx := context.Background()

	admin, err := in.users.GetUserByName(ctx, "admin")
	if err != nil {
		t.Fatalf("reading the admin: %v", err)
	}
	if err := in.users.SetPassword(ctx, admin.ID, firstPassword); err != nil {
		t.Fatalf("seeding a password: %v", err)
	}

	// Built here rather than through requestFrom, which sets no content type —
	// and a form post without one is a body ParseForm never looks at, so every
	// field would arrive empty and the test would pass for the wrong reason.
	change := func(current, next string) {
		t.Helper()
		form := url.Values{"current": {current}, "password": {next}, "again": {next}}
		req := httptest.NewRequest(http.MethodPost, "/account/password", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Authorization", "Bearer "+in.adminTok)
		in.srv.http.Handler.ServeHTTP(httptest.NewRecorder(), req)
	}

	// The status is 200 either way — the answer is a rendered page carrying the
	// refusal — so what is asserted is the only thing that matters: whether the
	// password actually moved.
	change("not-the-password", nextPassword)
	if _, _, err := in.users.Login(ctx, "admin", firstPassword); err != nil {
		t.Fatalf("a wrong current password changed the password anyway: %v", err)
	}

	change(firstPassword, nextPassword)
	if _, _, err := in.users.Login(ctx, "admin", nextPassword); err != nil {
		t.Fatalf("the right current password did not change it: %v", err)
	}
	if _, _, err := in.users.Login(ctx, "admin", firstPassword); err == nil {
		t.Error("the old password still works")
	}
}
