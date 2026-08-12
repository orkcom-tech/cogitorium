package server

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/orkcom-tech/cogitorium/internal/engine"
	"github.com/orkcom-tech/cogitorium/internal/work"
)

// An operator's own turn leaves the same record a delivery does, and every tool
// call in it names the agent that made it.
//
// Both halves were missing and they are the same omission. The record exists to
// answer "what actually happened", because a model's prose is not evidence — a
// 3b model once claimed to have run a gear having made no tool calls at all. But
// the record was built only for deliveries, so a person developing a workspace
// in the chat — which is how every workspace is built — got nothing; and it
// named tools without naming who called them, in a product whose whole premise
// is a different model per agent. execToolAs had the agent's name in scope and
// was logging it while dropping it from the record.
func TestAnOperatorsTurnLeavesARecordThatNamesTheAgent(t *testing.T) {
	d := newDoor(t)
	ctx := context.Background()

	// One tool call, then an answer: the smallest turn that has anything to
	// attribute. agent_list touches nothing and cannot fail for reasons of its
	// own, so a failure here is this test's subject and not its fixture.
	d.provider.answers(func(n int, c modelCall) modelReply {
		if n == 1 {
			return asksFor(modelToolCall{ID: "call_1", Name: "agent_list", Args: `{}`})
		}
		return says("done")
	})

	var done *engine.Record
	err := d.srv.engine.HandleUserMessage(ctx, d.wsID, "who is here?", func(ev engine.Event) {
		if ev.Type == "done" {
			done = ev.Did
		}
	})
	if err != nil {
		t.Fatalf("operator turn: %v", err)
	}

	if done == nil {
		t.Fatal("the turn ended without a record, so an operator building a workspace is told nothing about what it did")
	}
	if done.ModelCalls != 2 {
		t.Fatalf("the record counted %d model calls, want 2 — it is not being filled from the engine's own bookkeeping", done.ModelCalls)
	}
	if len(done.Tools) != 1 {
		t.Fatalf("the record holds %d tool calls, want 1: %+v", len(done.Tools), done.Tools)
	}

	tool := done.Tools[0]
	if tool.Name != "agent_list" || !tool.OK {
		t.Fatalf("wrong tool recorded: %+v", tool)
	}
	orch, err := d.spaces.GetAgentByName(ctx, d.wsID, "orchestrator")
	if err != nil {
		t.Fatalf("get orchestrator: %v", err)
	}
	if tool.Agent != orch.Name {
		t.Fatalf("the record does not say which agent made the call: agent=%q, want %q", tool.Agent, orch.Name)
	}
	// The orchestrator is where the turn started, so nothing was delegated.
	if tool.Depth != 0 {
		t.Fatalf("the orchestrator's own call was recorded at delegation depth %d, want 0", tool.Depth)
	}
}

// And a delegated call is attributed to the agent that made it, one level down —
// which is the case the attribution exists for. A record where every call is
// credited to the orchestrator would look correct and mean nothing.
func TestADelegatedToolCallIsAttributedToTheWorker(t *testing.T) {
	d := newDoor(t)
	ctx := context.Background()

	orch := d.agent(t, "orchestrator")
	worker, err := d.spaces.CreateAgent(ctx, d.wsID, "worker", "does the work", d.modelID)
	if err != nil {
		t.Fatalf("create worker: %v", err)
	}
	// The wire IS the capability: without it the delegation is refused and this
	// test would measure a refusal rather than an attribution.
	if _, err := d.spaces.CreateWire(ctx, d.wsID, orch.ID, worker.ID, "delegates"); err != nil {
		t.Fatalf("wire: %v", err)
	}

	d.provider.answers(func(n int, c modelCall) modelReply {
		switch n {
		case 1:
			return asksFor(modelToolCall{ID: "d1", Name: "delegate",
				Args: `{"agent":"worker","task":"list the agents"}`})
		case 2:
			// The worker's own turn.
			return asksFor(modelToolCall{ID: "w1", Name: "agent_list", Args: `{}`})
		case 3:
			return says("listed")
		}
		return says("done")
	})

	var done *engine.Record
	if err = d.srv.engine.HandleUserMessage(ctx, d.wsID, "ask the worker", func(ev engine.Event) {
		if ev.Type == "done" {
			done = ev.Did
		}
	}); err != nil {
		t.Fatalf("operator turn: %v", err)
	}
	if done == nil {
		t.Fatal("no record")
	}

	var found *engine.ToolRun
	for i := range done.Tools {
		if done.Tools[i].Name == "agent_list" {
			found = &done.Tools[i]
		}
	}
	if found == nil {
		t.Fatalf("the worker's tool call is not in the record at all: %+v", done.Tools)
	}
	if found.Agent != worker.Name {
		t.Fatalf("a call made by %q is recorded against %q", worker.Name, found.Agent)
	}
	if found.Depth != 1 {
		t.Fatalf("a delegated call is recorded at depth %d, want 1 — the record cannot be read as a tree", found.Depth)
	}
}

// An operator's turn holds the same lane a delivery queues behind, and gives it
// back when the turn ends — including when the turn fails.
//
// Two latches that could not see each other would let a chat turn and a
// delivery run at once in one workspace, and the turn state they would share
// holds the egress budget, both anti-worm taint latches and the run record, all
// keyed by workspace. A lane left claimed by a turn that is over is worse still:
// that workspace would never accept another delivery for as long as the process
// lives.
func TestAnOperatorsTurnTakesAndReleasesTheWorkspaceLane(t *testing.T) {
	d := newDoor(t)
	ctx := context.Background()
	lanes := work.NewStore(d.db)

	held := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	d.provider.answers(func(n int, c modelCall) modelReply {
		once.Do(func() { close(held) })
		<-release
		return says("done")
	})

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = d.srv.engine.HandleUserMessage(ctx, d.wsID, "hold it", func(engine.Event) {})
	}()
	<-held

	if _, claimed, err := lanes.Depth(ctx, work.Lane(d.wsID)); err != nil || claimed != 1 {
		t.Fatalf("a running operator turn holds %d claimed units (err %v), want 1 — a delivery would run beside it", claimed, err)
	}

	close(release)
	wg.Wait()

	waitFor(t, func() bool {
		_, claimed, err := lanes.Depth(ctx, work.Lane(d.wsID))
		return err == nil && claimed == 0
	}, "the lane to be released when the turn ended")
}

func waitFor(t *testing.T, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}
