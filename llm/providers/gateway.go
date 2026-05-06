// Package providers — gateway-backed Provider. Routes every request
// through llm-gateway-service via OpenAI-compatible HTTP. Drop-in
// replacement for foundry-service's direct anthropic/openai clients;
// callers swap one wiring line + every existing call site keeps working.
//
// Internal services authenticate with `Bearer sentiae-internal-<service>`
// so the gateway tags usage events with the originating service.
package providers

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/sentiae/platform-kit/llm"
)

// Gateway implements llm.Provider against llm-gateway-service.
type Gateway struct {
	baseURL     string
	serviceName string
	httpClient  *http.Client
	caps        llm.ProviderCaps
}

// Config wires the Gateway client.
type Config struct {
	// BaseURL is the gateway root, e.g. "http://llm-gateway:8090".
	// Empty = client is inert (every call returns ErrGatewayUnavailable).
	BaseURL string
	// ServiceName is the calling service identity, used in the
	// internal bearer token. e.g. "foundry", "conversation".
	ServiceName string
	// Timeout applies per-request. 0 = 60s default.
	Timeout time.Duration
}

// NewGateway builds the client.
func NewGateway(cfg Config) *Gateway {
	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = 60 * time.Second
	}
	return &Gateway{
		baseURL:     strings.TrimRight(cfg.BaseURL, "/"),
		serviceName: cfg.ServiceName,
		httpClient:  &http.Client{Timeout: timeout},
		caps: llm.ProviderCaps{
			SupportsTools:    true,
			SupportsThinking: true,
			SupportsVision:   false,
			SupportsEmbeds:   true,
			SupportsStream:   true,
		},
	}
}

// ID is the canonical provider key.
func (g *Gateway) ID() string { return "sentiae-gateway" }

// Capabilities reflects the union of capabilities exposed by the
// gateway. Per-model capabilities are honored upstream.
func (g *Gateway) Capabilities() llm.ProviderCaps { return g.caps }

