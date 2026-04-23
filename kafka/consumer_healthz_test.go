package kafka

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
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
