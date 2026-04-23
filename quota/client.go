// Package quota is a thin HTTP client for identity-service's
// organization quota check endpoint. Services that create users,
// repositories, or deployments can use it to gate an action with a
// single call before committing expensive work.
//
// This client exists because the quota check is currently only
// exposed over HTTP (no gRPC counterpart yet). When identity-service
// adds the gRPC method this package can be swapped without changing
// callers — or kept as the default HTTP fallback.
package quota

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// Action identifies which quota to check. Must match the `action`
// query-param values accepted by identity-service.
type Action string

const (
	ActionAddUser Action = "add_user"
	ActionAddRepo Action = "add_repo"
	ActionDeploy  Action = "deploy"
	ActionAddSpec Action = "add_spec"
	ActionTestRun Action = "test_run"
)

// Config holds the values needed to construct a Client.
type Config struct {
	BaseURL string        // e.g. "http://identity-service:8080"
	Timeout time.Duration // default: 5s
}

// Client is the HTTP quota client.
type Client struct {
	baseURL string
	http    *http.Client
}

// New builds a Client. An empty BaseURL produces a client whose Check
// method always returns (true, nil) — useful for local dev and
// integration tests where identity-service isn't running.
func New(cfg Config) *Client {
	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = 5 * time.Second
	}
	return &Client{
		baseURL: cfg.BaseURL,
		http:    &http.Client{Timeout: timeout},
	}
}

// Response mirrors the identity-service JSON body.
type Response struct {
	OrganizationID string `json:"organization_id"`
	Action         string `json:"action"`
	Allowed        bool   `json:"allowed"`
}

// Check returns (allowed, err). A transport error or non-2xx response
// surfaces as err; callers decide fail-open vs fail-closed per action.
func (c *Client) Check(ctx context.Context, orgID string, action Action) (bool, error) {
	if c == nil || c.baseURL == "" {
		return true, nil
	}
	if orgID == "" {
		return false, fmt.Errorf("quota: organization id is required")
	}
	u := fmt.Sprintf("%s/api/v1/identity/organizations/%s/quota/check?action=%s",
		c.baseURL, url.PathEscape(orgID), url.QueryEscape(string(action)))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return false, fmt.Errorf("quota: build request: %w", err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return false, fmt.Errorf("quota: do request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return false, fmt.Errorf("quota: identity-service status %d: %s", resp.StatusCode, string(body))
	}

	var envelope struct {
		Data *Response `json:"data"`
	}
	buf, err := io.ReadAll(resp.Body)
	if err != nil {
		return false, fmt.Errorf("quota: read body: %w", err)
	}
	if err := json.Unmarshal(buf, &envelope); err == nil && envelope.Data != nil {
		return envelope.Data.Allowed, nil
	}

	var flat Response
	if err := json.Unmarshal(buf, &flat); err != nil {
		return false, fmt.Errorf("quota: decode body: %w", err)
	}
	return flat.Allowed, nil
}
