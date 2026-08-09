package catalog

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/orkcom-tech/cogitorium/internal/store"
)

// The rules under test live half in Go and half in SQL — the cascade, the
// unique constraints, the foreign keys. A fake database would hide exactly
// the half that matters, so every test opens the real schema on disk.
func newTestStore(t *testing.T) (*Store, *sql.DB) {
	t.Helper()
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return NewStore(db), db
}

func countRows(t *testing.T, db *sql.DB, query string, args ...any) int {
	t.Helper()
	var n int
	if err := db.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatalf("count (%s): %v", query, err)
	}
	return n
}

// fakeProvider is a loopback stand-in for an OpenAI-compatible endpoint. It
// exists so a test can watch what the server actually sends to a provider
// address — the only honest way to check which credential is stored, since
// the key is deliberately unreachable from outside the package. No network,
// no model: httptest binds 127.0.0.1.
type fakeProvider struct {
	*httptest.Server
	mu   sync.Mutex
	seen []string // Authorization header of every request received
}

func newFakeProvider(t *testing.T) *fakeProvider {
	t.Helper()
	f := &fakeProvider{}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.seen = append(f.seen, r.Header.Get("Authorization"))
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"data":[{"id":"local-model"}]}`)
	}))
	t.Cleanup(f.Close)
	return f
}

// dial makes the server contact the provider the way the "test provider"
// button does, and returns the credential the provider received. It fails
// the test if no request arrived: a check on an absent request would pass no
// matter what the code did.
func (f *fakeProvider) dial(t *testing.T, s *Store, providerID int64) string {
	t.Helper()
	before := len(f.credentials())
	c, _, err := s.Client(context.Background(), providerID)
	if err != nil {
		t.Fatalf("build client for provider %d: %v", providerID, err)
	}
	models, err := c.ListModels(context.Background())
	if err != nil {
		t.Fatalf("list models from the stand-in provider: %v", err)
	}
	if len(models) != 1 || models[0] != "local-model" {
		t.Fatalf("stand-in provider was not reached properly, got models %v", models)
	}
	got := f.credentials()
	if len(got) != before+1 {
		t.Fatalf("no request reached the provider address; the credential check below would be meaningless")
	}
	return got[len(got)-1]
}

func (f *fakeProvider) credentials() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.seen...)
}

// A provider's API key is the operator's money and identity. The browser
// gets provider listings on every catalog page load, so a key that rides
// along in that JSON is leaked to every session, every proxy, and every
// browser cache. The listing must describe the key, never carry it.
func TestProviderListingNeverCarriesTheAPIKey(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, _ := newTestStore(t)

	const secret = "sk-ant-live-9f3a-never-in-a-response"
	created, err := s.CreateProvider(ctx, "paid", "anthropic", "", secret)
	if err != nil {
		t.Fatalf("create provider: %v", err)
	}
	if _, err := s.CreateProvider(ctx, "unpaid", "openai-compatible", "http://127.0.0.1:11434/v1", ""); err != nil {
		t.Fatalf("create keyless provider: %v", err)
	}
	fetched, err := s.GetProvider(ctx, created.ID)
	if err != nil {
		t.Fatalf("get provider: %v", err)
	}
	renamed := "paid-renamed"
	updated, err := s.UpdateProvider(ctx, created.ID, &renamed, nil, nil)
	if err != nil {
		t.Fatalf("update provider: %v", err)
	}
	list, err := s.ListProviders(ctx)
	if err != nil {
		t.Fatalf("list providers: %v", err)
	}

	// Every one of these values is handed straight to the JSON encoder by a
	// handler in internal/server, so each is a response body in its own right.
	for what, payload := range map[string]any{
		"the listing":         list,
		"the create response": created,
		"the get response":    fetched,
		"the update response": updated,
	} {
		body, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("marshal %s: %v", what, err)
		}
		if strings.Contains(string(body), secret) {
			t.Errorf("%s contains the provider API key: %s", what, body)
		}
	}

	body, err := json.Marshal(list)
	if err != nil {
		t.Fatalf("marshal listing: %v", err)
	}
	// Guard against a vacuous pass: an empty or crippled payload also fails to
	// contain the secret. The listing must still be a useful description.
	if !strings.Contains(string(body), `"name":"paid-renamed"`) || !strings.Contains(string(body), `"base_url":"https://api.anthropic.com"`) {
		t.Fatalf("listing lost the fields the UI renders: %s", body)
	}

	// has_key is what the UI uses to show "configured" versus "add a key".
	// Getting it wrong sends the operator to re-enter a key that is already
	// there, or hides that a provider will fail on first use.
	var decoded []struct {
		Name   string `json:"name"`
		HasKey bool   `json:"has_key"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("decode listing: %v", err)
	}
	want := map[string]bool{"paid-renamed": true, "unpaid": false}
	if len(decoded) != len(want) {
		t.Fatalf("expected %d providers in the listing, got %d: %s", len(want), len(decoded), body)
	}
	for _, p := range decoded {
		w, known := want[p.Name]
		if !known {
			t.Fatalf("unexpected provider %q in listing", p.Name)
		}
		if p.HasKey != w {
			t.Errorf("provider %q: has_key = %v, want %v", p.Name, p.HasKey, w)
		}
	}
}

