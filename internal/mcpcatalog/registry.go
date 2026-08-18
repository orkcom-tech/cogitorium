package mcpcatalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// The official MCP registry, read live.
//
// # Why this replaced a list compiled into the binary
//
// The first cut shipped six curated entries and refused to fetch anything,
// with a real argument: an install that does not phone home should not acquire
// a catalogue that does. What that argument missed is scale. The registry holds
// thousands of servers and two thirds of them are hosted services rather than
// packages — a hand-written list is not a smaller version of that, it is a
// different and much worse thing, and "we support MCP servers, except almost
// all of them" is not a sentence worth shipping.
//
// So the catalogue is fetched, and the objection is answered rather than
// ignored: it is fetched ONLY when an operator has said this install may make
// outbound requests, through the same switch the update check uses. An install
// that answered no has no library, which is honest — and `add by hand` is
// unaffected, because that never left the machine in the first place.
//
// # What is trusted here, which is nothing
//
// A registry entry is somebody else's description of somebody else's software.
// It is used to FILL IN A FORM and for no other purpose: what lands is a
// pending server that does nothing until an administrator reads the command or
// the URL it produced and approves it. Nothing here is executed, no version is
// resolved, and the text is rendered as text.
const registryBase = "https://registry.modelcontextprotocol.io"

// A fetch is bounded twice: one request, and the whole page.
const (
	fetchTimeout = 15 * time.Second
	// maxPage is what one search reads. The registry pages at 100 and an
	// operator scrolling a drawer is not reading a thousand.
	maxPage = 50
	// maxBody bounds the answer. It is somebody else's host.
	maxBody = 4 << 20
)

// Registry reads the published catalogue.
type Registry struct {
	base   string
	client *http.Client
}

func NewRegistry() *Registry {
	return &Registry{base: registryBase, client: &http.Client{Timeout: fetchTimeout}}
}

// registryAnswer is the shape of GET /v0/servers.
type registryAnswer struct {
	Servers []struct {
		Server struct {
			Name        string `json:"name"`
			Title       string `json:"title"`
			Description string `json:"description"`
			Version     string `json:"version"`
			Repository  struct {
				URL string `json:"url"`
			} `json:"repository"`
			// Packages are the ones this install can run as a child process.
			Packages []struct {
				RegistryType string `json:"registryType"`
				Identifier   string `json:"identifier"`
				Version      string `json:"version"`
				Transport    struct {
					Type string `json:"type"`
					URL  string `json:"url"`
				} `json:"transport"`
				RuntimeArguments []registryArg `json:"runtimeArguments"`
				PackageArguments []registryArg `json:"packageArguments"`
				EnvironmentVars  []registryVar `json:"environmentVariables"`
			} `json:"packages"`
			// Remotes are hosted services: no package, no process, a URL.
			Remotes []struct {
				Type    string        `json:"type"`
				URL     string        `json:"url"`
				Headers []registryVar `json:"headers"`
			} `json:"remotes"`
		} `json:"server"`
	} `json:"servers"`
	Metadata struct {
		NextCursor string `json:"nextCursor"`
	} `json:"metadata"`
}

type registryArg struct {
	Type       string `json:"type"`
	Name       string `json:"name"`
	Value      string `json:"value"`
	IsRequired bool   `json:"isRequired"`
}

type registryVar struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	IsRequired  bool   `json:"isRequired"`
	IsSecret    bool   `json:"isSecret"`
}

// Search reads one page of the registry, turned into entries this product can
// install.
func (r *Registry) Search(ctx context.Context, q string) ([]Entry, error) {
	u, err := url.Parse(r.base + "/v0/servers")
	if err != nil {
		return nil, err
	}
	query := u.Query()
	query.Set("limit", fmt.Sprint(maxPage))
	if s := strings.TrimSpace(q); s != "" {
		query.Set("search", s)
	}
	u.RawQuery = query.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	// Names the product asking and nothing else — no version, no install id.
	req.Header.Set("User-Agent", "cogitorium")

	res, err := r.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("could not reach the MCP registry: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("the MCP registry answered %s", res.Status)
	}
	raw, err := io.ReadAll(io.LimitReader(res.Body, maxBody))
	if err != nil {
		return nil, fmt.Errorf("could not read the MCP registry's answer: %w", err)
	}
	var answer registryAnswer
	if err := json.Unmarshal(raw, &answer); err != nil {
		return nil, fmt.Errorf("the MCP registry answered something this server could not read: %w", err)
	}

	out := make([]Entry, 0, len(answer.Servers))
	seen := map[string]bool{}
	for _, s := range answer.Servers {
		e, ok := entryFrom(s.Server.Name, s.Server.Title, s.Server.Description,
			s.Server.Repository.URL, s.Server.Packages, s.Server.Remotes)
		if !ok {
			continue
		}
		// The registry returns every published VERSION of a server, so the same
		// name arrives repeatedly. An operator wants the server, once.
		if seen[e.ID] {
			continue
		}
		seen[e.ID] = true
		out = append(out, e)
	}
	return out, nil
}

