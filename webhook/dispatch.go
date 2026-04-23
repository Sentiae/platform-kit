// Package webhook — shared outbound HTTP dispatcher.
//
// git-service, notification-service, and work-service all need to POST
// webhook payloads with the same headers, HMAC signature scheme, and
// timeout/TLS handling. The dispatcher lives here so each service only
// has to manage its own persistence (delivery rows, retry queues) and
// can share the on-the-wire semantics.
//
// The dispatcher is transport-only: it takes a Request describing what to
// send, does the POST, and returns a Response describing what happened.
// Persistence, retry scheduling, and delivery-state machinery stay in
// each service — those couple to service schemas and the shared package
// must not leak into them.
package webhook

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Default values used when a Request leaves them unset.
const (
	DefaultTimeout        = 30 * time.Second
	DefaultUserAgent      = "Sentiae-Hookshot/1.0"
	DefaultContentType    = "application/json"
	MaxResponseBodyBytes  = 10 * 1024
	SignatureHeader       = "X-Hub-Signature-256"
	EventHeader           = "X-Sentiae-Event"
	DeliveryIDHeader      = "X-Sentiae-Delivery"
)

// Request describes a single outbound webhook delivery attempt.
//
// Callers should leave URL/Payload required and fill the rest as needed;
// zero values get sensible defaults (see Default* constants).
type Request struct {
	URL         string
	Payload     string
	EventType   string
	DeliveryID  string
	ContentType string        // "json" / "form" / explicit MIME; defaults to application/json
	Secret      string        // HMAC-SHA256 signing key; empty skips signing
	UserAgent   string        // defaults to DefaultUserAgent
	ExtraHeaders map[string]string // merged in after the built-in headers
	Timeout     time.Duration // defaults to DefaultTimeout
}

// Response describes the outcome of a dispatch attempt.
//
// SentHeaders records the headers actually written so callers can persist
// them for audit (git-service stores these on its WebhookDelivery row).
// Body is truncated to MaxResponseBodyBytes so a misbehaving target can't
// blow memory.
type Response struct {
	StatusCode  int
	Body        string
	SentHeaders map[string]string
}

// Dispatch performs one HTTP POST of payload to req.URL and returns the
// response. A non-nil error means the request could not be sent (DNS,
// TCP, timeout). A 4xx/5xx response is NOT an error — the caller inspects
// Response.StatusCode and IsRetryable to decide what to do.
func Dispatch(ctx context.Context, req Request) (*Response, error) {
	if req.URL == "" {
		return nil, fmt.Errorf("webhook: URL is required")
	}

	timeout := req.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	client := &http.Client{Timeout: timeout}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, req.URL, strings.NewReader(req.Payload))
	if err != nil {
		return nil, fmt.Errorf("webhook: build request: %w", err)
	}

	headers := buildHeaders(req)
	for k, v := range headers {
		httpReq.Header.Set(k, v)
	}

	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("webhook: dispatch: %w", err)
	}
	defer resp.Body.Close()

	bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, MaxResponseBodyBytes))
	return &Response{
		StatusCode:  resp.StatusCode,
		Body:        string(bodyBytes),
		SentHeaders: headers,
	}, nil
}

// buildHeaders assembles the outbound header set from Request, including
// the HMAC signature when a Secret is provided. Kept public-adjacent (via
// Dispatch) so the same header contract is guaranteed across services.
func buildHeaders(req Request) map[string]string {
	headers := map[string]string{
		"Content-Type": resolveContentType(req.ContentType),
		"User-Agent":   defaultedString(req.UserAgent, DefaultUserAgent),
	}
	if req.EventType != "" {
		headers[EventHeader] = req.EventType
	}
	if req.DeliveryID != "" {
		headers[DeliveryIDHeader] = req.DeliveryID
	}
	if req.Secret != "" {
		headers[SignatureHeader] = "sha256=" + ComputeHMACSHA256(req.Secret, req.Payload)
	}
	for k, v := range req.ExtraHeaders {
		headers[k] = v
	}
	return headers
}

func resolveContentType(ct string) string {
	switch ct {
	case "", "json":
		return DefaultContentType
	case "form":
		return "application/x-www-form-urlencoded"
	default:
		return ct
	}
}

func defaultedString(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}
