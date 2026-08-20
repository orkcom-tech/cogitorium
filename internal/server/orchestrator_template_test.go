package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/orkcom-tech/cogitorium/internal/settings"
)

// Where an orchestrator comes from.
//
// Every workspace is created with one, and the only place that was ever
// visible was a picker on the new-workspace form — so somebody looking for it
// on the Models screen found a catalogue of models, no orchestrator anywhere,
// and concluded they were missing a step. The template is on that screen now,
// and choosing a model for it means the next workspace does not have to be
// asked again.
func TestTheOrchestratorTemplateIsOnTheModelsScreen(t *testing.T) {
	t.Parallel()
	in := newInstall(t, "127.0.0.1:8688", nil)
	modelID := seedOneModel(t, in)

	body := in.request(t, "GET", "/models", in.adminTok, "").Body.String()
	if !strings.Contains(body, "The orchestrator") {
		t.Fatal("the Models screen does not mention the orchestrator at all — which is the screen " +
			"somebody looks at when they are trying to work out how to make one")
	}
	// The role in full, because an orchestrator is not a mystery box and this
	// is the fastest way to say what one is.
	if !strings.Contains(body, "single point of contact") {
		t.Error("the role it starts with is not shown")
	}
	if !strings.Contains(body, `action="/models/orchestrator"`) {
		t.Error("there is no way to give it a model from this screen")
	}

	// Choose one.
	rec := in.form(t, "/models/orchestrator", "model_id="+strconv.FormatInt(modelID, 10))
	if rec.Code != http.StatusSeeOther && rec.Code != http.StatusOK {
		t.Fatalf("choosing the orchestrator's model: %d %s", rec.Code, rec.Body.String())
	}
	stored, err := in.srv.settings.Get(context.Background(), settings.OrchestratorModel)
	if err != nil || stored != strconv.FormatInt(modelID, 10) {
		t.Fatalf("the choice was not stored: %q (%v)", stored, err)
	}

	// And the new-workspace form opens on it, rather than asking again.
	body = in.request(t, "GET", "/workspaces?new=1", in.adminTok, "").Body.String()
	want := `value="` + strconv.FormatInt(modelID, 10) + `" selected`
	if !strings.Contains(body, want) {
		t.Errorf("the new-workspace picker does not open on the chosen model.\nWanted %q in:\n%s",
			want, body)
	}
}

// A stored id whose model was deleted must read as "nobody has chosen" rather
// than as a dangling number — otherwise the screen shows a picker with nothing
// marked and a workspace fails to create with a puzzle for a reason.
func TestAnOrchestratorModelThatIsGoneReadsAsUnset(t *testing.T) {
	t.Parallel()
	in := newInstall(t, "127.0.0.1:8688", nil)
	modelID := seedOneModel(t, in)

	if err := in.srv.settings.Set(context.Background(), settings.OrchestratorModel,
		strconv.FormatInt(modelID, 10)); err != nil {
		t.Fatal(err)
	}
	if got := in.srv.orchestratorModelID(context.Background()); got != modelID {
		t.Fatalf("the stored choice reads back as %d, want %d", got, modelID)
	}

	if err := in.srv.catalog.DeleteModel(context.Background(), modelID); err != nil {
		t.Fatal(err)
	}
	if got := in.srv.orchestratorModelID(context.Background()); got != 0 {
		t.Errorf("a deleted model still reads as the orchestrator's: %d", got)
	}
}

// The setting must name a model this install actually has. One that does not is
// a workspace that fails to create later, far from the screen that caused it.
func TestTheOrchestratorModelIsCheckedBeforeItIsStored(t *testing.T) {
	t.Parallel()
	in := newInstall(t, "127.0.0.1:8688", nil)

	rec := in.form(t, "/models/orchestrator", "model_id=99999")
	if rec.Code >= 500 {
		t.Fatalf("a model that does not exist should be refused, not crash: %d", rec.Code)
	}
	stored, _ := in.srv.settings.Get(context.Background(), settings.OrchestratorModel)
	if stored != "" {
		t.Errorf("a model this install does not have was stored anyway: %q", stored)
	}
}

// form posts a page form the way a browser does. The shared request helper
// sends no content type, and ParseForm reads nothing without one — a test that
// used it would pass while the handler saw an empty form.
func (in *install) form(t *testing.T, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("POST", path, strings.NewReader(body))
	req.RemoteAddr = onBox
	req.Header.Set("Authorization", "Bearer "+in.adminTok)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	in.srv.http.Handler.ServeHTTP(rec, req)
	return rec
}

// seedOneModel gives the install a provider and one model to offer.
func seedOneModel(t *testing.T, in *install) int64 {
	t.Helper()
	ctx := context.Background()
	p, err := in.cat.CreateProvider(ctx, "house", "anthropic", deadProvider, "")
	if err != nil {
		t.Fatalf("create provider: %v", err)
	}
	m, err := in.cat.CreateModel(ctx, p.ID, "claude-opus-5", "house")
	if err != nil {
		t.Fatalf("create model: %v", err)
	}
	return m.ID
}
