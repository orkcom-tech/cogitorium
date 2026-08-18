package server

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/orkcom-tech/cogitorium/internal/gear"
	"github.com/orkcom-tech/cogitorium/internal/schedule"
	"github.com/orkcom-tech/cogitorium/internal/workspace"
)

// A clock that dials an agent or a gear, rather than only an inlet task.
//
// The subject here is not "does a row store". It is that the direct paths went
// through the SAME machinery the task path does — one ledger row, one queued
// unit, one lane — because a direct schedule that skipped any of it would be a
// second, weaker way to run work, and the weaker one is the one nobody watches.

// due brings a schedule forward so a test can reach tomorrow without waiting.
func (d *door) due(t *testing.T, id int64) {
	t.Helper()
	if _, err := d.db.Exec(`UPDATE schedules SET next_at = ? WHERE id = ?`,
		time.Now().UTC().Add(-time.Minute).Format(time.RFC3339), id); err != nil {
		t.Fatalf("bring schedule %d forward: %v", id, err)
	}
}

func TestAScheduleCanDialAnAgentWithNoTaskInTheWay(t *testing.T) {
	d := newDoor(t)
	ctx := context.Background()
	agent := d.agent(t, "orchestrator")
	d.provider.answers(func(n int, c modelCall) modelReply { return says("swept") })

	sc, err := d.srv.schedules.Create(ctx, schedule.Schedule{
		WorkspaceID: d.wsID, TargetKind: schedule.TargetAgent, TargetAgentID: &agent.ID,
		Instruction: "sweep yesterday's tickets", Name: "nightly", Spec: "every 1m",
	})
	if err != nil {
		t.Fatalf("create an agent schedule: %v", err)
	}
	d.due(t, sc.ID)
	d.srv.tick(ctx)

	waitFor(t, func() bool {
		var n int
		_ = d.db.QueryRow(`SELECT COUNT(*) FROM inlet_runs WHERE workspace_id = ? AND state = 'completed'`,
			d.wsID).Scan(&n)
		return n == 1
	}, "the agent schedule to run")

	// The ledger row is the point: a direct firing must be as answerable as a
	// delivered one.
	var inletID *int64
	var address, taskName, agentName string
	if err := d.db.QueryRow(
		`SELECT inlet_id, inlet_address, task_name, agent_name FROM inlet_runs WHERE workspace_id = ?`,
		d.wsID).Scan(&inletID, &address, &taskName, &agentName); err != nil {
		t.Fatalf("read the ledger row: %v", err)
	}
	if inletID != nil {
		t.Fatalf("a clock firing recorded inlet_id %d; it went through no door at all", *inletID)
	}
	if address != clockAddress {
		t.Fatalf("the ledger says the run came from %q; a reader cannot tell what started it", address)
	}
	if taskName != "nightly" || agentName != agent.Name {
		t.Fatalf("the ledger names task %q agent %q", taskName, agentName)
	}
}

