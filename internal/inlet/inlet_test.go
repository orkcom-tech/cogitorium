package inlet

// These tests defend three promises this package makes on its own, without an
// HTTP layer: a payload that does not match its task is refused, a key opens
// exactly one door, and the run ledger says once and only once what became of
// a delivery.
//
// They run against a real SQLite database in a temp dir, created by the real
// migration runner and populated through the real stores. Nothing is stubbed:
// the provider registered for the workspace's model points at port 1, which
// refuses instantly, so a mutation that let one of these paths reach a model
// would fail loudly rather than dial anything.

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/orkcom-tech/cogitorium/internal/catalog"
	"github.com/orkcom-tech/cogitorium/internal/identity"
	"github.com/orkcom-tech/cogitorium/internal/store"
	"github.com/orkcom-tech/cogitorium/internal/workspace"
)

var quiet sync.Once

// newFixture builds the smallest real install an inlet needs: a user to own a
// workspace, a model for its orchestrator, and the workspace itself. The
// foreign key from inlets to workspaces is enforced (the DSN turns
// foreign_keys on), so a fabricated workspace id would not insert at all.
func newFixture(t *testing.T) (*Store, int64) {
	t.Helper()
	quiet.Do(func() { slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil))) })
	ctx := context.Background()

	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	admin, _, err := identity.NewStore(db).Bootstrap(ctx, "")
	if err != nil {
		t.Fatalf("bootstrap admin: %v", err)
	}
	cat := catalog.NewStore(db)
	provider, err := cat.CreateProvider(ctx, "house", "openai-compatible", "http://127.0.0.1:1", "sk-the-house-key")
	if err != nil {
		t.Fatalf("create provider: %v", err)
	}
	model, err := cat.CreateModel(ctx, provider.ID, "test-model", "house / test-model")
	if err != nil {
		t.Fatalf("create model: %v", err)
	}
	ws, err := workspace.NewStore(db).CreateWorkspace(ctx, "doors", "", model.ID, admin.ID)
	if err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	return NewStore(db), ws.ID
}

// --- 1. A payload that does not match the task's schema is refused. --------

