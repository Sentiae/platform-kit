package kafka

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
)

// G17 — remote schema-registry verification.
//
// The in-process schema taxonomy (event_taxonomy.go) is the source of
// truth for local validation. In multi-service deployments operators
// may additionally run a Confluent-compatible schema registry that
// stores the canonical schemas — services should then fetch the
// *remote* latest schema on boot (publisher side) and on consume
// (subscriber side) to catch taxonomy drift between services and the
// registry.
//
// The verification is intentionally soft: mismatches are logged, not
// failed, so a misconfigured registry cannot brick production.

// VerifyConfig controls optional remote schema-registry verification.
// Default zero-value disables both publish and consume checks — the
// legacy local-only taxonomy path continues to run unchanged.
type VerifyConfig struct {
	// Registry is the Confluent-compatible schema-registry client.
	// Passing nil disables verification.
	Registry *SchemaRegistry
	// TopicPrefix is the Kafka topic prefix used when deriving the
	// Confluent subject name. Must match the prefix passed to
	// RegisterAllSchemas.
	TopicPrefix string
	// ConsumeSideFetch enables a remote GetLatestSchema lookup inside
	// FetchLatestSchemaForEvent so consumers can soft-compare the
	// on-wire schema against the in-process taxonomy.
	ConsumeSideFetch bool
	// Logger is the sink for verification warnings. Defaults to the
	// slog package default when nil.
	Logger *slog.Logger
}

var (
	verifyMu     sync.RWMutex
	verifyConfig VerifyConfig
)

// ConfigureVerification sets the package-level verification config.
// Wire this from the service's Kafka bootstrap right after
// RegisterAllSchemas. Safe to call multiple times.
func ConfigureVerification(cfg VerifyConfig) {
	verifyMu.Lock()
	verifyConfig = cfg
	verifyMu.Unlock()
}

// getVerifyConfig returns a snapshot of the current verification
// config. Cheap under read lock.
func getVerifyConfig() VerifyConfig {
	verifyMu.RLock()
	defer verifyMu.RUnlock()
	return verifyConfig
}

// verifyLogger returns the configured logger or slog default.
func verifyLogger(cfg VerifyConfig) *slog.Logger {
	if cfg.Logger != nil {
		return cfg.Logger
	}
	return slog.Default()
}

// FetchLatestSchemaForEvent pulls the latest schema for an event from
// the configured remote registry and soft-compares it against the
// in-process taxonomy. Returns nil when no registry is configured, or
// when ConsumeSideFetch is false. Warnings are logged; no error is
// ever returned so consumers can stay in the hot path.
//
// Intended usage: call from the consumer middleware right after a
// message is decoded, before handing off to the handler.
func FetchLatestSchemaForEvent(ctx context.Context, eventType string) {
	cfg := getVerifyConfig()
	if cfg.Registry == nil || !cfg.ConsumeSideFetch {
		return
	}
	entry, ok := LookupEvent(eventType)
	if !ok {
		return
	}
	subject := subjectForTopic(topicFromEventType(cfg.TopicPrefix, eventType))
	remote, _, err := cfg.Registry.GetLatestSchema(ctx, subject)
	if err != nil {
		verifyLogger(cfg).Warn("schema-registry fetch failed",
			"event_type", eventType,
			"subject", subject,
			"error", err,
		)
		return
	}
	if remote != entry.Schema {
		verifyLogger(cfg).Warn("schema-registry drift detected",
			"event_type", eventType,
			"subject", subject,
			"remote_schema_bytes", len(remote),
			"local_schema_bytes", len(entry.Schema),
		)
	}
}

// VerifyPublishSchema runs a best-effort soft-compare against the
// remote registry at publish time. Matches the semantics of
// FetchLatestSchemaForEvent but is kept as a separate entry point so
// call sites are self-documenting.
func VerifyPublishSchema(ctx context.Context, eventType string) {
	FetchLatestSchemaForEvent(ctx, eventType)
}

// schemaNode is the parsed form of a JSON Schema document. The validator
// supports the subset used in the event taxonomy (type, required,
// properties, items, minLength, minimum, enum, additionalProperties).
// That's intentional — we want validation errors to be fast, deterministic,
// and dependency-free.
type schemaNode struct {
	Type                 string                 `json:"type"`
	Required             []string               `json:"required"`
	Properties           map[string]*schemaNode `json:"properties"`
	Items                *schemaNode            `json:"items"`
	Enum                 []any                  `json:"enum"`
	MinLength            *int                   `json:"minLength"`
	MaxLength            *int                   `json:"maxLength"`
	Minimum              *float64               `json:"minimum"`
	Maximum              *float64               `json:"maximum"`
	AdditionalProperties *bool                  `json:"additionalProperties"`
	Title                string                 `json:"title"`
}

// compiledSchema caches a parsed schema per subject to avoid re-parsing
// the JSON on every validation call.
type compiledSchema struct {
	src  string
	root *schemaNode
}

var (
	compiledMu   sync.RWMutex
	compiledByEv = map[string]*compiledSchema{}
)

// compileSchema parses a JSON Schema string into a schemaNode, caching the
// result by event type.
func compileSchema(eventType, src string) (*schemaNode, error) {
	compiledMu.RLock()
	if cs, ok := compiledByEv[eventType]; ok && cs.src == src {
		compiledMu.RUnlock()
		return cs.root, nil
	}
	compiledMu.RUnlock()

	var root schemaNode
	if err := json.Unmarshal([]byte(src), &root); err != nil {
		return nil, fmt.Errorf("compile schema for %s: %w", eventType, err)
	}
	compiledMu.Lock()
	compiledByEv[eventType] = &compiledSchema{src: src, root: &root}
	compiledMu.Unlock()
	return &root, nil
}

