//go:build !windows

package gear

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
	isolateProcess(cmd)
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
	found, _ := exec.Command("bash", "-c", "pgrep -f "+marker+" | wc -l").Output()
	if n := strings.TrimSpace(string(found)); n != "0" {
		exec.Command("bash", "-c", "pkill -f "+marker).Run()
		t.Fatalf("%s processes survived the timeout; the group was not killed", n)
	}
}
