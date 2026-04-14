package foundry

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Client is the SDK for calling foundry-service's unified dispatch API.
// All Sentiae services should use this client instead of hand-rolling HTTP calls.
type Client struct {
	baseURL    string
	httpClient *http.Client
	authToken  string
}

// ClientConfig holds configuration for the foundry client.
type ClientConfig struct {
	BaseURL string        // e.g., "http://foundry-service:8085"
	Timeout time.Duration // HTTP timeout (default: 60s)
}

// NewClient creates a new foundry SDK client.
func NewClient(cfg ClientConfig) *Client {
	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = 60 * time.Second
	}
	return &Client{
		baseURL: cfg.BaseURL,
		httpClient: &http.Client{
			Timeout: timeout,
		},
	}
}

// WithAuth sets the auth token for requests (forwarded from the original caller).
func (c *Client) WithAuth(token string) *Client {
	clone := *c
	clone.authToken = token
	return &clone
}

// Dispatch executes an operation synchronously.
//
// The foundry-service HTTP API wraps responses as {"data": <DispatchResult>}.
// We unwrap that envelope here; if the body is not enveloped (older
// callers / tests), we fall back to decoding the body directly.
func (c *Client) Dispatch(ctx context.Context, req DispatchRequest) (*DispatchResult, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}
	respBody, err := c.doRequest(ctx, http.MethodPost, "/api/v1/foundry/dispatch", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	var enveloped struct {
		Data *DispatchResult `json:"data"`
	}
	if err := json.Unmarshal(respBody, &enveloped); err == nil && enveloped.Data != nil &&
		(enveloped.Data.ID != "" || enveloped.Data.Operation != "" || enveloped.Data.Status != "" || len(enveloped.Data.Data) > 0) {
		return enveloped.Data, nil
	}
	var direct DispatchResult
	if err := json.Unmarshal(respBody, &direct); err != nil {
		return nil, fmt.Errorf("unmarshal response: %w", err)
	}
	return &direct, nil
}

// DispatchAsync enqueues an operation for asynchronous execution.
// Returns the job ID for polling.
func (c *Client) DispatchAsync(ctx context.Context, req DispatchRequest) (string, error) {
	var resp struct {
		Data struct {
			JobID     string `json:"job_id"`
			Operation string `json:"operation"`
			Status    string `json:"status"`
		} `json:"data"`
	}
	if err := c.post(ctx, "/api/v1/foundry/dispatch/async", req, &resp); err != nil {
		return "", err
	}
	return resp.Data.JobID, nil
}

// GetResult polls for an async job's result.
func (c *Client) GetResult(ctx context.Context, jobID string) (*AsyncJob, error) {
	var resp struct {
		Data AsyncJob `json:"data"`
	}
	if err := c.get(ctx, fmt.Sprintf("/api/v1/foundry/jobs/%s", jobID), &resp); err != nil {
		return nil, err
	}
	return &resp.Data, nil
}

// GetCapabilities returns the foundry service's available operations and resources.
func (c *Client) GetCapabilities(ctx context.Context) (*Capabilities, error) {
	var resp struct {
		Data Capabilities `json:"data"`
	}
	if err := c.get(ctx, "/api/v1/foundry/capabilities", &resp); err != nil {
		return nil, err
	}
	return &resp.Data, nil
}

// HealthCheck checks if the foundry service is healthy.
func (c *Client) HealthCheck(ctx context.Context) error {
	_, err := c.doRequest(ctx, http.MethodGet, "/health", nil)
	return err
}

// --- Internal helpers ---

func (c *Client) post(ctx context.Context, path string, body any, result any) error {
	data, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}

	respBody, err := c.doRequest(ctx, http.MethodPost, path, bytes.NewReader(data))
	if err != nil {
		return err
	}

	if result != nil {
		if err := json.Unmarshal(respBody, result); err != nil {
			return fmt.Errorf("unmarshal response: %w", err)
		}
	}
	return nil
}

func (c *Client) get(ctx context.Context, path string, result any) error {
	respBody, err := c.doRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		return err
	}

	if result != nil {
		if err := json.Unmarshal(respBody, result); err != nil {
			return fmt.Errorf("unmarshal response: %w", err)
		}
	}
	return nil
}

func (c *Client) doRequest(ctx context.Context, method, path string, body io.Reader) ([]byte, error) {
	url := c.baseURL + path

	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.authToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.authToken)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("foundry request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 10*1024*1024)) // 10MB limit
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("foundry returned %d: %s", resp.StatusCode, string(respBody))
	}

	return respBody, nil
}
