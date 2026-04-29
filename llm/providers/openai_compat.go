// Package providers contains LLM provider adapters for Sentiae.
package providers

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/sentiae/platform-kit/llm"
)

// CompatConfig configures an OpenAI-compatible provider.
type CompatConfig struct {
	// ProviderID must match provider_config.id (e.g. "openrouter", "deepseek").
	ProviderID  string
	DisplayName string
	BaseURL     string // e.g. "https://openrouter.ai/api/v1"
	APIKey      string
	// DefaultModel is used when CompletionRequest.Model is empty.
	DefaultModel string
	// HTTPClient allows injecting a custom client (timeouts, OTel transport).
	// If nil, a default 60s client is used.
	HTTPClient *http.Client
}

// Compat is a provider adapter for any OpenAI-API-compatible endpoint.
// One instance handles OpenRouter, DeepSeek, Fireworks, NVIDIA NIM, etc.
// Actual behaviour differences (pricing, model names) live in provider_config
// and model_offerings tables, not here.
type Compat struct {
	cfg    CompatConfig
	client *http.Client
}

// NewCompat creates a new OpenAI-compatible provider adapter.
func NewCompat(cfg CompatConfig) *Compat {
	hc := cfg.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: 60 * time.Second}
	}
	return &Compat{cfg: cfg, client: hc}
}

// Pre-built constructors for well-known compat providers.

func NewOpenRouter(apiKey string) *Compat {
	return NewCompat(CompatConfig{
		ProviderID:  "openrouter",
		DisplayName: "OpenRouter",
		BaseURL:     "https://openrouter.ai/api/v1",
		APIKey:      apiKey,
	})
}

func NewDeepSeek(apiKey string) *Compat {
	return NewCompat(CompatConfig{
		ProviderID:  "deepseek",
		DisplayName: "DeepSeek",
		BaseURL:     "https://api.deepseek.com/v1",
		APIKey:      apiKey,
	})
}

func NewFireworks(apiKey string) *Compat {
	return NewCompat(CompatConfig{
		ProviderID:  "fireworks",
		DisplayName: "Fireworks",
		BaseURL:     "https://api.fireworks.ai/inference/v1",
		APIKey:      apiKey,
	})
}

func NewNVIDIANIM(apiKey string) *Compat {
	return NewCompat(CompatConfig{
		ProviderID:  "nvidia_nim",
		DisplayName: "NVIDIA NIM",
		BaseURL:     "https://integrate.api.nvidia.com/v1",
		APIKey:      apiKey,
	})
}

func NewTogether(apiKey string) *Compat {
	return NewCompat(CompatConfig{
		ProviderID:  "together",
		DisplayName: "Together AI",
		BaseURL:     "https://api.together.xyz/v1",
		APIKey:      apiKey,
	})
}

func NewXAI(apiKey string) *Compat {
	return NewCompat(CompatConfig{
		ProviderID:  "xai",
		DisplayName: "xAI",
		BaseURL:     "https://api.x.ai/v1",
		APIKey:      apiKey,
	})
}

// ---------------------------------------------------------------------------
// Provider interface impl
// ---------------------------------------------------------------------------

func (c *Compat) ID() string { return c.cfg.ProviderID }

func (c *Compat) Capabilities() llm.ProviderCaps {
	return llm.ProviderCaps{
		SupportsTools:  true,
		SupportsVision: true,
		SupportsEmbeds: true,
		SupportsStream: true,
	}
}