// entryFrom turns one registry server into something installable, preferring
// the shape that costs the operator least.
//
// A REMOTE IS PREFERRED over a package when a server publishes both, and that
// is a security judgement rather than a convenience: a hosted endpoint runs no
// code on this machine, while a package is a download that executes here as
// this server's user, outside the sandbox. The operator can still choose the
// other by editing the row before approving it.
func entryFrom(name, title, description, repo string, packages []struct {
	RegistryType string `json:"registryType"`
	Identifier   string `json:"identifier"`
	Version      string `json:"version"`
	Transport    struct {
		Type string `json:"type"`
		URL  string `json:"url"`
	} `json:"transport"`
	RuntimeArguments []registryArg `json:"runtimeArguments"`
	PackageArguments []registryArg `json:"packageArguments"`
	EnvironmentVars  []registryVar `json:"environmentVariables"`
}, remotes []struct {
	Type    string        `json:"type"`
	URL     string        `json:"url"`
	Headers []registryVar `json:"headers"`
},
) (Entry, bool) {
	e := Entry{
		ID:      name,
		Name:    localName(name),
		Title:   strings.TrimSpace(firstNonEmpty(title, name)),
		Reaches: strings.TrimSpace(description),
		Docs:    repo,
	}
	if e.Name == "" {
		return Entry{}, false
	}

	for _, rem := range remotes {
		kind := rem.Type
		if kind == "streamable" {
			kind = "streamable-http"
		}
		if kind != "streamable-http" && kind != "sse" {
			continue
		}
		if !strings.HasPrefix(rem.URL, "https://") {
			// The client refuses these anyway; offering one would be a button
			// that always fails.
			continue
		}
		e.Transport = kind
		e.URL = rem.URL
		e.HeaderNames = map[string]string{}
		for _, h := range rem.Headers {
			if h.Name == "" {
				continue
			}
			// The NAME the operator will have to set, defaulted to the header's
			// own name so it is guessable. The value is never here.
			e.HeaderNames[h.Name] = envish(h.Name)
		}
		e.Needs = "a credential for " + hostOf(rem.URL) + ", set under Variables"
		if len(e.HeaderNames) == 0 {
			e.Needs = "nothing on this machine: it is a hosted service, reached over https"
		}
		return e, true
	}

	for _, p := range packages {
		cmd, args, ok := spawnFor(p.RegistryType, p.Identifier, p.Version)
		if !ok {
			continue
		}
		for _, a := range append(append([]registryArg{}, p.RuntimeArguments...), p.PackageArguments...) {
			if v := strings.TrimSpace(firstNonEmpty(a.Value, a.Name)); v != "" {
				args = append(args, v)
			}
		}
		e.Transport = "stdio"
		e.Command, e.Args = cmd, args
		for _, v := range p.EnvironmentVars {
			if v.Name != "" {
				e.EnvNames = append(e.EnvNames, v.Name)
			}
		}
		e.Needs = needsFor(p.RegistryType, e.EnvNames)
		return e, true
	}
	return Entry{}, false
}

// spawnFor maps a package registry onto the command that runs it.
//
// Only the three that can be run without installing anything first. A registry
// this cannot spawn produces no entry at all rather than an entry that fails,
// because a library whose buttons do nothing is worse than a shorter library.
func spawnFor(registry, identifier, version string) (string, []string, bool) {
	if identifier == "" {
		return "", nil, false
	}
	pinned := identifier
	// A version is pinned onto the identifier, so what an operator approves is
	// a version rather than whatever `latest` resolves to on the day it runs.
	//
	// THE SEPARATOR IS NOT SIMPLY "@". A scoped npm package is `@acme/thing`,
	// which already starts with one — checking for the character anywhere left
	// every scoped package unpinned, and scoped packages are most of them.
	// Only an `@` after the first character separates a version.
	if version != "" && !strings.Contains(strings.TrimPrefix(identifier, "@"), "@") {
		pinned = identifier + "@" + version
	}
	switch registry {
	case "npm":
		return "npx", []string{"-y", pinned}, true
	case "pypi":
		return "uvx", []string{pinned}, true
	case "oci":
		// --rm, or every call leaves a stopped container behind. -i because the
		// transport is the container's stdin.
		return "docker", []string{"run", "--rm", "-i", pinned}, true
	default:
		return "", nil, false
	}
}

func needsFor(registry string, envNames []string) string {
	base := map[string]string{
		"npm":  "node (npx) on this machine",
		"pypi": "python (uvx) on this machine",
		"oci":  "docker on this machine",
	}[registry]
	if base == "" {
		base = "the runtime for " + registry
	}
	if len(envNames) > 0 {
		base += ", and " + strings.Join(envNames, ", ") + " set under Variables"
	}
	return base
}

// localName turns a registry name — `io.github.acme/jira-mcp` — into something
// mcpstore will accept: lower case, digits, dash and underscore.
func localName(full string) string {
	name := full
	if i := strings.LastIndex(name, "/"); i >= 0 {
		name = name[i+1:]
	}
	var b strings.Builder
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	out := strings.Trim(b.String(), "-_")
	if len(out) > 40 {
		out = strings.Trim(out[:40], "-_")
	}
	return out
}

// envish suggests a variable name for a header, so an operator is offered
// AUTHORIZATION rather than a blank.
func envish(header string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(header) {
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteRune('_')
		}
	}
	return strings.Trim(b.String(), "_")
}

func hostOf(raw string) string {
	if u, err := url.Parse(raw); err == nil && u.Host != "" {
		return u.Host
	}
	return raw
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// ErrOffline is what a caller gets when this install has not agreed to make
// outbound requests. Distinguished from a network failure on purpose: one is a
// decision to respect and the other is a problem to report.
var ErrOffline = errors.New("this install has not agreed to make outbound requests, so it has no library to show")