// Renaming a provider is a label change. If it also dropped the stored key,
// the operator's next run would fail authentication with no hint why — and
// the UI would keep claiming the provider is configured.
func TestRenamingAProviderChangesNothingElse(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, _ := newTestStore(t)
	fake := newFakeProvider(t)

	p, err := s.CreateProvider(ctx, "local llama", "openai-compatible", fake.URL, "sk-original-key")
	if err != nil {
		t.Fatalf("create provider: %v", err)
	}
	renamed := "local llama 3"
	updated, err := s.UpdateProvider(ctx, p.ID, &renamed, nil, nil)
	if err != nil {
		t.Fatalf("rename provider: %v", err)
	}
	if updated.BaseURL != fake.URL {
		t.Errorf("a rename moved the provider address to %q, want %q", updated.BaseURL, fake.URL)
	}
	if !updated.HasKey {
		t.Error("a rename reported the key as gone")
	}
	if got, want := fake.dial(t, s, p.ID), "Bearer sk-original-key"; got != want {
		t.Errorf("after a rename the provider received %q, want %q", got, want)
	}
}

// Clearing the key is an explicit operator decision — usually revoking a
// leaked credential. If an empty key were treated as "no change", the
// revoked key would keep flying to the provider after the operator was told
// it had been removed.
func TestAnExplicitlyEmptyKeyIsActuallyCleared(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, _ := newTestStore(t)
	fake := newFakeProvider(t)

	p, err := s.CreateProvider(ctx, "local llama", "openai-compatible", fake.URL, "sk-leaked-key")
	if err != nil {
		t.Fatalf("create provider: %v", err)
	}
	if got, want := fake.dial(t, s, p.ID), "Bearer sk-leaked-key"; got != want {
		t.Fatalf("provider received %q before the key was cleared, want %q", got, want)
	}

	blank := ""
	updated, err := s.UpdateProvider(ctx, p.ID, nil, nil, &blank)
	if err != nil {
		t.Fatalf("clear key: %v", err)
	}
	if updated.HasKey {
		t.Error("has_key is still true after the key was cleared")
	}
	if got := fake.dial(t, s, p.ID); got != "" {
		t.Errorf("the cleared key is still sent to the provider: %q", got)
	}
}

// A provider address is where this install's credential gets delivered. An
// address that is not an http(s) URL is either a mistake that fails at run
// time with a confusing error, or an attempt to point the server at
// something that is not a web endpoint at all. Both are refused up front,
// and nothing is written.
func TestProviderAddressMustBeAnHTTPURL(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		baseURL string
	}{
		{"host and port with no scheme", "127.0.0.1:11434/v1"},
		{"file url", "file:///etc/passwd"},
		{"javascript url", "javascript:alert(document.cookie)"},
		{"ftp url", "ftp://models.example.com/v1"},
		{"protocol-relative", "//models.example.com/v1"},
		{"blank", "   "},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			s, db := newTestStore(t)

			if _, err := s.CreateProvider(ctx, "p", "openai-compatible", tc.baseURL, "sk-key"); err == nil {
				t.Fatalf("provider was created with base_url %q", tc.baseURL)
			}
			if n := countRows(t, db, `SELECT COUNT(*) FROM providers`); n != 0 {
				t.Fatalf("a rejected provider left %d row(s) behind", n)
			}
		})
	}
}

