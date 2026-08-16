// Package client talks to a running Cogitorium over its HTTP API.
//
// It exists because two things now need to: the MCP server and the command
// line. Two copies would be two error-handling behaviours, and the one that
// mattered would be whichever the reader was not looking at.
//
// It deliberately does not wrap the whole API. The surface here is what a
// person at a terminal or a client on stdio actually reaches for; anything
// broader belongs to a generated client, which is what docs/openapi.yaml is
// for.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// DefaultURL is where a server listens unless told otherwise.
const DefaultURL = "http://127.0.0.1:8688"

type Client struct {
	BaseURL string
	Token   string
	HTTP    *http.Client
}

// New resolves the address and token the way every command should: an explicit
// flag first, then the environment, then the default. Written once so the
// precedence cannot differ between two commands.
func New(url, token string) *Client {
	if url == "" {
		url = os.Getenv("COGITORIUM_URL")
	}
	if url == "" {
		url = DefaultURL
	}
	if token == "" {
		token = os.Getenv("COGITORIUM_TOKEN")
	}
	return &Client{
		BaseURL: strings.TrimSuffix(url, "/"),
		Token:   token,
		// A gear can legitimately run for minutes; this bounds a wedged server
		// rather than guessing how long work should take.
		HTTP: &http.Client{Timeout: 10 * time.Minute},
	}
}

// Do performs one request. A non-2xx answer becomes an error carrying the
// server's own message, which is written to be read by whoever caused it —
// replacing it with a status code would throw away the useful half.
func (c *Client) Do(ctx context.Context, method, path string, body, out any) error {
	var rdr io.Reader
	if body != nil {
		enc, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode the request: %w", err)
		}
		rdr = bytes.NewReader(enc)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, rdr)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	res, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("reach cogitorium at %s: %w", c.BaseURL, err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)

	if res.StatusCode >= 400 {
		return fmt.Errorf("%s: %s", res.Status, Message(raw))
	}
	if out != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			return fmt.Errorf("the server answered something this client cannot read: %w", err)
		}
	}
	return nil
}

// Message pulls the sentence out of an error response.
//
// Printing the body verbatim instead would show the reader
// "send Authorization: Bearer <inlet key>" — encoding/json escapes
// angle brackets by default, and the one message written specifically to be
// read by whoever caused the error is the last place to make them decode it.
func Message(raw []byte) string {
	var e struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(raw, &e) == nil && e.Error.Message != "" {
		return e.Error.Message
	}
	return strings.TrimSpace(string(raw))
}

// Deliver posts to a receiver with that receiver's own key rather than the
// management token. The two are separate on purpose: a door's credential opens
// that door and nothing else, and using the admin's would put the wrong caller
// in the ledger.
// async asks for a run number now instead of the answer later. Without it the
// call is held open for as long as the work takes, which is right at a prompt
// and wrong in anything with a timeout of its own.
func (c *Client) Deliver(ctx context.Context, address, task, key string, payload []byte, async bool) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.BaseURL+"/i/"+address+"/"+task, bytes.NewReader(payload))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	if async {
		req.Header.Set("Prefer", "respond-async")
	}
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	res, err := c.HTTP.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("deliver to %s/%s: %w", address, task, err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	return raw, res.StatusCode, nil
}

// The few shapes the command line reads back. Deliberately partial: a struct
// here is a promise to keep a field, and promising every field of every
// response would make this package the API rather than a view of it.

type Workspace struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

type Gear struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Status      string `json:"status"`
	Runtime     string `json:"runtime"`
	Version     int    `json:"version"`
}

type GearResult struct {
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	ExitCode int    `json:"exit_code"`
	TimedOut bool   `json:"timed_out"`
	Error    string `json:"error"`
}

type Inlet struct {
	Address     string `json:"address"`
	Description string `json:"description"`
	HasKey      bool   `json:"has_key"`
	Tasks       []struct {
		Name      string `json:"name"`
		Accepts   string `json:"accepts"`
		AgentName string `json:"agent_name"`
	} `json:"tasks"`
}

// ImportResult is what came of a bundle. The counts and the skip reasons are
// the whole point of printing anything: a bundle whose gears were all skipped
// imported "successfully" and produced a workspace that cannot do its work.
type ImportResult struct {
	Workspace     Workspace `json:"workspace"`
	Agents        int       `json:"agents"`
	Wires         int       `json:"wires"`
	GearsImported []string  `json:"gears_imported"`
	GearsSkipped  []struct {
		Name string `json:"name"`
		Why  string `json:"why"`
	} `json:"gears_skipped"`
	ContextFiles     int `json:"context_files"`
	UnresolvedModels []struct {
		Agent        string `json:"agent"`
		ProviderType string `json:"provider_type"`
		ModelName    string `json:"model_name"`
	} `json:"unresolved_models"`
}

type QueueEntry struct {
	Unit     int64  `json:"unit"`
	Kind     string `json:"kind"`
	State    string `json:"state"`
	Position int    `json:"position"`
	Run      *int64 `json:"run"`
	Since    string `json:"since"`
}

type QueueView struct {
	Running int          `json:"running"`
	Queued  int          `json:"queued"`
	Entries []QueueEntry `json:"entries"`
}

// Run is a delivery on the ledger. The field names are the server's, checked
// against internal/inlet rather than guessed — the first draft of this struct
// said "task" and "address", which decode to empty strings from a response
// that calls them task_name and inlet_address, and an empty string is what a
// silently wrong client looks like.
type Run struct {
	ID           int64           `json:"id"`
	State        string          `json:"state"`
	TaskName     string          `json:"task_name"`
	InletAddress string          `json:"inlet_address"`
	AgentName    string          `json:"agent_name"`
	Result       string          `json:"result"`
	Error        string          `json:"error"`
	Did          json.RawMessage `json:"did"`
}
