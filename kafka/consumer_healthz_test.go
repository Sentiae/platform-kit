package kafka

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
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

// A registered-but-idle consumer must be REPORTED as registered. Before the
// health map was seeded at registration, entries existed only once a message
// had been processed, so a freshly-deployed service returned "consumers": null
// and read to an operator as "no consumers running" — traffic history reported
// as registration.
func TestRegisterTopics_IdleConsumer_ReportedWithZeroCounters(t *testing.T) {
	c, err := NewConsumer(ConsumerConfig{
		Brokers: []string{"localhost:9092"},
		GroupID: "idlegroup",
		Topics:  []string{"t1", "t2"},
	})
	if err != nil {
		t.Fatal(err)
	}
	c.Subscribe("test.event.fired", func(_ context.Context, _ CloudEvent) error { return nil })

	c.registerTopics() // Start does this before the reader exists

	got := map[string]topicHealth{}
	for _, h := range c.Health() {
		got[h.Topic] = h
	}
	for _, topic := range []string{"t1", "t2"} {
		h, ok := got[topic]
		if !ok {
			t.Fatalf("topic %q missing from Health(): %+v", topic, got)
		}
		if h.GroupID != "idlegroup" {
			t.Errorf("topic %q group_id = %q, want idlegroup", topic, h.GroupID)
		}
		if h.MessagesOK != 0 || h.MessagesFailed != 0 || h.MessagesDeadLettered != 0 || h.Lag != 0 || h.LastOffset != 0 {
			t.Errorf("topic %q seeded with non-zero counters: %+v", topic, h)
		}
		// Zero LastProcessed is what distinguishes "registered, idle" from
		// "has processed messages" on the JSON surface (last_commit).
		if !h.LastProcessed.IsZero() {
			t.Errorf("topic %q seeded with non-zero last_processed: %v", topic, h.LastProcessed)
		}
	}

	// And it must reach the wire surface operators actually read.
	w := httptest.NewRecorder()
	ConsumersHealthzHandler("test-svc", c).ServeHTTP(w, httptest.NewRequest("GET", "/healthz/consumers", nil))
	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 (idle is not degraded)", w.Code)
	}
	var body ConsumersHealthResponse
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Consumers) != 2 {
		t.Fatalf("consumers = %+v, want 2 entries", body.Consumers)
	}
	for _, e := range body.Consumers {
		if !e.LastCommit.IsZero() {
			t.Errorf("idle entry %q has non-zero last_commit: %v", e.Topic, e.LastCommit)
		}
	}
}

// An empty consumer list must serialise as [] — a nil slice marshals as null,
// which makes naive clients TypeError rather than see an empty list.
func TestConsumersHealthzHandler_EmptyConsumers_MarshalsAsArrayNotNull(t *testing.T) {
	w := httptest.NewRecorder()
	ConsumersHealthzHandler("test-svc").ServeHTTP(w, httptest.NewRequest("GET", "/healthz/consumers", nil))

	raw := w.Body.String()
	if !strings.Contains(raw, `"consumers":[]`) {
		t.Errorf("body = %s, want \"consumers\":[]", raw)
	}
	if strings.Contains(raw, `"consumers":null`) {
		t.Errorf("consumers serialised as null: %s", raw)
	}
}