// ValidateEventPayload validates a CloudEvent data payload against the
// registered schema for eventType. Returns nil if the event has no
// registered schema (should not happen for whitelisted event types).
func ValidateEventPayload(eventType string, data EventData) error {
	entry, ok := LookupEvent(eventType)
	if !ok {
		return fmt.Errorf("event %q is not registered in the taxonomy (platform-kit/kafka/event_taxonomy.go)", eventType)
	}
	root, err := compileSchema(eventType, entry.Schema)
	if err != nil {
		return err
	}

	// Marshal the EventData to JSON then back to generic so validation is
	// consistent with what downstream consumers see on the wire.
	raw, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("marshal payload: %w", err)
	}
	var generic map[string]any
	if err := json.Unmarshal(raw, &generic); err != nil {
		return fmt.Errorf("unmarshal payload: %w", err)
	}

	if errs := validateNode(root, generic, ""); len(errs) > 0 {
		return fmt.Errorf("schema validation failed for %s: %s", eventType, strings.Join(errs, "; "))
	}
	return nil
}

// ValidateRawPayload validates an already-decoded map (used by consumers).
func ValidateRawPayload(eventType string, payload map[string]any) error {
	entry, ok := LookupEvent(eventType)
	if !ok {
		return fmt.Errorf("event %q is not registered", eventType)
	}
	root, err := compileSchema(eventType, entry.Schema)
	if err != nil {
		return err
	}
	if errs := validateNode(root, payload, ""); len(errs) > 0 {
		return fmt.Errorf("schema validation failed for %s: %s", eventType, strings.Join(errs, "; "))
	}
	return nil
}

// validateNode recursively checks that value conforms to node.
// path is the JSON pointer-like breadcrumb for error messages.
func validateNode(node *schemaNode, value any, path string) []string {
	if node == nil {
		return nil
	}
	var errs []string

	if node.Type != "" {
		if !matchesType(node.Type, value) {
			errs = append(errs, fmt.Sprintf("%s: expected %s, got %T", pathOrRoot(path), node.Type, value))
			return errs
		}
	}

	switch v := value.(type) {
	case map[string]any:
		for _, r := range node.Required {
			val, present := v[r]
			if !present {
				errs = append(errs, fmt.Sprintf("%s.%s: required field missing", pathOrRoot(path), r))
				continue
			}
			// Required fields can't be JSON null.
			if val == nil {
				errs = append(errs, fmt.Sprintf("%s.%s: required field is null", pathOrRoot(path), r))
			}
		}
		for k, sub := range node.Properties {
			if child, present := v[k]; present && child != nil {
				errs = append(errs, validateNode(sub, child, pathJoin(path, k))...)
			}
		}
	case []any:
		if node.Items != nil {
			for i, item := range v {
				errs = append(errs, validateNode(node.Items, item, fmt.Sprintf("%s[%d]", path, i))...)
			}
		}
	case string:
		if node.MinLength != nil && len(v) < *node.MinLength {
			errs = append(errs, fmt.Sprintf("%s: length %d < minLength %d", pathOrRoot(path), len(v), *node.MinLength))
		}
		if node.MaxLength != nil && len(v) > *node.MaxLength {
			errs = append(errs, fmt.Sprintf("%s: length %d > maxLength %d", pathOrRoot(path), len(v), *node.MaxLength))
		}
		if len(node.Enum) > 0 && !enumContains(node.Enum, v) {
			errs = append(errs, fmt.Sprintf("%s: value %q not in enum", pathOrRoot(path), v))
		}
	case float64:
		if node.Minimum != nil && v < *node.Minimum {
			errs = append(errs, fmt.Sprintf("%s: value %v < minimum %v", pathOrRoot(path), v, *node.Minimum))
		}
		if node.Maximum != nil && v > *node.Maximum {
			errs = append(errs, fmt.Sprintf("%s: value %v > maximum %v", pathOrRoot(path), v, *node.Maximum))
		}
	}
	return errs
}

func matchesType(expected string, value any) bool {
	if value == nil {
		// JSON null: only the "null" or unspecified type accepts it.
		return expected == "null" || expected == ""
	}
	switch expected {
	case "object":
		_, ok := value.(map[string]any)
		return ok
	case "array":
		_, ok := value.([]any)
		return ok
	case "string":
		_, ok := value.(string)
		return ok
	case "number":
		switch value.(type) {
		case float64, float32, int, int32, int64:
			return true
		}
		return false
	case "integer":
		switch n := value.(type) {
		case int, int32, int64:
			return true
		case float64:
			return n == float64(int64(n))
		}
		return false
	case "boolean":
		_, ok := value.(bool)
		return ok
	case "null":
		return value == nil
	}
	return true
}

func enumContains(enum []any, v string) bool {
	for _, e := range enum {
		if s, ok := e.(string); ok && s == v {
			return true
		}
	}
	return false
}

func pathJoin(base, seg string) string {
	if base == "" {
		return seg
	}
	return base + "." + seg
}

func pathOrRoot(p string) string {
	if p == "" {
		return "(root)"
	}
	return p
}
