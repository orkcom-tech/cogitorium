// Package gearnet is the outward gate for gears: the grant an operator makes
// at approval, the check a machine performs on every connection, and the record
// of what actually happened.
//
// It is deliberately NOT the per-query approval broker in internal/egress. That
// gate stops the turn and waits up to sixty seconds for a person to read one
// query string, and holds a single pending request for the whole process. It is
// the right shape for a search — a query is a sentence a human can judge in a
// second — and the wrong shape here twice over: an HTTP call has a method, a
// URL, headers and a body, which is a different act to approve and a slower
// one; and a gear in a pipeline would inherit the wait and stop being a
// pipeline. That is already why an unattended run is not offered web_search at
// all.
//
// So the decision moves to gear approval, where the operator is already reading
// the code, and what happens per connection is a machine check against the list
// they granted plus a row in the log. Empty list means anywhere, and that is a
// legitimate choice.
//
// # What this enforces, and what it does not
//
// A granted gear is given HTTP_PROXY, HTTPS_PROXY and ALL_PROXY pointing at
// this gate, with a credential that lives exactly as long as the run. Every
// connection made through it is checked against the granted hosts and recorded.
// curl, python-requests, urllib, httpx, wget and Go's own http.Client all honour
// those variables, so this is the ordinary path.
//
// It is not a containment boundary, and saying otherwise would be the lie this
// codebase keeps refusing to tell. Docker's bridge network has no host filter,
// so a gear that ignores the proxy variables and dials a socket itself reaches
// the network directly and leaves no row here. That is the same truth the plan
// already records about credentials: granting code the network and a key means
// it can send the key somewhere, and the approval screen is the control. A
// boundary that a machine enforces regardless of the code's cooperation belongs
// where the packets are — a Kubernetes NetworkPolicy, which is phase 3.
//
// One rule is absolute regardless of the grant, because it protects this server
// rather than the operator's policy: loopback, link-local and unspecified
// addresses are refused. Without that, "may reach anywhere" would include this
// server's own API on 127.0.0.1 — where a loopback request is trusted as the
// admin — and the cloud metadata endpoint on 169.254.169.254. Neither is
// something an operator grants by ticking a box marked "the network".
package gearnet

import (
	"errors"
	"fmt"
	"net"
	"sort"
	"strings"
)

// Grant is what one run may do: which gear, on whose behalf, and where it may
// reach. An empty Hosts is "anywhere".
type Grant struct {
	GearID      int64
	GearName    string
	Version     int
	WorkspaceID *int64
	AgentID     *int64
	// AgentName is empty for an operator's dry run from the catalog.
	AgentName string
	Hosts     []string
}

// The states one connection can end in. 'open' is the only live one; a row
// still holding it after a restart is settled as interrupted at startup.
const (
	StateOpen               = "open"
	StateClosed             = "closed"
	StateFailed             = "failed"
	StateInterrupted        = "interrupted"
	StateRefusedDestination = "refused_destination"
	StateRefusedLocal       = "refused_local"
	StateRefusedNoGrant     = "refused_no_grant"
	StateRefusedInvalid     = "refused_invalid"
)

// ValidHost judges one entry in a destination list and returns it in the form
// the check will use.
//
// A destination is a HOST: no scheme, no path, no port. Those are refused with
// a message rather than trimmed, because an operator who typed
// "https://api.example.com/v1" and got "api.example.com" back has been told
// their rule means something other than what they wrote — and a destination
// list is exactly the sort of thing nobody re-reads afterwards.
//
// A leading "*." allows any subdomain and NOT the apex: "*.example.com" covers
// api.example.com and does not cover example.com. That way an operator who
// means both says both, and one who means only the subdomains is not quietly
// given the parent.
func ValidHost(h string) (string, error) {
	h = strings.ToLower(strings.TrimSpace(h))
	h = strings.TrimSuffix(h, ".")
	if h == "" {
		return "", errors.New("a destination cannot be blank: remove the empty line, or leave the whole list empty to allow anywhere")
	}
	if strings.Contains(h, "://") || strings.Contains(h, "/") {
		return "", fmt.Errorf("%q is a URL: write just the host, e.g. api.example.com", h)
	}
	if strings.ContainsAny(h, " \t@?#") {
		return "", fmt.Errorf("%q is not a host: write just the host, e.g. api.example.com", h)
	}
	if ip := net.ParseIP(h); ip != nil {
		return h, nil
	}
	if strings.Contains(h, ":") {
		return "", fmt.Errorf("%q carries a port: a grant names hosts, not ports — write api.example.com", h)
	}
	body := strings.TrimPrefix(h, "*.")
	if body == "" || strings.Contains(body, "*") {
		return "", fmt.Errorf("%q is not a usable pattern: a wildcard may only be a leading \"*.\", as in *.example.com", h)
	}
	if len(body) > 253 {
		return "", fmt.Errorf("%q is too long to be a host name", h)
	}
	for _, label := range strings.Split(body, ".") {
		if label == "" || len(label) > 63 {
			return "", fmt.Errorf("%q is not a usable host name: each part must be 1 to 63 characters", h)
		}
		if strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
			return "", fmt.Errorf("%q is not a usable host name: a part cannot start or end with a hyphen", h)
		}
		for _, r := range label {
			if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' {
				return "", fmt.Errorf("%q is not a usable host name: use letters, digits and hyphens", h)
			}
		}
	}
	return h, nil
}

// NormalizeHosts validates a whole list, drops duplicates and blank lines, and
// returns it in a stable order. Sorted for the same reason declared names are:
// the list is read by a person at approval and compared against the previous
// version's, and an order that depends on typing makes two identical grants
// look different.
func NormalizeHosts(hosts []string) ([]string, error) {
	seen := map[string]bool{}
	out := make([]string, 0, len(hosts))
	for _, h := range hosts {
		if strings.TrimSpace(h) == "" {
			continue
		}
		v, err := ValidHost(h)
		if err != nil {
			return nil, err
		}
		if seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	sort.Strings(out)
	return out, nil
}

// Allows reports whether host is on the granted list. An empty list is
// anywhere — the operator said so.
func Allows(hosts []string, host string) bool {
	if len(hosts) == 0 {
		return true
	}
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	for _, h := range hosts {
		if suffix, ok := strings.CutPrefix(h, "*."); ok {
			// The dot is kept in the comparison so that *.example.com does not
			// match notexample.com, and the length test excludes the apex.
			if strings.HasSuffix(host, "."+suffix) {
				return true
			}
			continue
		}
		if host == h {
			return true
		}
	}
	return false
}
