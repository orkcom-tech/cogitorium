package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A caller can hand a job off and come back for it, with the key it already has.
//
// The two halves are one feature: 202 and a run number are useless if nothing
// can then fetch the run, and before this an inlet key was write-only — it
// opened one route and read nothing. The only way to let a pipeline poll its own
// job was to hand it a user token, a credential that can also delete workspaces
// and approve agent-authored code.
func TestADeliveryCanBeHandedOffAndCollected(t *testing.T) {
	d := newDoor(t)

	d.addJSONTask(t, "triage", `{"type":"object"}`, "orchestrator", "triage it")
	d.provider.answers(func(n int, c modelCall) modelReply { return says("triaged") })

	rec := d.deliverAsync(t, "triage", []byte(`{"id":7}`))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("an async delivery answered %d, want 202\n%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Preference-Applied"); got != "respond-async" {
		t.Fatalf("the response does not say the preference was applied: %q", got)
	}
	var accepted deliveryResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &accepted); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if accepted.Run == 0 || accepted.State != "queued" {
		t.Fatalf("202 did not name a waiting run: %+v", accepted)
	}
	// Location points at the place to look, so a caller does not have to
	// assemble the URL from a convention.
	if loc := rec.Header().Get("Location"); !strings.HasSuffix(loc, "/runs/"+itoa(accepted.Run)) {
		t.Fatalf("Location is %q", loc)
	}

	waitFor(t, func() bool { return d.runStatus(t, accepted.Run).State == "completed" }, "the run to finish")
	final := d.runStatus(t, accepted.Run)
	if final.Result != "triaged" {
		t.Fatalf("reading the run back gave %+v", final)
	}
}

// A key opens its own door and nothing else. Two doors into one workspace are
// two different callers, and one's key must not read the other's runs.
func TestAnInletKeyReadsOnlyItsOwnRuns(t *testing.T) {
	d := newDoor(t)

	other, err := d.srv.inlets.CreateInlet(t.Context(), d.wsID, "invoices", "another door")
	if err != nil {
		t.Fatalf("create a second inlet: %v", err)
	}
	otherKey, err := d.srv.inlets.IssueKey(t.Context(), other.ID)
	if err != nil {
		t.Fatalf("issue a key: %v", err)
	}

	d.addJSONTask(t, "triage", `{"type":"object"}`, "orchestrator", "triage it")
	d.provider.answers(func(n int, c modelCall) modelReply { return says("triaged") })
	rec := d.deliver(t, "triage", d.key, "application/json", []byte(`{"id":7}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("delivery: %d\n%s", rec.Code, rec.Body.String())
	}
	var done deliveryResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &done); err != nil {
		t.Fatalf("decode: %v", err)
	}

	// The other door's key, on this run, through that door's own address.
	req := httptest.NewRequest(http.MethodGet, "/i/invoices/runs/"+itoa(done.Run), nil)
	req.Header.Set("Authorization", "Bearer "+otherKey)
	got := httptest.NewRecorder()
	d.srv.http.Handler.ServeHTTP(got, req)
	if got.Code != http.StatusNotFound {
		t.Fatalf("another door's key read this run: %d\n%s", got.Code, got.Body.String())
	}

	// And no key at all is refused rather than answered.
	req = httptest.NewRequest(http.MethodGet, "/i/"+d.address+"/runs/"+itoa(done.Run), nil)
	got = httptest.NewRecorder()
	d.srv.http.Handler.ServeHTTP(got, req)
	if got.Code != http.StatusUnauthorized {
		t.Fatalf("an unauthenticated read answered %d", got.Code)
	}
}

// The file a run was given comes back byte for byte, whatever is in it.
//
// The record has always named files with paths and sizes and claimed the path
// was one a caller could fetch. It was not: the only file route was under a user
// token, capped at 2 MiB, refused anything that was not UTF-8, and returned
// content as a JSON string — so bytes could not have survived it.
func TestARunsFileCanBeFetchedWithTheInletKey(t *testing.T) {
	d := newDoor(t)

	d.addFileTask(t, "ingest", "application/octet-stream", "orchestrator", "read it")
	d.provider.answers(func(n int, c modelCall) modelReply { return says("read it") })

	// Bytes that are not text, so this proves the route carries a file rather
	// than a string that happens to look like one.
	body := []byte{0x00, 0x01, 0x02, 0xff, 0xfe, 'o', 'k', 0x00}
	rec := d.deliver(t, "ingest", d.key, "application/octet-stream", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("delivery: %d\n%s", rec.Code, rec.Body.String())
	}
	var done deliveryResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &done); err != nil {
		t.Fatalf("decode: %v", err)
	}

	var rel string
	if err := d.db.QueryRow(`SELECT payload_path FROM inlet_runs WHERE id = ?`, done.Run).Scan(&rel); err != nil {
		t.Fatalf("read the ledger row: %v", err)
	}
	got := d.runFile(t, done.Run, rel)
	if got.Code != http.StatusOK {
		t.Fatalf("fetching the run's file: %d\n%s", got.Code, got.Body.String())
	}
	if !bytesEqual(got.Body.Bytes(), body) {
		t.Fatalf("the bytes came back changed: %v", got.Body.Bytes())
	}

	// A path this run's record does not name is not this run's to fetch, even
	// inside the same workspace.
	if other := d.runFile(t, done.Run, "somebody-elses-report.md"); other.Code != http.StatusNotFound {
		t.Fatalf("a path the record does not name answered %d", other.Code)
	}
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func (d *door) deliverAsync(t *testing.T, task string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/i/"+d.address+"/"+task, strings.NewReader(string(body)))
	req.RemoteAddr = offBox
	req.Header.Set("Authorization", "Bearer "+d.key)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Prefer", "respond-async")
	rec := httptest.NewRecorder()
	d.srv.http.Handler.ServeHTTP(rec, req)
	return rec
}

func (d *door) runStatus(t *testing.T, runID int64) deliveryResponse {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/i/"+d.address+"/runs/"+itoa(runID), nil)
	req.Header.Set("Authorization", "Bearer "+d.key)
	rec := httptest.NewRecorder()
	d.srv.http.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("reading run %d: %d\n%s", runID, rec.Code, rec.Body.String())
	}
	var out deliveryResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return out
}

func (d *door) runFile(t *testing.T, runID int64, rel string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet,
		"/i/"+d.address+"/runs/"+itoa(runID)+"/file?path="+rel, nil)
	req.Header.Set("Authorization", "Bearer "+d.key)
	rec := httptest.NewRecorder()
	d.srv.http.Handler.ServeHTTP(rec, req)
	return rec
}
