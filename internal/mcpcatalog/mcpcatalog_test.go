package mcpcatalog

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

// The registry is somebody else's description of somebody else's software, and
// this package's whole job is to turn it into something this install can
// actually connect to — or into nothing at all. An entry that produces a button
// that always fails is worse than a shorter library.

// mcpstore.Install refuses a name that does not match this. An entry whose name
// is refused cannot be installed, which makes it a broken row in a list whose
// entire purpose is to be installed from.
var nameRule = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,39}$`)

func serving(t *testing.T, body string) *Registry {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	r := NewRegistry()
	r.base = srv.URL
	return r
}

func page(servers ...string) string {
	return `{"servers":[` + strings.Join(servers, ",") + `],"metadata":{}}`
}

func srvJSON(inner string) string { return `{"server":` + inner + `}` }

func TestAPackagedServerBecomesACommand(t *testing.T) {
	r := serving(t, page(srvJSON(`{
		"name":"io.github.acme/jira-mcp","title":"Jira","description":"your tickets",
		"repository":{"url":"https://example.invalid/repo"},
		"packages":[{"registryType":"npm","identifier":"@acme/jira-mcp","version":"1.2.3",
			"transport":{"type":"stdio"},
			"environmentVariables":[{"name":"JIRA_TOKEN","isRequired":true,"isSecret":true}]}]}`)))

	got, err := r.Search(context.Background(), "")
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d entries", len(got))
	}
	e := got[0]
	if e.Transport != "stdio" || e.Command != "npx" {
		t.Fatalf("an npm package did not become an npx command: %+v", e)
	}
	// Pinned, so what is approved is a version rather than whatever `latest`
	// means on the day it runs.
	if strings.Join(e.Args, " ") != "-y @acme/jira-mcp@1.2.3" {
		t.Fatalf("args are %q", e.Args)
	}
	if len(e.EnvNames) != 1 || e.EnvNames[0] != "JIRA_TOKEN" {
		t.Fatalf("the credential it needs was lost: %+v", e.EnvNames)
	}
	if !strings.Contains(e.Needs, "JIRA_TOKEN") || !strings.Contains(e.Needs, "node") {
		t.Fatalf("the prerequisite does not say what is needed: %q", e.Needs)
	}
}

func TestAHostedServerBecomesAURL(t *testing.T) {
	r := serving(t, page(srvJSON(`{
		"name":"ai.example/hosted","title":"Hosted","description":"a service",
		"remotes":[{"type":"streamable-http","url":"https://mcp.example.invalid/mcp",
			"headers":[{"name":"Authorization","isRequired":true,"isSecret":true}]}]}`)))

	got, err := r.Search(context.Background(), "")
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d entries", len(got))
	}
	e := got[0]
	if e.Transport != "streamable-http" || e.URL != "https://mcp.example.invalid/mcp" {
		t.Fatalf("a hosted server did not become a URL: %+v", e)
	}
	if e.Command != "" {
		t.Fatalf("a hosted server carries a command: %q", e.Command)
	}
	// A header maps to a NAME the operator will set, never to a value.
	if e.HeaderNames["Authorization"] != "AUTHORIZATION" {
		t.Fatalf("the header was not offered a variable name: %+v", e.HeaderNames)
	}
}

// The decision that matters when a server publishes both: a hosted endpoint
// runs no code on this machine, and a package executes here as this server's
// user, outside the sandbox.
func TestAServerOfferingBothIsTakenAsHosted(t *testing.T) {
	r := serving(t, page(srvJSON(`{
		"name":"ai.example/both","title":"Both",
		"packages":[{"registryType":"npm","identifier":"both-mcp","version":"1.0.0","transport":{"type":"stdio"}}],
		"remotes":[{"type":"streamable-http","url":"https://both.example.invalid/mcp"}]}`)))

	got, _ := r.Search(context.Background(), "")
	if len(got) != 1 || got[0].Transport != "streamable-http" {
		t.Fatalf("a server offering both was not taken as hosted: %+v", got)
	}
}

// Cleartext would send the credential and everything the agent says in clear,
// and the client refuses it — so offering it here would be a button that always
// fails.
func TestACleartextRemoteIsNotOffered(t *testing.T) {
	r := serving(t, page(srvJSON(`{
		"name":"ai.example/plain","remotes":[{"type":"streamable-http","url":"http://plain.example.invalid/mcp"}]}`)))
	got, _ := r.Search(context.Background(), "")
	if len(got) != 0 {
		t.Fatalf("a cleartext server was offered: %+v", got)
	}
}

// A registry this install cannot spawn produces no entry rather than one that
// fails on first use.
func TestAnUnrunnablePackageIsNotOffered(t *testing.T) {
	r := serving(t, page(srvJSON(`{
		"name":"ai.example/nuget","packages":[{"registryType":"nuget","identifier":"Thing","version":"1.0.0"}]}`)))
	got, _ := r.Search(context.Background(), "")
	if len(got) != 0 {
		t.Fatalf("a package this install cannot run was offered: %+v", got)
	}
}

// The registry returns every published VERSION of a server, so the same name
// arrives repeatedly. An operator wants the server, once.
func TestVersionsOfTheSameServerCollapse(t *testing.T) {
	one := srvJSON(`{"name":"ai.example/x","version":"1.0.0",
		"packages":[{"registryType":"npm","identifier":"x","version":"1.0.0"}]}`)
	two := srvJSON(`{"name":"ai.example/x","version":"1.0.1",
		"packages":[{"registryType":"npm","identifier":"x","version":"1.0.1"}]}`)
	r := serving(t, page(one, two))
	got, _ := r.Search(context.Background(), "")
	if len(got) != 1 {
		t.Fatalf("%d entries for one server", len(got))
	}
}

// Every name this offers has to be one the store will accept, or the library is
// a list of things that cannot be installed.
func TestEveryOfferedNameCouldActuallyBeInstalled(t *testing.T) {
	r := serving(t, page(
		srvJSON(`{"name":"io.github.Acme.Corp/Weird_Name.v2","packages":[{"registryType":"npm","identifier":"w","version":"1"}]}`),
		srvJSON(`{"name":"a/`+strings.Repeat("x", 90)+`","packages":[{"registryType":"npm","identifier":"y","version":"1"}]}`),
	))
	got, _ := r.Search(context.Background(), "")
	if len(got) == 0 {
		t.Fatal("nothing came back")
	}
	for _, e := range got {
		if !nameRule.MatchString(e.Name) {
			t.Errorf("%s produced local name %q, which the store would refuse", e.ID, e.Name)
		}
		if !e.Installable() {
			t.Errorf("%s came back not installable: %+v", e.ID, e)
		}
	}
}

// Nothing in an entry may be a credential. The registry is somebody else's
// text, and a value pasted into it would otherwise be rendered as a suggestion.
func TestNoEntryCarriesAValue(t *testing.T) {
	r := serving(t, page(srvJSON(`{
		"name":"ai.example/leaky",
		"remotes":[{"type":"streamable-http","url":"https://leaky.example.invalid/mcp",
			"headers":[{"name":"Authorization","value":"Bearer ghp_realtokenhere"}]}]}`)))
	got, _ := r.Search(context.Background(), "")
	if len(got) != 1 {
		t.Fatalf("got %d", len(got))
	}
	blob, _ := json.Marshal(got[0])
	if strings.Contains(string(blob), "ghp_") {
		t.Fatalf("a value from the registry reached the entry: %s", blob)
	}
}

// A registry that answers badly is reported rather than rendered as an empty
// library, which would read as "there is nothing to install".
func TestARegistryFailureIsAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "down", http.StatusBadGateway)
	}))
	t.Cleanup(srv.Close)
	r := NewRegistry()
	r.base = srv.URL
	if _, err := r.Search(context.Background(), ""); err == nil {
		t.Fatal("a failing registry came back as an empty library")
	}
}

func TestTheSearchTermReachesTheRegistry(t *testing.T) {
	var asked string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = r.URL.Query().Get("search")
		_, _ = w.Write([]byte(page()))
	}))
	t.Cleanup(srv.Close)
	r := NewRegistry()
	r.base = srv.URL
	if _, err := r.Search(context.Background(), "jira"); err != nil {
		t.Fatalf("search: %v", err)
	}
	if asked != "jira" {
		t.Fatalf("the registry was asked for %q", asked)
	}
}

// The registry answers a page at a time and holds thousands. Reading one page
// showed a slice and called it the library — and worse, most of a page can
// collapse to nothing, because every published VERSION comes back separately.
func TestTheCursorIsFollowed(t *testing.T) {
	var asked []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cursor := r.URL.Query().Get("cursor")
		asked = append(asked, cursor)
		switch cursor {
		case "":
			_, _ = w.Write([]byte(`{"servers":[` + srvJSON(`{"name":"a/one",
				"packages":[{"registryType":"npm","identifier":"one","version":"1"}]}`) +
				`],"metadata":{"nextCursor":"page2"}}`))
		case "page2":
			_, _ = w.Write([]byte(`{"servers":[` + srvJSON(`{"name":"a/two",
				"packages":[{"registryType":"npm","identifier":"two","version":"1"}]}`) +
				`],"metadata":{}}`))
		}
	}))
	t.Cleanup(srv.Close)
	r := NewRegistry()
	r.base = srv.URL

	got, err := r.Search(context.Background(), "")
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("%d entries; the second page was not read", len(got))
	}
	if len(asked) != 2 || asked[1] != "page2" {
		t.Fatalf("cursors asked for: %q", asked)
	}
}

// A registry that answers a cursor forever must not be an infinite loop with a
// network on it.
func TestAnEndlessCursorIsBounded(t *testing.T) {
	pages := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pages++
		_, _ = w.Write([]byte(`{"servers":[],"metadata":{"nextCursor":"more"}}`))
	}))
	t.Cleanup(srv.Close)
	r := NewRegistry()
	r.base = srv.URL

	if _, err := r.Search(context.Background(), ""); err != nil {
		t.Fatalf("search: %v", err)
	}
	if pages > maxPages {
		t.Fatalf("read %d pages, which is past the bound", pages)
	}
}

// A later page failing must not throw away the earlier ones: a short library
// beats an error where there was a list.
func TestAFailedLaterPageKeepsWhatWasFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("cursor") != "" {
			http.Error(w, "gone", http.StatusBadGateway)
			return
		}
		_, _ = w.Write([]byte(`{"servers":[` + srvJSON(`{"name":"a/one",
			"packages":[{"registryType":"npm","identifier":"one","version":"1"}]}`) +
			`],"metadata":{"nextCursor":"page2"}}`))
	}))
	t.Cleanup(srv.Close)
	r := NewRegistry()
	r.base = srv.URL

	got, err := r.Search(context.Background(), "")
	if err != nil {
		t.Fatalf("a failing second page lost the first: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("%d entries", len(got))
	}
}
