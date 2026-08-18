package server

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/orkcom-tech/cogitorium/internal/config"
	"github.com/orkcom-tech/cogitorium/internal/settings"
	"github.com/orkcom-tech/cogitorium/internal/update"
)

// The update check, over HTTP.
//
// The claim under test is not "does it fetch a tag" — internal/update owns that
// against its own server. It is the boundary: who may make this install talk to
// the outside, and whether a browser can undo a decision made in the config
// file. An install that switched this off must stay off no matter what arrives
// on a socket.

// newDoorWithUpdate is the ordinary fixture with the check in a chosen state.
func newDoorWithUpdate(t *testing.T, mode string) *door {
	t.Helper()
	return doorAround(t, newInstall(t, doorListen, func(c *config.Config) { c.UpdateCheck = mode }))
}

// Reading is for anybody signed in: a version is a fact about the install, the
// way its health is.
func TestAnybodySignedInMayReadTheUpdateState(t *testing.T) {
	d := newDoorWithUpdate(t, update.ModeAsk)
	rec := d.request(t, http.MethodGet, "/api/v1/updates", d.memberToken(t), "")
	if rec.Code != http.StatusOK {
		t.Fatalf("a member could not read the update state: %d %s", rec.Code, rec.Body.String())
	}
	var r update.Report
	if err := json.Unmarshal(rec.Body.Bytes(), &r); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if r.Mode != update.ModeAsk {
		t.Fatalf("mode came back %q, want %q", r.Mode, update.ModeAsk)
	}
	// Nothing has been asked, and the report must say so rather than implying
	// the install is current.
	if !r.CheckedAt.IsZero() {
		t.Fatal("an install that has asked nothing reported a check time")
	}
}

// Deciding is an administrator's: it is an outbound request on behalf of
// everybody on the install, not a preference one person holds.
func TestOnlyAnAdministratorMayDecideOrAsk(t *testing.T) {
	d := newDoorWithUpdate(t, update.ModeAsk)
	member := d.memberToken(t)

	for _, c := range []struct{ method, path, body string }{
		{http.MethodPost, "/api/v1/updates/check", ""},
		{http.MethodPut, "/api/v1/updates/mode", `{"mode":"on"}`},
	} {
		rec := d.request(t, c.method, c.path, member, c.body)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("%s %s answered %d for a member; want 403: %s",
				c.method, c.path, rec.Code, rec.Body.String())
		}
	}
}

// THE ONE THAT MATTERS. `off` is set in a file on the server's own disk. A
// browser that could undo it would make that file a suggestion.
func TestOffCannotBeUndoneOverHTTP(t *testing.T) {
	d := newDoorWithUpdate(t, update.ModeOff)

	rec := d.request(t, http.MethodPut, "/api/v1/updates/mode", d.adminTok, `{"mode":"on"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("switching a configured-off check on answered %d; want 409: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "update_check") {
		t.Fatalf("the refusal does not name the setting an operator would have to change: %s", rec.Body.String())
	}

	// And it is still off afterwards.
	rec = d.request(t, http.MethodGet, "/api/v1/updates", d.adminTok, "")
	var r update.Report
	_ = json.Unmarshal(rec.Body.Bytes(), &r)
	if r.Mode != update.ModeOff {
		t.Fatalf("mode is %q after a refused change; want off", r.Mode)
	}
}

// Check now is refused under off too — that setting is about the machine, not
// about how often somebody is willing to be interrupted.
func TestCheckNowIsRefusedWhenTheInstallSaysOff(t *testing.T) {
	d := newDoorWithUpdate(t, update.ModeOff)
	rec := d.request(t, http.MethodPost, "/api/v1/updates/check", d.adminTok, "")
	if rec.Code != http.StatusConflict {
		t.Fatalf("check now answered %d with the check configured off; want 409: %s", rec.Code, rec.Body.String())
	}
}

// An unparseable mode is refused rather than stored: a setting nobody can read
// would be a check in an unknown state.
func TestAnUnknownModeIsRefused(t *testing.T) {
	d := newDoorWithUpdate(t, update.ModeAsk)
	rec := d.request(t, http.MethodPut, "/api/v1/updates/mode", d.adminTok, `{"mode":"sometimes"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("an unknown mode answered %d; want 409: %s", rec.Code, rec.Body.String())
	}
	rec = d.request(t, http.MethodGet, "/api/v1/updates", d.adminTok, "")
	var r update.Report
	_ = json.Unmarshal(rec.Body.Bytes(), &r)
	if r.Mode != update.ModeAsk {
		t.Fatalf("a refused mode still changed the setting to %q", r.Mode)
	}
}

// The report always names how this copy was installed, because that is what
// decides whether there is an honest command to offer at all.
func TestTheReportSaysHowThisCopyWasInstalled(t *testing.T) {
	d := newDoorWithUpdate(t, update.ModeAsk)
	rec := d.request(t, http.MethodGet, "/api/v1/updates", d.adminTok, "")
	var r update.Report
	if err := json.Unmarshal(rec.Body.Bytes(), &r); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if r.Install.Kind == "" {
		t.Fatal("the report named no install kind; `manual` is an answer and empty is not")
	}
	if r.Install.Note == "" {
		t.Fatalf("install kind %q carries no sentence for the operator", r.Install.Kind)
	}
}

// The answer must survive the process. Before this, the operator answered, the
// server restarted, and the same question came back — which reads as a product
// that did not listen.
func TestTheAnswerSurvivesARestartOfTheServer(t *testing.T) {
	d := newDoorWithUpdate(t, update.ModeAsk)

	rec := d.request(t, http.MethodPut, "/api/v1/updates/mode", d.adminTok, `{"mode":"off"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("answering off: %d %s", rec.Code, rec.Body.String())
	}

	// A second Server over the SAME database is what a restart is, as far as
	// anything durable is concerned.
	again := update.New(update.ModeAsk, "v1.5.0", nil)
	again.Load(t.Context(), settings.NewStore(d.db))
	if again.Mode() != update.ModeOff {
		t.Fatalf("after a restart the setting is %q; the operator answered off", again.Mode())
	}
}