// Supplying no address at all is a different mistake from supplying a bad one,
// and the operator reads the difference in the message. Anthropic has a default
// to fall back on; an openai-compatible provider has nowhere to go, so the
// answer is "base_url is required" rather than "must start with http://", which
// is nonsense advice about a field they left empty.
//
// This is asserted separately because the blank case is already rejected by the
// scheme check further down — deleting the "required" branch keeps the refusal
// and silently degrades only the explanation, which no test of err != nil sees.
func TestAnOpenAICompatibleProviderWithNoAddressIsToldTheAddressIsRequired(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, _ := newTestStore(t)

	_, err := s.CreateProvider(ctx, "p", "openai-compatible", "   ", "sk-key")
	if err == nil {
		t.Fatal("a provider with no address was created")
	}
	if !strings.Contains(err.Error(), "required") {
		t.Errorf("an empty base_url produced %q; the operator supplied nothing, so the message has to say it is required rather than describe a format", err)
	}

	// The same blank input is fine for Anthropic, which has a default. If this
	// ever starts failing, the "required" rule has been widened into a demand
	// that every provider be addressed by hand.
	if _, err := s.CreateProvider(ctx, "claude", "anthropic", "   ", "sk-key"); err != nil {
		t.Errorf("anthropic has a default address and must not require one: %v", err)
	}
}

// The type decides which wire protocol and which auth header the key is sent
// with. An unknown type must be refused with a message naming the choices,
// not bounced off the database's CHECK constraint — the operator sees that
// message in the UI and has to be able to act on it.
func TestUnknownProviderTypeIsRefusedWithAUsableMessage(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, db := newTestStore(t)

	_, err := s.CreateProvider(ctx, "ollama", "ollama", "http://127.0.0.1:11434/v1", "")
	if err == nil {
		t.Fatal("a provider with an unsupported type was created")
	}
	msg := err.Error()
	if !strings.Contains(msg, "anthropic") || !strings.Contains(msg, "openai-compatible") {
		t.Errorf("the error does not tell the operator what types exist: %v", err)
	}
	if strings.Contains(msg, "CHECK constraint") {
		t.Errorf("the type was rejected by the database, not validated: %v", err)
	}
	if n := countRows(t, db, `SELECT COUNT(*) FROM providers`); n != 0 {
		t.Fatalf("a rejected provider left %d row(s) behind", n)
	}
}

// An Anthropic provider created without an address must land on Anthropic's
// own API and nowhere else — this is the value the stored key is delivered
// to. The literal is spelled out here on purpose: reading it from the same
// constant the code writes it from would let a typo in that constant pass.
func TestAnthropicProviderDefaultsToAnthropicsOwnAPI(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, _ := newTestStore(t)

	for _, given := range []string{"", "   "} {
		p, err := s.CreateProvider(ctx, "anthropic "+given+"x", "anthropic", given, "sk-ant-key")
		if err != nil {
			t.Fatalf("create anthropic provider with base_url %q: %v", given, err)
		}
		if p.BaseURL != "https://api.anthropic.com" {
			t.Errorf("base_url %q with input %q, want https://api.anthropic.com", p.BaseURL, given)
		}
	}
}

// The address is stored exactly as chosen: surrounding whitespace from a
// copy-paste is dropped rather than baked into every request URL.
func TestProviderAddressIsStoredTrimmed(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, _ := newTestStore(t)
	fake := newFakeProvider(t)

	p, err := s.CreateProvider(ctx, "local", "openai-compatible", "  "+fake.URL+"\n", "sk-key")
	if err != nil {
		t.Fatalf("create provider: %v", err)
	}
	if p.BaseURL != fake.URL {
		t.Fatalf("stored base_url %q, want %q", p.BaseURL, fake.URL)
	}
	fake.dial(t, s, p.ID) // and the stored address is actually reachable
}

// A rejected update must change nothing at all. If the name landed while the
// address was refused, the operator would be told the edit failed and yet
// half of it happened — and would have no way to know which half.
func TestARejectedUpdateLeavesTheProviderExactlyAsItWas(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, _ := newTestStore(t)

	p, err := s.CreateProvider(ctx, "trusted gateway", "openai-compatible", "https://gateway.internal/v1", "sk-key")
	if err != nil {
		t.Fatalf("create provider: %v", err)
	}
	renamed := "renamed"
	bad := "ftp://attacker.example.com/v1"
	if _, err := s.UpdateProvider(ctx, p.ID, &renamed, &bad, nil); err == nil {
		t.Fatal("the provider was repointed at a non-http address")
	}

	after, err := s.GetProvider(ctx, p.ID)
	if err != nil {
		t.Fatalf("get provider: %v", err)
	}
	if after.BaseURL != "https://gateway.internal/v1" {
		t.Errorf("base_url is now %q; the operator did not choose it", after.BaseURL)
	}
	if after.Name != "trusted gateway" {
		t.Errorf("name is now %q; the rejected update partially applied", after.Name)
	}
	if !after.HasKey {
		t.Error("the rejected update dropped the stored key")
	}
}

