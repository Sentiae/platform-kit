// Package llm defines the canonical multi-provider LLM interface for Sentiae.
// Foundry-service uses this interface internally; other services that need
// direct LLM access (e.g. catalog sync, eval runner) should import from here.
package llm

import (
	"context"
	"encoding/json"
)

// ---------------------------------------------------------------------------
// Core types
// ---------------------------------------------------------------------------

// ModelInfo describes a single model as reported by the provider.
type ModelInfo struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name,omitempty"`
	CreatedAt   int64  `json:"created_at,omitempty"` // unix timestamp, if provided
}

// ProviderCaps summarises what a provider adapter can do.
type ProviderCaps struct {
	SupportsTools    bool
	SupportsThinking bool
	SupportsVision   bool
	SupportsEmbeds   bool
	SupportsStream   bool
}

// Message is a single turn in the conversation.
type Message struct {
	Role    string `json:"role"`    // system | user | assistant | tool
	Content string `json:"content"` // plain text for simple messages
}

// ToolDefinition is a callable tool the model may invoke.
type ToolDefinition struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"` // JSON Schema
}

// CompletionRequest is the provider-agnostic request.
type CompletionRequest struct {
	Messages    []Message        `json:"messages"`
	Tools       []ToolDefinition `json:"tools,omitempty"`
	ToolChoice  string           `json:"tool_choice,omitempty"` // auto | any | none
	Model       string           `json:"model,omitempty"`
	MaxTokens   int              `json:"max_tokens,omitempty"`
	Temperature float64          `json:"temperature,omitempty"`
	Stream      bool             `json:"stream,omitempty"`
}

// Completion is the provider-agnostic response.
type Completion struct {
	Content      string `json:"content"`
	StopReason   string `json:"stop_reason"`
	Model        string `json:"model"`
	InputTokens  int    `json:"input_tokens"`
	OutputTokens int    `json:"output_tokens"`
	LatencyMs    int64  `json:"latency_ms"`
}

// Event is a single streaming token/event from the provider.
type Event struct {
	Type       string `json:"type"` // text_delta | tool_use_start | tool_use_delta | done | error
	Text       string `json:"text,omitempty"`
	ToolCallID string `json:"tool_call_id,omitempty"`
	ToolName   string `json:"tool_name,omitempty"`
	ToolInput  string `json:"tool_input,omitempty"` // partial JSON
	StopReason string `json:"stop_reason,omitempty"`
	Err        error  `json:"-"`
}

// EmbedRequest holds the text(s) to embed.
type EmbedRequest struct {
	Model string   `json:"model,omitempty"`
	Input []string `json:"input"`
}

// ---------------------------------------------------------------------------
// Provider interface
// ---------------------------------------------------------------------------

// Provider is the canonical interface every LLM adapter must satisfy.
// Adapters live in platform-kit/llm/providers/ or foundry-service for
// adapters that need foundry-internal types.
type Provider interface {
	// ID is the canonical provider key matching provider_config.id.
	ID() string

	// Capabilities describes what this provider supports.
	Capabilities() ProviderCaps

	// ListModels fetches the available models from the provider's API.
	// Used by the catalog sync worker.
	ListModels(ctx context.Context) ([]ModelInfo, error)

	// Complete sends a non-streaming completion request.
	Complete(ctx context.Context, req CompletionRequest) (Completion, error)

	// Stream sends a streaming completion request.
	// The returned channel is closed when streaming ends or ctx is cancelled.
	Stream(ctx context.Context, req CompletionRequest) (<-chan Event, error)

	// Embed returns vector embeddings for the given input strings.
	Embed(ctx context.Context, req EmbedRequest) ([][]float32, error)

	// HealthCheck returns nil if the provider is reachable.
	HealthCheck(ctx context.Context) error
}
