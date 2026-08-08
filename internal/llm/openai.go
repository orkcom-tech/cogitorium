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
		return nil, httpError(resp)
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

func (c *openAIClient) Stream(ctx context.Context, model string, messages []Message, onDelta func(string) error) error {
	payload := map[string]any{
		"model":    model,
		"stream":   true,
		"messages": messages,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url("/chat/completions"), bytes.NewReader(body))
	if err != nil {
		return err
	}
	c.headers(req)

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("openai-compatible stream: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return httpError(resp)
	}

	return readSSE(resp.Body, func(_, data string) error {
		if data == "[DONE]" {
			return nil
		}
		var ev struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
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
		if len(ev.Choices) > 0 && ev.Choices[0].Delta.Content != "" {
			return onDelta(ev.Choices[0].Delta.Content)
		}
		return nil
	})
}
