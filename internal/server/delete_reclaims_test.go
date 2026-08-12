package server

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/orkcom-tech/cogitorium/internal/workdir"
)

// Deleting a workspace deletes its files and its unreachable rows.
//
// It used to delete neither. DeleteWorkspace was a single DELETE, so the agents,
// wires and timeline cascaded and everything else stayed: the whole directory on
// disk — every delivered file, every gear output, every attachment — plus the
// rows in the three tables whose workspace_id deliberately carries no foreign
// key, because an audit row should outlive what it audits. That reasoning holds
// while the workspace exists and stops the moment it does not; what is left is
// rows nobody can reach, keyed to an id SQLite will hand out again.
//
// On one ReadWriteOnce volume this is how a disk fills with the contents of
// workspaces nobody can open.
func TestDeletingAWorkspaceReclaimsItsDiskAndItsRows(t *testing.T) {
	d := newDoor(t)
	ctx := context.Background()

	// A delivery, so the workspace owns both a file on disk and a ledger row in
	// a table that does not cascade. What the model answers does not matter —
	// the row is written either way, which is the point of the ledger.
	d.addJSONTask(t, "triage", `{"type":"object"}`, "orchestrator", "triage it")
	d.provider.answers(func(n int, c modelCall) modelReply { return says("triaged") })
	rec := d.deliver(t, "triage", d.key, "application/json", []byte(`{"id":7}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("setup delivery: %d\n%s", rec.Code, rec.Body.String())
	}

	dir := workdir.Dir(d.dir, d.wsID)
	if dir == "" {
		t.Fatal("the workspace has no directory, so this test would prove nothing")
	}
	marker := filepath.Join(dir, "report.md")
	if err := os.WriteFile(marker, []byte("work"), 0o600); err != nil {
		t.Fatalf("write a workspace file: %v", err)
	}

	var runsBefore int
	if err := d.db.QueryRow(`SELECT COUNT(*) FROM inlet_runs WHERE workspace_id = ?`, d.wsID).Scan(&runsBefore); err != nil {
		t.Fatalf("count runs: %v", err)
	}
	if runsBefore == 0 {
		t.Fatal("the delivery left no ledger row, so the row half of this test measures nothing")
	}

	if err := d.spaces.DeleteWorkspace(ctx, d.wsID); err != nil {
		t.Fatalf("delete workspace: %v", err)
	}
	// The store deletes rows; the handler removes the directory. Both halves
	// are asserted, and the second is called the way the handler calls it.
	if err := workdir.Remove(d.dir, d.wsID); err != nil {
		t.Fatalf("remove workspace files: %v", err)
	}

	for _, table := range []string{"inlet_runs", "search_queries", "gear_connections"} {
		var n int
		if err := d.db.QueryRow(`SELECT COUNT(*) FROM `+table+` WHERE workspace_id = ?`, d.wsID).Scan(&n); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if n != 0 {
			t.Fatalf("%d row(s) survived in %s, unreachable and keyed to an id that will be reused", n, table)
		}
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("the workspace's files are still on disk after it was deleted: %v", err)
	}
}

// workdir.Remove must not be written in terms of workdir.Dir.
//
// Dir CREATES the directory as a side effect of naming it, and returns "" when
// it cannot — and os.RemoveAll("") is a no-op that returns nil. A Remove built
// on Dir therefore reports success on exactly the installs where it removed
// nothing. This wedges MkdirAll by putting a FILE where the workspaces
// directory belongs, which is the cheapest way to make the failing branch
// actually run.
func TestRemovingAWorkspaceDirectoryDoesNotReportSuccessWithoutRemovingIt(t *testing.T) {
	base := t.TempDir()
	blocked := filepath.Join(base, "workspaces")
	if err := os.WriteFile(blocked, []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("stage the blocked path: %v", err)
	}

	err := workdir.Remove(base, 42)
	if err == nil {
		t.Fatal("removing a workspace directory succeeded while the path it lives under is not a directory — " +
			"a caller is being told the disk was reclaimed when nothing was touched")
	}

	// And the obstruction is still there: it reported failure and did not
	// quietly delete something else on the way.
	if _, statErr := os.Stat(blocked); statErr != nil {
		t.Fatalf("the blocking file is gone: %v", statErr)
	}
}
