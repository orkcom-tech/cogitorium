// Package llm talks to model providers. Two adapter types cover the field:
// the Anthropic Messages API and OpenAI-compatible chat completions (OpenAI,
// OpenRouter, Groq, Ollama, LM Studio, vLLM, llama.cpp-server, …).
package llm

import (
	"context"
	"fmt"
	"net/http"
	"time"
)

type Message struct {
	Role    string `json:"role"` // system | user | assistant
	Content string `json:"content"`
}

// Client is one configured provider endpoint.
type Client interface {
	// ListModels returns the model names the provider reports. Doubles as
	// the connectivity test.
	ListModels(ctx context.Context) ([]string, error)
	// Stream sends a chat request and calls onDelta for every text chunk.
	Stream(ctx context.Context, model string, messages []Message, onDelta func(text string) error) error
}

const (
	TypeAnthropic        = "anthropic"
	TypeOpenAICompatible = "openai-compatible"
)

// DefaultBaseURL returns the base URL used when a provider is created
// without one. Only Anthropic has a canonical default; OpenAI-compatible
// deployments are inherently endpoint-specific.
func DefaultBaseURL(providerType string) string {
	if providerType == TypeAnthropic {
		return "https://api.anthropic.com"
	}
	return ""
}

func New(providerType, baseURL, apiKey string) (Client, error) {
	httpc := &http.Client{Timeout: 5 * time.Minute}
	switch providerType {
	case TypeAnthropic:
		return &anthropicClient{baseURL: baseURL, apiKey: apiKey, http: httpc}, nil
	case TypeOpenAICompatible:
		return &openAIClient{baseURL: baseURL, apiKey: apiKey, http: httpc}, nil
	default:
		return nil, fmt.Errorf("unknown provider type %q", providerType)
	}
}
