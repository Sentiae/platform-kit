package audit_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/sentiae/platform-kit/audit"
	"github.com/sentiae/platform-kit/kafka"
)

// fakePublisher captures published events for assertions.
type fakePublisher struct {
	events  []kafka.Event
	failOn  string
	calls   int
}

func (p *fakePublisher) Publish(_ context.Context, eventType string, data kafka.EventData) error {
	p.calls++
	if p.failOn == eventType {
		return errors.New("boom")
	}
	p.events = append(p.events, kafka.Event{Type: eventType, Data: data})
	return nil
}
func (p *fakePublisher) PublishBatch(_ context.Context, _ []kafka.Event) error { return nil }
func (p *fakePublisher) Close() error                                          { return nil }

func TestRecorder_ValidationErrors(t *testing.T) {
	t.Parallel()
	pub := &fakePublisher{}
	r := audit.NewRecorder(nil, pub, "test-service")

	cases := []struct {
		name   string
		actor  audit.ActorRef
		action string
		target audit.TargetRef
	}{
		{
			name:   "missing_actor_type",
			actor:  audit.ActorRef{ID: "x"},
			action: "x.y",
			target: audit.TargetRef{Type: "u", ID: "1"},
		},
		{
			name:   "missing_actor_id",
			actor:  audit.ActorRef{Type: audit.ActorUser},
			action: "x.y",
			target: audit.TargetRef{Type: "u", ID: "1"},
		},
		{
			name:   "missing_action",
			actor:  audit.ActorRef{Type: audit.ActorUser, ID: "u"},
			action: "",
			target: audit.TargetRef{Type: "u", ID: "1"},
		},
		{
			name:   "missing_target_type",
			actor:  audit.ActorRef{Type: audit.ActorUser, ID: "u"},
			action: "x.y",
			target: audit.TargetRef{ID: "1"},
		},
		{
			name:   "missing_target_id",
			actor:  audit.ActorRef{Type: audit.ActorUser, ID: "u"},
			action: "x.y",
			target: audit.TargetRef{Type: "u"},
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if err := r.Record(context.Background(), tc.actor, tc.action, tc.target, nil); err == nil {
				t.Fatalf("expected validation error")
			}
		})
	}
	if pub.calls != 0 {
		t.Fatalf("publisher should not have been called on validation errors, got %d", pub.calls)
	}
}

func TestRecorder_PublishesEvent(t *testing.T) {
	t.Parallel()
	pub := &fakePublisher{}
	r := audit.NewRecorder(nil, pub, "identity-service")

	actorID := uuid.NewString()
	targetID := uuid.NewString()
	orgID := uuid.NewString()

	err := r.Record(context.Background(),
		audit.ActorRef{Type: audit.ActorUser, ID: actorID, Email: "a@b"},
		"user.suspended",
		audit.TargetRef{Type: "user", ID: targetID, OrganizationID: orgID},
		map[string]any{"reason": "spam"},
	)
	if err != nil {
		t.Fatalf("record failed: %v", err)
	}
	if len(pub.events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(pub.events))
	}
	evt := pub.events[0]
	if evt.Type != audit.EventType {
		t.Fatalf("unexpected event type %q", evt.Type)
	}
	if evt.Data.ActorID != actorID || evt.Data.ResourceID != targetID || evt.Data.OrganizationID != orgID {
		t.Fatalf("unexpected event data: %+v", evt.Data)
	}
	if evt.Data.Metadata["service"] != "identity-service" {
		t.Fatalf("service metadata missing: %+v", evt.Data.Metadata)
	}
	if evt.Data.Metadata["action"] != "user.suspended" {
		t.Fatalf("action metadata missing: %+v", evt.Data.Metadata)
	}
}

func TestRecorder_PublishError(t *testing.T) {
	t.Parallel()
	pub := &fakePublisher{failOn: audit.EventType}
	r := audit.NewRecorder(nil, pub, "ops-service")
	err := r.Record(context.Background(),
		audit.ActorRef{Type: audit.ActorService, ID: "svc"},
		"team.archived",
		audit.TargetRef{Type: "team", ID: "t1", OrganizationID: "o1"},
		nil,
	)
	if err == nil {
		t.Fatal("expected publish error")
	}
}

func TestNoopRecorder(t *testing.T) {
	t.Parallel()
	var r audit.Recorder = audit.Noop{}
	if err := r.Record(context.Background(),
		audit.ActorRef{Type: audit.ActorUser, ID: "u"},
		"x.y",
		audit.TargetRef{Type: "z", ID: "1"},
		nil,
	); err != nil {
		t.Fatalf("noop should not error: %v", err)
	}
}
