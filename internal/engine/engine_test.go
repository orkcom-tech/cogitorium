package engine

import (
	"testing"

	"github.com/orkcom-tech/cogitorium/internal/llm"
)

// repairHistory guards the timeline replay: a workspace whose history
// violates a provider's tool-protocol invariants is rejected on every
// subsequent turn, which previously bricked it permanently. These cases are
// the failure modes that actually occurred.

func userTurn(text string) llm.Turn { return llm.Turn{Role: "user", Text: text} }

func assistantCall(text, id, name, input string) llm.Turn {
	return llm.Turn{Role: "assistant", Text: text, ToolCalls: []llm.ToolCall{{ID: id, Name: name, InputJSON: input}}}
}

func resultTurn(id, name, content string) llm.Turn {
	return llm.Turn{Role: "user", ToolResults: []llm.ToolResult{{CallID: id, Name: name, Content: content}}}
}

// countUnpaired reports calls without a following result and results
// answering no call — the two states providers reject.
func countUnpaired(turns []llm.Turn) (danglingCalls, orphanResults int) {
	for i, t := range turns {
		if len(t.ToolCalls) > 0 {
			var answered map[string]bool
			if i+1 < len(turns) {
				answered = map[string]bool{}
				for _, r := range turns[i+1].ToolResults {
					answered[r.CallID] = true
				}
			}
			for _, c := range t.ToolCalls {
				if !answered[c.ID] {
					danglingCalls++
				}
			}
		}
		if len(t.ToolResults) > 0 {
			called := map[string]bool{}
			if i > 0 {
				for _, c := range turns[i-1].ToolCalls {
					called[c.ID] = true
				}
			}
			for _, r := range t.ToolResults {
				if !called[r.CallID] {
					orphanResults++
				}
			}
		}
	}
	return
}

func TestRepairHistoryPairsInterruptedToolCall(t *testing.T) {
	// The Stop button mid-tool: assistant tool_call persisted, result never was.
	raw := []llm.Turn{
		userTurn("do the thing"),
		assistantCall("", "call_1", "delegate", `{"agent":"worker"}`),
		userTurn("and now this"),
	}

	got := repairHistory(raw)

	if dangling, orphans := countUnpaired(got); dangling != 0 || orphans != 0 {
		t.Fatalf("repaired history still violates the protocol: %d dangling calls, %d orphan results", dangling, orphans)
	}
	var synthesized bool
	for _, turn := range got {
		for _, r := range turn.ToolResults {
			if r.CallID == "call_1" && r.IsError {
				synthesized = true
			}
		}
	}
	if !synthesized {
		t.Error("expected a synthesized error result for the interrupted call")
	}
}

func TestRepairHistoryDropsInvalidCallInput(t *testing.T) {
	// max_tokens truncation mid tool_use leaves unparseable input, which
	// used to fail request construction before any HTTP call.
	raw := []llm.Turn{
		userTurn("go"),
		assistantCall("partial answer", "call_1", "delegate", `{"agent":"wor`),
	}

	got := repairHistory(raw)

	for _, turn := range got {
		for _, c := range turn.ToolCalls {
			t.Fatalf("truncated call %q survived repair", c.InputJSON)
		}
	}
	if len(got) != 2 || got[1].Text != "partial answer" {
		t.Fatalf("expected the assistant text to survive as a plain turn, got %+v", got)
	}
}

func TestRepairHistoryDropsOrphanResults(t *testing.T) {
	// The 500-row window can open mid exchange, cutting off the call.
	raw := []llm.Turn{
		resultTurn("call_ghost", "delegate", "some output"),
		userTurn("continue"),
	}

	got := repairHistory(raw)

	if _, orphans := countUnpaired(got); orphans != 0 {
		t.Fatalf("orphan results survived repair: %d", orphans)
	}
	if len(got) != 1 || got[0].Text != "continue" {
		t.Fatalf("expected history to open at the operator message, got %+v", got)
	}
}

func TestRepairHistoryMergesConsecutiveUserTurns(t *testing.T) {
	// A failed turn leaves operator text followed by operator text;
	// alternation-enforcing providers reject that.
	raw := []llm.Turn{userTurn("first"), userTurn("second")}

	got := repairHistory(raw)

	if len(got) != 1 {
		t.Fatalf("expected consecutive user turns to merge, got %d turns", len(got))
	}
	if got[0].Text != "first\n\nsecond" {
		t.Errorf("merged text = %q", got[0].Text)
	}
}

func TestRepairHistoryStartsAtOperatorMessage(t *testing.T) {
	raw := []llm.Turn{
		{Role: "assistant", Text: "leftover from a cut window"},
		userTurn("the real start"),
	}

	got := repairHistory(raw)

	if len(got) == 0 || got[0].Role != "user" {
		t.Fatalf("history must open with an operator message, got %+v", got)
	}
}

func TestRepairHistoryKeepsValidExchangeIntact(t *testing.T) {
	raw := []llm.Turn{
		userTurn("go"),
		assistantCall("", "call_1", "delegate", `{"agent":"worker"}`),
		resultTurn("call_1", "delegate", "worker said hi"),
		{Role: "assistant", Text: "the worker says hi"},
	}

	got := repairHistory(raw)

	if len(got) != len(raw) {
		t.Fatalf("a valid exchange must pass through unchanged: %d -> %d turns", len(raw), len(got))
	}
	if dangling, orphans := countUnpaired(got); dangling != 0 || orphans != 0 {
		t.Fatalf("valid exchange reported as broken: %d dangling, %d orphans", dangling, orphans)
	}
}
