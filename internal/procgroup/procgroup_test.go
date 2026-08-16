//go:build !windows

package procgroup

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The defect this guards: a gear that backgrounds anything used to outlive its
// timeout and, worse, block the call that started it forever — exec kills only
// the process it started, and Wait then blocks on output pipes the orphan
// inherited. Measured before the fix: still blocked twelve seconds after a
// two-second timeout, with two processes alive.
//
// Only the unsandboxed path needs this. With Docker the container IS the
// process group and removing it takes everything inside — which is why this
// test drives exec directly, the way the executor's local branch does.
func TestTimeoutStopsTheWholeProcessGroup(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash unavailable")
	}
	dir := t.TempDir()
	script := filepath.Join(dir, "m.sh")
	// The marker makes the survivors findable without matching every sleep on
	// the machine, including other tests running in parallel.
	marker := "cogitorium-procgroup-" + t.Name()
	// exec -a renames argv[0], so the survivors are findable by a string of our
	// own without giving sleep an argument it would reject — an earlier version
	// wrote `sleep 300 MARKER`, which sleep refuses, so the child died instantly
	// and the survivor check passed no matter what the code did.
	body := "#!/bin/bash\n(exec -a " + marker + " sleep 300) &\n(exec -a " + marker + " sleep 300)\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bash", script)
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader("{}")
	var out, errOut bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errOut
	Isolate(cmd)
	cmd.WaitDelay = 2 * time.Second

	done := make(chan struct{})
	start := time.Now()
	go func() {
		_ = cmd.Run()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatalf("the call is still blocked %.0fs after a 1s timeout: the timeout does not time out",
			time.Since(start).Seconds())
	}
	if elapsed := time.Since(start); elapsed > 6*time.Second {
		t.Errorf("returned after %s, which is far past the timeout plus WaitDelay", elapsed)
	}

	// And the work itself must actually be dead, not merely unwaited-for.
	time.Sleep(300 * time.Millisecond)
	if alive := survivors(t, marker); len(alive) != 0 {
		for _, pid := range alive {
			_ = exec.Command("kill", "-9", pid).Run()
		}
		t.Fatalf("%d process(es) survived the timeout (pids %s); the group was not killed",
			len(alive), strings.Join(alive, ", "))
	}
}

// survivors lists the processes still carrying the marker.
//
// It reads ps and filters in Go rather than asking pgrep, because the marker
// must not appear in the argv of the command doing the looking. `pgrep -f M`
// run through a shell puts M in that shell's own command line, and on Linux the
// shell outlives the pipeline long enough to match itself — which reported one
// survivor on every CI run while the group was in fact being killed correctly.
// The bug was in the question, not the answer.
func survivors(t *testing.T, marker string) []string {
	t.Helper()
	// -A -o is spelled the same on Linux and macOS. The marker is argv[0], so
	// the width at which either one truncates a long command line is irrelevant.
	out, err := exec.Command("ps", "-A", "-o", "pid=,args=").Output()
	if err != nil {
		t.Fatalf("ps: %v", err)
	}
	var pids []string
	for _, line := range strings.Split(string(out), "\n") {
		if !strings.Contains(line, marker) {
			continue
		}
		if pid, _, ok := strings.Cut(strings.TrimSpace(line), " "); ok {
			pids = append(pids, pid)
		}
	}
	return pids
}
