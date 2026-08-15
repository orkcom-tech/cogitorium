package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// HTTPBackend turns a running Cogitorium into a set of MCP tools.
//
// It holds no database. Everything it knows it asks the server for, over the
// same API the interface uses, with a token — so an MCP client can never see
// or do more than the credential it was given allows.
type HTTPBackend struct {
	BaseURL string
	Token   string
	// InletKeys maps a receiver address to its key. Delivery does not use the
	// management token: a receiver's door has its own credential by design, and
	// borrowing the admin's would make the ledger record the wrong caller.
	InletKeys map[string]string
	// WorkspaceID scopes which receivers are offered. Zero means every
	// workspace the token can see.
	WorkspaceID int64

	HTTP *http.Client
}

// The two prefixes an MCP client sees. Namespaced because the two kinds have
// separate rules — one is a tool an operator approved, the other is a door with
// its own key — and a client should not have to guess which it is calling.
const (
	gearPrefix    = "gear_"
	receiveprefix = "receive_"
)

func (b *HTTPBackend) client() *http.Client {
	if b.HTTP != nil {
		return b.HTTP
	}
	// A gear or a delivery can legitimately take minutes; the timeout here is
	// a ceiling on a wedged server, not a guess at how long work should take.
	return &http.Client{Timeout: 10 * time.Minute}
}