// Two providers the operator cannot tell apart in a dropdown are a
// misconfiguration waiting to happen, so the name is unique — including
// after trimming, otherwise "openai " and "openai" would both be listed.
// The conflict must arrive as ErrConflict: that is what turns into a 409
// with a readable message instead of a 500 with a driver error in it.
func TestDuplicateProviderNamesConflict(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, db := newTestStore(t)

	if _, err := s.CreateProvider(ctx, "openai", "openai-compatible", "https://api.openai.com/v1", "sk-a"); err != nil {
		t.Fatalf("create provider: %v", err)
	}
	for _, name := range []string{"openai", "  openai  "} {
		_, err := s.CreateProvider(ctx, name, "openai-compatible", "https://api.openai.com/v1", "sk-b")
		if !errors.Is(err, ErrConflict) {
			t.Errorf("creating %q again: err = %v, want ErrConflict", name, err)
		}
	}
	if n := countRows(t, db, `SELECT COUNT(*) FROM providers`); n != 1 {
		t.Fatalf("%d providers exist; duplicates were stored", n)
	}
}

// A model row is a pointer to a provider. Accepting one for a provider that
// is not there produces a catalog entry that can never run, and the operator
// only finds out when an agent using it fails mid-conversation.
func TestCreatingAModelForAMissingProviderIsNotFound(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, db := newTestStore(t)

	if _, err := s.CreateModel(ctx, 4242, "gpt-4o", "GPT-4o"); !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound (the API answers 404 for it, not 500)", err)
	}

	// The realistic version: the provider was deleted in another tab while
	// this dropdown was still showing it.
	p, err := s.CreateProvider(ctx, "gone", "openai-compatible", "https://api.openai.com/v1", "sk-key")
	if err != nil {
		t.Fatalf("create provider: %v", err)
	}
	if err := s.DeleteProvider(ctx, p.ID); err != nil {
		t.Fatalf("delete provider: %v", err)
	}
	if _, err := s.CreateModel(ctx, p.ID, "gpt-4o", ""); !errors.Is(err, ErrNotFound) {
		t.Errorf("stale provider id: err = %v, want ErrNotFound", err)
	}
	if n := countRows(t, db, `SELECT COUNT(*) FROM models`); n != 0 {
		t.Fatalf("%d model row(s) were written for providers that do not exist", n)
	}
}

// The same model name twice on one provider gives the operator two identical
// dropdown entries; the same name on two providers is normal (the same model
// served by a local runtime and by a hosted one) and must stay allowed.
func TestDuplicateModelNamesConflictPerProviderOnly(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, db := newTestStore(t)

	a, err := s.CreateProvider(ctx, "hosted", "openai-compatible", "https://api.openai.com/v1", "sk-a")
	if err != nil {
		t.Fatalf("create provider: %v", err)
	}
	b, err := s.CreateProvider(ctx, "local", "openai-compatible", "http://127.0.0.1:11434/v1", "")
	if err != nil {
		t.Fatalf("create provider: %v", err)
	}
	if _, err := s.CreateModel(ctx, a.ID, "llama3", ""); err != nil {
		t.Fatalf("create model: %v", err)
	}
	for _, name := range []string{"llama3", "  llama3  "} {
		_, err := s.CreateModel(ctx, a.ID, name, "second label")
		if !errors.Is(err, ErrConflict) {
			t.Errorf("re-adding %q to the same provider: err = %v, want ErrConflict", name, err)
		}
	}
	if _, err := s.CreateModel(ctx, b.ID, "llama3", ""); err != nil {
		t.Errorf("the same model on a different provider was refused: %v", err)
	}
	if n := countRows(t, db, `SELECT COUNT(*) FROM models`); n != 2 {
		t.Fatalf("%d models stored, want 2", n)
	}
}

