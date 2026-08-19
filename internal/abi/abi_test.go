package abi

import (
	"encoding/json"
	"strings"
	"testing"
)

// The contract is compiled in, read by the catalog's CI, and stamped into
// every published plugin. A silent bump invalidates all of them.
func TestTheContractIsDeliberate(t *testing.T) {
	if Version != 1 {
		t.Fatalf("the contract moved to %d. It moves only when this vocabulary BREAKS — "+
			"never for an addition, because a plugin that would still work must not be "+
			"refused. Move it with this test, on purpose.", Version)
	}
}

// Roles and calls are published vocabulary. Renaming one is a breaking change
// for every plugin that named it, so the lists are pinned.
func TestTheVocabularyIsStable(t *testing.T) {
	wantRoles := []string{"route", "provider", "filter", "event", "tool", "schedule", "command"}
	if got := Roles(); !same(got, wantRoles) {
		t.Errorf("roles changed: %v, want %v", got, wantRoles)
	}
	wantCalls := []string{"log", "render", "http", "api", "kv", "enqueue", "config", "now", "rand"}
	if got := Calls(); !same(got, wantCalls) {
		t.Errorf("calls changed: %v, want %v", got, wantCalls)
	}
}

func TestUnknownRolesAndCallsAreRefused(t *testing.T) {
	if ValidRole("middleware") {
		t.Error("an unknown role must be refused, not registered as something nobody calls")
	}
	if ValidCall("exec") {
		t.Error("an unknown call must be refused")
	}
}

// Exactly one of template, content and data carries the answer. Two would need
// a precedence rule, and a precedence rule is a thing every author has to
// learn and every runtime has to agree on.
func TestAResponseCarriesExactlyOneAnswer(t *testing.T) {
	ok := []Response{
		{Template: "acme.page.home", Model: map[string]string{}},
		{Content: &Content{Type: "text/plain", Body: []byte("hi")}},
		{Data: json.RawMessage(`{"x":1}`)},
		{Status: 204},
		{Error: "not for you"},
	}
	for _, r := range ok {
		if err := r.Validate(); err != nil {
			t.Errorf("%+v should be valid: %v", r, err)
		}
	}

	bad := []Response{
		{Template: "a.page.b", Data: json.RawMessage(`{}`)},
		{Content: &Content{}, Data: json.RawMessage(`{}`)},
		{},
	}
	for _, r := range bad {
		if err := r.Validate(); err == nil {
			t.Errorf("%+v should be refused", r)
		}
	}
}

// A refusal is the whole answer. Pairing it with a body leaves the host
// deciding which one the author meant.
func TestAnErrorMayNotCarryABody(t *testing.T) {
	r := Response{Error: "no", Template: "a.page.b"}
	if err := r.Validate(); err == nil {
		t.Fatal("an error alongside a body must be refused")
	}
}

// The template branch is the one that matters: a backend answering this way
// participates in late binding, so what it produced can itself be overridden.
func TestTheTemplateBranchSurvivesARoundTrip(t *testing.T) {
	in := Response{Template: "acme.stage.panel", Model: map[string]any{"Count": 3}}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out Response
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if out.Template != in.Template {
		t.Errorf("template = %q", out.Template)
	}
	if err := out.Validate(); err != nil {
		t.Errorf("a round-tripped response should stay valid: %v", err)
	}
}

// A plugin that received the operator's credential could act as them
// everywhere, which is the opposite of it holding a scoped one.
func TestTheRequestCarriesNoCredential(t *testing.T) {
	b, err := json.Marshal(Request{Ctx: Ctx{Viewer: Viewer{ID: 1, Name: "admin", SignedIn: true}}})
	if err != nil {
		t.Fatal(err)
	}
	s := strings.ToLower(string(b))
	for _, forbidden := range []string{"token", "cookie", "authorization", "password", "secret"} {
		if strings.Contains(s, forbidden) {
			t.Errorf("the request envelope carries %q: %s", forbidden, b)
		}
	}
}

// A tool is called by something that has to guess otherwise.
func TestAToolNeedsASchemaAndASummary(t *testing.T) {
	if err := (Export{Name: "search", Role: RoleTool}).Validate(); err == nil {
		t.Error("a tool without a schema must be refused")
	}
	if err := (Export{Name: "search", Role: RoleTool, Schema: json.RawMessage(`{}`)}).Validate(); err == nil {
		t.Error("a tool without a summary must be refused")
	}
	ok := Export{Name: "search", Role: RoleTool, Schema: json.RawMessage(`{}`), Summary: "Find things"}
	if err := ok.Validate(); err != nil {
		t.Errorf("a complete tool should be valid: %v", err)
	}
}

// Nothing else is called by something that has to guess, so nothing else
// carries that cost.
func TestOtherRolesNeedNoSchema(t *testing.T) {
	for _, r := range []Role{RoleRoute, RoleProvider, RoleFilter, RoleEvent, RoleSchedule, RoleCommand} {
		if err := (Export{Name: "x", Role: r}).Validate(); err != nil {
			t.Errorf("role %q should not require a schema: %v", r, err)
		}
	}
}

func TestAnUnknownRoleRefusalListsTheVocabulary(t *testing.T) {
	err := Export{Name: "x", Role: "middleware"}.Validate()
	if err == nil {
		t.Fatal("expected a refusal")
	}
	for _, want := range []string{"provider", "tool", "route"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal should list the roles, missing %q: %v", want, err)
		}
	}
}

// Compare-and-set and increment are in the vocabulary because two instances of
// a plugin WILL race, and an author should not discover that from a corrupted
// count.
func TestStorageOffersTheRaceFreeOperations(t *testing.T) {
	for _, op := range []KVOp{KVGet, KVSet, KVDelete, KVList, KVCAS, KVIncr} {
		if op == "" {
			t.Error("a storage operation has no name")
		}
	}
	if KVCAS != "cas" || KVIncr != "incr" {
		t.Errorf("the race-free operations were renamed: %q %q", KVCAS, KVIncr)
	}
}

// A refusal from the host is an ordinary thing a plugin handles, so it is a
// value rather than a transport failure.
func TestAHostRefusalIsAValue(t *testing.T) {
	b, _ := json.Marshal(HostReply{Err: "api.acme.com is not in the hosts this plugin was granted"})
	var out HostReply
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if out.Err == "" {
		t.Error("the refusal did not survive the round trip")
	}
	if len(out.Output) != 0 {
		t.Error("a refusal carries no output")
	}
}

func same(a, b []string) bool {
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
