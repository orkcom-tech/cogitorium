package websearch

import (
	"net/netip"
	"strings"
	"testing"
)

// These tests are written before the implementation and each name states the
// consequence rather than the mechanism, so a failure reads as "this server
// can now reach X" rather than "a table row changed".

func testPolicy(t *testing.T) *addrPolicy {
	t.Helper()
	p, err := newAddrPolicy([]string{"127.0.0.1:8688"})
	if err != nil {
		t.Fatalf("building the policy failed: %v", err)
	}
	return p
}

func TestRefusesNon443Port(t *testing.T) {
	p := testPolicy(t)
	// The port pin is a security control, not scheme hygiene: it is what
	// keeps IMDS (port 80) and this server's own API unreachable even if the
	// prefix table below is ever wrong.
	for _, addr := range []string{"93.184.216.34:80", "93.184.216.34:8688", "93.184.216.34:8443", "93.184.216.34:0"} {
		if err := p.permit(addr); err == nil {
			t.Errorf("%s was permitted; any port but 443 must be refused", addr)
		}
	}
	if err := p.permit("93.184.216.34:443"); err != nil {
		t.Errorf("a public address on 443 must be permitted, got %v", err)
	}
}

func TestRefusesLoopbackAndThisServer(t *testing.T) {
	p := testPolicy(t)
	for _, addr := range []string{
		"127.0.0.1:443", "127.0.0.2:443", "[::1]:443",
		"[::ffff:127.0.0.1]:443", // IPv4-mapped loopback
	} {
		if err := p.permit(addr); err == nil {
			t.Errorf("%s was permitted; the server must never be able to call itself", addr)
		}
	}
}

func TestRefusesCloudMetadata(t *testing.T) {
	p := testPolicy(t)
	for _, addr := range []string{"169.254.169.254:443", "[fe80::1]:443", "169.254.170.2:443"} {
		if err := p.permit(addr); err == nil {
			t.Errorf("%s was permitted; link-local reaches instance credentials", addr)
		}
	}
}

func TestRefusesPrivateRanges(t *testing.T) {
	p := testPolicy(t)
	for _, addr := range []string{
		"10.0.0.5:443",
		"172.16.0.5:443",
		"172.31.255.254:443",
		"192.168.1.1:443", // the range the reviewed design's table omitted
		"100.64.0.1:443",  // CGNAT
		"[fc00::1]:443",   // unique local
	} {
		if err := p.permit(addr); err == nil {
			t.Errorf("%s was permitted; private ranges are the internal network", addr)
		}
	}
}

func TestRefusesTransitionAddressesThatCarryIPv4Inside(t *testing.T) {
	p := testPolicy(t)
	// Each of these encodes an IPv4 destination inside an IPv6 address, so a
	// naive "is it v6? then it is public" check hands over the internal net.
	for _, addr := range []string{
		"[64:ff9b::7f00:1]:443", // NAT64 wrapping 127.0.0.1
		"[2002:7f00:1::]:443",   // 6to4 wrapping 127.0.0.1
		"[2001::1]:443",         // Teredo
		"[::7f00:1]:443",        // IPv4-compatible
		"[100::1]:443",          // discard-only
	} {
		if err := p.permit(addr); err == nil {
			t.Errorf("%s was permitted; it reaches an IPv4 destination through a v6 wrapper", addr)
		}
	}
}

// Mutation-checked: deleting the v6Global requirement makes this test fail
// and nothing else, which is what proves the rule is load-bearing rather
// than decorative. 4000::/3 is unassigned — no deny row covers it and every
// stdlib predicate calls it an ordinary global address.
func TestRefusesUnassignedIPv6OutsideTheGlobalRange(t *testing.T) {
	p := testPolicy(t)
	for _, addr := range []string{"[4000::1]:443", "[8000::1]:443", "[c000::1]:443"} {
		if err := p.permit(addr); err == nil {
			t.Errorf("%s was permitted; only 2000::/3 may be dialled, so an unassigned range "+
				"or a future transition mechanism is refused by default rather than permitted by omission", addr)
		}
	}
	if err := p.permit("[2606:4700:4700::1111]:443"); err != nil {
		t.Errorf("an ordinary global v6 address must be permitted, got %v", err)
	}
}

