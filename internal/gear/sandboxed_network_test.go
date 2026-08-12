package gear

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/orkcom-tech/cogitorium/internal/secrets"
)

// The network grant where it is actually a boundary.
//
// internal/gearnet proves the gate itself with real sockets: what it carries,
// what it refuses, and what it writes down. What it cannot prove is the wiring —
// that a container gets --network at all, that host.docker.internal resolves
// inside it, and that the address the gate hands out is one the gear can
// actually reach. That is what this test is for, and there is no way to
// establish it by reading code.
//
// So: a real container, a real gear compiled for it, a real origin server on an
// address this machine holds, and the same executor the server runs. The gear
// is run twice — once ungranted and once granted — because "the grant works" and
// "the refusal works" are two claims and only one of them survives a container
// that quietly had the network all along.

// fetchSource is a gear that reaches out and says what happened. Go's default
// transport reads HTTP_PROXY, which is exactly how a gear finds the gate: no
// cooperation beyond using an ordinary HTTP client.
const fetchSource = `package main

import (
	"fmt"
	"io"
	"net/http"
	"os"
)

func main() {
	r, err := http.Get(os.Getenv("TARGET"))
	if err != nil {
		fmt.Printf("ERR=%v\n", err)
		return
	}
	defer r.Body.Close()
	b, _ := io.ReadAll(r.Body)
	fmt.Printf("STATUS=%d BODY=%s\n", r.StatusCode, b)
}
`

// selfAddr is an address this machine holds that the gate will agree to dial.
// The loopback is refused whatever the grant says, so an origin has to live
// somewhere real.
func selfAddr(t *testing.T) string {
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
		ip = ip.Unmap()
		if ip.Is4() && !ip.IsLoopback() && !ip.IsLinkLocalUnicast() && !ip.IsUnspecified() {
			return ip.String()
		}
	}
	t.Skip("this machine holds no routable address, so there is nowhere a gear could be allowed to reach")
	return ""
}

func TestSandboxedGearReachesOnlyWhatTheOperatorGranted(t *testing.T) {
	s := newSandboxed(t)
	ctx := context.Background()
	host := selfAddr(t)

	var hits atomic.Int64
	origin := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
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

	// Where to go is a named value the operator set, which is the other half of
	// this work: a gear is given names, never values, and this proves the two
	// halves compose in one run.
	if _, err := secrets.NewStore(s.db, nil).Set(ctx, nil, "TARGET", secrets.KindVariable, origin.URL, ""); err != nil {
		t.Fatalf("set TARGET: %v", err)
	}

	g := s.approveBinaryWithEnv("fetch", "fetch", fetchSource, []string{"TARGET"})

	// Ungranted: the container has --network none, so there is nothing to reach
	// and nothing to record.
	res, err := s.exec.Run(ctx, g, `{}`, Caller{AgentID: &s.agentID, WorkspaceID: &s.wsID, AgentName: "worker"})
	if err != nil {
		t.Fatalf("run an ungranted gear: %v (stderr: %s)", err, res.Stderr)
	}
	t.Logf("ungranted, the gear said: %s", strings.TrimSpace(res.Stdout))
	if !strings.Contains(res.Stdout, "ERR=") {
		t.Fatalf("a gear nobody granted the network reached %q: %s", origin.URL, res.Stdout)
	}
	if hits.Load() != 0 {
		t.Fatalf("the origin was reached %d times by an ungranted gear", hits.Load())
	}

	// Granted, to that host and nothing else.
	g, err = s.gears.SetNetwork(ctx, g.ID, true, []string{host})
	if err != nil {
		t.Fatalf("grant the network: %v", err)
	}
	res, err = s.exec.Run(ctx, g, `{}`, Caller{AgentID: &s.agentID, WorkspaceID: &s.wsID, AgentName: "worker"})
	if err != nil {
		t.Fatalf("run a granted gear: %v (stderr: %s)", err, res.Stderr)
	}
	t.Logf("granted, the gear said: %s", strings.TrimSpace(res.Stdout))
	if !strings.Contains(res.Stdout, "BODY=the-origin-answered") {
		t.Fatalf("a granted gear could not reach %q: %s", origin.URL, res.Stdout)
	}
	if hits.Load() != 1 {
		t.Fatalf("the origin saw %d requests, want exactly the granted one", hits.Load())
	}

	// And the connection is on record: this is the whole reason the traffic goes
	// through the gate rather than straight out of the container.
	conns, err := s.gate.Store().ForGear(ctx, g.ID, 0)
	if err != nil {
		t.Fatalf("read the connection log: %v", err)
	}
	if len(conns) != 1 {
		t.Fatalf("the connection log holds %d rows, want the one connection that happened: %+v", len(conns), conns)
	}
	c := conns[0]
	if c.Host != host || c.State != "closed" || c.AgentName != "worker" {
		t.Errorf("the row says host=%q state=%q agent=%q; want %s, closed, worker", c.Host, c.State, c.AgentName, host)
	}
	if c.BytesRecv == 0 {
		t.Errorf("the row counts nothing received, and the origin answered")
	}

	// Granted somewhere else: the gate refuses it, and the row says why. Same
	// gear, same code, different sentence in the grant.
	if _, err := s.gears.SetNetwork(ctx, g.ID, true, []string{"api.example.com"}); err != nil {
		t.Fatalf("regrant: %v", err)
	}
	g, err = s.gears.Get(ctx, g.ID)
	if err != nil {
		t.Fatal(err)
	}
	res, err = s.exec.Run(ctx, g, `{}`, Caller{AgentID: &s.agentID, WorkspaceID: &s.wsID, AgentName: "worker"})
	if err != nil {
		t.Fatalf("run a gear granted elsewhere: %v (stderr: %s)", err, res.Stderr)
	}
	t.Logf("granted elsewhere, the gear said: %s", strings.TrimSpace(res.Stdout))
	if hits.Load() != 1 {
		t.Fatalf("the origin was reached again by a gear granted only api.example.com")
	}
	if conns, err = s.gate.Store().ForGear(ctx, g.ID, 0); err != nil {
		t.Fatal(err)
	}
	if len(conns) != 2 || conns[0].State != "refused_destination" {
		t.Fatalf("the refusal is not on record: %+v", conns)
	}
}

