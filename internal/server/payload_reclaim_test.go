package server

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/orkcom-tech/cogitorium/internal/workdir"
	"github.com/orkcom-tech/cogitorium/internal/workspace"
)

// A file delivery whose run never began leaves nothing on disk.
//
// The case that produced this was the busy race: the busy check happens before
// the body is read, deliberately — eight concurrent 32 MiB deliveries into a
// busy workspace once took the heap from 2 MiB to 592 MiB — but it is advisory,
// the authoritative check is the lock inside the engine, and a workspace can
// become busy in the gap. By then the bytes have landed. The caller is told to
// retry, they do, and a second copy lands under a new run number while the first
// sits on the volume with nothing that will ever come back for it.
//
// Driving that race from a test is not worth the flakiness, and it is not the
// only way in: RunUnattended refuses an agent with no model, and an agent that
// is gone, on the lines directly above the same lock. All three reach one branch
// through neverBegan, so proving it deterministically with one proves the
// branch. The first attempt at this test drove the race instead, and passed with
// the fix REVERTED — it was measuring the pre-check, which refuses before the
// body is read and therefore never had a file to leave behind.
func TestADeliveryThatNeverRanLeavesNoFileBehind(t *testing.T) {
	d := newDoor(t)

	// An agent with no model bound. RunUnattended refuses on exactly the line
	// it refuses a busy workspace on — above its own lock, before any model is
	// called — so this is the same orphan by a route a test can drive without
	// racing. The busy case reaches the identical branch through neverBegan.
	if _, err := d.spaces.CreateAgentSpec(context.Background(), d.wsID, workspace.AgentSpec{
		Name: "unbound", Role: "has no model yet",
	}); err != nil {
		t.Fatalf("create an agent with no model: %v", err)
	}
	d.addFileTask(t, "ingest", "text/plain", "unbound", "read it")

	rec := d.deliver(t, "ingest", d.key, "text/plain", []byte("the bytes nobody will read"))
	if rec.Code == http.StatusOK {
		t.Fatalf("the delivery ran, so this test measures nothing: %s", rec.Body.String())
	}

	// Nothing under the inlet directory: this refused delivery is the only one
	// that has happened, so anything here is its orphan.
	root := workdir.Dir(d.dir, d.wsID)
	var left []string
	_ = filepath.Walk(filepath.Join(root, workdir.InletDir), func(p string, fi os.FileInfo, err error) error {
		if err == nil && fi != nil && !fi.IsDir() {
			left = append(left, p)
		}
		return nil
	})
	if len(left) != 0 {
		t.Fatalf("a delivery that never ran left its bytes on the volume: %v", left)
	}

	// And the ledger does not point at a file that is gone. A row naming
	// something that no longer exists is worse than either half alone.
	var path string
	if err := d.db.QueryRow(`SELECT payload_path FROM inlet_runs WHERE workspace_id = ? ORDER BY id DESC LIMIT 1`,
		d.wsID).Scan(&path); err != nil {
		t.Fatalf("read the ledger row: %v", err)
	}
	if path != "" {
		t.Fatalf("the ledger still names the payload it deleted: %q", path)
	}
}

// The control: a delivery that RUNS keeps its file, because the agent was given
// the path and the operator may want to see what arrived. A reclaim that fired
// on every failure would quietly delete evidence.
func TestADeliveryThatRunsKeepsItsFile(t *testing.T) {
	d := newDoor(t)

	d.addFileTask(t, "ingest", "text/plain", "orchestrator", "read it")
	d.provider.answers(func(n int, c modelCall) modelReply { return says("read it") })

	rec := d.deliver(t, "ingest", d.key, "text/plain", []byte("keep me"))
	if rec.Code != http.StatusOK {
		t.Fatalf("delivery: %d\n%s", rec.Code, rec.Body.String())
	}

	var rel string
	if err := d.db.QueryRow(`SELECT payload_path FROM inlet_runs WHERE workspace_id = ? ORDER BY id DESC LIMIT 1`,
		d.wsID).Scan(&rel); err != nil {
		t.Fatalf("read the ledger row: %v", err)
	}
	if rel == "" {
		t.Fatal("a delivery that ran has no payload path on its row, so this test cannot tell whether the file was kept")
	}
	if _, err := os.Stat(filepath.Join(workdir.Dir(d.dir, d.wsID), filepath.FromSlash(rel))); err != nil {
		t.Fatalf("the file a delivery ran on was removed: %v", err)
	}
}
