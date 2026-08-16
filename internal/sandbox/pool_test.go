package sandbox

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Warm containers, against a real daemon.
//
// Two claims, and they pull in opposite directions, which is why both are
// checked: a pooled run really does land in a container that has already run
// something — otherwise the feature buys nothing — and a run that was given
// anything worth protecting really does not.

func poolDocker(t *testing.T) *Docker {
	t.Helper()
	d := NewDocker("", "")
	if d == nil || !d.Available(context.Background()) {
		t.Skip("docker does not answer; warm containers cannot be exercised here")
	}
	return d
}

// payload writes a one-line shell gear and returns its directory.
func payloadDir(t *testing.T, script string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.sh"), []byte(script), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// The point of the feature: the second run is in the same machine as the first.
//
// Proved by the container's own boot id rather than by anything this package
// reports about itself — a counter incremented in Go would pass whether or not
// a container was ever reused.
func TestASecondPooledRunLandsInTheSameContainer(t *testing.T) {
	d := poolDocker(t)
	d.SetPool(2)
	t.Cleanup(d.Close)

	// The container's own hostname, which Docker sets to its id. NOT the boot
	// id: /proc/sys/kernel/random/boot_id is the HOST's, identical in every
	// container on the machine — the first version of this test used it and
	// passed whether or not anything was ever reused.
	script := `hostname`
	var seen []string
	for range 2 {
		res, err := d.Run(context.Background(), Spec{
			Dir: payloadDir(t, script), Command: "sh", Args: []string{"main.sh"},
			TimeoutSeconds: 30, Reusable: true, Writable: true,
		})
		if err != nil {
			t.Fatalf("run: %v (stderr %s)", err, res.Stderr)
		}
		seen = append(seen, strings.TrimSpace(res.Stdout))
	}
	if seen[0] == "" {
		t.Fatal("the run reported nothing, so nothing can be said about which container it was in")
	}
	if seen[0] != seen[1] {
		t.Fatalf("two pooled runs were in different containers (%q, %q), so the pool handed out nothing "+
			"and the feature costs isolation for no latency at all", seen[0], seen[1])
	}
}

// And the boundary: a run that says it is not reusable gets a machine of its
// own, whatever the pool holds.
func TestARunThatIsNotReusableGetsItsOwnContainer(t *testing.T) {
	d := poolDocker(t)
	d.SetPool(2)
	t.Cleanup(d.Close)

	// The container's own hostname, which Docker sets to its id. NOT the boot
	// id: /proc/sys/kernel/random/boot_id is the HOST's, identical in every
	// container on the machine — the first version of this test used it and
	// passed whether or not anything was ever reused.
	script := `hostname`
	warm, err := d.Run(context.Background(), Spec{
		Dir: payloadDir(t, script), Command: "sh", Args: []string{"main.sh"},
		TimeoutSeconds: 30, Reusable: true, Writable: true,
	})
	if err != nil {
		t.Fatalf("the warming run: %v", err)
	}
	cold, err := d.Run(context.Background(), Spec{
		Dir: payloadDir(t, script), Command: "sh", Args: []string{"main.sh"},
		TimeoutSeconds: 30, Reusable: false, Writable: true,
	})
	if err != nil {
		t.Fatalf("the run that asked not to be pooled: %v", err)
	}
	if strings.TrimSpace(warm.Stdout) == strings.TrimSpace(cold.Stdout) {
		t.Fatal("a run that said it was not reusable was given a container that had already run something — " +
			"which is the case the flag exists for: it is the run holding a credential")
	}
}

// A pooled run must not find the previous one's code. The payload is the one
// thing that is destroyed between runs, and it is what a gear would read.
func TestAPooledRunCannotSeeThePreviousRunsPayload(t *testing.T) {
	d := poolDocker(t)
	d.SetPool(1)
	t.Cleanup(d.Close)

	first := payloadDir(t, "echo first")
	if err := os.WriteFile(filepath.Join(first, "secret-notes.txt"), []byte("the first run's file"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Run(context.Background(), Spec{
		Dir: first, Command: "sh", Args: []string{"main.sh"},
		TimeoutSeconds: 30, Reusable: true, Writable: true,
	}); err != nil {
		t.Fatalf("the first run: %v", err)
	}

	res, err := d.Run(context.Background(), Spec{
		Dir:            payloadDir(t, `ls -a . | tr '\n' ' '`),
		Command:        "sh",
		Args:           []string{"main.sh"},
		TimeoutSeconds: 30, Reusable: true, Writable: true,
	})
	if err != nil {
		t.Fatalf("the second run: %v", err)
	}
	if strings.Contains(res.Stdout, "secret-notes.txt") {
		t.Fatalf("the second run can read the first run's files: %s", res.Stdout)
	}
	if !strings.Contains(res.Stdout, "main.sh") {
		t.Fatalf("the second run cannot see its own payload either: %s", res.Stdout)
	}
}

// A run that timed out leaves its process behind, so its container must not go
// back into the pool.
func TestATimedOutRunRetiresItsContainer(t *testing.T) {
	d := poolDocker(t)
	d.SetPool(2)
	t.Cleanup(d.Close)

	res, err := d.Run(context.Background(), Spec{
		Dir: payloadDir(t, "sleep 30"), Command: "sh", Args: []string{"main.sh"},
		TimeoutSeconds: 2, Reusable: true, Writable: true,
	})
	if err == nil || !res.TimedOut {
		t.Fatalf("the run was expected to time out: %v %+v", err, res)
	}
	if n := len(d.pool.idle); n != 0 {
		t.Fatalf("a timed-out run left %d container(s) in the pool, and whatever timed out is still in there", n)
	}
}

// Pooling off is the default, and off has to mean off rather than "a pool of
// one" — every run getting a machine with no history is what this package
// otherwise promises.
func TestPoolingIsOffUnlessAskedFor(t *testing.T) {
	d := NewDocker("", "")
	if d == nil {
		t.Skip("no docker binary")
	}
	if d.pool != nil {
		t.Fatal("a new Docker runner came with a pool nobody asked for")
	}
	if d.warmable(Spec{Reusable: true, Writable: true}) {
		t.Fatal("a run would be pooled on an install that never enabled pooling")
	}
	d.SetPool(1)
	if !d.warmable(Spec{Reusable: true, Writable: true}) {
		t.Fatal("pooling was enabled and a reusable run still would not use it")
	}
	// A read-only payload is root-owned, and removing it between runs would
	// need a root that can override file permissions — a capability this
	// container does not have and must not be given to save a few hundred
	// milliseconds.
	if d.warmable(Spec{Reusable: true, Writable: false}) {
		t.Fatal("a run with a read-only payload would be pooled, and its code could not then be cleared out")
	}
	// A granted run can never be pooled: a container's network mode is fixed
	// when it is created, so a pooled one cannot become a granted one.
	if d.warmable(Spec{Reusable: true, Writable: true, Network: true}) {
		t.Fatal("a run granted the network would be given a container created with --network none")
	}
}

// Closing the server takes the pool with it. Containers left running are a
// machine's memory held by a process that has exited.
func TestClosingRetiresEveryPooledContainer(t *testing.T) {
	d := poolDocker(t)
	d.SetPool(2)

	if _, err := d.Run(context.Background(), Spec{
		Dir: payloadDir(t, "echo hi"), Command: "sh", Args: []string{"main.sh"},
		TimeoutSeconds: 30, Reusable: true, Writable: true,
	}); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(d.pool.idle) != 1 {
		t.Fatalf("the pool holds %d containers after one reusable run", len(d.pool.idle))
	}
	id := d.pool.idle[0].id
	d.Close()
	if len(d.pool.idle) != 0 {
		t.Fatal("Close left containers in the pool")
	}
	if err := exec.Command("docker", "inspect", id).Run(); err == nil {
		t.Fatalf("container %s is still on this machine after the pool was closed", id)
	}
}
