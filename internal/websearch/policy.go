// Package websearch is the only place in Cogitorium that can reach the
// internet, and it can reach exactly one host.
//
// The shape of this package is the security argument. An agent calls
// web_search(query): it names no host, no path, no port and no scheme, so
// there is nothing for a hostile string to redirect. Every attack that works
// by making a checker and a dialer disagree about a destination — alternate
// IP encodings, punycode lookalikes, userinfo confusion, wildcard matchers,
// open redirects on a permitted host — has no input to work on here.
//
// This file holds the pure decisions: which addresses may be dialled, which
// authority may be requested, and what counts as a query. No I/O, so all of
// it is table-testable and the tests were written first.
package websearch

import (
	"errors"
	"net"
	"net/netip"
	"net/url"
	"strings"
	"unicode"
)

const (
	// searchHost is ASCII and lowercase so that comparison is ==, not a
	// normalisation routine that can disagree with the resolver.
	searchHost = "echo-page.com"
	searchPath = "/api/search"

	// maxQueryRunes bounds one request. It is also the exfiltration budget
	// per call, which is why it is small and why the audit column is wider.
	maxQueryRunes = 256

	// blobRun is the length of an unbroken non-whitespace run that gets
	// flagged as blob-shaped. A tripwire, not a control.
	blobRun = 48
)

// errRefused is deliberately one fixed value with no detail. Returning the
// underlying error would hand the model an internal port scanner: Go's
// *url.Error stringifies the full URL and the dial target, and tool errors go
// straight into the model's context and the transcript.
var errRefused = errors.New("that destination is not permitted")

// denied is a default-deny table: an address is dialled only if it matches
// nothing here and passes the predicates in permit. Written as data so each
// row has a test, and deliberately redundant with the stdlib predicates below
// — the reviewed design's table omitted 192.168.0.0/16, which is exactly the
// kind of gap a second, differently-derived layer catches.
var denied = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),          // this network
	netip.MustParsePrefix("10.0.0.0/8"),         // RFC1918
	netip.MustParsePrefix("100.64.0.0/10"),      // CGNAT
	netip.MustParsePrefix("127.0.0.0/8"),        // loopback
	netip.MustParsePrefix("169.254.0.0/16"),     // link-local, incl. cloud metadata
	netip.MustParsePrefix("172.16.0.0/12"),      // RFC1918
	netip.MustParsePrefix("192.0.0.0/24"),       // IETF protocol assignments
	netip.MustParsePrefix("192.0.2.0/24"),       // TEST-NET-1
	netip.MustParsePrefix("192.88.99.0/24"),     // 6to4 relay anycast
	netip.MustParsePrefix("192.168.0.0/16"),     // RFC1918
	netip.MustParsePrefix("198.18.0.0/15"),      // benchmarking
	netip.MustParsePrefix("198.51.100.0/24"),    // TEST-NET-2
	netip.MustParsePrefix("203.0.113.0/24"),     // TEST-NET-3
	netip.MustParsePrefix("224.0.0.0/4"),        // multicast
	netip.MustParsePrefix("240.0.0.0/4"),        // reserved, incl. broadcast
	netip.MustParsePrefix("::/128"),             // unspecified
	netip.MustParsePrefix("::1/128"),            // loopback
	netip.MustParsePrefix("::/96"),              // IPv4-compatible
	netip.MustParsePrefix("::ffff:0:0/96"),      // IPv4-mapped
	netip.MustParsePrefix("64:ff9b::/96"),       // NAT64
	netip.MustParsePrefix("64:ff9b:1::/48"),     // local-use NAT64
	netip.MustParsePrefix("100::/64"),           // discard-only
	netip.MustParsePrefix("2001::/32"),          // Teredo
	netip.MustParsePrefix("2001:db8::/32"),      // documentation
	netip.MustParsePrefix("2002::/16"),          // 6to4
	netip.MustParsePrefix("fc00::/7"),           // unique local
	netip.MustParsePrefix("fe80::/10"),          // link-local
	netip.MustParsePrefix("ff00::/8"),           // multicast
}

// v6Global is the only IPv6 range that may be dialled at all. Requiring
// membership rather than listing exclusions is what makes an unlisted future
// transition mechanism refused by default instead of permitted by omission.
var v6Global = netip.MustParsePrefix("2000::/3")

// addrPolicy decides whether one resolved address may be connected to. It
// carries this machine's own addresses because a public IP that happens to be
// ours is still ours — it is how the searcher would reach a service bound to
// 0.0.0.0 on this host.
type addrPolicy struct {
	local map[netip.Addr]bool
}