// The instruction reaches the model AS ITSELF. A task's payload is fenced as
// untrusted because a caller outside the workspace wrote it; this sentence was
// typed by an operator into this install, and fencing it would be telling the
// agent to ignore the only thing it was given.
func TestAnAgentScheduleSendsTheInstructionUnfenced(t *testing.T) {
	d := newDoor(t)
	ctx := context.Background()
	agent := d.agent(t, "orchestrator")

	var seen string
	d.provider.answers(func(n int, c modelCall) modelReply {
		if seen == "" {
			// Raw, not a decoded field: the claim is about what actually went
			// on the wire, including what is NOT in it.
			seen = c.Raw
		}
		return says("ok")
	})

	sc, err := d.srv.schedules.Create(ctx, schedule.Schedule{
		WorkspaceID: d.wsID, TargetKind: schedule.TargetAgent, TargetAgentID: &agent.ID,
		Instruction: "sweep yesterday's tickets", Name: "nightly", Spec: "every 1m",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	d.due(t, sc.ID)
	d.srv.tick(ctx)
	waitFor(t, func() bool { return seen != "" }, "the agent to be given its turn")

	if !strings.Contains(seen, "sweep yesterday's tickets") {
		t.Fatalf("the agent was not told what the schedule says. It got:\n%s", seen)
	}
	if strings.Contains(seen, "untrusted") {
		t.Fatalf("an operator's own instruction was fenced as untrusted payload:\n%s", seen)
	}
}

// An agent schedule with nothing to say is refused where the operator can still
// read the error, not at 03:00 forever.
func TestAnAgentScheduleNeedsSomethingToSay(t *testing.T) {
	d := newDoor(t)
	agent := d.agent(t, "orchestrator")

	_, err := d.srv.schedules.Create(context.Background(), schedule.Schedule{
		WorkspaceID: d.wsID, TargetKind: schedule.TargetAgent, TargetAgentID: &agent.ID,
		Name: "empty", Spec: "every 1m",
	})
	if err == nil {
		t.Fatal("a schedule with no instruction was accepted; every firing would be an empty prompt")
	}
}

// THE ONE THAT MATTERS MOST. A gear schedule is unattended execution of code by
// somebody who did not approve it, so creating one is an administrator's act —
// the same rule granting an MCP server follows.
func TestOnlyAnAdministratorMayScheduleAGear(t *testing.T) {
	d := newDoor(t)
	ctx := t.Context()

	// A member who genuinely REACHES this workspace, which is the whole point:
	// somebody refused for having no access proves nothing about the gear gate.
	// They are given a team and the workspace shared with it, so the only thing
	// left standing between them and an unattended gear run is the admin check.
	member, memberTok, err := d.users.CreateUser(ctx, "member-"+t.Name(), "member", "")
	if err != nil {
		t.Fatalf("create a member: %v", err)
	}
	team, err := d.users.CreateTeam(ctx, "schedulers-"+t.Name())
	if err != nil {
		t.Fatalf("create a team: %v", err)
	}
	if err := d.users.AddTeamMember(ctx, team.ID, member.ID); err != nil {
		t.Fatalf("add the member to the team: %v", err)
	}
	if _, err := d.srv.workspaces.ShareWith(ctx, d.wsID, team.ID); err != nil {
		t.Fatalf("share the workspace: %v", err)
	}

	// Proof the access is real: an agent schedule, which is workspace work, is
	// allowed for the same caller on the same workspace.
	agent := d.agent(t, workspace.OrchestratorName)
	rec := d.request(t, http.MethodPost, "/api/v1/workspaces/"+id(d.wsID)+"/schedules", memberTok,
		`{"target_kind":"agent","target_agent_id":`+id(agent.ID)+`,"instruction":"sweep","name":"sweep","spec":"every 1m"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("the member cannot reach this workspace at all, so the gear check below proves nothing: %d %s",
			rec.Code, rec.Body.String())
	}

	// And the gear one is refused for being a gear.
	rec = d.request(t, http.MethodPost, "/api/v1/workspaces/"+id(d.wsID)+"/schedules", memberTok,
		`{"target_kind":"gear","target_gear_id":1,"name":"nightly","spec":"every 1m"}`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("a member scheduled a gear — unattended execution of code they did not approve: %d %s",
			rec.Code, rec.Body.String())
	}
}

// A clock is the caller, and there is no second gate behind it — so it may only
// point at code somebody read.
func TestAGearScheduleRefusesUnapprovedCode(t *testing.T) {
	d := newDoor(t)
	orch := d.agent(t, workspace.OrchestratorName)
	g, err := d.srv.gears.Forge(t.Context(), "backup", "never approved", nil,
		"python", "main.py", `{"type":"object","properties":{}}`, nil,
		[]gear.File{{Path: "main.py", Content: "print(1)\n"}}, d.wsID, orch.ID)
	if err != nil {
		t.Fatalf("forge: %v", err)
	}
	if g.Status == gear.StatusApproved {
		t.Fatalf("a freshly forged gear is %q, so this test proves nothing", g.Status)
	}

	rec := d.request(t, http.MethodPost, "/api/v1/workspaces/"+id(d.wsID)+"/schedules", d.adminTok,
		`{"target_kind":"gear","target_gear_id":`+id(g.ID)+`,"name":"nightly","spec":"every 1m"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("a pending gear was scheduled: %d %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "approved") {
		t.Fatalf("the refusal does not say why: %s", rec.Body.String())
	}
}

// Deleting an agent must not silently take the nightly job with it. The row
// survives with a null target, reads as broken, and refuses to fire.
func TestDeletingAnAgentBreaksItsScheduleRatherThanDeletingIt(t *testing.T) {
	d := newDoor(t)
	ctx := context.Background()
	agent := d.agent(t, "orchestrator")

	sc, err := d.srv.schedules.Create(ctx, schedule.Schedule{
		WorkspaceID: d.wsID, TargetKind: schedule.TargetAgent, TargetAgentID: &agent.ID,
		Instruction: "sweep", Name: "nightly", Spec: "every 1m",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := d.db.Exec(`DELETE FROM agents WHERE id = ?`, agent.ID); err != nil {
		t.Fatalf("delete the agent: %v", err)
	}

	after, err := d.srv.schedules.Get(ctx, sc.ID)
	if err != nil {
		t.Fatalf("the schedule went with its agent, so nobody can learn why the job stopped: %v", err)
	}
	if !after.Broken() {
		t.Fatalf("a schedule whose agent is gone does not read as broken: %+v", after)
	}

	// And it refuses rather than firing into nothing.
	d.due(t, sc.ID)
	d.srv.tick(ctx)
	again, err := d.srv.schedules.Get(ctx, sc.ID)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if again.LastOutcome != schedule.OutcomeFailed {
		t.Fatalf("a broken schedule fired anyway; outcome %q", again.LastOutcome)
	}
}

// A task schedule still works exactly as it did. The whole argument for adding
// the direct paths was that the task path is right when a job genuinely has a
// door, so breaking it would have defeated the change.
func TestTheTaskPathIsUnchanged(t *testing.T) {
	d := newDoor(t)
	ctx := context.Background()
	task := d.addJSONTask(t, "nightly", `{"type":"object"}`, "orchestrator", "do the nightly thing")
	d.provider.answers(func(n int, c modelCall) modelReply { return says("done") })

	sc, err := d.srv.schedules.Create(ctx, schedule.Schedule{
		WorkspaceID: d.wsID, TaskID: &task.ID, Name: "nightly", Spec: "every 1m", Payload: `{"id":7}`,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if sc.TargetKind != schedule.TargetTask {
		t.Fatalf("a schedule created without a target kind became %q; it must default to task", sc.TargetKind)
	}
	d.due(t, sc.ID)
	d.srv.tick(ctx)

	waitFor(t, func() bool {
		var n int
		_ = d.db.QueryRow(`SELECT COUNT(*) FROM inlet_runs WHERE workspace_id = ? AND state = 'completed'`,
			d.wsID).Scan(&n)
		return n == 1
	}, "the task schedule to run")
}

// ── editing a clock, which used to mean deleting and redrawing it ─────────

// A schedule you cannot correct is one people replace, and a replaced schedule
// has no history. The counters are the point of this test.
func TestEditingAScheduleKeepsWhatItHasDone(t *testing.T) {
	d := newDoor(t)
	ctx := context.Background()
	agent := d.agent(t, workspace.OrchestratorName)
	d.provider.answers(func(n int, c modelCall) modelReply { return says("swept") })

	sc, err := d.srv.schedules.Create(ctx, schedule.Schedule{
		WorkspaceID: d.wsID, TargetKind: schedule.TargetAgent, TargetAgentID: &agent.ID,
		Instruction: "sweep", Name: "nightly", Spec: "every 1m",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	// Give it a history worth keeping.
	d.due(t, sc.ID)
	d.srv.tick(ctx)
	waitFor(t, func() bool {
		after, _ := d.srv.schedules.Get(ctx, sc.ID)
		return after.Fires == 1
	}, "the schedule to fire once")

	rec := d.request(t, http.MethodPut, "/api/v1/schedules/"+id(sc.ID), d.adminTok,
		`{"spec":"0 3 * * 1-5","tz":"Europe/Berlin","instruction":"sweep harder"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("edit: %d %s", rec.Code, rec.Body.String())
	}

	after, err := d.srv.schedules.Get(ctx, sc.ID)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if after.Spec != "0 3 * * 1-5" || after.TZ != "Europe/Berlin" || after.Instruction != "sweep harder" {
		t.Fatalf("the edit did not land: %+v", after)
	}
	if after.Fires != 1 {
		t.Fatalf("editing lost the counters: fires=%d", after.Fires)
	}
	if after.ID != sc.ID {
		t.Fatal("editing produced a different schedule")
	}
	// The next firing is recomputed rather than carried over, or a schedule
	// edited from every-minute to nightly fires once more on the old rule.
	if !after.NextAt.After(time.Now().UTC().Add(time.Hour)) {
		t.Fatalf("next_at was not recomputed from the new spec: %s", after.NextAt)
	}
}

// An omitted field is left alone. An edit that blanked what it did not mention
// would make a partial body dangerous.
func TestAnOmittedFieldSurvivesAnEdit(t *testing.T) {
	d := newDoor(t)
	agent := d.agent(t, workspace.OrchestratorName)
	sc, err := d.srv.schedules.Create(t.Context(), schedule.Schedule{
		WorkspaceID: d.wsID, TargetKind: schedule.TargetAgent, TargetAgentID: &agent.ID,
		Instruction: "sweep", Name: "nightly", Spec: "every 1m", OnMiss: "run",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	rec := d.request(t, http.MethodPut, "/api/v1/schedules/"+id(sc.ID), d.adminTok, `{"spec":"every 5m"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("edit: %d %s", rec.Code, rec.Body.String())
	}
	after, _ := d.srv.schedules.Get(t.Context(), sc.ID)
	if after.Instruction != "sweep" || after.Name != "nightly" || after.OnMiss != "run" {
		t.Fatalf("an omitted field was blanked: %+v", after)
	}
}

// A gear schedule stays an administrator's to change, for the same reason it
// was theirs to create: an edit that moves its spec decides when unattended
// code runs.
func TestOnlyAnAdministratorMayEditAGearSchedule(t *testing.T) {
	d := newDoor(t)
	ctx := t.Context()
	orch := d.agent(t, workspace.OrchestratorName)
	g, err := d.srv.gears.Forge(ctx, "backup", "", nil, "python", "main.py",
		`{"type":"object","properties":{}}`, nil,
		[]gear.File{{Path: "main.py", Content: "print(1)\n"}}, d.wsID, orch.ID)
	if err != nil {
		t.Fatalf("forge: %v", err)
	}
	if _, err := d.srv.gears.SetStatus(ctx, g.ID, gear.StatusApproved, gear.Actor{Name: "the test"}); err != nil {
		t.Fatalf("approve: %v", err)
	}
	sc, err := d.srv.schedules.Create(ctx, schedule.Schedule{
		WorkspaceID: d.wsID, TargetKind: schedule.TargetGear, TargetGearID: &g.ID,
		Name: "nightly-backup", Spec: "every 1m",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	member, memberTok, err := d.users.CreateUser(ctx, "member-"+t.Name(), "member", "")
	if err != nil {
		t.Fatalf("member: %v", err)
	}
	team, err := d.users.CreateTeam(ctx, "t-"+t.Name())
	if err != nil {
		t.Fatalf("team: %v", err)
	}
	if err := d.users.AddTeamMember(ctx, team.ID, member.ID); err != nil {
		t.Fatalf("add: %v", err)
	}
	if _, err := d.srv.workspaces.ShareWith(ctx, d.wsID, team.ID); err != nil {
		t.Fatalf("share: %v", err)
	}

	rec := d.request(t, http.MethodPut, "/api/v1/schedules/"+id(sc.ID), memberTok, `{"spec":"every 5m"}`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("a member moved a gear schedule's clock: %d %s", rec.Code, rec.Body.String())
	}
}