// TestAPayloadIsCheckedAgainstTheTaskSchema is the refusal itself: every case
// here is a body a caller could send that the task says cannot arrive. The
// server test proves no model is called for these; this one proves they are
// caught at all, and that the complaint names the field so the caller can fix
// it without the operator's help.
func TestAPayloadIsCheckedAgainstTheTaskSchema(t *testing.T) {
	t.Parallel()

	const ticket = `{
	  "type": "object",
	  "properties": {
	    "id":       {"type": "integer", "minimum": 1},
	    "title":    {"type": "string", "minLength": 3, "maxLength": 80},
	    "severity": {"enum": ["low", "high"]},
	    "tags":     {"type": "array", "minItems": 1, "items": {"type": "string"}}
	  },
	  "required": ["id", "title"],
	  "additionalProperties": false
	}`

	refused := []struct{ name, body, wants string }{
		{"a required field was not sent", `{"title":"disk full"}`, "id is required but was not sent"},
		{"the wrong type", `{"id":"7","title":"disk full"}`, "expected an integer, got a string"},
		{"a fractional integer", `{"id":7.5,"title":"disk full"}`, "expected an integer, got a number"},
		{"below the minimum", `{"id":0,"title":"disk full"}`, "must be at least 1"},
		{"too short", `{"id":7,"title":"no"}`, "needs at least 3 characters"},
		{"not in the enum", `{"id":7,"title":"disk full","severity":"urgent"}`, "not one of the allowed values"},
		{"an empty array", `{"id":7,"title":"disk full","tags":[]}`, "needs at least 1 item"},
		{"a wrong type inside an array", `{"id":7,"title":"disk full","tags":[1]}`, "expected a string, got a number"},
		{"a field the task does not accept", `{"id":7,"title":"disk full","exec":"rm -rf /"}`, `"exec" is not a field this task accepts`},
		{"not JSON at all", `id=7&title=disk+full`, "not valid JSON"},
		{"an empty body", ``, "the body is empty"},
		{"two documents in one body", `{"id":7,"title":"disk full"}{"id":8,"title":"again"}`, "more than one JSON document"},
		{"a bare string where an object was promised", `"disk full"`, "expected an object, got a string"},
	}
	for _, c := range refused {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			err := ValidatePayload(ticket, []byte(c.body))
			if err == nil {
				t.Fatalf("this body was accepted: %s", c.body)
			}
			if !strings.Contains(err.Error(), c.wants) {
				t.Fatalf("refusal does not say what was wrong.\nbody:  %s\nwant:  %s\ngot:   %s", c.body, c.wants, err)
			}
		})
	}

	// Controls: the same schema accepts what it says it accepts, so the
	// refusals above are the schema being enforced and not everything being
	// refused.
	for _, body := range []string{
		`{"id":7,"title":"disk full"}`,
		`{"id":7,"title":"disk full","severity":"high","tags":["ops"]}`,
	} {
		if err := ValidatePayload(ticket, []byte(body)); err != nil {
			t.Fatalf("a valid payload was refused: %s\n%v", body, err)
		}
	}

	// A body over the ceiling is refused without being parsed at all: the
	// point of the bound is that a caller cannot make this server allocate a
	// megabyte of JSON per request.
	huge := []byte(`{"id":7,"title":"` + strings.Repeat("x", MaxJSONPayload) + `"}`)
	err := ValidatePayload(ticket, huge)
	if err == nil || !strings.Contains(err.Error(), "belongs in a file task") {
		t.Fatalf("an oversize body: %v", err)
	}
}

// TestASchemaThisServerCannotEnforceIsRefusedWhenTheTaskIsWritten is the other
// half of the same promise, and the one that is easy to get wrong. A validator
// that ignored the keywords it does not implement would accept this schema,
// tell the operator their payloads are checked, and check nothing.
func TestASchemaThisServerCannotEnforceIsRefusedWhenTheTaskIsWritten(t *testing.T) {
	t.Parallel()
	s, wsID := newFixture(t)
	ctx := context.Background()

	in, err := s.CreateInlet(ctx, wsID, "tickets", "")
	if err != nil {
		t.Fatalf("create inlet: %v", err)
	}

	unenforceable := []struct{ name, keyword, schema string }{
		{"pattern", "pattern", `{"type":"object","properties":{"id":{"type":"string","pattern":"^[0-9]+$"}}}`},
		{"format", "format", `{"type":"object","properties":{"at":{"type":"string","format":"date-time"}}}`},
		{"oneOf", "oneOf", `{"oneOf":[{"type":"object"},{"type":"array"}]}`},
		{"a reference", "$ref", `{"$ref":"#/definitions/ticket"}`},
		{"a keyword nested inside properties", "multipleOf", `{"type":"object","properties":{"n":{"type":"number","multipleOf":2}}}`},
	}
	for _, c := range unenforceable {
		t.Run(c.name, func(t *testing.T) {
			err := CheckSchema(c.schema)
			if err == nil {
				t.Fatalf("a schema using %q was accepted, so payloads would be checked against a keyword nothing enforces", c.keyword)
			}
			// The operator is looking at the schema they just typed, so the
			// complaint has to name the keyword and say what may be used
			// instead. "invalid schema" would send them to the source.
			if !strings.Contains(err.Error(), c.keyword) || !strings.Contains(err.Error(), "Supported:") {
				t.Fatalf("refusal does not tell the operator what to write instead: %v", err)
			}

			// And it is refused where it is written, not where it is used.
			_, addErr := s.AddTask(ctx, in.ID, Task{
				Name: "triage", Accepts: AcceptsJSON, Schema: c.schema,
				AgentName: "orchestrator", Instruction: "triage it",
			})
			if addErr == nil {
				t.Fatalf("the task was stored with a schema this server cannot enforce")
			}
		})
	}

	// Control: a schema built only from the enforced subset is stored, so the
	// refusals above are about the keyword and not about schemas in general.
	if _, err := s.AddTask(ctx, in.ID, Task{
		Name: "triage", Accepts: AcceptsJSON,
		Schema:      `{"type":"object","properties":{"id":{"type":"integer"}},"required":["id"]}`,
		AgentName:   "orchestrator",
		Instruction: "triage it",
	}); err != nil {
		t.Fatalf("an enforceable schema was refused: %v", err)
	}
}

