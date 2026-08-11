package llm

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strings"
)

const anthropicAPIVersion = "2023-06-01"

type anthropicClient struct {
	baseURL string
	apiKey  string
	http    *http.Client
}

// url joins the base URL with an API path that already carries /v1. A base
// entered with a trailing /v1 (a natural habit from OpenAI-style bases) is
// tolerated rather than producing /v1/v1/….
func (c *anthropicClient) url(path string) string {
	base := strings.TrimSuffix(c.baseURL, "/")
	base = strings.TrimSuffix(base, "/v1")
	return base + path
}

func (c *anthropicClient) headers(req *http.Request) {
	req.Header.Set("x-api-key", c.apiKey)
	req.Header.Set("anthropic-version", anthropicAPIVersion)
	req.Header.Set("content-type", "application/json")
}

func (c *anthropicClient) ListModels(ctx context.Context) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url("/v1/models?limit=100"), nil)
	if err != nil {
		return nil, err
	}
	c.headers(req)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("anthropic list models: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, httpError(resp)
	}

	var out struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("anthropic list models: decode: %w", err)
	}
	names := make([]string, 0, len(out.Data))
	for _, m := range out.Data {
		names = append(names, m.ID)
	}
	return names, nil
}

// anthropicContent converts a neutral Turn into Messages-API content blocks.
func anthropicContent(t Turn) (any, error) {
	blocks := []map[string]any{}
	for _, tr := range t.ToolResults {
		blocks = append(blocks, map[string]any{
			"type":        "tool_result",
			"tool_use_id": tr.CallID,
			"content":     tr.Content,
			"is_error":    tr.IsError,
		})
	}
	// Files come before the prose that asks about them: Anthropic documents
	// that an image placed ahead of the question is answered better, and the
	// question is almost always "what is in this".
	for _, p := range t.Parts {
		block, err := anthropicBlock(p)
		if err != nil {
			return nil, err
		}
		blocks = append(blocks, block)
	}
	if t.Text != "" {
		blocks = append(blocks, map[string]any{"type": "text", "text": t.Text})
	}
	for _, tc := range t.ToolCalls {
		var input any = map[string]any{}
		if tc.InputJSON != "" {
			if err := json.Unmarshal([]byte(tc.InputJSON), &input); err != nil {
				return nil, fmt.Errorf("tool call %s: input is not valid JSON: %w", tc.Name, err)
			}
		}
		blocks = append(blocks, map[string]any{
			"type":  "tool_use",
			"id":    tc.ID,
			"name":  tc.Name,
			"input": input,
		})
	}
	if len(blocks) == 0 {
		// The API rejects empty content; this indicates a caller bug.
		return nil, fmt.Errorf("turn with role %q has no content", t.Role)
	}
	return blocks, nil
}

// anthropicBlock renders one content part as a Messages-API block. Files go as
// a base64 source: the API takes a URL source too, but the file is on this
// machine and Anthropic has no way to fetch it from here.
func anthropicBlock(p Part) (map[string]any, error) {
	switch p.Kind {
	case PartText:
		return map[string]any{"type": "text", "text": p.Text}, nil
	case PartImage, PartDocument:
		// The block type is the part kind by another name: an image is an
		// "image", a PDF is a "document", and both carry the same source.
		blockType := "image"
		if p.Kind == PartDocument {
			blockType = "document"
		}
		return map[string]any{
			"type": blockType,
			"source": map[string]any{
				"type":       "base64",
				"media_type": p.MediaType,
				"data":       base64.StdEncoding.EncodeToString(p.Data),
			},
		}, nil
	}
	// checkRequestParts runs first and rejects every other kind, so reaching
	// here means a caller built a Part this package does not know.
	_, err := p.check()
	if err == nil {
		err = fmt.Errorf("content kind %q cannot be sent to an Anthropic model", p.Kind)
	}
	return nil, err
}