// Deleting a provider takes its models with it — leaving them would show the
// operator models that cannot be dialled. It must take only its own.
func TestDeletingAProviderTakesItsModelsAndOnlyItsModels(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, db := newTestStore(t)

	doomed, err := s.CreateProvider(ctx, "aaa doomed", "openai-compatible", "https://doomed.example.com/v1", "sk-a")
	if err != nil {
		t.Fatalf("create provider: %v", err)
	}
	keeper, err := s.CreateProvider(ctx, "bbb keeper", "openai-compatible", "https://keeper.example.com/v1", "sk-b")
	if err != nil {
		t.Fatalf("create provider: %v", err)
	}
	m1, err := s.CreateModel(ctx, doomed.ID, "doomed-one", "")
	if err != nil {
		t.Fatalf("create model: %v", err)
	}
	if _, err := s.CreateModel(ctx, doomed.ID, "doomed-two", ""); err != nil {
		t.Fatalf("create model: %v", err)
	}
	if _, err := s.CreateModel(ctx, keeper.ID, "kept-one", ""); err != nil {
		t.Fatalf("create model: %v", err)
	}

	if err := s.DeleteProvider(ctx, doomed.ID); err != nil {
		t.Fatalf("deleting a provider that still has models failed: %v", err)
	}
	if _, err := s.GetProvider(ctx, doomed.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("get deleted provider: err = %v, want ErrNotFound", err)
	}
	if _, err := s.GetModel(ctx, m1.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("get model of deleted provider: err = %v, want ErrNotFound", err)
	}

	models, err := s.ListModels(ctx)
	if err != nil {
		t.Fatalf("list models: %v", err)
	}
	if len(models) != 1 || models[0].ModelName != "kept-one" || models[0].ProviderName != "bbb keeper" {
		t.Fatalf("listing after the delete: %+v, want only kept-one on bbb keeper", models)
	}
	// The listing joins on providers, so an orphaned row would be invisible
	// there while still sitting in the table. Check the table itself.
	if n := countRows(t, db, `SELECT COUNT(*) FROM models`); n != 1 {
		t.Fatalf("%d model rows remain, want 1: the deleted provider's models were orphaned, not removed", n)
	}
}

// SQLite hands a deleted row's id to the next insert. If a provider's models
// were merely orphaned rather than deleted, the next provider the operator
// creates would silently inherit them — and agents pointed at those entries
// would send prompts and the new provider's key to a vendor nobody chose.
func TestANewProviderInheritsNoModelsFromADeletedOne(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, db := newTestStore(t)

	old, err := s.CreateProvider(ctx, "old vendor", "openai-compatible", "https://old.example.com/v1", "sk-old")
	if err != nil {
		t.Fatalf("create provider: %v", err)
	}
	if _, err := s.CreateModel(ctx, old.ID, "old-secret-model", ""); err != nil {
		t.Fatalf("create model: %v", err)
	}
	if err := s.DeleteProvider(ctx, old.ID); err != nil {
		t.Fatalf("delete provider: %v", err)
	}

	fresh, err := s.CreateProvider(ctx, "new vendor", "openai-compatible", "https://new.example.com/v1", "sk-new")
	if err != nil {
		t.Fatalf("create provider: %v", err)
	}
	if n := countRows(t, db, `SELECT COUNT(*) FROM models WHERE provider_id = ?`, fresh.ID); n != 0 {
		t.Fatalf("the new provider came with %d model(s) the operator never added", n)
	}
	models, err := s.ListModels(ctx)
	if err != nil {
		t.Fatalf("list models: %v", err)
	}
	if len(models) != 0 {
		t.Fatalf("catalog shows %+v after every model's provider was deleted", models)
	}
}