func TestRefusesUnspecifiedBroadcastAndReserved(t *testing.T) {
	p := testPolicy(t)
	for _, addr := range []string{
		"0.0.0.0:443", "0.0.0.1:443", "255.255.255.255:443",
		"224.0.0.1:443", "240.0.0.1:443", "[::]:443", "[ff02::1]:443",
	} {
		if err := p.permit(addr); err == nil {
			t.Errorf("%s was permitted", addr)
		}
	}
}

func TestRefusesAnythingItCannotParse(t *testing.T) {
	p := testPolicy(t)
	// The zero value of the decision is deny: a shape we do not understand is
	// refused rather than passed through to the dialer.
	for _, addr := range []string{
		"", ":443", "host:", "echo-page.com:443", "2130706433:443",
		"0x7f.0.0.1:443", "127.0.0.1", "[::1]", "127.0.0.1:443:443",
	} {
		if err := p.permit(addr); err == nil {
			t.Errorf("%q was permitted; unparseable input must be refused", addr)
		}
	}
}

func TestRefusesTheMachinesOwnInterfaceAddresses(t *testing.T) {
	// A public address that happens to be this machine's is still this
	// machine: it is how the searcher would reach a service bound to 0.0.0.0.
	p, err := newAddrPolicy([]string{"127.0.0.1:8688", "93.184.216.34"})
	if err != nil {
		t.Fatalf("building the policy failed: %v", err)
	}
	if err := p.permit("93.184.216.34:443"); err == nil {
		t.Error("an address bound by this host was permitted")
	}
}

func TestAuthorityGuardRefusesEveryHostButTheOne(t *testing.T) {
	for _, raw := range []string{
		"https://notecho-page.com/api/search",
		"https://echo-page.com.evil.tld/api/search",
		"https://evil.tld/echo-page.com",
		"http://echo-page.com/api/search", // scheme downgrade
		"https://user@evil.tld/",
		"https://echo-page.com:8443/api/search",
	} {
		if err := checkAuthority(raw); err == nil {
			t.Errorf("%s was permitted", raw)
		}
	}
	for _, raw := range []string{
		"https://echo-page.com/api/search",
		"https://ECHO-PAGE.COM/api/search", // case
		"https://echo-page.com./api/search", // trailing dot
		"https://echo-page.com:443/api/search",
	} {
		if err := checkAuthority(raw); err != nil {
			t.Errorf("%s was refused: %v", raw, err)
		}
	}
}

func TestValidateQueryRefusesWhatCannotBeAQuestion(t *testing.T) {
	long := strings.Repeat("a", 257)
	for _, q := range []string{"", "   ", "hello\x00world", "line\nbreak", long} {
		if _, _, err := ValidateQuery(q); err == nil {
			t.Errorf("%q was accepted", q)
		}
	}
}

func TestValidateQueryFlagsBlobsWithoutRefusingThem(t *testing.T) {
	// A long unbroken run is what base64 looks like. It is a tripwire, not a
	// control — spacing defeats it — so it flags rather than refuses, and the
	// flag is what the operator sees.
	blob := strings.Repeat("QUJDREVG", 7) // 56 chars, no whitespace
	_, flagged, err := ValidateQuery("look up " + blob)
	if err != nil {
		t.Fatalf("a flagged query must still be returned, got %v", err)
	}
	if !flagged {
		t.Error("a 56-character unbroken run was not flagged")
	}
	_, flagged, err = ValidateQuery("how do I configure a reverse proxy for websockets")
	if err != nil {
		t.Fatalf("an ordinary question was refused: %v", err)
	}
	if flagged {
		t.Error("an ordinary question was flagged; the tripwire is too tight to be read")
	}
}

func TestValidateQueryTrimsButDoesNotRewrite(t *testing.T) {
	// What is recorded in the audit table must be what leaves the machine.
	q, _, err := ValidateQuery("  spaced out  ")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if q != "spaced out" {
		t.Errorf("got %q, want %q", q, "spaced out")
	}
}

func TestDeniedTableParses(t *testing.T) {
	// MustParsePrefix would panic at init; this asserts the table is sane
	// even if it is ever built from strings at runtime.
	for _, p := range denied {
		if !p.IsValid() {
			t.Errorf("invalid prefix in the deny table: %v", p)
		}
		if p.Addr() != p.Masked().Addr() {
			t.Errorf("prefix %v has host bits set; Contains would behave unexpectedly", p)
		}
	}
	if _, err := netip.ParsePrefix("2000::/3"); err != nil {
		t.Fatalf("the v6 global range is unparseable: %v", err)
	}
}
