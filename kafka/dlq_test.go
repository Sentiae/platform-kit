package kafka

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"
)

// fakePublisher captures Publish calls so AsHandler's end-to-end shape can
// be asserted without a live Kafka broker.
type fakePublisher struct {
	mu       sync.Mutex
	events   []fakePublished
	err      error
	closeErr error
}

type fakePublished struct {
	Type string
	Data EventData
}

func (p *fakePublisher) Publish(_ context.Context, eventType string, data EventData) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.err != nil {
		return p.err
	}
	p.events = append(p.events, fakePublished{Type: eventType, Data: data})
	return nil
}

func (p *fakePublisher) PublishBatch(ctx context.Context, events []Event) error {
	for _, e := range events {
		if err := p.Publish(ctx, e.Type, e.Data); err != nil {
			return err
		}
	}
	return nil
}

func (p *fakePublisher) Close() error { return p.closeErr }
func (p *fakePublisher) EnsureTopics(_ context.Context) error { return nil }

func TestDLQProducer_DefaultSuffix(t *testing.T) {
	p := NewDLQProducer(nil, "")
	if p.suffix != ".dlq" {
		t.Errorf("suffix = %q, want .dlq", p.suffix)
	}
}

func TestDLQProducer_Send_NoPublisher(t *testing.T) {
	p := NewDLQProducer(nil, ".dlq")
	err := p.Send(context.Background(), "sentiae.identity.user",
		"identity.user.registered", EventData{}, 3, errTest)
	if err != nil {
		t.Errorf("Send() with nil publisher should be a no-op, got %v", err)
	}
}

func TestDLQProducer_Send_StampsMetadata(t *testing.T) {
	fp := &fakePublisher{}
	p := NewDLQProducer(fp, ".dlq")

	data := EventData{ResourceType: "user", ResourceID: "u-1"}
	lastErr := errors.New("boom")

	if err := p.Send(context.Background(), "sentiae.identity.user",
		"identity.user.registered", data, 3, lastErr); err != nil {
		t.Fatalf("Send() err = %v", err)
	}

	fp.mu.Lock()
	defer fp.mu.Unlock()
	if len(fp.events) != 1 {
		t.Fatalf("expected 1 event published, got %d", len(fp.events))
	}
	ev := fp.events[0]
	if ev.Type != "identity.user.registered.dlq" {
		t.Errorf("event type = %q, want identity.user.registered.dlq", ev.Type)
	}
	if ev.Data.Metadata["dlq_source_topic"] != "sentiae.identity.user" {
		t.Errorf("dlq_source_topic missing or wrong: %v", ev.Data.Metadata)
	}
	if ev.Data.Metadata["dlq_attempt"] != "3" {
		t.Errorf("dlq_attempt = %v, want 3", ev.Data.Metadata["dlq_attempt"])
	}
	if ev.Data.Metadata["dlq_error"] != "boom" {
		t.Errorf("dlq_error = %v, want boom", ev.Data.Metadata["dlq_error"])
	}
}

func TestDLQProducer_AsHandler_UnwrapsCloudEvent(t *testing.T) {
	fp := &fakePublisher{}
	p := NewDLQProducer(fp, ".dlq")
	handler := p.AsHandler("test")

	// Build a CloudEvent like the real consumer sees.
	data := EventData{
		ResourceType: "user",
		ResourceID:   "u-42",
		Timestamp:    time.Now().UTC(),
	}
	rawData, _ := json.Marshal(data)
	ce := CloudEvent{
		SpecVersion: "1.0",
		ID:          "evt-1",
		Source:      "identity-service",
		Type:        "identity.user.registered",
		Data:        rawData,
	}
	payload, _ := json.Marshal(ce)

	handler(context.Background(), FailedMessage{
		Topic:      "sentiae.identity.user",
		Value:      payload,
		Offset:     99,
		EventType:  "identity.user.registered",
		RetryCount: 3,
		LastError:  errTest,
	})

	fp.mu.Lock()
	defer fp.mu.Unlock()
	if len(fp.events) != 1 {
		t.Fatalf("expected 1 event routed to DLQ, got %d", len(fp.events))
	}
	got := fp.events[0]
	if got.Type != "identity.user.registered.dlq" {
		t.Errorf("DLQ type = %q", got.Type)
	}
	if got.Data.ResourceID != "u-42" {
		t.Errorf("EventData not preserved: %+v", got.Data)
	}
	if got.Data.Metadata["dlq_attempt"] != "3" {
		t.Errorf("retry metadata missing: %+v", got.Data.Metadata)
	}
}

func TestDLQProducer_AsHandler_FallsBackOnBadPayload(t *testing.T) {
	fp := &fakePublisher{}
	p := NewDLQProducer(fp, ".dlq")
	handler := p.AsHandler("")

	handler(context.Background(), FailedMessage{
		Topic:      "sentiae.identity.user",
		Value:      []byte("not-json"),
		EventType:  "identity.user.registered",
		RetryCount: 3,
		LastError:  errTest,
	})

	fp.mu.Lock()
	defer fp.mu.Unlock()
	if len(fp.events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(fp.events))
	}
	if fp.events[0].Type != "identity.user.registered.dlq" {
		t.Errorf("type = %q", fp.events[0].Type)
	}
}

func TestIsDLQTopic(t *testing.T) {
	cases := map[string]bool{
		"sentiae.identity.user.dlq":  true,
		"sentiae.identity.user":      false,
		"sentiae.identity.user-dlq":  true,
		"sentiae.identity.user.dlq.retry": true,
		"":                           false,
	}
	for in, want := range cases {
		if got := IsDLQTopic(in); got != want {
			t.Errorf("IsDLQTopic(%q) = %v, want %v", in, got, want)
		}
	}
}
