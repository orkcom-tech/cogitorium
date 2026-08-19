package plugin

import (
	"fmt"
	"sort"
	"strings"

	"github.com/orkcom-tech/cogitorium/internal/gearnet"
)

// What a plugin is allowed to reach, resolved from what the operator approved.
//
// The rules are the gate's own — gearnet.NormalizeHosts and gearnet.Allows are
// called here rather than reimplemented, because two allowlists that are
// supposed to agree eventually do not, and the one that is wrong is whichever
// nobody was looking at.
//
// One rule differs, deliberately and in the strict direction. For a gear, an
// empty destination list with a network grant means anywhere: the operator
// switched networking on and declined to narrow it. A plugin has no such
// switch — `hosts:` absent means the author never asked for the network, so it
// gets none. Absence is a refusal here, not a blank cheque.

// Grants is one plugin's approved reach.
type Grants struct {
	plugin string
	// hosts is normalized. Empty means no outbound network at all.
	hosts []string
	// secrets are the NAMES a plugin may ask to have substituted. It never
	// holds a value; the gate swaps a stand-in for the real thing at the edge.
	secrets map[string]bool
	scopes  map[string]bool
}

// ResolveGrants reads a manifest into the reach it is asking for.
//
// This is what the operator's approval screen is describing and what the
// gateway enforces — the same structure, so there is no gap between what was
// shown and what is checked.
func ResolveGrants(m Manifest) (Grants, error) {
	g := Grants{
		plugin:  m.ID,
		secrets: map[string]bool{},
		scopes:  map[string]bool{},
	}
	if len(m.Hosts) > 0 {
		hosts, err := gearnet.NormalizeHosts(m.Hosts)
		if err != nil {
			return Grants{}, fmt.Errorf("plugin %q: hosts: %w", m.ID, err)
		}
		g.hosts = hosts
	}
	for _, s := range m.Secrets {
		g.secrets[s] = true
	}
	for _, s := range m.API {
		g.scopes[s] = true
	}
	return g, nil
}

// AllowHost decides one destination.
//
// The refusal names the destination and what was granted, because the person
// reading it is either an author who mistyped a hostname or an operator
// deciding whether to widen the grant, and both need the same two facts.
func (g Grants) AllowHost(host string) error {
	host = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
	if h, _, ok := strings.Cut(host, ":"); ok {
		host = h
	}
	if host == "" {
		return fmt.Errorf("plugin %q asked to reach an empty host", g.plugin)
	}
	if len(g.hosts) == 0 {
		return fmt.Errorf("plugin %q asked to reach %s, but it was not granted any network. "+
			"A plugin reaches only what its manifest lists under hosts: and an operator approved",
			g.plugin, host)
	}
	if !gearnet.Allows(g.hosts, host) {
		return fmt.Errorf("plugin %q asked to reach %s, which is not among what it was granted: %s",
			g.plugin, host, strings.Join(g.hosts, ", "))
	}
	return nil
}

// AllowSecret decides whether a plugin may name a credential.
//
// Naming is all it ever does. The value is never handed over — the gate
// substitutes it at the edge — so this gate is about which doors a plugin may
// point at, not about what it may hold.
func (g Grants) AllowSecret(name string) error {
	if g.secrets[name] {
		return nil
	}
	if len(g.secrets) == 0 {
		return fmt.Errorf("plugin %q asked for the credential %s, but it declared none", g.plugin, name)
	}
	return fmt.Errorf("plugin %q asked for the credential %s, which is not among what it declared: %s",
		g.plugin, name, strings.Join(sortedSet(g.secrets), ", "))
}

// AllowScope decides one call against this server's own API.
//
// A plugin calls with a token minted for it, never with the operator's, so
// this is the whole extent of what that token can do.
func (g Grants) AllowScope(scope string) error {
	if g.scopes[scope] {
		return nil
	}
	// A read is implied by a write on the same subject: a plugin granted
	// runs:write that could not read a run back would have to be granted both
	// to do one thing, and an approval screen listing two lines for one
	// capability teaches an operator to skim.
	if subject, action, ok := strings.Cut(scope, ":"); ok && action == "read" {
		if g.scopes[subject+":write"] {
			return nil
		}
	}
	if len(g.scopes) == 0 {
		return fmt.Errorf("plugin %q called %s, but it was granted no API access", g.plugin, scope)
	}
	return fmt.Errorf("plugin %q called %s, which is not among what it was granted: %s",
		g.plugin, scope, strings.Join(sortedSet(g.scopes), ", "))
}

// Hosts is what was granted, for a screen that shows it.
func (g Grants) Hosts() []string { return append([]string(nil), g.hosts...) }

// SecretNames is what the plugin may name.
func (g Grants) SecretNames() []string { return sortedSet(g.secrets) }

// Scopes is what its token may do.
func (g Grants) Scopes() []string { return sortedSet(g.scopes) }

// Networked reports whether the plugin was granted any outbound reach.
func (g Grants) Networked() bool { return len(g.hosts) > 0 }

func sortedSet(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
