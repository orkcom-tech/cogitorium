package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	neturl "net/url"
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
			if t.Text != "" || len(t.Parts) > 0 {
				m["content"] = openAIContent(t)
			}
			msgs = append(msgs, m)
			continue
		}
		if t.Text != "" || len(t.Parts) > 0 || len(t.ToolResults) == 0 {
			msgs = append(msgs, map[string]any{"role": t.Role, "content": openAIContent(t)})
		}
	}
	return msgs
}

// openAIContent is a turn's content field: a plain string when the turn is
// text, an array of parts when it carries files. The string case is not an
// array of one — a text-only turn crosses the wire exactly as it did before
// parts existed, and every OpenAI-compatible server in the field accepts a
// string, while some local ones do not accept the array form at all.
func openAIContent(t Turn) any {
	if len(t.Parts) == 0 {
		return t.Text
	}
	// Files first, then the prose, for the same reason as the Anthropic
	// adapter: the question refers to what precedes it.
	parts := make([]map[string]any, 0, len(t.Parts)+1)
	for _, p := range t.Parts {
		parts = append(parts, openAIPart(p))
	}
	if t.Text != "" {
		parts = append(parts, map[string]any{"type": "text", "text": t.Text})
	}
	return parts
}

// openAIPart renders one content part in the chat-completions vocabulary.
// Both file kinds carry the same data: URL — the protocol has no base64 field
// of its own — under the part type each is read from.
func openAIPart(p Part) map[string]any {
	switch p.Kind {
	case PartImage:
		return map[string]any{"type": "image_url", "image_url": map[string]any{"url": p.dataURL()}}
	case PartDocument:
		// A "file" part is how OpenAI itself takes a PDF. Local servers behind
		// this same protocol mostly do not implement it, which is exactly why
		// a model is not sent a document until the operator has said in the
		// catalog that it takes one — this adapter cannot know what is on the
		// other end of a base URL, and guessing costs a failed turn.
		return map[string]any{"type": "file", "file": map[string]any{
			"filename":  p.fileName(),
			"file_data": p.dataURL(),
		}}
	default:
		return map[string]any{"type": "text", "text": p.Text}
	}
}

func (c *openAIClient) Chat(ctx context.Context, r Request, onDelta func(string) error) (Result, error) {
	if err := checkRequestParts(r.Messages); err != nil {
		return Result{}, err
	}
	payload := map[string]any{
		"model":    r.Model,
		"stream":   true,
		"messages": openAIMessages(r.System, r.Messages),
		// Usage only arrives on a stream if it is asked for. Servers that do
		// not know the option ignore it; those that do send a final chunk
		// with the totals, which is the only way to bill an agent honestly.
		"stream_options": map[string]any{"include_usage": true},
	}
	if r.MaxTokens > 0 {
		payload["max_tokens"] = r.MaxTokens
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
		// Tool calls under assembly, in stream order. slotFor maps the
		// protocol's index field to a slot; a fragment carrying a NEW id at
		// an already-used index starts a new call — some local servers omit
		// or zero the index, and keying on it alone would merge calls.
		slots   []*ToolCall
		args    []*strings.Builder
		slotFor = map[int]int{}
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
			Usage *struct {
				PromptTokens     int `json:"prompt_tokens"`
				CompletionTokens int `json:"completion_tokens"`
			} `json:"usage"`
		}
		if err := json.Unmarshal([]byte(data), &ev); err != nil {
			slog.Warn("openai-compatible: bad stream chunk", "err", err)
			return nil
		}
		if ev.Error != nil {
			return fmt.Errorf("openai-compatible stream error: %s", ev.Error.Message)
		}
		// The usage chunk carries no choices, so it must be read before the
		// empty-choices guard below returns.
		if ev.Usage != nil {
			result.Usage.InputTokens = ev.Usage.PromptTokens
			result.Usage.OutputTokens = ev.Usage.CompletionTokens
			result.Usage.Reported = true
		}
		if len(ev.Choices) == 0 {
			return nil
		}
		ch := ev.Choices[0]
		if ch.FinishReason != nil && *ch.FinishReason != "" {
			finish = *ch.FinishReason
		}
		// A chunk may carry BOTH content and tool_calls — returning early
		// on content would silently drop the calls.
		if ch.Delta.Content != "" {
			text.WriteString(ch.Delta.Content)
			if onDelta != nil {
				if err := onDelta(ch.Delta.Content); err != nil {
					return err
				}
			}
		}
		for _, tc := range ch.Delta.ToolCalls {
			s, ok := slotFor[tc.Index]
			if !ok || (tc.ID != "" && slots[s].ID != "" && slots[s].ID != tc.ID) {
				slots = append(slots, &ToolCall{})
				args = append(args, &strings.Builder{})
				s = len(slots) - 1
				slotFor[tc.Index] = s
			}
			if tc.ID != "" {
				slots[s].ID = tc.ID
			}
			if tc.Function.Name != "" {
				slots[s].Name = tc.Function.Name
			}
			args[s].WriteString(tc.Function.Arguments)
		}
		return nil
	})
	if err != nil {
		return Result{}, err
	}

	result.Text = text.String()
	for i, tc := range slots {
		in := args[i].String()
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
	case "length":
		// Token-limit truncation must stay visible even when tool calls
		// were being assembled — they may be incomplete.
		result.StopReason = "length"
	case "stop", "":
		if len(result.ToolCalls) > 0 {
			result.StopReason = StopToolUse
		} else {
			result.StopReason = StopEndTurn
		}
	default:
		result.StopReason = finish
	}
	return result, nil
}