func (c *anthropicClient) Chat(ctx context.Context, r Request, onDelta func(string) error) (Result, error) {
	if err := checkRequestParts(r.Messages); err != nil {
		return Result{}, err
	}
	msgs := make([]map[string]any, 0, len(r.Messages))
	for _, t := range r.Messages {
		content, err := anthropicContent(t)
		if err != nil {
			return Result{}, err
		}
		msgs = append(msgs, map[string]any{"role": t.Role, "content": content})
	}

	maxTokens := r.MaxTokens
	if maxTokens <= 0 {
		maxTokens = defaultMaxTokens
	}
	payload := map[string]any{
		"model":      r.Model,
		"max_tokens": maxTokens,
		"stream":     true,
		"messages":   msgs,
	}
	if r.System != "" {
		payload["system"] = r.System
	}
	if len(r.Tools) > 0 {
		tools := make([]map[string]any, 0, len(r.Tools))
		for _, t := range r.Tools {
			tools = append(tools, map[string]any{
				"name":         t.Name,
				"description":  t.Description,
				"input_schema": t.InputSchema,
			})
		}
		payload["tools"] = tools
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return Result{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url("/v1/messages"), bytes.NewReader(body))
	if err != nil {
		return Result{}, err
	}
	c.headers(req)

	resp, err := c.http.Do(req)
	if err != nil {
		return Result{}, fmt.Errorf("anthropic chat: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Result{}, httpError(resp)
	}

	var (
		result Result
		text   strings.Builder
		// tool_use blocks under assembly, keyed by content-block index
		pending = map[int]*ToolCall{}
		inputs  = map[int]*strings.Builder{}
	)

	err = readSSE(resp.Body, func(event, data string) error {
		switch event {
		case "content_block_start":
			var ev struct {
				Index        int `json:"index"`
				ContentBlock struct {
					Type string `json:"type"`
					ID   string `json:"id"`
					Name string `json:"name"`
				} `json:"content_block"`
			}
			if err := json.Unmarshal([]byte(data), &ev); err != nil {
				slog.Warn("anthropic: bad content_block_start", "err", err)
				return nil
			}
			if ev.ContentBlock.Type == "tool_use" {
				pending[ev.Index] = &ToolCall{ID: ev.ContentBlock.ID, Name: ev.ContentBlock.Name}
				inputs[ev.Index] = &strings.Builder{}
			}
		case "content_block_delta":
			var ev struct {
				Index int `json:"index"`
				Delta struct {
					Type        string `json:"type"`
					Text        string `json:"text"`
					PartialJSON string `json:"partial_json"`
				} `json:"delta"`
			}
			if err := json.Unmarshal([]byte(data), &ev); err != nil {
				slog.Warn("anthropic: bad content_block_delta", "err", err)
				return nil
			}
			switch ev.Delta.Type {
			case "text_delta":
				text.WriteString(ev.Delta.Text)
				if onDelta != nil && ev.Delta.Text != "" {
					return onDelta(ev.Delta.Text)
				}
			case "input_json_delta":
				if b, ok := inputs[ev.Index]; ok {
					b.WriteString(ev.Delta.PartialJSON)
				}
			}
		case "message_start":
			// Input tokens arrive once, up front; output tokens accumulate
			// and are restated in message_delta.
			var ev struct {
				Message struct {
					Usage struct {
						InputTokens  int `json:"input_tokens"`
						OutputTokens int `json:"output_tokens"`
					} `json:"usage"`
				} `json:"message"`
			}
			if err := json.Unmarshal([]byte(data), &ev); err == nil {
				result.Usage.InputTokens = ev.Message.Usage.InputTokens
				result.Usage.OutputTokens = ev.Message.Usage.OutputTokens
				result.Usage.Reported = true
			}
		case "message_delta":
			var ev struct {
				Delta struct {
					StopReason string `json:"stop_reason"`
				} `json:"delta"`
				Usage struct {
					InputTokens  int `json:"input_tokens"`
					OutputTokens int `json:"output_tokens"`
				} `json:"usage"`
			}
			if err := json.Unmarshal([]byte(data), &ev); err == nil {
				if ev.Delta.StopReason != "" {
					result.StopReason = ev.Delta.StopReason
				}
				if ev.Usage.OutputTokens > 0 {
					result.Usage.OutputTokens = ev.Usage.OutputTokens
					result.Usage.Reported = true
				}
				if ev.Usage.InputTokens > 0 {
					result.Usage.InputTokens = ev.Usage.InputTokens
				}
			}
		case "error":
			var ev struct {
				Error struct {
					Type    string `json:"type"`
					Message string `json:"message"`
				} `json:"error"`
			}
			if err := json.Unmarshal([]byte(data), &ev); err != nil {
				return fmt.Errorf("anthropic stream error event (unparsed): %s", data)
			}
			return fmt.Errorf("anthropic stream error: %s: %s", ev.Error.Type, ev.Error.Message)
		}
		return nil
	})
	if err != nil {
		return Result{}, err
	}

	result.Text = text.String()
	// Map iteration order is random; keep tool calls in stream order.
	idxs := make([]int, 0, len(pending))
	for i := range pending {
		idxs = append(idxs, i)
	}
	sort.Ints(idxs)
	for _, idx := range idxs {
		tc := pending[idx]
		in := inputs[idx].String()
		if in == "" {
			in = "{}"
		}
		tc.InputJSON = in
		result.ToolCalls = append(result.ToolCalls, *tc)
	}
	// Normalize: Anthropic already uses end_turn / tool_use.
	if result.StopReason == "" {
		result.StopReason = StopEndTurn
	}
	return result, nil
}
