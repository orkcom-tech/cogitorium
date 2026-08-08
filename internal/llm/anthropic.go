package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
)

const anthropicAPIVersion = "2023-06-01"

type anthropicClient struct {
	baseURL string
	apiKey  string
	http    *http.Client
}

func (c *anthropicClient) url(path string) string {
	return strings.TrimSuffix(c.baseURL, "/") + path
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

func (c *anthropicClient) Stream(ctx context.Context, model string, messages []Message, onDelta func(string) error) error {
	// The Messages API takes system prompts as a top-level field.
	var system strings.Builder
	chat := make([]Message, 0, len(messages))
	for _, m := range messages {
		if m.Role == "system" {
			if system.Len() > 0 {
				system.WriteString("\n\n")
			}
			system.WriteString(m.Content)
			continue
		}
		chat = append(chat, m)
	}

	payload := map[string]any{
		"model":      model,
		"max_tokens": 8192,
		"stream":     true,
		"messages":   chat,
	}
	if system.Len() > 0 {
		payload["system"] = system.String()
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url("/v1/messages"), bytes.NewReader(body))
	if err != nil {
		return err
	}
	c.headers(req)

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("anthropic stream: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return httpError(resp)
	}

	return readSSE(resp.Body, func(event, data string) error {
		switch event {
		case "content_block_delta":
			var ev struct {
				Delta struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"delta"`
			}
			if err := json.Unmarshal([]byte(data), &ev); err != nil {
				slog.Warn("anthropic: bad delta event", "err", err)
				return nil
			}
			if ev.Delta.Type == "text_delta" && ev.Delta.Text != "" {
				return onDelta(ev.Delta.Text)
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
}