// ListModels fetches available models via GET /v1/models.
func (c *Compat) ListModels(ctx context.Context) ([]llm.ModelInfo, error) {
	body, err := c.get(ctx, "/models")
	if err != nil {
		return nil, err
	}
	var resp struct {
		Data []struct {
			ID      string `json:"id"`
			Created int64  `json:"created"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("parse models response: %w", err)
	}
	out := make([]llm.ModelInfo, len(resp.Data))
	for i, m := range resp.Data {
		out[i] = llm.ModelInfo{ID: m.ID, CreatedAt: m.Created}
	}
	return out, nil
}

// Complete sends a non-streaming chat completion request.
func (c *Compat) Complete(ctx context.Context, req llm.CompletionRequest) (llm.Completion, error) {
	start := time.Now()
	payload := c.buildPayload(req, false)

	body, err := c.post(ctx, "/chat/completions", payload)
	if err != nil {
		return llm.Completion{}, err
	}

	var resp struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Model string `json:"model"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return llm.Completion{}, fmt.Errorf("parse completion: %w", err)
	}

	content := ""
	stopReason := ""
	if len(resp.Choices) > 0 {
		content = resp.Choices[0].Message.Content
		stopReason = resp.Choices[0].FinishReason
	}

	return llm.Completion{
		Content:      content,
		StopReason:   stopReason,
		Model:        resp.Model,
		InputTokens:  resp.Usage.PromptTokens,
		OutputTokens: resp.Usage.CompletionTokens,
		LatencyMs:    time.Since(start).Milliseconds(),
	}, nil
}

// Stream sends a streaming chat completion request using SSE.
func (c *Compat) Stream(ctx context.Context, req llm.CompletionRequest) (<-chan llm.Event, error) {
	payload := c.buildPayload(req, true)
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.cfg.BaseURL+"/chat/completions", bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	c.setHeaders(httpReq)

	resp, err := c.client.Do(httpReq)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, fmt.Errorf("provider %s: HTTP %d: %s", c.cfg.ProviderID, resp.StatusCode, b)
	}

	ch := make(chan llm.Event, 32)
	go func() {
		defer close(ch)
		defer resp.Body.Close()
		scanner := bufio.NewScanner(resp.Body)
		for scanner.Scan() {
			line := scanner.Text()
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			payload := strings.TrimPrefix(line, "data: ")
			if payload == "[DONE]" {
				ch <- llm.Event{Type: "done"}
				return
			}
			var chunk struct {
				Choices []struct {
					Delta struct {
						Content string `json:"content"`
					} `json:"delta"`
					FinishReason *string `json:"finish_reason"`
				} `json:"choices"`
			}
			if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
				continue
			}
			for _, choice := range chunk.Choices {
				if choice.Delta.Content != "" {
					ch <- llm.Event{Type: "text_delta", Text: choice.Delta.Content}
				}
				if choice.FinishReason != nil {
					ch <- llm.Event{Type: "done", StopReason: *choice.FinishReason}
				}
			}
		}
		if err := scanner.Err(); err != nil {
			ch <- llm.Event{Type: "error", Err: err}
		}
	}()
	return ch, nil
}

// Embed calls the /v1/embeddings endpoint.
func (c *Compat) Embed(ctx context.Context, req llm.EmbedRequest) ([][]float32, error) {
	payload := map[string]any{
		"model": req.Model,
		"input": req.Input,
	}
	body, err := c.post(ctx, "/embeddings", payload)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Data []struct {
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("parse embeddings: %w", err)
	}
	out := make([][]float32, len(resp.Data))
	for i, d := range resp.Data {
		out[i] = d.Embedding
	}
	return out, nil
}

// HealthCheck sends a lightweight models list request to verify connectivity.
func (c *Compat) HealthCheck(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_, err := c.get(ctx, "/models")
	return err
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func (c *Compat) buildPayload(req llm.CompletionRequest, stream bool) map[string]any {
	model := req.Model
	if model == "" {
		model = c.cfg.DefaultModel
	}
	msgs := make([]map[string]any, len(req.Messages))
	for i, m := range req.Messages {
		msgs[i] = map[string]any{"role": m.Role, "content": m.Content}
	}
	payload := map[string]any{
		"model":    model,
		"messages": msgs,
		"stream":   stream,
	}
	if req.MaxTokens > 0 {
		payload["max_tokens"] = req.MaxTokens
	}
	if req.Temperature > 0 {
		payload["temperature"] = req.Temperature
	}
	if len(req.Tools) > 0 {
		tools := make([]map[string]any, len(req.Tools))
		for i, t := range req.Tools {
			tools[i] = map[string]any{
				"type": "function",
				"function": map[string]any{
					"name":        t.Name,
					"description": t.Description,
					"parameters":  t.InputSchema,
				},
			}
		}
		payload["tools"] = tools
		if req.ToolChoice != "" {
			payload["tool_choice"] = req.ToolChoice
		}
	}
	return payload
}

func (c *Compat) setHeaders(r *http.Request) {
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)
}

func (c *Compat) post(ctx context.Context, path string, payload any) ([]byte, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.BaseURL+path, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	c.setHeaders(req)
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s POST %s: %w", c.cfg.ProviderID, path, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("provider %s: HTTP %d: %s", c.cfg.ProviderID, resp.StatusCode, body)
	}
	return body, nil
}

func (c *Compat) get(ctx context.Context, path string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.cfg.BaseURL+path, nil)
	if err != nil {
		return nil, err
	}
	c.setHeaders(req)
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s GET %s: %w", c.cfg.ProviderID, path, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("provider %s: HTTP %d: %s", c.cfg.ProviderID, resp.StatusCode, body)
	}
	return body, nil
}