// newAddrPolicy collects the addresses this machine answers on. extra takes
// the configured listen address, which may be a host:port or a bare host.
func newAddrPolicy(extra []string) (*addrPolicy, error) {
	p := &addrPolicy{local: map[netip.Addr]bool{}}

	ifaceAddrs, err := net.InterfaceAddrs()
	if err != nil {
		// Failing closed here would make the server unbootable on a machine
		// with an odd network stack; failing open would silently drop a
		// layer. Neither is acceptable, so the caller is told.
		return nil, err
	}
	for _, a := range ifaceAddrs {
		if ipnet, ok := a.(*net.IPNet); ok {
			if ip, ok := netip.AddrFromSlice(ipnet.IP); ok {
				p.local[ip.Unmap()] = true
			}
		}
	}
	for _, e := range extra {
		host := e
		if h, _, err := net.SplitHostPort(e); err == nil {
			host = h
		}
		if ip, err := netip.ParseAddr(strings.Trim(host, "[]")); err == nil {
			p.local[ip.Unmap()] = true
		}
	}
	return p, nil
}

// permit is called from net.Dialer.Control: after resolution, before connect,
// once per candidate address. That is the only hook where the check and the
// connect cannot be separated by a re-resolve, which is what makes DNS
// rebinding and Happy-Eyeballs multi-address races unexploitable here.
func (p *addrPolicy) permit(addrPort string) error {
	ap, err := netip.ParseAddrPort(addrPort)
	if err != nil {
		return errRefused // the zero value of the decision is deny
	}
	// The port pin is a security control, not scheme hygiene: it is what
	// keeps 169.254.169.254:80 and this server's own :8688 unreachable even
	// if the table above is ever wrong.
	if ap.Port() != 443 {
		return errRefused
	}

	// Unmap so ::ffff:127.0.0.1 is judged as 127.0.0.1 and the table's v4
	// rows apply as written. It is not the control that stops mapped
	// addresses — without it they are Is6 and fail the 2000::/3 requirement
	// below, so they fail closed either way — but it is what keeps the local
	// address set comparable.
	ip := ap.Addr().Unmap()
	if !ip.IsValid() {
		return errRefused
	}
	if ip.Is6() && !v6Global.Contains(ip) {
		return errRefused
	}
	// Redundant with the table today — mutation-checked, and deleting this
	// block breaks no test. It is kept because it is derived differently: it
	// is what still refuses loopback and RFC1918 if a row is ever removed
	// from the table by someone tidying up. Stated plainly so nobody reads it
	// as covering something the table misses.
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsMulticast() || ip.IsInterfaceLocalMulticast() {
		return errRefused
	}
	for _, d := range denied {
		if d.Contains(ip) {
			return errRefused
		}
	}
	if p.local[ip] {
		return errRefused
	}
	return nil
}

// checkAuthority runs per request rather than per connection. Keep-alive and
// HTTP/2 pooling mean a dial-time check is a backstop, not a gate: a second
// request can ride a connection that was authorised for the first.
func checkAuthority(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return errRefused
	}
	if u.Scheme != "https" {
		return errRefused
	}
	if u.User != nil {
		// user@host is the classic way to make a URL read as one host and
		// resolve as another.
		return errRefused
	}
	if !hostIs(u.Host, searchHost) {
		return errRefused
	}
	return nil
}

// hostIs compares an authority against the one permitted name. Lowercasing
// and stripping a trailing dot are the only normalisations, and both are
// applied to one ASCII constant, so there is no IDNA step where the checker
// and the resolver could disagree.
func hostIs(authority, want string) bool {
	h := authority
	if host, port, err := net.SplitHostPort(authority); err == nil {
		if port != "443" {
			return false
		}
		h = host
	}
	h = strings.ToLower(strings.TrimSuffix(strings.Trim(h, "[]"), "."))
	return h == want
}

// ValidateQuery is the whole of what a model may put on the wire. It returns
// the query to send, whether it looked blob-shaped, and an error if it cannot
// be sent at all. The returned string is what is both sent and recorded — the
// audit row must never differ from what left the machine.
func ValidateQuery(q string) (string, bool, error) {
	q = strings.TrimSpace(q)
	if q == "" {
		return "", false, errors.New("the search query is empty")
	}
	if len([]rune(q)) > maxQueryRunes {
		return "", false, errors.New("the search query is too long (256 characters maximum)")
	}
	for _, r := range q {
		if r != ' ' && unicode.IsControl(r) {
			return "", false, errors.New("the search query contains control characters")
		}
	}

	// A long unbroken run is what base64 and hex look like. Flagging rather
	// than refusing is deliberate: spacing defeats the check, so treating it
	// as a control would be a lie, while the flag is genuinely useful to an
	// operator reading the audit table.
	flagged := false
	run := 0
	for _, r := range q {
		if unicode.IsSpace(r) {
			run = 0
			continue
		}
		run++
		if run > blobRun {
			flagged = true
			break
		}
	}
	return q, flagged, nil
}
