package gearnet

import (
	"context"
	"crypto/tls"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/orkcom-tech/cogitorium/internal/store"
)

// The outward gate, proved by carrying real traffic rather than by reading the
// code that would carry it.
//
// Everything here is real: a real SQLite database in a temp dir, a real gate on
// a real loopback port, a real origin server on a real socket, and a real HTTP
// client configured exactly as a gear's would be — through HTTP_PROXY, with the
// credential the run was issued. What is asserted is what an operator can
// actually check afterwards: the bytes arrived, or they did not, and the row
// says which.
//
// The origin cannot live on the loopback, because refusing the loopback
// whatever the grant says is one of the rules under test. It goes on an address
// this machine actually holds, and the tests that need one skip when there is
// none — a laptop with no network at all is a real machine, and a test that
// fails there would be lying about what broke.

func testDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open the database: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func testGate(t *testing.T, db *sql.DB) *Gate {
	t.Helper()
	g, err := New(db, "127.0.0.1:0")
	if err != nil {
		t.Fatalf("open the gate: %v", err)
	}
	t.Cleanup(func() { g.Close() })
	return g
}

// routableSelf returns an address of this machine that the gate will agree to
// dial. Loopback is refused by design, so an end-to-end test needs a real one.
func routableSelf(t *testing.T) string {
	t.Helper()
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		t.Skipf("cannot read this machine's addresses: %v", err)
	}
	for _, a := range addrs {
		n, ok := a.(*net.IPNet)
		if !ok {
			continue
		}
		ip, ok := netip.AddrFromSlice(n.IP)
		if !ok {
			continue
		}
		if ip = ip.Unmap(); ip.Is4() && routable(ip) {
			return ip.String()
		}
	}
	t.Skip("this machine holds no address the gate would dial, so there is nowhere to put an origin server")
	return ""
}

// origin is a server on the given address that counts what reached it, so a
// refusal can be proved to have happened BEFORE anything left the machine
// rather than merely to have been reported.
type origin struct {
	*httptest.Server
	hits atomic.Int64
}

func newOrigin(t *testing.T, host string, useTLS bool) *origin {
	t.Helper()
	o := &origin{}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		o.hits.Add(1)
		body, _ := io.ReadAll(r.Body)
		fmt.Fprintf(w, "seen %s%s body=%q", r.Method, r.URL.Path, body)
	}))
	ln, err := net.Listen("tcp", net.JoinHostPort(host, "0"))
	if err != nil {
		t.Skipf("cannot listen on %s: %v", host, err)
	}
	srv.Listener.Close()
	srv.Listener = ln
	if useTLS {
		srv.StartTLS()
	} else {
		srv.Start()
	}
	t.Cleanup(srv.Close)
	o.Server = srv
	return o
}

// gearClient is an HTTP client set up the way a gear's is: every request
// through the gate, with the credential the run was given.
func gearClient(t *testing.T, ticket *Ticket) *http.Client {
	t.Helper()
	u, err := url.Parse(ticket.Env("")["HTTP_PROXY"])
	if err != nil {
		t.Fatalf("the ticket produced an unusable proxy address: %v", err)
	}
	return &http.Client{Transport: &http.Transport{
		Proxy:           http.ProxyURL(u),
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, // the origin is httptest's own certificate
	}}
}

