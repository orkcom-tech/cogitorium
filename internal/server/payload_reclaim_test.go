package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/orkcom-tech/cogitorium/internal/engine"
	"github.com/orkcom-tech/cogitorium/internal/workdir"
)

// A file delivery whose run never began leaves nothing on disk.
//
// The bytes land when the delivery is accepted, and the run happens later, in a
// worker. Between those two moments the workspace can change under it — and the
// case driven here is the one that survives every guard at the door: the task's
// agent is DELETED while its delivery waits its turn. The run then never begins,
// and without the reclaim its file sits on the volume with nothing that will
// ever come back for it.
//
// The queue is what makes this testable without racing. The delivery is put
// behind a chat turn that holds the lane, the agent is deleted while it waits,
// and the lane is then released — which is a sequence, not a coincidence of
// timing.
//
// An earlier version of this test used an agent with no model. That route is
// now refused at the door, before the body is read, so it never lands a file:
// the test would have passed while measuring nothing, which is exactly how its
// FIRST version failed — it drove the busy race and passed with the fix
// reverted.
func TestADeliveryThatNeverRanLeavesNoFileBehind(t *testing.T) {
	d := newDoor(t)
	ctx := context.Background()

	worker, err := d.spaces.CreateAgent(ctx, d.wsID, "ingester", "reads what arrives", d.modelID)
	if err != nil {
		t.Fatalf("create the target agent: %v", err)
	}
	d.addFileTask(t, "ingest", "text/plain", worker.Name, "read it")

	// Hold the lane so the delivery is certainly queued rather than run.
	held := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	d.provider.answers(func(n int, c modelCall) modelReply {
		once.Do(func() { close(held) })
		<-release
		return says("held")
	})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = d.srv.engine.HandleUserMessage(ctx, d.wsID, "hold the lane", func(engine.Event) {})
	}()
	<-held

	delivered := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		delivered <- d.deliver(t, "ingest", d.key, "text/plain", []byte("the bytes nobody will read"))
	}()

	// The delivery is now waiting with its bytes on disk. Take its agent away.
	waitFor(t, func() bool {
		var n int
		_ = d.db.QueryRow(`SELECT COUNT(*) FROM inlet_runs WHERE workspace_id = ? AND state = 'queued'`,
			d.wsID).Scan(&n)
		return n == 1
	}, "the delivery to be queued")
	if err := d.spaces.DeleteAgent(ctx, worker.ID); err != nil {
		t.Fatalf("delete the target agent: %v", err)
	}

	close(release)
	wg.Wait()
	rec := <-delivered
	if rec.Code == http.StatusOK {
		t.Fatalf("the delivery ran against an agent that was deleted: %s", rec.Body.String())
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
