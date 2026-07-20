package kafka

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestConsumersHealthzHandler_NilConsumer(t *testing.T) {
	h := ConsumersHealthzHandler("test-svc", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/healthz/consumers", nil))

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
	var body ConsumersHealthResponse
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Service != "test-svc" {
		t.Errorf("service = %q", body.Service)
	}
	if body.Status != "ok" {
		t.Errorf("status = %q, want ok", body.Status)
	}
}

func TestConsumersHealthzHandler_AggregatesSubscriptions(t *testing.T) {
	cfg := ConsumerConfig{
		Brokers: []string{"localhost:9092"},
		GroupID: "testgroup",
		Topics:  []string{"t1"},
	}
	c, err := NewConsumer(cfg)
	if err != nil {
		t.Fatal(err)
	}
	c.Subscribe("test.event.fired", func(_ context.Context, e CloudEvent) error { return nil })

	h := ConsumersHealthzHandler("test-svc", c)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/healthz/consumers", nil))

	var body ConsumersHealthResponse
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	found := false
	for _, s := range body.Subscriptions {
		if s == "test.event.fired" {
			found = true
		}
	}
	if !found {
		t.Errorf("subscriptions missing test.event.fired: %+v", body.Subscriptions)
	}
}

func TestConsumersHealthzHandler_DeadLetterDegrades(t *testing.T) {
	cfg := ConsumerConfig{
		Brokers: []string{"localhost:9092"},
		GroupID: "dlqgroup",
		Topics:  []string{"dlqtopic"},
	}
	c, err := NewConsumer(cfg)
	if err != nil {
		t.Fatal(err)
	}

	before := testutil.ToFloat64(messagesDeadLetteredTotal.WithLabelValues("dlqtopic", "dlqgroup"))
	c.recordDeadLetter("dlqtopic") // simulate a poison message parked in the DLQ

	// Prometheus counter incremented by exactly 1.
	if got := testutil.ToFloat64(messagesDeadLetteredTotal.WithLabelValues("dlqtopic", "dlqgroup")); got != before+1 {
		t.Errorf("dead-letter counter = %v, want %v", got, before+1)
	}

	h := ConsumersHealthzHandler("test-svc", c)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/healthz/consumers", nil))

	// A parked DLQ message must flip the surface to degraded (503) — never ok.
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status code = %d, want 503 (degraded)", w.Code)
	}
	var body ConsumersHealthResponse
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Status != "degraded" {
		t.Errorf("status = %q, want degraded", body.Status)
	}
	var found bool
	for _, e := range body.Consumers {
		if e.Topic == "dlqtopic" {
			found = true
			if e.MessagesDeadLettered != 1 {
				t.Errorf("messages_dead_lettered = %d, want 1", e.MessagesDeadLettered)
			}
		}
	}
	if !found {
		t.Errorf("healthz entry for dlqtopic missing: %+v", body.Consumers)
	}
}
