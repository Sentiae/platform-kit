package kafka

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

// SchemaRegistry is a Confluent-compatible schema registry client.
// It validates event schemas on publish and caches them locally so
// the hot path only hits the registry when a new schema version is
// seen for the first time.
type SchemaRegistry struct {
	baseURL string
	client  *http.Client
	mu      sync.RWMutex
	cache   map[string]int // subject → latest version id
}

// NewSchemaRegistry builds a schema registry client.
func NewSchemaRegistry(baseURL string) *SchemaRegistry {
	return &SchemaRegistry{
		baseURL: baseURL,
		client:  &http.Client{Timeout: 10 * time.Second},
		cache:   make(map[string]int),
	}
}

// RegisterSchema registers a JSON schema for a subject (typically
// the topic name). Returns the schema ID assigned by the registry.
func (r *SchemaRegistry) RegisterSchema(ctx context.Context, subject, schema string) (int, error) {
	body, _ := json.Marshal(map[string]string{
		"schemaType": "JSON",
		"schema":     schema,
	})
	url := fmt.Sprintf("%s/subjects/%s/versions", r.baseURL, subject)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/vnd.schemaregistry.v1+json")
	resp, err := r.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return 0, fmt.Errorf("schema registry: %d %s", resp.StatusCode, string(raw))
	}
	var result struct {
		ID int `json:"id"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return 0, err
	}
	r.mu.Lock()
	r.cache[subject] = result.ID
	r.mu.Unlock()
	return result.ID, nil
}

// GetLatestSchema fetches the latest version for a subject.
func (r *SchemaRegistry) GetLatestSchema(ctx context.Context, subject string) (string, int, error) {
	url := fmt.Sprintf("%s/subjects/%s/versions/latest", r.baseURL, subject)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", 0, err
	}
	resp, err := r.client.Do(req)
	if err != nil {
		return "", 0, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return "", 0, fmt.Errorf("schema registry: %d %s", resp.StatusCode, string(raw))
	}
	var result struct {
		Schema  string `json:"schema"`
		Version int    `json:"version"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return "", 0, err
	}
	return result.Schema, result.Version, nil
}

// CachedVersion returns the cached latest version for a subject,
// or 0 if not cached. Used in the hot path to avoid a round-trip
// when the publisher already knows it's using the current schema.
func (r *SchemaRegistry) CachedVersion(subject string) int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.cache[subject]
}