func (b *HTTPBackend) do(ctx context.Context, method, path string, body any, out any) error {
	var rdr io.Reader
	if body != nil {
		enc, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(enc)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimSuffix(b.BaseURL, "/")+path, rdr)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if b.Token != "" {
		req.Header.Set("Authorization", "Bearer "+b.Token)
	}
	res, err := b.client().Do(req)
	if err != nil {
		return fmt.Errorf("reach cogitorium at %s: %w", b.BaseURL, err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	if res.StatusCode >= 400 {
		// The server's own message, which is written to be read by whoever
		// caused it — passing it through beats replacing it with a status code.
		var e struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(raw, &e) == nil && e.Error.Message != "" {
			return fmt.Errorf("%s: %s", res.Status, e.Error.Message)
		}
		return fmt.Errorf("%s: %s", res.Status, strings.TrimSpace(string(raw)))
	}
	if out != nil && len(raw) > 0 {
		return json.Unmarshal(raw, out)
	}
	return nil
}

type apiGear struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Status      string `json:"status"`
	ArgsSchema  string `json:"args_schema"`
}

type apiInlet struct {
	Address string `json:"address"`
	Tasks   []struct {
		Name        string          `json:"name"`
		Accepts     string          `json:"accepts"`
		Schema      string          `json:"schema"`
		Instruction string          `json:"instruction"`
		AgentName   string          `json:"agent_name"`
		Expect      json.RawMessage `json:"expect"`
	} `json:"tasks"`
}

type apiWorkspace struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// Tools lists what this install offers right now.
func (b *HTTPBackend) Tools(ctx context.Context) ([]Tool, error) {
	var tools []Tool

	var gears []apiGear
	if err := b.do(ctx, http.MethodGet, "/api/v1/gears", nil, &gears); err != nil {
		return nil, err
	}
	for _, g := range gears {
		// Approved only. A pending gear is code the operator has not read yet,
		// and the whole point of the gate is that nothing calls it — least of
		// all something outside this install.
		if g.Status != "approved" {
			continue
		}
		schema := json.RawMessage(g.ArgsSchema)
		if len(bytes.TrimSpace(schema)) == 0 {
			schema = json.RawMessage(`{"type":"object"}`)
		}
		desc := g.Description
		if desc == "" {
			desc = "A gear forged in this Cogitorium install."
		}
		tools = append(tools, Tool{
			Name:        gearPrefix + g.Name,
			Description: desc + " Runs in a sandboxed container on the Cogitorium host.",
			InputSchema: schema,
		})
	}

	spaces, err := b.workspaces(ctx)
	if err != nil {
		return nil, err
	}
	for _, ws := range spaces {
		var inlets []apiInlet
		if err := b.do(ctx, http.MethodGet, "/api/v1/workspaces/"+strconv.FormatInt(ws.ID, 10)+"/inlets", nil, &inlets); err != nil {
			return nil, err
		}
		for _, in := range inlets {
			for _, t := range in.Tasks {
				if t.Accepts != "json" {
					// A file task takes bytes with a content type, which is not
					// a JSON tool call. Left out rather than half-supported.
					continue
				}
				schema := json.RawMessage(t.Schema)
				if len(bytes.TrimSpace(schema)) == 0 {
					schema = json.RawMessage(`{"type":"object"}`)
				}
				tools = append(tools, Tool{
					Name: receiveprefix + in.Address + "_" + t.Name,
					Description: fmt.Sprintf(
						"Workspace %q: %s The agent %q does the work, and the answer comes back on this call.",
						ws.Name, t.Instruction, t.AgentName),
					InputSchema: schema,
				})
			}
		}
	}
	return tools, nil
}

func (b *HTTPBackend) workspaces(ctx context.Context) ([]apiWorkspace, error) {
	var all []apiWorkspace
	if err := b.do(ctx, http.MethodGet, "/api/v1/workspaces", nil, &all); err != nil {
		return nil, err
	}
	if b.WorkspaceID == 0 {
		return all, nil
	}
	for _, w := range all {
		if w.ID == b.WorkspaceID {
			return []apiWorkspace{w}, nil
		}
	}
	return nil, fmt.Errorf("workspace %d is not one this token can see", b.WorkspaceID)
}

// Call runs a tool by the name Tools gave it.
func (b *HTTPBackend) Call(ctx context.Context, name string, args json.RawMessage) (string, bool, error) {
	if len(bytes.TrimSpace(args)) == 0 {
		args = json.RawMessage("{}")
	}
	switch {
	case strings.HasPrefix(name, gearPrefix):
		return b.callGear(ctx, strings.TrimPrefix(name, gearPrefix), args)
	case strings.HasPrefix(name, receiveprefix):
		return b.deliver(ctx, strings.TrimPrefix(name, receiveprefix), args)
	}
	return "", false, fmt.Errorf("no tool called %q — call tools/list for what this install offers", name)
}

func (b *HTTPBackend) callGear(ctx context.Context, gearName string, args json.RawMessage) (string, bool, error) {
	var gears []apiGear
	if err := b.do(ctx, http.MethodGet, "/api/v1/gears", nil, &gears); err != nil {
		return "", false, err
	}
	for _, g := range gears {
		if g.Name != gearName {
			continue
		}
		// Checked here as well as on the server. Not defence in depth theatre:
		// the list an MCP client holds can be minutes old, and a gear disabled
		// in between should refuse with a sentence rather than a 403 the client
		// renders as a protocol failure.
		if g.Status != "approved" {
			return fmt.Sprintf("The gear %q is %s, not approved, so it cannot be run.", g.Name, g.Status), true, nil
		}
		var out struct {
			Stdout   string `json:"stdout"`
			Stderr   string `json:"stderr"`
			ExitCode int    `json:"exit_code"`
			TimedOut bool   `json:"timed_out"`
			Error    string `json:"error"`
		}
		// /invoke, not /run: the second is the dry run and bypasses approval by
		// design. Calling it here would make the gate optional for anything
		// holding a token.
		err := b.do(ctx, http.MethodPost, "/api/v1/gears/"+strconv.FormatInt(g.ID, 10)+"/invoke",
			map[string]any{"args": args}, &out)
		if err != nil {
			return "", false, err
		}
		return gearResultText(out.Stdout, out.Stderr, out.Error, out.ExitCode, out.TimedOut),
			out.Error != "" || out.ExitCode != 0 || out.TimedOut, nil
	}
	return "", false, fmt.Errorf("no gear called %q in this install", gearName)
}

func gearResultText(stdout, stderr, errText string, exit int, timedOut bool) string {
	var b strings.Builder
	if stdout != "" {
		b.WriteString(stdout)
	}
	// stderr is included rather than dropped: a gear that writes its diagnosis
	// there and exits non-zero would otherwise report an empty failure.
	if stderr != "" {
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString("stderr: " + stderr)
	}
	if timedOut {
		b.WriteString("\n(the gear hit its timeout and was stopped)")
	}
	if errText != "" {
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString(errText)
	}
	if b.Len() == 0 {
		b.WriteString(fmt.Sprintf("(the gear produced no output; exit code %d)", exit))
	}
	return b.String()
}

// deliver posts to a receiver task. The tool name carries address and task,
// and the address is matched against the keys this process was given rather
// than parsed out of it — an address may itself contain an underscore.
func (b *HTTPBackend) deliver(ctx context.Context, suffix string, args json.RawMessage) (string, bool, error) {
	address, task := "", ""
	for addr := range b.InletKeys {
		if strings.HasPrefix(suffix, addr+"_") {
			// Longest match wins, so "tickets" does not shadow "tickets_eu".
			if len(addr) > len(address) {
				address, task = addr, strings.TrimPrefix(suffix, addr+"_")
			}
		}
	}
	if address == "" {
		return "", false, fmt.Errorf("no key was given for the receiver in %q — pass --inlet-key <address>=<key> "+
			"for every receiver this MCP server should offer", suffix)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimSuffix(b.BaseURL, "/")+"/i/"+address+"/"+task, bytes.NewReader(args))
	if err != nil {
		return "", false, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+b.InletKeys[address])
	res, err := b.client().Do(req)
	if err != nil {
		return "", false, fmt.Errorf("deliver to %s/%s: %w", address, task, err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)

	var d struct {
		Run    int64  `json:"run"`
		State  string `json:"state"`
		Result string `json:"result"`
		Error  string `json:"error"`
	}
	if json.Unmarshal(raw, &d) != nil || d.State == "" {
		return strings.TrimSpace(string(raw)), res.StatusCode >= 400, nil
	}
	if d.State == "completed" {
		return d.Result, false, nil
	}
	// A refusal is a result, not a transport failure: the model asked for
	// something the door does not accept and should be told exactly that.
	return fmt.Sprintf("run %d %s: %s", d.Run, d.State, d.Error), true, nil
}
