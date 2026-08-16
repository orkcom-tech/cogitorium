package gearnet

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
)

// The whole claim of this feature, end to end through real TLS: the gear holds
// a stand-in, and the destination receives the credential.
//
// Nothing here is stubbed. The origin is a real HTTPS server with its own
// certificate, the gear's client is a real HTTP client configured exactly the
// way the executor configures one — proxy from the ticket, the gate's own
// certificate as its root — and the gate does a real TLS handshake in both
// directions. If the substitution did not happen at the edge, the origin would
// see the stand-in, and it says so.
func TestTheDestinationGetsTheSecretAndTheRunNeverHeldIt(t *testing.T) {
	host := routableSelf(t)
	seen := &recorder{}
	srv, originCA := seen.serve(t, host)

	g := testGate(t, testDB(t))
	trustOrigin(t, g, originCA)

	ticket, err := g.Open(Grant{GearID: 1, GearName: "poster"})
	if err != nil {
		t.Fatalf("open a ticket: %v", err)
	}
	defer ticket.Close()

	const real = "sk-live-9c1f4b7e-the-actual-credential"
	ref := ticket.Reference(real)
	if ref == real {
		t.Fatal("no stand-in was minted, so the run would hold the credential itself")
	}
	if !strings.HasPrefix(ref, refPrefix) {
		t.Fatalf("a stand-in should be recognisable when it turns up somewhere it should not: %q", ref)
	}

	client := gearClientTrusting(t, g, ticket)
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/v1/things?key="+ref,
		strings.NewReader(`{"token":"`+ref+`","note":"and one in the body"}`))
	if err != nil {
		t.Fatal(err)
	}
	// Exactly what a gear does: put the value it was given into the request.
	req.Header.Set("Authorization", "Bearer "+ref)
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("the gear's request did not complete: %v", err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("the gate answered %d rather than carrying the request", resp.StatusCode)
	}

	if seen.auth.Load() == nil {
		t.Fatal("the origin was never reached")
	}
	auth := *seen.auth.Load()
	if auth != "Bearer "+real {
		t.Fatalf("the destination received %q — the substitution did not happen at the edge, so the "+
			"stand-in went out as if it were the credential", auth)
	}
	if q := *seen.query.Load(); q != "key="+real {
		t.Fatalf("a stand-in in the query string was not substituted: %q", q)
	}
	if b := *seen.body.Load(); !strings.Contains(b, real) || strings.Contains(b, refPrefix) {
		t.Fatalf("a stand-in in the body was not substituted: %s", b)
	}
}

// A stand-in stops meaning anything when the run ends. That is what makes it
// safe for a gear to hold one at all: exfiltrating it exfiltrates a string that
// opens nothing.
func TestAStandInDiesWithTheRunThatMintedIt(t *testing.T) {
	host := routableSelf(t)
	seen := &recorder{}
	srv, originCA := seen.serve(t, host)

	g := testGate(t, testDB(t))
	trustOrigin(t, g, originCA)

	first, err := g.Open(Grant{GearID: 1, GearName: "one"})
	if err != nil {
		t.Fatal(err)
	}
	ref := first.Reference("sk-the-credential")
	first.Close()

	// A second run, which never asked for that secret, sends the first one's
	// stand-in. It must travel as the meaningless string it now is.
	second, err := g.Open(Grant{GearID: 2, GearName: "two"})
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	// The second run holds a stand-in of its own, so its TLS is read inside —
	// which is the case where a leaked one WOULD be substituted if the gate
	// kept a single table instead of one per ticket.
	second.Reference("sk-a-different-credential")

	client := gearClientTrusting(t, g, second)
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/", nil)
	req.Header.Set("Authorization", "Bearer "+ref)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	if auth := *seen.auth.Load(); auth != "Bearer "+ref {
		t.Fatalf("another run's stand-in was exchanged for a real value: %q", auth)
	}
}

// A run with nothing to substitute is tunnelled exactly as it always was. The
// gate must not start reading inside every granted gear's TLS because one
// feature needed to read inside some.
func TestARunWithoutStandInsIsStillATunnel(t *testing.T) {
	g := testGate(t, testDB(t))
	ticket, err := g.Open(Grant{GearID: 1, GearName: "plain"})
	if err != nil {
		t.Fatal(err)
	}
	defer ticket.Close()
	if ticket.Intercepts() {
		t.Fatal("a run holding no stand-ins would have its TLS terminated")
	}
	ticket.Reference("sk-something")
	if !ticket.Intercepts() {
		t.Fatal("a run holding a stand-in must be intercepted, or the stand-in never becomes the value")
	}
}