// ListModels hits GET /v1/models. Returns empty when the gateway
// hasn't been configured (BaseURL empty) so catalog sync runs no-op.
func (g *Gateway) ListModels(ctx context.Context) ([]llm.ModelInfo, error) {
	if g.baseURL == "" {
		return nil, nil
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, g.baseURL+"/v1/models", nil)
	if err != nil {
		return nil, err
	}
	g.setAuth(httpReq)
	resp, err := g.httpClient.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("gateway: list models %d %s", resp.StatusCode, string(body))
	}
	var parsed struct {
		Data []struct {
			ID      string `json:"id"`
			Created int64  `json:"created"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, err
	}
	out := make([]llm.ModelInfo, 0, len(parsed.Data))
	for _, m := range parsed.Data {
		out = append(out, llm.ModelInfo{ID: m.ID, CreatedAt: m.Created})
	}
	return out, nil
}

type chatRequestBody struct {
	Model       string        `json:"model"`
	Messages    []llm.Message `json:"messages"`
	MaxTokens   int           `json:"max_tokens,omitempty"`
	Temperature float64       `json:"temperature,omitempty"`
	Stream      bool          `json:"stream,omitempty"`
	Tools       []openAITool  `json:"tools,omitempty"`
	ToolChoice  string        `json:"tool_choice,omitempty"`
}

type openAITool struct {
	Type     string                 `json:"type"`
	Function map[string]interface{} `json:"function"`
}

type chatResponseBody struct {
	ID      string `json:"id"`
	Model   string `json:"model"`
	Choices []struct {
		Message struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
}

// Complete dispatches a non-streaming chat completion.
func (g *Gateway) Complete(ctx context.Context, req llm.CompletionRequest) (llm.Completion, error) {
	if g.baseURL == "" {
		return llm.Completion{}, ErrGatewayUnavailable
	}
	body := chatRequestBody{
		Model:       req.Model,
		Messages:    req.Messages,
		MaxTokens:   req.MaxTokens,
		Temperature: req.Temperature,
		Tools:       toolsToOpenAI(req.Tools),
		ToolChoice:  req.ToolChoice,
	}
	started := time.Now()
	out, err := g.do(ctx, "/v1/chat/completions", body)
	if err != nil {
		return llm.Completion{}, err
	}
	var parsed chatResponseBody
	if err := json.Unmarshal(out, &parsed); err != nil {
		return llm.Completion{}, err
	}
	if len(parsed.Choices) == 0 {
		return llm.Completion{}, errors.New("gateway: empty choices")
	}
	return llm.Completion{
		Content:      parsed.Choices[0].Message.Content,
		StopReason:   parsed.Choices[0].FinishReason,
		Model:        parsed.Model,
		InputTokens:  parsed.Usage.PromptTokens,
		OutputTokens: parsed.Usage.CompletionTokens,
		LatencyMs:    time.Since(started).Milliseconds(),
	}, nil
}

// Stream dispatches a streaming chat completion via SSE.
func (g *Gateway) Stream(ctx context.Context, req llm.CompletionRequest) (<-chan llm.Event, error) {
	if g.baseURL == "" {
		return nil, ErrGatewayUnavailable
	}
	body := chatRequestBody{
		Model:       req.Model,
		Messages:    req.Messages,
		MaxTokens:   req.MaxTokens,
		Temperature: req.Temperature,
		Stream:      true,
	}
	jsonBody, _ := json.Marshal(body)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, g.baseURL+"/v1/chat/completions", bytes.NewReader(jsonBody))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	g.setAuth(httpReq)
	resp, err := g.httpClient.Do(httpReq)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("gateway: stream %d %s", resp.StatusCode, string(b))
	}

	events := make(chan llm.Event, 16)
	go func() {
		defer close(events)
		defer resp.Body.Close()
		scanner := bufio.NewScanner(resp.Body)
		scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
		for scanner.Scan() {
			line := scanner.Text()
			payload, ok := strings.CutPrefix(line, "data: ")
			if !ok {
				continue
			}
			if payload == "[DONE]" {
				events <- llm.Event{Type: "done"}
				return
			}
			var ev struct {
				Choices []struct {
					Delta struct {
						Content string `json:"content"`
					} `json:"delta"`
					FinishReason string `json:"finish_reason"`
				} `json:"choices"`
			}
			if err := json.Unmarshal([]byte(payload), &ev); err != nil {
				continue
			}
			for _, c := range ev.Choices {
				if c.Delta.Content != "" {
					events <- llm.Event{Type: "text_delta", Text: c.Delta.Content}
				}
				if c.FinishReason != "" {
					events <- llm.Event{Type: "done", StopReason: c.FinishReason}
				}
			}
		}
		if err := scanner.Err(); err != nil {
			events <- llm.Event{Type: "error", Err: err}
		}
	}()
	return events, nil
}

type embedRequestBody struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

type embedResponseBody struct {
	Data []struct {
		Index     int       `json:"index"`
		Embedding []float32 `json:"embedding"`
	} `json:"data"`
}

// Embed dispatches a batch embed request.
func (g *Gateway) Embed(ctx context.Context, req llm.EmbedRequest) ([][]float32, error) {
	if g.baseURL == "" {
		return nil, ErrGatewayUnavailable
	}
	body := embedRequestBody{Model: req.Model, Input: req.Input}
	out, err := g.do(ctx, "/v1/embeddings", body)
	if err != nil {
		return nil, err
	}
	var parsed embedResponseBody
	if err := json.Unmarshal(out, &parsed); err != nil {
		return nil, err
	}
	vecs := make([][]float32, len(req.Input))
	for _, d := range parsed.Data {
		if d.Index >= 0 && d.Index < len(vecs) {
			vecs[d.Index] = d.Embedding
		}
	}
	return vecs, nil
}

// HealthCheck pings /healthz.
func (g *Gateway) HealthCheck(ctx context.Context) error {
	if g.baseURL == "" {
		return ErrGatewayUnavailable
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, g.baseURL+"/healthz", nil)
	if err != nil {
		return err
	}
	resp, err := g.httpClient.Do(httpReq)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("gateway: health %d", resp.StatusCode)
	}
	return nil
}

// do is the shared POST + auth helper.
func (g *Gateway) do(ctx context.Context, path string, body any) ([]byte, error) {
	jsonBody, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, g.baseURL+path, bytes.NewReader(jsonBody))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	g.setAuth(httpReq)
	resp, err := g.httpClient.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	out, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("gateway: %d %s", resp.StatusCode, string(out))
	}
	return out, nil
}

func (g *Gateway) setAuth(r *http.Request) {
	if g.serviceName != "" {
		r.Header.Set("Authorization", "Bearer sentiae-internal-"+g.serviceName)
	}
}

// toolsToOpenAI converts canonical ToolDefinition into the
// OpenAI-compat Tool shape the gateway expects.
func toolsToOpenAI(tools []llm.ToolDefinition) []openAITool {
	if len(tools) == 0 {
		return nil
	}
	out := make([]openAITool, 0, len(tools))
	for _, t := range tools {
		var params map[string]interface{}
		if len(t.InputSchema) > 0 {
			_ = json.Unmarshal(t.InputSchema, &params)
		}
		out = append(out, openAITool{
			Type: "function",
			Function: map[string]interface{}{
				"name":        t.Name,
				"description": t.Description,
				"parameters":  params,
			},
		})
	}
	return out
}

// ErrGatewayUnavailable is returned when the client wasn't given a
// BaseURL — calls degrade gracefully so dev environments without
// the gateway running don't crash.
var ErrGatewayUnavailable = errors.New("gateway: BaseURL not configured")

var _ llm.Provider = (*Gateway)(nil)
