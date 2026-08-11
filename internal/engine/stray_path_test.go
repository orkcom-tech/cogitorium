package engine

import (
	"context"
	"strings"
	"testing"

	"github.com/orkcom-tech/cogitorium/internal/gear"
)

// The detector behind the refusal: which argument values count as "you meant
// to hand this file over". The refusal itself is asserted through real
// dispatch below, because a detector that works and a gate that fires are two
// different claims.
//
// This is the failure a live run produced, and no test with a scripted model
// could have: the script always passes _files correctly, because whoever wrote
// it knew the protocol. A real model, told a file's path, wrote it into the
// gear's own "archive" argument and passed no _files. Nothing was staged into
// in/, no out/ was collected, and the gear — unsandboxed, holding the server's
// file access — opened the host path anyway, unpacked into a directory nobody
// reads, and printed success. The agent reported that success accurately. The
// answer was right and the work was gone.
//
// The rule is narrow on purpose. It fires on a value naming one of the
// directories files actually arrive in, and nowhere else: a gear taking a URL,
// a glob or a regex is ordinary, and refusing those would be worse than the
// bug. Every "leave it alone" case below is as much the specification as the
// refusals are.
func TestStrayWorkspacePathSpotsAFileNamedInTheWrongArgument(t *testing.T) {
	cases := []struct {
		name  string
		args  string
		stray string
	}{
		{"an inlet payload in the gear's own argument",
			`{"archive":"inlets/drop/4-payload.zip","into":"unpacked"}`,
			"inlets/drop/4-payload.zip"},
		{"a chat attachment",
			`{"file":"attachments/20260812-abc/report.pdf"}`,
			"attachments/20260812-abc/report.pdf"},
		{"another gear's output, which is how a pipeline chains",
			`{"src":"gears/unpack/20260812-xyz/rows.csv"}`,
			"gears/unpack/20260812-xyz/rows.csv"},
		{"the absolute path on this machine, which is what the model actually chose",
			`{"archive":"/private/tmp/x/data/workspaces/1/inlets/drop/4-payload.zip"}`,
			"/private/tmp/x/data/workspaces/1/inlets/drop/4-payload.zip"},
		{"a path that only looks safe until it is cleaned",
			`{"archive":"./inlets/drop/../drop/4-payload.zip"}`,
			"./inlets/drop/../drop/4-payload.zip"},

		{"a url is not a path", `{"url":"https://example.com/inlets/drop/x.zip"}`, ""},
		{"a glob is not a path", `{"pattern":"*.csv"}`, ""},
		{"a regex is not a path", `{"re":"^[a-z]+/[0-9]+$"}`, ""},
		{"a plain string", `{"text":"unpack the archive please"}`, ""},
		{"a number", `{"limit":10}`, ""},
		{"a relative name that is not in a file directory", `{"out":"results/summary.txt"}`, ""},
		{"no arguments at all", `{}`, ""},
		{"not an object", `[1,2,3]`, ""},
		{"not even json", `nonsense`, ""},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := strayWorkspacePath(c.args)
			if got != c.stray {
				if c.stray == "" {
					t.Fatalf("%s was reported as a stray workspace path %q; refusing an ordinary argument is worse than the bug this guards",
						c.args, got)
				}
				t.Fatalf("%s: got %q, want the call refused for naming %q", c.args, got, c.stray)
			}
		})
	}
}

// And the gate fires, through the same dispatch a model's turn goes through.
//
// The detector above proving correct is not the same claim as the call being
// refused: a rule that is computed and not enforced is the shape of every bug
// this file exists for. So this one runs the real path and reads the error.
func TestAGearCallThatNamesAFileButHandsOverNoneIsRefused(t *testing.T) {
	f := newFilesFixture(t)
	ctx := context.Background()

	// A real gear, approved and bound, because the guard sits after the gear is
	// resolved: without one the call would be refused for the wrong reason.
	g, err := f.gears.Forge(ctx, "unpack", "unpacks an archive", nil, "bash", "main.sh", "",
		[]gear.File{{Path: "main.sh", Content: "cat >/dev/null; echo '{\"ok\":true}'"}}, f.wsID, 0)
	if err != nil {
		t.Fatalf("forge: %v", err)
	}
	if _, err := f.gears.SetStatus(ctx, g.ID, gear.StatusApproved); err != nil {
		t.Fatalf("approve: %v", err)
	}
	if _, err := f.gears.Bind(ctx, g.ID, f.wsID, nil); err != nil {
		t.Fatalf("bind: %v", err)
	}

	out, err := f.call("gear_unpack", `{"archive":"inlets/drop/4-payload.zip","into":"unpacked"}`)
	if err == nil {
		t.Fatalf("the call ran with nothing staged; it answered %q — a gear that opens the host path anyway "+
			"and writes where nobody collects is how an answer comes back right with the work gone", out)
	}
	for _, want := range []string{"inlets/drop/4-payload.zip", gearFilesArg} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not mention %q, so the model cannot correct itself: %v", want, err)
		}
	}

	// The same call with the file actually handed over is not refused. Without
	// this the assertion above would pass on a gear that never runs at all.
	if _, err := f.call("gear_unpack",
		`{"_files":["inlets/drop/4-payload.zip"],"archive":"inlets/drop/4-payload.zip"}`); err != nil {
		if strings.Contains(err.Error(), gearFilesArg) {
			t.Errorf("a call that DID hand the file over was refused by the same guard: %v", err)
		}
	}
}
