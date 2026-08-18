package mcpclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/orkcom-tech/cogitorium/internal/mcp/mcpwire"
)

// Resources and prompts, the two halves of MCP this client did not consume.
//
// # Why they were missing and why that was wrong
//
// Only tools were read, so a server offering documents offered them to nothing.
// That is a real gap rather than a cosmetic one: a good part of what people
// publish is a resource server — a wiki, a drive, a codebase — and against this
// install every one of them looked empty.
//
// # Why they are NOT offered to the model as tools
//
// The obvious shortcut is to wrap each resource in a synthetic `read_x` tool
// and each prompt in a `use_y`. It is wrong for a reason worth stating: a tool
// is something an agent DECIDES to call, and the model chooses from the list on
// every turn. A server with four hundred documents would put four hundred tool
// definitions in every request — the bill and the confusion — and none of them
// is a capability, they are all the same capability applied to different
// arguments.
//
// So a server's resources are reachable through ONE tool that takes a URI, and
// its prompts through one that takes a name. The list is a thing the operator
// browses, not a thing the model carries.

// Resource is one thing a server says it holds.
type Resource struct {
	URI         string `json:"uri"`
	Name        string `json:"name"`
	Description string `json:"description"`
	MIMEType    string `json:"mimeType"`
}

// Prompt is one template a server offers.
type Prompt struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Arguments   []struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Required    bool   `json:"required"`
	} `json:"arguments"`
}

// Resources lists what a server holds, following cursors to the end.
//
// Capped for the same reason the tool list is: a server with a hundred thousand
// documents is a server whose list nobody reads, and an unbounded fetch is a
// memory spike on somebody else's data.
func (c *Conn) Resources(ctx context.Context, cap int) ([]Resource, bool, error) {
	var all []Resource
	cursor := ""
	for {
		params := map[string]any{}
		if cursor != "" {
			params["cursor"] = cursor
		}
		raw, err := c.Call(ctx, "resources/list", params)
		if err != nil {
			// A server that does not do resources answers "method not found",
			// which is not a failure of this call — it is the answer. An empty
			// list is what the caller wanted to know.
			if notImplemented(err) {
				return nil, false, nil
			}
			return nil, false, err
		}
		var page struct {
			Resources  []Resource `json:"resources"`
			NextCursor string     `json:"nextCursor"`
		}
		if err := json.Unmarshal(raw, &page); err != nil {
			return nil, false, fmt.Errorf("the MCP server %q listed its resources unreadably: %w", c.spec.Name, err)
		}
		all = append(all, page.Resources...)
		if len(all) >= cap {
			return all[:cap], true, nil
		}
		if page.NextCursor == "" {
			return all, false, nil
		}
		cursor = page.NextCursor
	}
}

// ReadResource fetches one document by URI.
func (c *Conn) ReadResource(ctx context.Context, uri string) (CallResult, error) {
	raw, err := c.Call(ctx, "resources/read", map[string]any{"uri": uri})
	if err != nil {
		return CallResult{}, err
	}
	var out struct {
		Contents []struct {
			URI      string `json:"uri"`
			MIMEType string `json:"mimeType"`
			Text     string `json:"text"`
			Blob     string `json:"blob"`
		} `json:"contents"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return CallResult{}, fmt.Errorf("the MCP server %q answered %q with something this cannot read: %w",
			c.spec.Name, uri, err)
	}
	var text []string
	var dropped []string
	for _, part := range out.Contents {
		switch {
		case part.Text != "":
			text = append(text, part.Text)
		case part.Blob != "":
			// Named rather than decoded. A blob is base64 of arbitrary bytes,
			// and putting a megabyte of it into a prompt is a bill for
			// something the model cannot read anyway — the same rule the tool
			// path already follows for images and audio.
			dropped = append(dropped, "binary content at "+part.URI)
		}
	}
	return CallResult{Text: strings.Join(text, "\n\n"), Dropped: dropped}, nil
}

// Prompts lists the templates a server offers.
func (c *Conn) Prompts(ctx context.Context, cap int) ([]Prompt, bool, error) {
	var all []Prompt
	cursor := ""
	for {
		params := map[string]any{}
		if cursor != "" {
			params["cursor"] = cursor
		}
		raw, err := c.Call(ctx, "prompts/list", params)
		if err != nil {
			if notImplemented(err) {
				return nil, false, nil
			}
			return nil, false, err
		}
		var page struct {
			Prompts    []Prompt `json:"prompts"`
			NextCursor string   `json:"nextCursor"`
		}
		if err := json.Unmarshal(raw, &page); err != nil {
			return nil, false, fmt.Errorf("the MCP server %q listed its prompts unreadably: %w", c.spec.Name, err)
		}
		all = append(all, page.Prompts...)
		if len(all) >= cap {
			return all[:cap], true, nil
		}
		if page.NextCursor == "" {
			return all, false, nil
		}
		cursor = page.NextCursor
	}
}

// GetPrompt renders one template with its arguments.
func (c *Conn) GetPrompt(ctx context.Context, name string, args json.RawMessage) (CallResult, error) {
	params := map[string]any{"name": name}
	if len(args) > 0 {
		params["arguments"] = args
	}
	raw, err := c.Call(ctx, "prompts/get", params)
	if err != nil {
		return CallResult{}, err
	}
	var out struct {
		Description string `json:"description"`
		Messages    []struct {
			Role    string `json:"role"`
			Content struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return CallResult{}, fmt.Errorf("the MCP server %q answered prompt %q with something this cannot read: %w",
			c.spec.Name, name, err)
	}
	// Flattened to text, with the role kept as a label rather than as
	// structure. A prompt template arrives as a conversation, and splicing
	// somebody else's "assistant" turns into this agent's history would let a
	// server write words into the transcript as though the model had said them.
	var b []string
	for _, m := range out.Messages {
		if m.Content.Text == "" {
			continue
		}
		b = append(b, strings.TrimSpace(m.Role)+": "+m.Content.Text)
	}
	return CallResult{Text: strings.Join(b, "\n\n")}, nil
}

// notImplemented is a server saying it does not do this at all, which is an
// answer rather than a failure. Matched on the JSON-RPC code the spec defines
// for it, and on the text as well because not every server sets the code.
func notImplemented(err error) bool {
	if err == nil {
		return false
	}
	// Conn.Call wraps the server's error with %w, so errors.As finds it.
	var rpc *mcpwire.RPCError
	if errors.As(err, &rpc) && rpc.Code == mcpwire.CodeMethodNotFound {
		return true
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "method not found") || strings.Contains(s, "not supported")
}