// A new version returns to pending, and so does its network grant. This is the
// rule that already exists for the source, applied to the other half of the
// same decision — and it is worth a test of its own because the failure it
// prevents is silent: a gear that reaches the network on code nobody read.
func TestForgingANewVersionWithdrawsTheNetworkGrant(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	g := f.approve("caller", "echo hello\n")
	if g, err := f.gears.SetNetwork(ctx, g.ID, true, []string{"api.example.com"}); err != nil {
		t.Fatalf("grant: %v", err)
	} else if !g.NetworkGranted {
		t.Fatal("the grant did not stick")
	}

	again, err := f.gears.Forge(ctx, "caller", "a test gear", nil, "bash", "main.sh", "", nil,
		[]File{{Path: "main.sh", Content: "echo hello again\n"}}, f.wsID, f.agentID)
	if err != nil {
		t.Fatalf("forge a new version: %v", err)
	}
	if again.Status != StatusPending {
		t.Errorf("the new version is %q; a new version is always pending", again.Status)
	}
	if again.NetworkGranted || len(again.NetworkHosts) != 0 {
		t.Errorf("the new version kept the network grant (%v, %v); approval covers exact content and so does the grant",
			again.NetworkGranted, again.NetworkHosts)
	}
}

// The gate is not optional for a granted gear: a server built without one
// refuses the run rather than letting it out unrecorded.
func TestAGrantedGearIsRefusedWhenThereIsNoGate(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	g := f.approve("caller", "echo hello\n")
	if _, err := f.gears.SetNetwork(ctx, g.ID, true, nil); err != nil {
		t.Fatalf("grant: %v", err)
	}
	g, err := f.gears.Get(ctx, g.ID)
	if err != nil {
		t.Fatal(err)
	}

	gateless := NewExecutor(f.gears, f.dataDir, nil, testResolver(t, f.db), nil)
	if _, err := gateless.Run(ctx, g, `{}`, Caller{AgentID: &f.agentID, WorkspaceID: &f.wsID}); err == nil {
		t.Fatal("a granted gear ran on a server with no gate, so its traffic would go unrecorded")
	} else if !strings.Contains(err.Error(), "no gate") {
		t.Errorf("the refusal does not say what is missing: %v", err)
	}

	// And nothing was recorded as a run, because nothing ran.
	runs, err := f.gears.ListRuns(ctx, g.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 0 {
		t.Errorf("a run that never happened is in the audit trail: %+v", runs)
	}
}