// Removing a provider must not quietly delete the operator's agents along
// with it. The agent keeps its wiring, its role and its history and simply
// loses its model, which the UI can then flag for reassignment. (The rows
// belong to another package; the rule fires from DeleteProvider here, and it
// is the destructive one, so it is checked here.)
func TestDeletingAProviderDoesNotDeleteTheAgentsThatUsedIt(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, db := newTestStore(t)

	p, err := s.CreateProvider(ctx, "vendor", "openai-compatible", "https://vendor.example.com/v1", "sk-key")
	if err != nil {
		t.Fatalf("create provider: %v", err)
	}
	m, err := s.CreateModel(ctx, p.ID, "vendor-model", "")
	if err != nil {
		t.Fatalf("create model: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO workspaces (id, name, created_at, updated_at) VALUES (1, 'ws', 'now', 'now')`); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO agents (id, workspace_id, name, model_id, created_at, updated_at) VALUES (7, 1, 'researcher', ?, 'now', 'now')`,
		m.ID); err != nil {
		t.Fatalf("seed agent: %v", err)
	}

	if err := s.DeleteProvider(ctx, p.ID); err != nil {
		t.Fatalf("delete provider: %v", err)
	}

	var name string
	var modelID sql.NullInt64
	err = db.QueryRow(`SELECT name, model_id FROM agents WHERE id = 7`).Scan(&name, &modelID)
	if errors.Is(err, sql.ErrNoRows) {
		t.Fatal("deleting a provider deleted the operator's agent")
	}
	if err != nil {
		t.Fatalf("read agent: %v", err)
	}
	if name != "researcher" {
		t.Errorf("agent name is now %q", name)
	}
	if modelID.Valid {
		t.Errorf("the agent still points at model %d, which no longer exists", modelID.Int64)
	}
}

// Removing one model from the catalog removes exactly that one. A delete
// that reached further would strip other providers' models — and every agent
// wired to them — from a single click on the wrong row.
func TestDeletingAModelRemovesOnlyThatModel(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, db := newTestStore(t)

	a, err := s.CreateProvider(ctx, "aaa", "openai-compatible", "https://a.example.com/v1", "")
	if err != nil {
		t.Fatalf("create provider: %v", err)
	}
	b, err := s.CreateProvider(ctx, "bbb", "openai-compatible", "https://b.example.com/v1", "")
	if err != nil {
		t.Fatalf("create provider: %v", err)
	}
	doomed, err := s.CreateModel(ctx, a.ID, "doomed", "")
	if err != nil {
		t.Fatalf("create model: %v", err)
	}
	for _, m := range []struct {
		provider int64
		name     string
	}{{a.ID, "sibling"}, {b.ID, "stranger"}} {
		if _, err := s.CreateModel(ctx, m.provider, m.name, ""); err != nil {
			t.Fatalf("create model %s: %v", m.name, err)
		}
	}

	if err := s.DeleteModel(ctx, doomed.ID); err != nil {
		t.Fatalf("delete model: %v", err)
	}
	models, err := s.ListModels(ctx)
	if err != nil {
		t.Fatalf("list models: %v", err)
	}
	var left []string
	for _, m := range models {
		left = append(left, m.ProviderName+"/"+m.ModelName)
	}
	if strings.Join(left, ",") != "aaa/sibling,bbb/stranger" {
		t.Fatalf("catalog holds %v after deleting one model, want [aaa/sibling bbb/stranger]", left)
	}
	if n := countRows(t, db, `SELECT COUNT(*) FROM models`); n != 2 {
		t.Fatalf("%d model rows remain, want 2", n)
	}
}

// Deleting something that is not there is a 404, not a cheerful 204. An
// operator whose delete silently "succeeded" against a stale list would
// believe a provider holding a live key is gone when it is not.
func TestDeletingWhatIsNotThereIsReportedAsNotFound(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, _ := newTestStore(t)

	if err := s.DeleteProvider(ctx, 9999); !errors.Is(err, ErrNotFound) {
		t.Errorf("delete missing provider: err = %v, want ErrNotFound", err)
	}
	if err := s.DeleteModel(ctx, 9999); !errors.Is(err, ErrNotFound) {
		t.Errorf("delete missing model: err = %v, want ErrNotFound", err)
	}

	// And deleting a real model reports success exactly once: the second
	// attempt is a 404, so a double-click cannot report two deletions.
	p, err := s.CreateProvider(ctx, "vendor", "openai-compatible", "https://vendor.example.com/v1", "")
	if err != nil {
		t.Fatalf("create provider: %v", err)
	}
	m, err := s.CreateModel(ctx, p.ID, "m", "")
	if err != nil {
		t.Fatalf("create model: %v", err)
	}
	if err := s.DeleteModel(ctx, m.ID); err != nil {
		t.Fatalf("delete model: %v", err)
	}
	if err := s.DeleteModel(ctx, m.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("second delete of the same model: err = %v, want ErrNotFound", err)
	}
}

// A blank name produces an unclickable, unidentifiable row in the catalog
// UI. Whitespace counts as blank, and nothing is written when it is refused.
func TestBlankNamesAreRefusedAndNothingIsWritten(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, db := newTestStore(t)

	if _, err := s.CreateProvider(ctx, "   ", "anthropic", "", "sk-key"); err == nil {
		t.Error("a provider with a blank name was created")
	}
	if n := countRows(t, db, `SELECT COUNT(*) FROM providers`); n != 0 {
		t.Fatalf("%d provider row(s) written for a blank name", n)
	}

	p, err := s.CreateProvider(ctx, "vendor", "openai-compatible", "https://vendor.example.com/v1", "sk-key")
	if err != nil {
		t.Fatalf("create provider: %v", err)
	}
	if _, err := s.CreateModel(ctx, p.ID, "  \t ", "Nicely Labelled"); err == nil {
		t.Error("a model with a blank model_name was created")
	}
	if n := countRows(t, db, `SELECT COUNT(*) FROM models`); n != 0 {
		t.Fatalf("%d model row(s) written for a blank model_name", n)
	}

	blank := " "
	if _, err := s.UpdateProvider(ctx, p.ID, &blank, nil, nil); err == nil {
		t.Error("a provider was renamed to a blank name")
	}
	after, err := s.GetProvider(ctx, p.ID)
	if err != nil {
		t.Fatalf("get provider: %v", err)
	}
	if after.Name != "vendor" {
		t.Errorf("name is now %q after a refused rename", after.Name)
	}
}

// Both listings feed pickers the operator scans by eye. Insertion order
// there means the list reshuffles every time something is added, and the
// same model appears in a different place on every page load.
func TestListingsAreOrderedByName(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, _ := newTestStore(t)

	// Created in deliberately wrong order, so insertion order and sorted
	// order cannot be confused for one another.
	zeta, err := s.CreateProvider(ctx, "zeta", "openai-compatible", "https://zeta.example.com/v1", "")
	if err != nil {
		t.Fatalf("create provider: %v", err)
	}
	alpha, err := s.CreateProvider(ctx, "alpha", "openai-compatible", "https://alpha.example.com/v1", "")
	if err != nil {
		t.Fatalf("create provider: %v", err)
	}
	for _, m := range []struct {
		provider int64
		name     string
	}{
		{zeta.ID, "z-second"},
		{zeta.ID, "z-first"},
		{alpha.ID, "a-only"},
	} {
		if _, err := s.CreateModel(ctx, m.provider, m.name, ""); err != nil {
			t.Fatalf("create model %s: %v", m.name, err)
		}
	}

	providers, err := s.ListProviders(ctx)
	if err != nil {
		t.Fatalf("list providers: %v", err)
	}
	var gotProviders []string
	for _, p := range providers {
		gotProviders = append(gotProviders, p.Name)
	}
	if strings.Join(gotProviders, ",") != "alpha,zeta" {
		t.Errorf("provider order %v, want [alpha zeta]", gotProviders)
	}

	models, err := s.ListModels(ctx)
	if err != nil {
		t.Fatalf("list models: %v", err)
	}
	var gotModels []string
	for _, m := range models {
		gotModels = append(gotModels, m.ProviderName+"/"+m.ModelName)
	}
	if strings.Join(gotModels, ",") != "alpha/a-only,zeta/z-first,zeta/z-second" {
		t.Errorf("model order %v, want [alpha/a-only zeta/z-first zeta/z-second]", gotModels)
	}
}

// The model listing is what the agent editor shows; a model that cannot say
// which provider and protocol it belongs to cannot be assigned correctly.
func TestModelsCarryTheirProviderIdentity(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, _ := newTestStore(t)

	p, err := s.CreateProvider(ctx, "claude", "anthropic", "", "sk-ant-key")
	if err != nil {
		t.Fatalf("create provider: %v", err)
	}
	created, err := s.CreateModel(ctx, p.ID, "  claude-sonnet-4  ", "Sonnet")
	if err != nil {
		t.Fatalf("create model: %v", err)
	}
	if created.ModelName != "claude-sonnet-4" {
		t.Errorf("model_name %q was not trimmed; it goes on the wire verbatim", created.ModelName)
	}
	if created.ProviderName != "claude" || created.ProviderType != "anthropic" || created.ProviderID != p.ID {
		t.Errorf("model does not carry its provider: %+v", created)
	}
	if created.Label != "Sonnet" {
		t.Errorf("label %q, want Sonnet", created.Label)
	}

	body, err := json.Marshal(created)
	if err != nil {
		t.Fatalf("marshal model: %v", err)
	}
	if strings.Contains(string(body), "sk-ant-key") {
		t.Errorf("the model payload carries the provider key: %s", body)
	}
}
