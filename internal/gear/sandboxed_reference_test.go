package gear

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/orkcom-tech/cogitorium/internal/gearnet"
	"github.com/orkcom-tech/cogitorium/internal/secrets"
)

// The claim, in the place where it is a boundary: the container never holds the
// credential, and the destination receives it.
//
// internal/gearnet proves the substitution itself, through real TLS in both
// directions. What it cannot prove is the wiring — that the value the executor
// puts in a container's environment is the stand-in and not the secret, that
// the gate's certificate reaches the payload, and that a run with no grant
// still gets the real value because there is no edge to substitute at.
//
// So: a real container, a real gear, a real origin, and the executor the server
// actually builds.
const tellSource = `package main

import (
	"fmt"
	"io"
	"net/http"
	"os"
)

func main() {
	key := os.Getenv("API_KEY")
	// What the gear itself can see. If this is the credential, the feature does
	// not exist.
	fmt.Printf("HELD=%s\n", key)
	if _, err := os.Stat("/work/.gearnet-ca.crt"); err == nil {
		fmt.Printf("CA=yes\n")
	} else {
		fmt.Printf("CA=no\n")
	}
	req, _ := http.NewRequest("GET", os.Getenv("TARGET"), nil)
	req.Header.Set("Authorization", "Bearer "+key)
	r, err := http.DefaultClient.Do(req)
	if err != nil {
		fmt.Printf("ERR=%v\n", err)
		return
	}
	defer r.Body.Close()
	b, _ := io.ReadAll(r.Body)
	fmt.Printf("STATUS=%d BODY=%s\n", r.StatusCode, b)
}
`

func TestAGrantedRunHoldsAStandInAndTheDestinationGetsTheSecret(t *testing.T) {
	s := newSandboxed(t)
	ctx := context.Background()
	host := selfAddr(t)

	const credential = "sk-live-the-actual-credential-value"
	var got atomic.Pointer[string]
	var hits atomic.Int64
	origin := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		a := r.Header.Get("Authorization")
		got.Store(&a)
		fmt.Fprint(w, "the-origin-answered")
	}))
	ln, err := net.Listen("tcp", net.JoinHostPort(host, "0"))
	if err != nil {
		t.Skipf("cannot listen on %s: %v", host, err)
	}
	origin.Listener.Close()
	origin.Listener = ln
	origin.Start()
	defer origin.Close()

	// A real store with a real key, because a secret is only a secret if it was
	// sealed the way this install seals one.
	key, err := secrets.NewKey("this-is-a-test-key-of-sufficient-length")
	if err != nil {
		t.Fatalf("derive a key: %v", err)
	}
	store := secrets.NewStore(s.db, key)
	if _, err := store.Set(ctx, nil, "API_KEY", secrets.KindSecret, credential, ""); err != nil {
		t.Fatalf("set the secret: %v", err)
	}
	if _, err := store.Set(ctx, nil, "TARGET", secrets.KindVariable, origin.URL, ""); err != nil {
		t.Fatalf("set the target: %v", err)
	}
	resolver, err := secrets.NewResolver(store, t.TempDir(), t.TempDir())
	if err != nil {
		t.Fatalf("build the resolver: %v", err)
	}
	s.exec = NewExecutor(s.gears, s.dataDir, s.exec.sandbox, resolver, s.gate)

	g := s.approveBinaryWithEnv("tell", "tell", tellSource, []string{"API_KEY", "TARGET"})
	if g, err = s.gears.SetNetwork(ctx, g.ID, true, []string{host}); err != nil {
		t.Fatalf("grant the network: %v", err)
	}

	res, err := s.exec.Run(ctx, g, `{}`, Caller{AgentID: &s.agentID, WorkspaceID: &s.wsID, AgentName: "worker"})
	if err != nil {
		t.Fatalf("run the gear: %v (stderr: %s)", err, res.Stderr)
	}
	t.Logf("the gear said: %s", strings.TrimSpace(res.Stdout))

	held := valueOf(t, res.Stdout, "HELD=")
	if held == credential {
		t.Fatal("the container was given the credential itself, which is the thing this feature removes")
	}
	if !strings.HasPrefix(held, "cogitorium-secret-") {
		t.Fatalf("the container holds %q, which is neither the credential nor a stand-in", held)
	}
	if valueOf(t, res.Stdout, "CA=") != "yes" {
		t.Fatal("the gate's certificate did not reach the payload, so a gear making an HTTPS call would " +
			"fail to verify a connection this install deliberately terminates")
	}
	if hits.Load() != 1 {
		t.Fatalf("the origin saw %d requests, want the one the gear made", hits.Load())
	}
	if seen := *got.Load(); seen != "Bearer "+credential {
		t.Fatalf("the destination received %q — the stand-in was not exchanged at the edge, so the gear's "+
			"call would fail and the credential never arrives", seen)
	}
	// The redactor still covers the real value: a destination that echoes it
	// back must not put it in the record.
	if strings.Contains(res.Stdout, credential) {
		t.Fatal("the credential appears in what the run reported")
	}
}

