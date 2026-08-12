package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/orkcom-tech/cogitorium/internal/config"
	"github.com/orkcom-tech/cogitorium/internal/gear"
	"github.com/orkcom-tech/cogitorium/internal/workspace"
)

// What the HTTP layer hands out about a name a gear was given.
//
// internal/gear proves that a secret does not reach the recorded run, the log
// or the operator's live stream. This asks the other question: whether any
// response this server writes carries the value — the interface's own list, the
// approval screen, the execution history, a workspace's overrides, and a bundle
// somebody forwards by email.
//
// It is written as a search over the whole response body rather than as an
// assertion on a field, because the failure it exists to catch is a field
// nobody thought about. Every search runs twice: once for a secret, which must
// be nowhere, and once for a variable of the same shape, which must be
// somewhere. Without the second, a search that had quietly stopped matching
// would pass every case here.

const (
	// The two values, distinct enough that finding either is unambiguous.
	apiSecret   = "sk-live-payroll-4d19b7c2-never-publish-this"
	apiVariable = "https://payroll.example.com/public-endpoint"
	// Long enough for secrets.NewKey; nothing else about it matters.
	apiSecretKey = "a-server-test-key-that-is-long-enough-to-be-accepted"
)

// namedInstall is an install that can hold secrets of its own and runs gears as
// subprocesses, so a real run happens without a Docker daemon.
type namedInstall struct {
	*install
	ownerID  int64
	wsID     int64
	agentID  int64
	gearID   int64
	response map[string]string // every body this test searched, by what it was
}

func newNamedInstall(t *testing.T) *namedInstall {
	t.Helper()
	ctx := context.Background()
	// A nil runner is the unsandboxed subprocess backend — the one an install
	// without Docker runs, and the only way a gear here actually executes.
	in := newInstallWithSandbox(t, offBox, nil, nil, func(c *config.Config) {
		c.SecretKey = apiSecretKey
	})
	n := &namedInstall{install: in, response: map[string]string{}}

	owner, _, err := in.users.CreateUser(ctx, "operator", "member", "")
	if err != nil {
		t.Fatalf("create the workspace owner: %v", err)
	}
	n.ownerID = owner.ID
	provider, err := in.cat.CreateProvider(ctx, "house", "openai-compatible", deadProvider, "sk-the-house-key")
	if err != nil {
		t.Fatalf("create provider: %v", err)
	}
	model, err := in.cat.CreateModel(ctx, provider.ID, "test-model", "")
	if err != nil {
		t.Fatalf("create model: %v", err)
	}
	ws, err := in.spaces.CreateWorkspace(ctx, "payroll", "", model.ID, owner.ID)
	if err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	n.wsID = ws.ID
	agent, err := in.spaces.CreateAgentSpec(ctx, ws.ID, workspace.AgentSpec{
		Name: "worker", Role: "You work.", ModelID: &model.ID,
	})
	if err != nil {
		t.Fatalf("create agent: %v", err)
	}
	n.agentID = agent.ID
	return n
}

// admin sends a request as the administrator and fails on an unexpected status.
func (n *namedInstall) admin(t *testing.T, method, path, body string, want int) string {
	t.Helper()
	rec := n.request(t, method, path, n.adminTok, body)
	if rec.Code != want {
		t.Fatalf("%s %s answered %d, want %d: %s", method, path, rec.Code, want, rec.Body.String())
	}
	return rec.Body.String()
}

// searched records a response under the name of the screen it feeds, so a
// failure says which screen leaked rather than which URL.
func (n *namedInstall) searched(t *testing.T, what, method, path string) {
	t.Helper()
	n.response[what] = n.admin(t, method, path, "", http.StatusOK)
}