// A body larger than the buffered bound is still rewritten, including a
// stand-in that lands across a read boundary — which is the case a simpler
// implementation gets wrong on some payload sizes and not others.
func TestAStandInSplitAcrossReadsIsStillSubstituted(t *testing.T) {
	g := testGate(t, testDB(t))
	ticket, err := g.Open(Grant{GearID: 1, GearName: "big"})
	if err != nil {
		t.Fatal(err)
	}
	defer ticket.Close()
	ref := ticket.Reference("THE-REAL-VALUE")

	// Straddle the reader's 32 KiB chunk boundary at every offset the stand-in
	// could be cut at.
	for _, offset := range []int{1, len(ref) / 2, len(ref) - 1} {
		pad := strings.Repeat("x", 32*1024-offset)
		got, err := io.ReadAll(substituteReader(strings.NewReader(pad+ref+"tail"), ticket))
		if err != nil {
			t.Fatalf("offset %d: %v", offset, err)
		}
		if want := pad + "THE-REAL-VALUE" + "tail"; string(got) != want {
			t.Fatalf("offset %d: a stand-in cut across two reads was not substituted:\n got %q\nwant %q",
				offset, tailOf(string(got)), tailOf(want))
		}
	}
}

func tailOf(s string) string {
	if len(s) > 60 {
		return "…" + s[len(s)-60:]
	}
	return s
}

// recorder is a real HTTPS origin that keeps what it was actually sent.
type recorder struct {
	auth  atomic.Pointer[string]
	query atomic.Pointer[string]
	body  atomic.Pointer[string]
}

// serve starts a real HTTPS origin whose certificate is valid for the address
// it is actually on, and returns the authority that signed it.
//
// httptest's own certificate names only the loopback, and the gate refuses a
// loopback destination — so the two cannot meet without minting one. It is
// signed by this package's own authority code, which means the certificate the
// gate verifies upstream is a real one from a real private CA.
func (rec *recorder) serve(t *testing.T, host string) (*httptest.Server, []byte) {
	t.Helper()
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a, q := r.Header.Get("Authorization"), r.URL.RawQuery
		raw, _ := io.ReadAll(r.Body)
		b := string(raw)
		rec.auth.Store(&a)
		rec.query.Store(&q)
		rec.body.Store(&b)
		fmt.Fprint(w, "ok")
	}))
	ln, err := net.Listen("tcp", net.JoinHostPort(host, "0"))
	if err != nil {
		t.Skipf("cannot listen on %s: %v", host, err)
	}
	srv.Listener.Close()
	srv.Listener = ln

	ca, err := loadAuthority(t.TempDir())
	if err != nil {
		t.Fatalf("build an authority for the origin: %v", err)
	}
	leaf, err := ca.leaf(host)
	if err != nil {
		t.Fatalf("mint the origin a certificate: %v", err)
	}
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{*leaf}, MinVersion: tls.VersionTLS12}
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return srv, ca.pem
}

// trustOrigin points the gate's upstream transport at the origin's own
// certificate. Verification stays ON — this is a private authority being
// trusted deliberately, which is the same thing an operator does for an
// internal service, rather than verification being switched off.
func trustOrigin(t *testing.T, g *Gate, caPEM []byte) {
	t.Helper()
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		t.Fatal("the origin's authority is not loadable")
	}
	g.tr.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
}

// gearClientTrusting is the executor's own arrangement: the run's proxy, and
// the gate's certificate as the client's root. Verification is on, so this also
// proves the certificate the executor hands a gear is one that works.
func gearClientTrusting(t *testing.T, g *Gate, ticket *Ticket) *http.Client {
	t.Helper()
	u, err := url.Parse(ticket.Env("")["HTTP_PROXY"])
	if err != nil {
		t.Fatalf("the ticket produced an unusable proxy address: %v", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(g.CACert()) {
		t.Fatal("the gate's certificate is not one a client can load, so no gear could ever trust it")
	}
	return &http.Client{Transport: &http.Transport{
		Proxy:           http.ProxyURL(u),
		TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12},
	}}
}
