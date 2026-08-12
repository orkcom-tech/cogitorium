package gear

import (
	"context"
	"errors"
	"io/fs"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// Two runs of one gear must not be able to destroy each other.
//
// The gear directory is rebuilt from the database before every run, and
// rebuilding starts with os.RemoveAll. A run that shared its directory with
// another run therefore had its code deleted out from under it by the second
// run's setup — mid-execution, with no error from either side beyond whatever
// the interpreter says about a file that stopped existing.
//
// This was never hypothetical and never needed two people: prepare's own
// comment already described two agents in two workspaces calling one gear at
// the same moment, and gave a private directory only to the calls that carry
// files. The calls that carry none — which is most of them — shared one.
func TestTwoRunsOfOneGearDoNotDeleteEachOther(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	// The gear writes a file beside itself, waits, then checks it is still
	// there. That is the whole test: a second run rebuilding the same
	// directory begins with os.RemoveAll, so the first run's own scratch file
	// stops existing while it is still running.
	//
	// Checking that the SCRIPT survives would not do — materialize deletes and
	// immediately rewrites it with identical content, so a reader a moment
	// later sees a file that looks untouched. Anything the running gear itself
	// created is not rewritten, and is simply gone.
	g := f.approve("scratch", `#!/bin/sh
d=$(dirname "$0")
echo mine > "$d/scratch-marker"
sleep 0.5
if [ ! -f "$d/scratch-marker" ]; then
  echo "ANOTHER RUN DELETED MY DIRECTORY"
  exit 9
fi
echo survived
`)

	call := func() (Result, error) {
		return f.exec.Run(ctx, g, `{}`, Caller{
			AgentID: &f.agentID, WorkspaceID: &f.wsID, AgentName: "worker",
		})
	}

	var wg sync.WaitGroup
	var first Result
	var firstErr error
	wg.Add(1)
	go func() {
		defer wg.Done()
		first, firstErr = call()
	}()

	// Comfortably inside the first run's sleep, so the second run's setup
	// lands in the middle of it.
	time.Sleep(150 * time.Millisecond)
	second, secondErr := call()
	wg.Wait()

	for _, c := range []struct {
		name string
		res  Result
		err  error
	}{{"the first run", first, firstErr}, {"the second run", second, secondErr}} {
		if c.err != nil {
			t.Fatalf("%s could not run at all: %v (stderr: %s)", c.name, c.err, c.res.Stderr)
		}
		if !strings.Contains(c.res.Stdout, "survived") {
			t.Fatalf("%s did not survive a concurrent run of the same gear\nexit: %d\nstdout: %s\nstderr: %s",
				c.name, c.res.ExitCode, c.res.Stdout, c.res.Stderr)
		}
	}
}

// And a run's directory does not survive it.
//
// This is the half that keeps the fix honest: per-run directories that were
// never removed would turn one shared directory into an unbounded pile of them.
// The empty <name>/ above it is deliberately left — two runs racing to remove a
// shared parent is the very thing this change exists to avoid, and an empty
// directory per gear costs nothing.
func TestAGearRunLeavesNoDirectoryBehind(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	g := f.approve("tidy", "#!/bin/sh\necho tidy\n")
	if _, err := f.exec.Run(ctx, g, `{}`, Caller{
		AgentID: &f.agentID, WorkspaceID: &f.wsID, AgentName: "worker",
	}); err != nil {
		t.Fatalf("run: %v", err)
	}

	var runs []string
	for _, d := range leftovers(t, f.exec.baseDir) {
		if strings.Contains(filepath.Base(d), ".run-") {
			runs = append(runs, d)
		}
	}
	if len(runs) != 0 {
		t.Fatalf("the run left its directory behind: %v", runs)
	}
}

// leftovers lists every directory under the gear base, so a test can say
// "nothing survived" without knowing the naming scheme.
func leftovers(t *testing.T, base string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(base, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if d.IsDir() && p != base {
			out = append(out, p)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", base, err)
	}
	return out
}