// --- 2. A key opens exactly one door. -------------------------------------

// TestAKeyOpensExactlyOneDoor covers every way a key can be wrong: absent,
// forged, issued for a different inlet, or retired by a rotation. Each is a
// stranger delivering work into somebody's workspace if it passes.
func TestAKeyOpensExactlyOneDoor(t *testing.T) {
	t.Parallel()
	s, wsID := newFixture(t)
	ctx := context.Background()

	tickets, err := s.CreateInlet(ctx, wsID, "tickets", "")
	if err != nil {
		t.Fatalf("create inlet: %v", err)
	}
	invoices, err := s.CreateInlet(ctx, wsID, "invoices", "")
	if err != nil {
		t.Fatalf("create inlet: %v", err)
	}

	// A door with no key issued is the state an imported bundle arrives in: it
	// carries an inlet's shape and never its credential. It must refuse
	// everything, including the empty string a request with no Authorization
	// header offers.
	if tickets.HasKey {
		t.Fatal("a freshly created inlet already has a key; issuing one is meant to be a separate decision")
	}
	for _, presented := range []string{"", "cgi-tickets-anything", "null", "undefined"} {
		if tickets.MatchesKey(presented) {
			t.Fatalf("an inlet with no key issued opened for %q", presented)
		}
	}

	ticketKey, err := s.IssueKey(ctx, tickets.ID)
	if err != nil {
		t.Fatalf("issue key: %v", err)
	}
	invoiceKey, err := s.IssueKey(ctx, invoices.ID)
	if err != nil {
		t.Fatalf("issue key: %v", err)
	}

	tickets = reload(t, s, tickets.ID)
	invoices = reload(t, s, invoices.ID)

	if !tickets.MatchesKey(ticketKey) {
		t.Fatal("the key just issued for this inlet does not open it")
	}
	if !invoices.MatchesKey(invoiceKey) {
		t.Fatal("the key just issued for this inlet does not open it")
	}

	// The key names its door in plain sight, which is exactly why the match
	// cannot be about the name: this string is one an attacker can construct.
	if !strings.HasPrefix(ticketKey, KeyPrefix+"-tickets-") {
		t.Fatalf("an inlet key no longer identifies itself on sight: %q", ticketKey)
	}
	forged := KeyPrefix + "-tickets-" + strings.Repeat("0", 64)

	for _, c := range []struct {
		name     string
		door     Inlet
		presents string
	}{
		{"no credential at all", tickets, ""},
		{"a forged key naming the right door", tickets, forged},
		{"another door's key", tickets, invoiceKey},
		{"this door's key at the other door", invoices, ticketKey},
		{"the key with a character changed", tickets, ticketKey[:len(ticketKey)-1] + "0"},
		{"the key with its prefix stripped", tickets, strings.TrimPrefix(ticketKey, KeyPrefix+"-")},
		{"a truncated key", tickets, ticketKey[:len(ticketKey)-4]},
	} {
		if c.door.MatchesKey(c.presents) {
			t.Fatalf("%s opened inlet %q", c.name, c.door.Address)
		}
	}

	// Rotation is the answer to a leaked key, so the old one has to stop
	// working the moment the new one exists. A rotation that left both valid
	// would mean a leak could never be closed.
	rotated, err := s.IssueKey(ctx, tickets.ID)
	if err != nil {
		t.Fatalf("rotate key: %v", err)
	}
	tickets = reload(t, s, tickets.ID)
	if tickets.MatchesKey(ticketKey) {
		t.Fatal("the retired key still opens the door, so a leaked key can never be closed")
	}
	if !tickets.MatchesKey(rotated) {
		t.Fatal("the freshly rotated key does not open the door")
	}

	// The digest is what makes a stolen database useless, so it must not be
	// served to anyone. The struct goes straight to the management API.
	body, err := json.Marshal(tickets)
	if err != nil {
		t.Fatalf("marshal inlet: %v", err)
	}
	if strings.Contains(string(body), hashKey(rotated)) {
		t.Fatalf("the key hash is serialised to the management API: %s", body)
	}
	if strings.Contains(string(body), rotated) {
		t.Fatalf("the key itself is serialised to the management API: %s", body)
	}
}

