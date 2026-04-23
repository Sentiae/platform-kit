// Package comments provides the shared client each service calls when
// it wants to create/read comments anchored to a domain entity. §16.3
// — unifies the 4 parallel comment tables (work_comments,
// git_pr_line_comments, incident_comments, deployment_comments) onto
// conversation-service's ChannelTypeContext primitive.
//
// Usage from work-service:
//
//	client := comments.NewClient(os.Getenv("CONVERSATION_SERVICE_URL"))
//	id, err := client.Create(ctx, comments.CreateInput{
//	    ContextType: comments.ContextTypeSpec,
//	    ContextID:   specID.String(),
//	    AuthorID:    userID.String(),
//	    Body:        "Looks good — approved.",
//	})
//
// Services migrate incrementally: new comment paths use this client,
// legacy per-service tables are frozen and eventually deleted after a
// backfill migrates historical rows into context channels.
package comments

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// ContextType enumerates the anchor kinds a comment can reference.
// Matches conversation-service's domain.ContextType so the conversation
// backend can store comments natively.
type ContextType string

const (
	ContextTypeFeature    ContextType = "feature"
	ContextTypeSpec       ContextType = "spec"
	ContextTypeCodeLine   ContextType = "code_line"   // ContextID = "repo:branch:path:line"
	ContextTypeCanvasNode ContextType = "canvas_node"
	ContextTypeIncident   ContextType = "incident"
	ContextTypeDeploy     ContextType = "deploy"
	ContextTypePR         ContextType = "pull_request"
)

// CreateInput is the payload services post to create a comment.
type CreateInput struct {
	ContextType ContextType `json:"context_type"`
	ContextID   string      `json:"context_id"`
	AuthorID    string      `json:"author_id"`
	Body        string      `json:"body"`
	// Mentions is an optional list of @-mentioned user ids. The
	// conversation-service MentionRouter will do the routing fan-out.
	Mentions []string `json:"mentions,omitempty"`
}

// Comment is the shape returned when listing comments for a context.
type Comment struct {
	ID          string      `json:"id"`
	ContextType ContextType `json:"context_type"`
	ContextID   string      `json:"context_id"`
	AuthorID    string      `json:"author_id"`
	Body        string      `json:"body"`
	CreatedAt   time.Time   `json:"created_at"`
	UpdatedAt   time.Time   `json:"updated_at"`
}

// Client is the HTTP client for conversation-service comments.
type Client struct {
	baseURL string
	http    *http.Client
	token   string
}

// NewClient builds a comments client against conversation-service.
// Pass an auth token if the target service enforces API auth.
func NewClient(baseURL string) *Client {
	return &Client{
		baseURL: baseURL,
		http:    &http.Client{Timeout: 5 * time.Second},
	}
}

// WithToken attaches a bearer token to every request.
func (c *Client) WithToken(token string) *Client {
	c.token = token
	return c
}

// Create posts a comment and returns its id. Safe to call with a
// zero-value Client (returns an error). Conversation-service handles
// idempotency + mention routing.
func (c *Client) Create(ctx context.Context, in CreateInput) (string, error) {
	if c == nil || c.baseURL == "" {
		return "", fmt.Errorf("comments client: no baseURL configured")
	}
	body, err := json.Marshal(in)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/api/v1/comments", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("comments create: %d: %s", resp.StatusCode, string(raw))
	}
	var out struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", err
	}
	return out.ID, nil
}

// List returns comments for a (context_type, context_id) anchor.
func (c *Client) List(ctx context.Context, contextType ContextType, contextID string) ([]Comment, error) {
	if c == nil || c.baseURL == "" {
		return nil, fmt.Errorf("comments client: no baseURL configured")
	}
	url := fmt.Sprintf("%s/api/v1/comments?context_type=%s&context_id=%s",
		c.baseURL, contextType, contextID)
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("comments list: %d: %s", resp.StatusCode, string(raw))
	}
	var out struct {
		Items []Comment `json:"items"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return out.Items, nil
}
