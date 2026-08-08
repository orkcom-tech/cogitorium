package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	neturl "net/url"
	"sort"
	"strings"
)

// openAIClient speaks the OpenAI chat-completions protocol. baseURL is the
// API root including any version segment, e.g. "https://api.openai.com/v1"
// or "http://localhost:11434/v1" (Ollama).
type openAIClient struct {
	baseURL string
	apiKey  string
	http    *http.Client
}

func (c *openAIClient) url(path string) string {
	return strings.TrimSuffix(c.baseURL, "/") + path
}

// hint404 augments a 404 from a base URL without a version segment: the
// overwhelmingly common cause is a missing /v1 (OpenAI, Ollama, LM Studio
// all serve only under /v1). Blind normalization would break gateways
// mounted at the root, so hint instead of rewriting.
func (c *openAIClient) hint404(err error, resp *http.Response) error {
	if resp.StatusCode != http.StatusNotFound {
		return err
	}
	u, parseErr := neturl.Parse(c.baseURL)
	if parseErr != nil || strings.Contains(u.Path, "/v") {
		return err
	}
	return fmt.Errorf("%w — the base URL %q has no version segment; did you mean %q?", err, c.baseURL, strings.TrimSuffix(c.baseURL, "/")+"/v1")
}

func (c *openAIClient) headers(req *http.Request) {
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	req.Header.Set("Content-Type", "application/json")
}

func (c *openAIClient) ListModels(ctx context.Context) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url("/models"), nil)
	if err != nil {
		return nil, err
	}
	c.headers(req)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("openai-compatible list models: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, c.hint404(httpError(resp), resp)
	}

	var out struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("openai-compatible list models: decode: %w", err)
	}
	names := make([]string, 0, len(out.Data))
	for _, m := range out.Data {
		names = append(names, m.ID)
	}
	return names, nil
}

// openAIMessages converts neutral turns to chat-completions messages. Tool
// results become separate role:"tool" messages, per the protocol.
func openAIMessages(system string, turns []Turn) []map[string]any {
	msgs := []map[string]any{}
	if system != "" {
		msgs = append(msgs, map[string]any{"role": "system", "content": system})
	}
	for _, t := range turns {
		for _, tr := range t.ToolResults {
			msgs = append(msgs, map[string]any{
				"role":         "tool",
				"tool_call_id": tr.CallID,
				"content":      tr.Content,
			})
		}
		if t.Role == "assistant" && len(t.ToolCalls) > 0 {
			calls := make([]map[string]any, 0, len(t.ToolCalls))
			for _, tc := range t.ToolCalls {
				calls = append(calls, map[string]any{
					"id":   tc.ID,
					"type": "function",
					"function": map[string]any{
						"name":      tc.Name,
						"arguments": tc.InputJSON,
					},
				})
			}
			m := map[string]any{"role": "assistant", "tool_calls": calls}
			if t.Text != "" {
				m["content"] = t.Text
			}
			msgs = append(msgs, m)
			continue
		}
		if t.Text != "" || len(t.ToolResults) == 0 {
			msgs = append(msgs, map[string]any{"role": t.Role, "content": t.Text})
		}
	}
	return msgs
}

func (c *openAIClient) Chat(ctx context.Context, r Request, onDelta func(string) error) (Result, error) {
	payload := map[string]any{
		"model":    r.Model,
		"stream":   true,
		"messages": openAIMessages(r.System, r.Messages),
	}
	if len(r.Tools) > 0 {
		tools := make([]map[string]any, 0, len(r.Tools))
		for _, t := range r.Tools {
			tools = append(tools, map[string]any{
				"type": "function",
				"function": map[string]any{
					"name":        t.Name,
					"description": t.Description,
					"parameters":  t.InputSchema,
				},
			})
		}
		payload["tools"] = tools
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return Result{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url("/chat/completions"), bytes.NewReader(body))
	if err != nil {
		return Result{}, err
	}
	c.headers(req)

	resp, err := c.http.Do(req)
	if err != nil {
		return Result{}, fmt.Errorf("openai-compatible chat: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Result{}, c.hint404(httpError(resp), resp)
	}

	var (
		result Result
		text   strings.Builder
		// tool calls under assembly, keyed by the protocol's index field
		pending = map[int]*ToolCall{}
		args    = map[int]*strings.Builder{}
		finish  string
	)

	err = readSSE(resp.Body, func(_, data string) error {
		if data == "[DONE]" {
			return nil
		}
		var ev struct {
			Choices []struct {
				Delta struct {
					Content   string `json:"content"`
					ToolCalls []struct {
						Index    int    `json:"index"`
						ID       string `json:"id"`
						Function struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"delta"`
				FinishReason *string `json:"finish_reason"`
			} `json:"choices"`
			Error *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal([]byte(data), &ev); err != nil {
			slog.Warn("openai-compatible: bad stream chunk", "err", err)
			return nil
		}
		if ev.Error != nil {
			return fmt.Errorf("openai-compatible stream error: %s", ev.Error.Message)
		}
		if len(ev.Choices) == 0 {
			return nil
		}
		ch := ev.Choices[0]
		if ch.FinishReason != nil && *ch.FinishReason != "" {
			finish = *ch.FinishReason
		}
		if ch.Delta.Content != "" {
			text.WriteString(ch.Delta.Content)
			if onDelta != nil {
				return onDelta(ch.Delta.Content)
			}
		}
		for _, tc := range ch.Delta.ToolCalls {
			p, ok := pending[tc.Index]
			if !ok {
				p = &ToolCall{}
				pending[tc.Index] = p
				args[tc.Index] = &strings.Builder{}
			}
			if tc.ID != "" {
				p.ID = tc.ID
			}
			if tc.Function.Name != "" {
				p.Name = tc.Function.Name
			}
			args[tc.Index].WriteString(tc.Function.Arguments)
		}
		return nil
	})
	if err != nil {
		return Result{}, err
	}

	result.Text = text.String()
	idxs := make([]int, 0, len(pending))
	for i := range pending {
		idxs = append(idxs, i)
	}
	sort.Ints(idxs)
	for i, idx := range idxs {
		tc := pending[idx]
		in := args[idx].String()
		if in == "" {
			in = "{}"
		}
		tc.InputJSON = in
		if tc.ID == "" {
			// Some local servers omit call ids; synthesize stable ones.
			tc.ID = fmt.Sprintf("call_%d", i)
		}
		result.ToolCalls = append(result.ToolCalls, *tc)
	}

	switch finish {
	case "tool_calls":
		result.StopReason = StopToolUse
	case "stop", "":
		result.StopReason = StopEndTurn
	default:
		result.StopReason = finish
	}
	if len(result.ToolCalls) > 0 {
		result.StopReason = StopToolUse
	}
	return result, nil
}