func reload(t *testing.T, s *Store, id int64) Inlet {
	t.Helper()
	in, err := s.GetInlet(context.Background(), id)
	if err != nil {
		t.Fatalf("reload inlet %d: %v", id, err)
	}
	return in
}

// --- 8. The ledger, at the store. -----------------------------------------

// TestARunIsRecordedBeforeTheWorkAndSettledExactlyOnce is the ledger's whole
// contract. The row exists before anything can fail, and the guarded UPDATE is
// what stops two writers leaving a row that claims both that the agent
// answered and that the payload was refused.
func TestARunIsRecordedBeforeTheWorkAndSettledExactlyOnce(t *testing.T) {
	t.Parallel()
	s, wsID := newFixture(t)
	ctx := context.Background()

	runID, err := s.Accept(ctx, wsID, 7, "tickets", "triage", "orchestrator")
	if err != nil {
		t.Fatalf("accept: %v", err)
	}

	// Before the payload has even been looked at, the delivery is on record.
	run, err := s.GetRun(ctx, runID)
	if err != nil {
		t.Fatalf("get run: %v", err)
	}
	if run.State != StateAccepted {
		t.Fatalf("a delivery is recorded as %q before the work; want %q", run.State, StateAccepted)
	}
	if run.AgentID != nil || run.PayloadBytes != 0 {
		t.Fatalf("the row records work that has not happened: agent_id=%v payload_bytes=%d", run.AgentID, run.PayloadBytes)
	}

	if err := s.Begin(ctx, runID, 3, 128, ""); err != nil {
		t.Fatalf("begin: %v", err)
	}
	if run = get(t, s, runID); run.State != StateRunning || run.PayloadBytes != 128 {
		t.Fatalf("after Begin: state=%q payload_bytes=%d", run.State, run.PayloadBytes)
	}

	if err := s.Settle(ctx, runID, StateCompleted, "the answer", "", nil); err != nil {
		t.Fatalf("settle: %v", err)
	}
	if run = get(t, s, runID); run.State != StateCompleted || run.Result != "the answer" {
		t.Fatalf("after Settle: state=%q result=%q", run.State, run.Result)
	}

	// A second settle must affect nothing and say so. Reported rather than
	// swallowed, because it means two things tried to finish one run.
	if err := s.Settle(ctx, runID, StateFailed, "", "something else finished it", nil); err == nil {
		t.Fatal("a run was settled twice, so a completed run can be overwritten with a failure")
	}
	if run = get(t, s, runID); run.State != StateCompleted || run.Result != "the answer" || run.Error != "" {
		t.Fatalf("the second settle changed the row: state=%q result=%q error=%q", run.State, run.Result, run.Error)
	}

	// A refused run never reached Begin, so Begin must not be able to revive
	// it: a payload that was refused stays refused, whatever arrives late.
	refusedID, err := s.Accept(ctx, wsID, 7, "tickets", "triage", "orchestrator")
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	if err := s.Settle(ctx, refusedID, StateRefusedSchema, "", "id is required but was not sent", nil); err != nil {
		t.Fatalf("settle refusal: %v", err)
	}
	if err := s.Begin(ctx, refusedID, 3, 128, ""); err == nil {
		t.Fatal("a refused run was started anyway")
	}
	if run = get(t, s, refusedID); run.State != StateRefusedSchema || run.AgentID != nil || run.PayloadBytes != 0 {
		t.Fatalf("a refused run was altered: state=%q agent_id=%v payload_bytes=%d", run.State, run.AgentID, run.PayloadBytes)
	}

	// Startup reconciliation: a run lives in one process's memory, so anything
	// still live after a restart can never finish. Terminal rows are history
	// and must not be touched.
	liveID, err := s.Accept(ctx, wsID, 7, "tickets", "triage", "orchestrator")
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	if err := s.Begin(ctx, liveID, 3, 1, ""); err != nil {
		t.Fatalf("begin: %v", err)
	}
	n, err := s.ReconcileRuns(ctx)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if n != 1 {
		t.Fatalf("reconciliation moved %d rows; only the one still running should have moved", n)
	}
	if run = get(t, s, liveID); run.State != StateInterrupted {
		t.Fatalf("a run left running by a restart reads as %q", run.State)
	}
	if run = get(t, s, runID); run.State != StateCompleted {
		t.Fatalf("reconciliation rewrote a finished run to %q", run.State)
	}
	if run = get(t, s, refusedID); run.State != StateRefusedSchema {
		t.Fatalf("reconciliation rewrote a refused run to %q", run.State)
	}
}