// A gear without the network gets the real value, because a stand-in is only
// worth anything at an edge and an ungranted run has none. This is the rule
// stated in resolveEnv, checked rather than trusted — the alternative would be
// a gear that silently receives a credential which cannot work anywhere.
func TestAnUngrantedRunStillGetsTheRealValue(t *testing.T) {
	s := newSandboxed(t)
	ctx := context.Background()

	const credential = "sk-only-useful-inside-this-process"
	key, err := secrets.NewKey("this-is-a-test-key-of-sufficient-length")
	if err != nil {
		t.Fatal(err)
	}
	store := secrets.NewStore(s.db, key)
	if _, err := store.Set(ctx, nil, "API_KEY", secrets.KindSecret, credential, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Set(ctx, nil, "TARGET", secrets.KindVariable, "http://127.0.0.1:1/", ""); err != nil {
		t.Fatal(err)
	}
	resolver, err := secrets.NewResolver(store, t.TempDir(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s.exec = NewExecutor(s.gears, s.dataDir, s.exec.sandbox, resolver, s.gate)

	g := s.approveBinaryWithEnv("hold", "hold", tellSource, []string{"API_KEY", "TARGET"})
	res, err := s.exec.Run(ctx, g, `{}`, Caller{AgentID: &s.agentID, WorkspaceID: &s.wsID, AgentName: "worker"})
	if err != nil {
		t.Fatalf("run: %v (stderr: %s)", err, res.Stderr)
	}
	// The value itself cannot be read back out of the record, because the
	// redactor covers it — which is the point of the redactor and is the proof
	// that the run held the real thing. A stand-in is not redacted (there is
	// nothing secret about one), so the two cases are told apart by which of
	// them the record shows: "[redacted]" here, and a readable stand-in in the
	// granted run above.
	held := valueOf(t, res.Stdout, "HELD=")
	if strings.HasPrefix(held, "cogitorium-secret-") {
		t.Fatalf("an ungranted run was given a stand-in (%s) — there is no gate in its way to turn one "+
			"back into anything, so it would hold a credential that cannot work anywhere", held)
	}
	if held != "[redacted]" {
		t.Fatalf("an ungranted run printed %q; the real value should have been there and been redacted "+
			"on the way into the record", held)
	}
	if valueOf(t, res.Stdout, "CA=") != "no" {
		t.Fatal("a run whose TLS is never read inside was handed the gate's certificate anyway")
	}
}

// The certificate the executor writes has to be one a client can actually load.
// A file that is present but unparseable would pass a "does it exist" check and
// fail every TLS handshake the gear makes.
func TestTheCertificateHandedToARunIsUsable(t *testing.T) {
	dir := t.TempDir()
	g, err := gearnet.New(newFixture(t).db, "127.0.0.1:0", dir)
	if err != nil {
		t.Fatalf("open a gate: %v", err)
	}
	defer g.Close()
	pem := g.CACert()
	if len(pem) == 0 {
		t.Fatal("the gate has no certificate to hand out")
	}
	if !strings.HasPrefix(string(pem), "-----BEGIN CERTIFICATE-----") {
		t.Fatalf("what the gate hands out is not a certificate: %.40q", pem)
	}
	// And it is the same one on the next start, because a gear or an image that
	// pinned it must not find it changed underneath.
	again, err := gearnet.New(newFixture(t).db, "127.0.0.1:0", dir)
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	if string(again.CACert()) != string(pem) {
		t.Fatal("the gate minted a new authority on restart, so anything that trusted the old one stops working")
	}
	if _, err := os.Stat(filepath.Join(dir, "gearnet-ca.key")); err != nil {
		t.Fatalf("the signing key was not kept: %v", err)
	}
}

func valueOf(t *testing.T, out, prefix string) string {
	t.Helper()
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(line, prefix))
		}
	}
	t.Fatalf("the gear printed no %s line:\n%s", prefix, out)
	return ""
}
