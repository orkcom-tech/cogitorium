// Package llm talks to model providers. Two adapter types cover the field:
// the Anthropic Messages API and OpenAI-compatible chat completions (OpenAI,
// OpenRouter, Groq, Ollama, LM Studio, vLLM, llama.cpp-server, …). Both
// support streaming text and tool calling through one interface.
package llm

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"time"
)

// Turn is one conversation turn in provider-neutral form.
type Turn struct {
	Role        string       // user | assistant
	Text        string       // may be empty on tool-only turns
	ToolCalls   []ToolCall   // assistant turns: tools the model invoked
	ToolResults []ToolResult // user turns: results being returned to the model
}

type Tool struct {
	Name        string
	Description string
	// InputSchema is a JSON Schema object for the tool arguments.
	InputSchema map[string]any
}

type ToolCall struct {
	ID        string
	Name      string
	InputJSON string // raw JSON arguments as produced by the model
}

type ToolResult struct {
	CallID  string
	Name    string
	Content string
	IsError bool
}

type Request struct {
	Model     string
	System    string
	Messages  []Turn
	Tools     []Tool
	MaxTokens int // 0 = adapter default
}

// Result is one completed model turn.
type Result struct {
	Text       string
	ToolCalls  []ToolCall
	StopReason string // "end_turn" | "tool_use" | provider-specific raw value
}

const (
	StopEndTurn = "end_turn"
	StopToolUse = "tool_use"
)

// Client is one configured provider endpoint.
type Client interface {
	// ListModels returns the model names the provider reports. Doubles as
	// the connectivity test.
	ListModels(ctx context.Context) ([]string, error)
	// Chat runs one model turn. onDelta (may be nil) receives text chunks
	// as they stream; the returned Result carries the assembled turn.
	Chat(ctx context.Context, req Request, onDelta func(text string) error) (Result, error)
}

const (
	TypeAnthropic        = "anthropic"
	TypeOpenAICompatible = "openai-compatible"

	defaultMaxTokens = 8192
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
	// No total-exchange Timeout: it would hard-kill streams that legitimately
	// run long (slow local models). Connection-phase timeouts bound failures;
	// stream lifetime is bounded by the caller's ctx.
	httpc := &http.Client{Transport: &http.Transport{
		DialContext:           (&net.Dialer{Timeout: 10 * time.Second}).DialContext,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 120 * time.Second,
	}}
	switch providerType {
	case TypeAnthropic:
		return &anthropicClient{baseURL: baseURL, apiKey: apiKey, http: httpc}, nil
	case TypeOpenAICompatible:
		return &openAIClient{baseURL: baseURL, apiKey: apiKey, http: httpc}, nil
	default:
		return nil, fmt.Errorf("unknown provider type %q", providerType)
	}
}