func get(t *testing.T, s *Store, id int64) Run {
	t.Helper()
	r, err := s.GetRun(context.Background(), id)
	if err != nil {
		t.Fatalf("get run %d: %v", id, err)
	}
	return r
}

// --- 7. What a file task will accept, before anything touches the disk. ----

// TestAFileTaskComparesWhatArrivedWithWhatItAccepts guards the check the
// server runs before a byte is written. The wildcard is the interesting half:
// an operator writes "image/*" meaning "any image", and it must not mean
// "anything".
func TestAFileTaskComparesWhatArrivedWithWhatItAccepts(t *testing.T) {
	t.Parallel()

	cases := []struct {
		accepts, got string
		want         bool
	}{
		{"image/png", "image/png", true},
		{"image/png", "image/jpeg", false},
		{"image/png", "text/plain", false},
		{"image/png", "", false},
		{"image/png", "IMAGE/PNG", true},
		// A charset is not a different media type; refusing over it would be
		// pedantry the operator has to debug.
		{"text/csv", "text/csv; charset=utf-8", true},
		{"image/*", "image/png", true},
		{"image/*", "image/svg+xml", true},
		{"image/*", "text/html", false},
		// The prefix must be compared as a type, not as a string: this is the
		// mutation that turns "any image" into "anything that starts the same".
		{"image/*", "imagemagick/script", false},
		// An empty accepts is a real choice — an inlet whose caller was told
		// what to send — so it takes anything, including nothing stated.
		{"", "application/pdf", true},
		{"", "", true},
		{"*/*", "application/pdf", true},
	}
	for _, c := range cases {
		if got := MatchesContentType(c.accepts, c.got); got != c.want {
			t.Fatalf("a task accepting %q, given %q: got %v, want %v", c.accepts, c.got, got, c.want)
		}
	}

	// A type the server cannot compare against is refused where it is written,
	// so the operator finds out rather than the caller.
	if err := CheckContentType("image/png; boundary"); err == nil {
		t.Fatal("a content type this server cannot parse was accepted onto a task")
	}
	if err := CheckContentType("image/*"); err != nil {
		t.Fatalf("a wildcard content type was refused: %v", err)
	}
}