// 4, at the HTTP boundary, together with 5.
//
// One install, one secret, one variable, one gear that prints both, and one
// real run. Then every response an operator or an agent can obtain is searched
// for both values.
func TestNoResponseCarriesASecretsValueAndEveryOneCarriesAVariables(t *testing.T) {
	n := newNamedInstall(t)
	ctx := context.Background()

	// Setting them. The response to setting a secret is itself a surface: it is
	// the one moment the server has the value in hand and is writing JSON.
	set := n.admin(t, "PUT", "/api/v1/env/PAYROLL_TOKEN",
		fmt.Sprintf(`{"kind":"secret","value":%q,"description":"the payroll key"}`, apiSecret), http.StatusOK)
	n.response["the answer to setting a secret"] = set
	n.response["the answer to setting a variable"] = n.admin(t, "PUT", "/api/v1/env/PAYROLL_ENDPOINT",
		fmt.Sprintf(`{"kind":"variable","value":%q}`, apiVariable), http.StatusOK)

	// A workspace override, so the workspace-scoped routes have something of
	// their own to withhold rather than passing by default.
	n.response["the answer to setting a workspace override"] = n.admin(t, "PUT",
		fmt.Sprintf("/api/v1/workspaces/%d/env/PAYROLL_TOKEN", n.wsID),
		fmt.Sprintf(`{"kind":"secret","value":%q}`, apiSecret), http.StatusOK)

	// A gear that asks for both and prints both.
	created := n.admin(t, "POST", "/api/v1/gears", `{
		"name": "payroll_reporter",
		"description": "prints what it was given",
		"runtime": "bash",
		"code": "set -u\nprintf 'endpoint %s token %s\\n' \"$PAYROLL_ENDPOINT\" \"$PAYROLL_TOKEN\"\n",
		"env_names": ["PAYROLL_ENDPOINT", "PAYROLL_TOKEN"]
	}`, http.StatusCreated)
	var forged gear.Gear
	if err := json.Unmarshal([]byte(created), &forged); err != nil {
		t.Fatalf("read the forged gear: %v", err)
	}
	n.gearID = forged.ID

	n.admin(t, "PATCH", fmt.Sprintf("/api/v1/gears/%d", n.gearID), `{"status":"approved"}`, http.StatusOK)
	n.admin(t, "POST", fmt.Sprintf("/api/v1/workspaces/%d/gears", n.wsID),
		fmt.Sprintf(`{"gear_id":%d}`, n.gearID), http.StatusCreated)

	// A real run, through the executor this server built, so the execution
	// history below holds something to withhold. The gear runs on behalf of the
	// workspace, so the override above is the value it gets.
	g, err := n.srv.gears.Get(ctx, n.gearID)
	if err != nil {
		t.Fatalf("read the gear back: %v", err)
	}
	res, err := n.srv.gearExec.Run(ctx, g, `{}`, gear.Caller{AgentID: &n.agentID, WorkspaceID: &n.wsID, AgentName: "worker"})
	if err != nil {
		t.Fatalf("run the gear: %v (stderr: %s)", err, res.Stderr)
	}
	// The vacuity guard for the run itself: the gear was given both values, and
	// printed both. If it had printed neither, every absence below would be an
	// absence of nothing.
	if !strings.Contains(res.Stdout, apiVariable) {
		t.Fatalf("the gear did not print the variable it was given, so nothing below is being tested: %q", res.Stdout)
	}

	// Every screen, by name.
	n.searched(t, "the install-wide Variables page", "GET", "/api/v1/env")
	n.searched(t, "the workspace's Variables panel", "GET", fmt.Sprintf("/api/v1/workspaces/%d/env", n.wsID))
	n.searched(t, "the gear catalog", "GET", "/api/v1/gears")
	n.searched(t, "the approval screen", "GET", fmt.Sprintf("/api/v1/gears/%d", n.gearID))
	n.searched(t, "the execution history", "GET", fmt.Sprintf("/api/v1/gears/%d/runs", n.gearID))
	n.searched(t, "the connection log", "GET", fmt.Sprintf("/api/v1/gears/%d/connections", n.gearID))
	n.searched(t, "an exported bundle", "GET", fmt.Sprintf("/api/v1/workspaces/%d/export?gears=1", n.wsID))

	for what, body := range n.response {
		if i := strings.Index(body, apiSecret); i >= 0 {
			from := max(i-80, 0)
			to := min(i+len(apiSecret)+80, len(body))
			t.Errorf("%s carries the secret's value:\n…%s…", what, body[from:to])
		}
	}

	// 5. The variable is visible on the two screens that exist to show what a
	// name means, and inside the run's own output. A secret is on none of them.
	// The pair is what makes them two things rather than one with a label.
	for _, what := range []string{"the install-wide Variables page", "the answer to setting a variable"} {
		if !strings.Contains(n.response[what], apiVariable) {
			t.Errorf("%s does not show a variable's value, and a variable is the kind that is shown:\n%s",
				what, n.response[what])
		}
	}
	if !strings.Contains(n.response["the execution history"], apiVariable) {
		t.Errorf("the execution history dropped the variable the gear printed:\n%s", n.response["the execution history"])
	}

	// And the secret was redacted rather than the gear having failed to print
	// it: the placeholder is in the history where the value would have been.
	if !strings.Contains(n.response["the execution history"], "[redacted]") {
		t.Errorf("the execution history holds no placeholder, so the run never printed the secret at all:\n%s",
			n.response["the execution history"])
	}

	// The approval screen shows the NAMES — withholding those would be the
	// opposite failure, an operator approving a gear without knowing what it
	// will be handed.
	for _, name := range []string{"PAYROLL_TOKEN", "PAYROLL_ENDPOINT"} {
		if !strings.Contains(n.response["the approval screen"], name) {
			t.Errorf("the approval screen does not name %s, so the decision is made blind:\n%s",
				name, n.response["the approval screen"])
		}
	}
}