// settled waits for the gate to finish writing a connection down.
//
// A tunnel is 'open' for exactly as long as it is open, and an HTTP client
// holds one for reuse after the answer arrives — so reading the row the instant
// the body is in hand is asking before the connection has ended. Closing the
// idle connections is what a finished run does to a gear's, only sooner.
func settled(t *testing.T, g *Gate, client *http.Client) []Connection {
	t.Helper()
	client.CloseIdleConnections()
	for range 200 {
		got := rows(t, g, gearID)
		if len(got) > 0 && got[0].State != StateOpen {
			return got
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("the connection was still open two seconds after it ended: %+v", rows(t, g, gearID))
	return nil
}

func rows(t *testing.T, g *Gate, gearID int64) []Connection {
	t.Helper()
	got, err := g.Store().ForGear(context.Background(), gearID, 0)
	if err != nil {
		t.Fatalf("read the connection log: %v", err)
	}
	return got
}

const gearID = 7

func grantOf(hosts ...string) Grant {
	return Grant{GearID: gearID, GearName: "fetcher", Version: 3, AgentName: "worker", Hosts: hosts}
}

func openTicket(t *testing.T, g *Gate, hosts ...string) *Ticket {
	t.Helper()
	tk, err := g.Open(grantOf(hosts...))
	if err != nil {
		t.Fatalf("open a ticket: %v", err)
	}
	t.Cleanup(tk.Close)
	return tk
}

// A granted run reaches a plain-HTTP destination on its list, and the row says
// so — with the bytes, which is the half of the record that says how much left.
func TestGateCarriesAPlainRequestAndRecordsIt(t *testing.T) {
	host := routableSelf(t)
	db := testDB(t)
	g := testGate(t, db)
	o := newOrigin(t, host, false)

	tk := openTicket(t, g, host)
	client := gearClient(t, tk)
	res, err := client.Post(o.URL+"/report", "text/plain", strings.NewReader("twelve bytes"))
	if err != nil {
		t.Fatalf("the gear's request did not get through: %v", err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusOK || !strings.Contains(string(body), `body="twelve bytes"`) {
		t.Fatalf("the origin answered %d %q; the request did not arrive intact", res.StatusCode, body)
	}
	if o.hits.Load() != 1 {
		t.Fatalf("the origin saw %d requests, want exactly 1", o.hits.Load())
	}

	got := settled(t, g, client)
	if len(got) != 1 {
		t.Fatalf("the log holds %d rows, want 1: %+v", len(got), got)
	}
	c := got[0]
	if c.State != StateClosed || c.Host != host || c.Method != http.MethodPost {
		t.Errorf("the row says state=%q host=%q method=%q; want closed, %s, POST", c.State, c.Host, c.Method, host)
	}
	if c.GearName != "fetcher" || c.Version != 3 || c.AgentName != "worker" {
		t.Errorf("the row names %q v%d for %q; the run's identity did not reach the log", c.GearName, c.Version, c.AgentName)
	}
	if c.BytesSent != 12 {
		t.Errorf("the row counts %d bytes sent, want the 12 the gear posted", c.BytesSent)
	}
	if c.BytesRecv == 0 {
		t.Errorf("the row counts nothing received, and the answer was not empty")
	}
}

// The tunnel, which is every https:// call a gear makes. What travels inside is
// opaque to the gate on purpose; what has to be true is that it arrived and
// that the row names where it went.
func TestGateTunnelsAndRecordsIt(t *testing.T) {
	host := routableSelf(t)
	db := testDB(t)
	g := testGate(t, db)
	o := newOrigin(t, host, true)

	tk := openTicket(t, g, host)
	client := gearClient(t, tk)
	res, err := client.Get(o.URL + "/secure")
	if err != nil {
		t.Fatalf("the tunnel did not carry the request: %v", err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	if !strings.Contains(string(body), "seen GET/secure") {
		t.Fatalf("the origin answered %q; the tunnel did not carry the request intact", body)
	}

	got := settled(t, g, client)
	if len(got) != 1 {
		t.Fatalf("the log holds %d rows, want 1: %+v", len(got), got)
	}
	c := got[0]
	if c.State != StateClosed || c.Method != http.MethodConnect || c.Host != host {
		t.Errorf("the row says state=%q method=%q host=%q; want closed, CONNECT, %s", c.State, c.Method, c.Host, host)
	}
	// The TLS handshake alone is hundreds of bytes each way, so a tunnel that
	// carried a request and an answer cannot have counted zero.
	if c.BytesSent == 0 || c.BytesRecv == 0 {
		t.Errorf("the row counts %d sent and %d received for a completed tunnel", c.BytesSent, c.BytesRecv)
	}
}

// A destination the operator did not grant is refused before anything leaves
// the machine — which is the claim, and is why the origin's own count is what
// this asserts rather than the client's error.
func TestGateRefusesADestinationNotGranted(t *testing.T) {
	host := routableSelf(t)
	db := testDB(t)
	g := testGate(t, db)
	o := newOrigin(t, host, false)

	tk := openTicket(t, g, "api.example.com", "*.internal.example.com")
	res, err := gearClient(t, tk).Get(o.URL + "/report")
	if err != nil {
		t.Fatalf("the gate broke instead of refusing: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusForbidden {
		t.Errorf("the gate answered %d, want 403", res.StatusCode)
	}
	if o.hits.Load() != 0 {
		t.Fatalf("the origin was reached %d times; the refusal happened after the bytes left", o.hits.Load())
	}

	got := rows(t, g, gearID)
	if len(got) != 1 || got[0].State != StateRefusedDestination {
		t.Fatalf("the log does not hold one refused_destination row: %+v", got)
	}
	// The grant as it was at the time, copied into the row: the gear's list will
	// change, and the question afterwards is what the rule WAS.
	if len(got[0].Allowed) != 2 {
		t.Errorf("the row records the allowed list as %v; want the two hosts granted", got[0].Allowed)
	}
	if !strings.Contains(got[0].Error, "api.example.com") {
		t.Errorf("the row's reason does not say what was allowed instead: %q", got[0].Error)
	}
}

// An empty list is anywhere, and it is a choice the operator is allowed to
// make. Asserted by carrying real traffic to a destination that is on no list,
// because Allows() returning true for an empty slice is a claim about a
// function and this is a claim about the gate: a "safer" default that refused
// everything until a list was typed would pass every unit test above and break
// every gear granted the whole internet.
func TestAnEmptyDestinationListMeansAnywhere(t *testing.T) {
	host := routableSelf(t)
	db := testDB(t)
	g := testGate(t, db)
	o := newOrigin(t, host, false)

	tk := openTicket(t, g) // granted, with no destinations named
	client := gearClient(t, tk)
	res, err := client.Get(o.URL + "/anywhere")
	if err != nil {
		t.Fatalf("a gear granted anywhere could not reach %s: %v", host, err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusOK || !strings.Contains(string(body), "seen GET/anywhere") {
		t.Fatalf("the gate answered %d %q for a grant that named no destinations", res.StatusCode, body)
	}
	if o.hits.Load() != 1 {
		t.Fatalf("the origin saw %d requests, want the one that was allowed", o.hits.Load())
	}

	// Recorded like every other connection, and the row says the grant was
	// empty — which is what tells an operator reading it later that this gear
	// could have gone anywhere at all.
	got := settled(t, g, client)
	if len(got) != 1 || got[0].State != StateClosed {
		t.Fatalf("the log does not hold one closed row: %+v", got)
	}
	if len(got[0].Allowed) != 0 {
		t.Errorf("the row records the granted list as %v; it was empty, and the row must say so", got[0].Allowed)
	}
}

// The one rule the operator cannot grant away. "Anywhere" is a legitimate
// choice and it still does not include this server's own API, where a loopback
// request is trusted as the administrator.
func TestGateRefusesTheLoopbackWhateverTheGrantSays(t *testing.T) {
	db := testDB(t)
	g := testGate(t, db)
	o := newOrigin(t, "127.0.0.1", false)

	tk := openTicket(t, g) // no hosts at all: anywhere
	res, err := gearClient(t, tk).Get(o.URL + "/admin")
	if err != nil {
		t.Fatalf("the gate broke instead of refusing: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusForbidden {
		t.Errorf("the gate answered %d for a loopback destination, want 403", res.StatusCode)
	}
	if o.hits.Load() != 0 {
		t.Fatalf("a service on this machine was reached %d times through the gate", o.hits.Load())
	}
	got := rows(t, g, gearID)
	if len(got) != 1 || got[0].State != StateRefusedLocal {
		t.Fatalf("the log does not hold one refused_local row: %+v", got)
	}
}

// A credential outlives its run only far enough to be recognised and refused,
// so a process a gear left behind is named in the log rather than answered.
func TestGateRefusesTrafficFromARunThatHasFinished(t *testing.T) {
	host := routableSelf(t)
	db := testDB(t)
	g := testGate(t, db)
	o := newOrigin(t, host, false)

	tk, err := g.Open(grantOf(host))
	if err != nil {
		t.Fatalf("open a ticket: %v", err)
	}
	client := gearClient(t, tk)
	tk.Close()

	res, err := client.Get(o.URL + "/late")
	if err != nil {
		t.Fatalf("the gate broke instead of refusing: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusProxyAuthRequired {
		t.Errorf("the gate answered %d after the run finished, want 407", res.StatusCode)
	}
	if o.hits.Load() != 0 {
		t.Fatalf("the origin was reached %d times by a finished run", o.hits.Load())
	}
	got := rows(t, g, gearID)
	if len(got) != 1 || got[0].State != StateRefusedNoGrant {
		t.Fatalf("the log does not hold one refused_no_grant row: %+v", got)
	}
	if got[0].GearName != "fetcher" {
		t.Errorf("the row does not name the gear that left the process behind: %+v", got[0])
	}
}

// Anything can reach the port and present nothing. It is answered and NOT
// recorded: a stranger must not be able to fill the operator's audit log, and a
// row that cannot name a gear answers no question anyone has.
func TestGateAnswersAnUncredentialedRequestWithoutWritingARow(t *testing.T) {
	db := testDB(t)
	g := testGate(t, db)

	res, err := http.Get("http://" + g.Addr() + "/")
	if err != nil {
		t.Fatalf("reach the gate: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusProxyAuthRequired {
		t.Errorf("the gate answered %d to a request with no credential, want 407", res.StatusCode)
	}
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM gear_connections`).Scan(&n); err != nil {
		t.Fatalf("count the log: %v", err)
	}
	if n != 0 {
		t.Errorf("an uncredentialed request wrote %d rows into the connection log", n)
	}
}

// A row still open after a restart is a socket that died with the process.
func TestReconcileClosesConnectionsTheProcessTookWithIt(t *testing.T) {
	db := testDB(t)
	s := NewStore(db)
	ctx := context.Background()
	id, err := s.Record(ctx, Connection{GearName: "fetcher", Host: "api.example.com", Port: 443, State: StateOpen})
	if err != nil {
		t.Fatalf("record: %v", err)
	}
	n, err := s.Reconcile(ctx)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if n != 1 {
		t.Fatalf("reconcile settled %d rows, want 1", n)
	}
	var state string
	if err := db.QueryRow(`SELECT state FROM gear_connections WHERE id = ?`, id).Scan(&state); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if state != StateInterrupted {
		t.Errorf("the row says %q after a restart, want interrupted", state)
	}
	// And a settled row is not reopened by a second pass.
	if again, err := s.Reconcile(ctx); err != nil || again != 0 {
		t.Errorf("a second reconcile touched %d rows (err %v); terminal must mean terminal", again, err)
	}
}

func TestValidHost(t *testing.T) {
	ok := map[string]string{
		"api.example.com":   "api.example.com",
		"  API.Example.COM": "api.example.com",
		"example.com.":      "example.com",
		"*.example.com":     "*.example.com",
		"192.0.2.10":        "192.0.2.10",
		"2001:db8::1":       "2001:db8::1",
		"localhost":         "localhost",
	}
	for in, want := range ok {
		got, err := ValidHost(in)
		if err != nil {
			t.Errorf("ValidHost(%q) refused a usable host: %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("ValidHost(%q) = %q, want %q", in, got, want)
		}
	}
	// Every one of these is a thing an operator plausibly types, and each has to
	// come back as a sentence rather than as a silently different rule.
	for _, bad := range []string{
		"", "https://api.example.com", "api.example.com/v1", "api.example.com:443",
		"api.*.com", "*", "api example.com", "user@example.com", "-example.com", "example-.com",
	} {
		if got, err := ValidHost(bad); err == nil {
			t.Errorf("ValidHost(%q) accepted it as %q", bad, got)
		}
	}
}

func TestAllows(t *testing.T) {
	cases := []struct {
		hosts []string
		host  string
		want  bool
	}{
		{nil, "anything.example.com", true}, // empty is anywhere, by the operator's choice
		{[]string{"api.example.com"}, "api.example.com", true},
		{[]string{"api.example.com"}, "API.Example.com", true},
		{[]string{"api.example.com"}, "api.example.com.", true},
		{[]string{"api.example.com"}, "other.example.com", false},
		{[]string{"*.example.com"}, "api.example.com", true},
		{[]string{"*.example.com"}, "deep.api.example.com", true},
		{[]string{"*.example.com"}, "example.com", false},    // the wildcard is not the apex
		{[]string{"*.example.com"}, "notexample.com", false}, // and not a bare suffix match
		{[]string{"a.example.com", "b.example.com"}, "b.example.com", true},
	}
	for _, c := range cases {
		if got := Allows(c.hosts, c.host); got != c.want {
			t.Errorf("Allows(%v, %q) = %v, want %v", c.hosts, c.host, got, c.want)
		}
	}
}

func TestNormalizeHostsRefusesTheWholeListWhenOneEntryIsWrong(t *testing.T) {
	if _, err := NormalizeHosts([]string{"api.example.com", "https://oops"}); err == nil {
		t.Fatal("a list with one unusable entry was accepted; half a grant is worse than none")
	}
	got, err := NormalizeHosts([]string{" B.example.com ", "a.example.com", "", "B.example.com"})
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if len(got) != 2 || got[0] != "a.example.com" || got[1] != "b.example.com" {
		t.Errorf("NormalizeHosts gave %v; want the two hosts, deduplicated and sorted", got)
	}
}

// splitTarget is what turns what a client asked for into what gets dialled and
// what gets written down.
func TestSplitTarget(t *testing.T) {
	cases := []struct {
		in, defPort, host string
		port              int
		ok                bool
	}{
		{"api.example.com:443", "80", "api.example.com", 443, true},
		{"api.example.com", "80", "api.example.com", 80, true},
		{"API.Example.com.", "443", "api.example.com", 443, true},
		{"[2001:db8::1]:8443", "80", "2001:db8::1", 8443, true},
		{"", "80", "", 0, false},
		{"api.example.com:0", "80", "api.example.com", 0, false},
		{"api.example.com:notaport", "80", "api.example.com", 0, false},
	}
	for _, c := range cases {
		host, port, ok := splitTarget(c.in, c.defPort)
		if host != c.host || port != c.port || ok != c.ok {
			t.Errorf("splitTarget(%q, %q) = %q, %d, %v; want %q, %d, %v",
				c.in, c.defPort, host, port, ok, c.host, c.port, c.ok)
		}
	}
}

// A name that resolves only to this machine is refused as firmly as an address
// that is written as one, because otherwise the rule is one DNS record wide.
func TestRoutableAddrsRefusesANameThatPointsHere(t *testing.T) {
	if _, err := routableAddrs(context.Background(), "localhost"); !errors.Is(err, errLocal) {
		t.Errorf("localhost was resolved to something dialable (err %v)", err)
	}
	if _, err := routableAddrs(context.Background(), "169.254.169.254"); !errors.Is(err, errLocal) {
		t.Errorf("the cloud metadata address was accepted (err %v)", err)
	}
}
