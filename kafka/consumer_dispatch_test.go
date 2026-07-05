package kafka

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	kafkago "github.com/segmentio/kafka-go"
)

// newDispatchConsumer builds a KafkaConsumer wired for handleFetchedMessage
// unit tests: no reader, DLQ captured, health map ready.
func newDispatchConsumer(dlq DeadLetterFunc, disableSchema bool) *KafkaConsumer {
	return &KafkaConsumer{
		cfg: ConsumerConfig{
			Logger:                  noopLogger(),
			GroupID:                 "test-group",
			MaxRetries:              3,
			DisableSchemaValidation: disableSchema,
			DeadLetterFunc:          dlq,
		},
		handlers: make(map[string]EventHandler),
		health:   make(map[string]*topicHealth),
	}
}

func ceMsg(t *testing.T, topic, eventType string, offset, highWaterMark int64, data string) kafkago.Message {
	t.Helper()
	ce := CloudEvent{
		SpecVersion: "1.0",
		ID:          "evt-1",
		Source:      "test",
		Type:        eventType,
		Data:        json.RawMessage(data),
	}
	payload, err := json.Marshal(ce)
	if err != nil {
		t.Fatalf("marshal cloudevent: %v", err)
	}
	return kafkago.Message{
		Topic:         topic,
		Offset:        offset,
		HighWaterMark: highWaterMark,
		Value:         payload,
	}
}

func TestHandleFetchedMessage_RoutesByType(t *testing.T) {
	c := newDispatchConsumer(nil, true)
	var aCalled, bCalled int32
	c.handlers["type.a"] = func(_ context.Context, _ CloudEvent) error { atomic.AddInt32(&aCalled, 1); return nil }
	c.handlers["type.b"] = func(_ context.Context, _ CloudEvent) error { atomic.AddInt32(&bCalled, 1); return nil }

	c.handleFetchedMessage(context.Background(), ceMsg(t, "topic1", "type.a", 0, 1, `{}`))

	if aCalled != 1 {
		t.Errorf("handler a called %d times, want 1", aCalled)
	}
	if bCalled != 0 {
		t.Errorf("handler b called %d times, want 0", bCalled)
	}
}

func TestHandleFetchedMessage_UnknownType_NoDispatchNoFailure(t *testing.T) {
	var dlqCalled int32
	c := newDispatchConsumer(func(_ context.Context, _ FailedMessage) { atomic.AddInt32(&dlqCalled, 1) }, true)

	c.handleFetchedMessage(context.Background(), ceMsg(t, "topic1", "type.unknown", 0, 1, `{}`))

	if dlqCalled != 0 {
		t.Errorf("dlq called %d times, want 0", dlqCalled)
	}
	if len(c.health) != 0 {
		t.Errorf("health recorded for unknown type: %+v", c.health)
	}
}

func TestHandleFetchedMessage_UnmarshalFailure_ToDLQ(t *testing.T) {
	var mu sync.Mutex
	var captured *FailedMessage
	c := newDispatchConsumer(func(_ context.Context, m FailedMessage) {
		mu.Lock()
		defer mu.Unlock()
		captured = &m
	}, true)
	c.handlers["type.a"] = func(_ context.Context, _ CloudEvent) error { return nil }

	msg := kafkago.Message{Topic: "topic1", Offset: 7, HighWaterMark: 8, Value: []byte("not-json{")}
	c.handleFetchedMessage(context.Background(), msg)

	mu.Lock()
	defer mu.Unlock()
	if captured == nil {
		t.Fatal("expected DLQ capture on unmarshal failure")
	}
	if !strings.Contains(captured.LastError.Error(), "unmarshal") {
		t.Errorf("LastError = %q, want reason containing 'unmarshal'", captured.LastError.Error())
	}
	if captured.Topic != "topic1" {
		t.Errorf("DLQ topic = %q, want topic1", captured.Topic)
	}
}

func TestHandleFetchedMessage_SchemaValidationFailure_ToDLQAndRecordFailure(t *testing.T) {
	if err := RegisterExtensionEvent(RegisteredEvent{
		Type:        "test.dispatch.schema",
		Domain:      "test",
		Description: "dispatch schema test",
		Owner:       "platform-kit",
		Schema: `{
			"type": "object",
			"required": ["user_id"],
			"properties": {"user_id": {"type": "string"}}
		}`,
	}); err != nil {
		t.Fatalf("register schema: %v", err)
	}

	var mu sync.Mutex
	var captured *FailedMessage
	c := newDispatchConsumer(func(_ context.Context, m FailedMessage) {
		mu.Lock()
		defer mu.Unlock()
		captured = &m
	}, false) // schema validation ENABLED
	c.handlers["test.dispatch.schema"] = func(_ context.Context, _ CloudEvent) error { return nil }

	// Payload omits required user_id.
	c.handleFetchedMessage(context.Background(), ceMsg(t, "topic1", "test.dispatch.schema", 3, 4, `{"not_user_id":"oops"}`))

	mu.Lock()
	defer mu.Unlock()
	if captured == nil {
		t.Fatal("expected DLQ capture on schema validation failure")
	}
	if !strings.Contains(captured.LastError.Error(), "schema_validation_failed") {
		t.Errorf("LastError = %q, want 'schema_validation_failed'", captured.LastError.Error())
	}
	h, ok := c.health["topic1"]
	if !ok || h.MessagesFailed != 1 {
		t.Errorf("expected recordFailure on topic1, got %+v", c.health)
	}
}

func TestHandleFetchedMessage_HandlerExhaustsRetries_ToDLQAndRecordFailure(t *testing.T) {
	var mu sync.Mutex
	var captured *FailedMessage
	var attempts int32
	c := newDispatchConsumer(func(_ context.Context, m FailedMessage) {
		mu.Lock()
		defer mu.Unlock()
		captured = &m
	}, true)
	c.handlers["type.a"] = func(_ context.Context, _ CloudEvent) error {
		atomic.AddInt32(&attempts, 1)
		return errTest
	}

	c.handleFetchedMessage(context.Background(), ceMsg(t, "topic1", "type.a", 2, 5, `{}`))

	if attempts != int32(c.cfg.MaxRetries) {
		t.Errorf("handler attempts = %d, want %d", attempts, c.cfg.MaxRetries)
	}
	mu.Lock()
	defer mu.Unlock()
	if captured == nil {
		t.Fatal("expected DLQ capture after retries exhausted")
	}
	if captured.RetryCount != c.cfg.MaxRetries {
		t.Errorf("RetryCount = %d, want %d", captured.RetryCount, c.cfg.MaxRetries)
	}
	h, ok := c.health["topic1"]
	if !ok || h.MessagesFailed != 1 {
		t.Errorf("expected recordFailure on topic1, got %+v", c.health)
	}
}

func TestHandleFetchedMessage_Success_RecordsLagAndTopic(t *testing.T) {
	c := newDispatchConsumer(nil, true)
	c.handlers["type.a"] = func(_ context.Context, _ CloudEvent) error { return nil }

	// Offset 5, HWM 10 → lag = 10 - 5 - 1 = 4.
	c.handleFetchedMessage(context.Background(), ceMsg(t, "topic-x", "type.a", 5, 10, `{}`))

	h, ok := c.health["topic-x"]
	if !ok {
		t.Fatalf("no health recorded, got %+v", c.health)
	}
	if h.MessagesOK != 1 {
		t.Errorf("MessagesOK = %d, want 1", h.MessagesOK)
	}
	if h.Lag != 4 {
		t.Errorf("Lag = %d, want 4", h.Lag)
	}
	if h.Topic != "topic-x" {
		t.Errorf("Topic = %q, want topic-x", h.Topic)
	}
}

func TestHandleFetchedMessage_TwoTopics_SeparateHealthKeys(t *testing.T) {
	c := newDispatchConsumer(nil, true)
	c.handlers["type.a"] = func(_ context.Context, _ CloudEvent) error { return nil }

	c.handleFetchedMessage(context.Background(), ceMsg(t, "topic1", "type.a", 0, 3, `{}`))
	c.handleFetchedMessage(context.Background(), ceMsg(t, "topic2", "type.a", 1, 9, `{}`))

	if h1, ok := c.health["topic1"]; !ok || h1.MessagesOK != 1 || h1.Lag != 2 {
		t.Errorf("topic1 health = %+v, want OK=1 Lag=2", c.health["topic1"])
	}
	if h2, ok := c.health["topic2"]; !ok || h2.MessagesOK != 1 || h2.Lag != 7 {
		t.Errorf("topic2 health = %+v, want OK=1 Lag=7", c.health["topic2"])
	}
}

// --- logger filter -------------------------------------------------------

type captureHandler struct {
	mu      sync.Mutex
	records []slog.Record
}

func (h *captureHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h *captureHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.records = append(h.records, r)
	return nil
}
func (h *captureHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *captureHandler) WithGroup(string) slog.Handler      { return h }

func (h *captureHandler) last() slog.Record {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.records[len(h.records)-1]
}

func TestKafkaLoggerFromSlog_LevelByMessage(t *testing.T) {
	tests := []struct {
		name   string
		format string
		args   []any
		want   slog.Level
	}{
		// Group lifecycle → Info. Formats copied from kafka-go@v0.4.50.
		{"joined group upper", "Joined group %s as member %s in generation %d", []any{"g", "m", 1}, slog.LevelInfo},
		{"joined group lower", "joined group %s as member %s in generation %d", []any{"g", "m", 1}, slog.LevelInfo},
		{"leaving group", "Leaving group %s, member %s", []any{"g", "m"}, slog.LevelInfo},
		{"rebalancing group", "Partition changes found, rebalancing group: %v.", []any{"g"}, slog.LevelInfo},
		{"sync group finished", "sync group finished for group, %v", []any{"g"}, slog.LevelInfo},
		{"received empty assignments", "received empty assignments for group, %v as member %s for generation %d", []any{"g", "m", 1}, slog.LevelInfo},
		{"assigned member/topic/partitions", "assigned member/topic/partitions %v/%v/%v", []any{"m", "t", "0,1"}, slog.LevelInfo},
		{"subscribed to topics and partitions", "subscribed to topics and partitions: %+v", []any{map[string]int{"t": 0}}, slog.LevelInfo},
		// Routine chatter → Debug.
		{"committed offsets", "committed offsets for group %s: \n%s", []any{"g", "report"}, slog.LevelDebug},
		{"started heartbeat", "started heartbeat for group, %v [%v]", []any{"g", "5s"}, slog.LevelDebug},
		{"entering loop", "entering loop for consumer group, %v\n", []any{"g"}, slog.LevelDebug},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := &captureHandler{}
			fn := kafkaLoggerFromSlog(slog.New(h), false)
			fn(tt.format, tt.args...)
			if got := h.last().Level; got != tt.want {
				t.Errorf("level = %v, want %v (msg: %q)", got, tt.want, tt.format)
			}
		})
	}
}

func TestKafkaLoggerFromSlog_ErrorPath(t *testing.T) {
	h := &captureHandler{}
	fn := kafkaLoggerFromSlog(slog.New(h), true)
	fn("Failed to join group %s: %v", "g", errTest)
	if got := h.last().Level; got != slog.LevelError {
		t.Errorf("level = %v, want Error", got)
	}
}

// --- assignment assertion ------------------------------------------------

type fakeDescriber struct {
	mu    sync.Mutex
	resp  *kafkago.DescribeGroupsResponse
	err   error
	calls int32
}

func (f *fakeDescriber) DescribeGroups(_ context.Context, _ *kafkago.DescribeGroupsRequest) (*kafkago.DescribeGroupsResponse, error) {
	atomic.AddInt32(&f.calls, 1)
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.resp, f.err
}

func newAssignmentConsumer(d groupDescriber) *KafkaConsumer {
	return &KafkaConsumer{
		cfg: ConsumerConfig{
			Logger:                 noopLogger(),
			GroupID:                "test-group",
			Topics:                 []string{"t"},
			AssignmentDeadline:     40 * time.Millisecond,
			AssignmentPollInterval: 5 * time.Millisecond,
		},
		handlers:  make(map[string]EventHandler),
		health:    make(map[string]*topicHealth),
		describer: d,
	}
}

func emptyGroupResp(groupID string) *kafkago.DescribeGroupsResponse {
	return &kafkago.DescribeGroupsResponse{
		Groups: []kafkago.DescribeGroupsResponseGroup{{GroupID: groupID}},
	}
}

func assignedGroupResp(groupID string, partitions int) *kafkago.DescribeGroupsResponse {
	ps := make([]int, partitions)
	for i := range ps {
		ps[i] = i
	}
	return &kafkago.DescribeGroupsResponse{
		Groups: []kafkago.DescribeGroupsResponseGroup{{
			GroupID: groupID,
			Members: []kafkago.DescribeGroupsResponseMember{{
				MemberID: "m1",
				MemberAssignments: kafkago.DescribeGroupsResponseAssignments{
					Topics: []kafkago.GroupMemberTopic{{Topic: "t", Partitions: ps}},
				},
			}},
		}},
	}
}

func TestAssertAssignment_ZeroPartitions_SetsError(t *testing.T) {
	c := newAssignmentConsumer(&fakeDescriber{resp: emptyGroupResp("test-group")})
	c.assertAssignment(context.Background())
	if c.AssignmentError() == nil {
		t.Fatal("expected AssignmentError after deadline with zero partitions")
	}
	if !strings.Contains(c.AssignmentError().Error(), "ZERO partitions") {
		t.Errorf("error = %q, want mention of ZERO partitions", c.AssignmentError().Error())
	}
}

func TestAssertAssignment_HasPartitions_NoError(t *testing.T) {
	c := newAssignmentConsumer(&fakeDescriber{resp: assignedGroupResp("test-group", 3)})
	c.assertAssignment(context.Background())
	if err := c.AssignmentError(); err != nil {
		t.Errorf("unexpected AssignmentError: %v", err)
	}
}

func TestAssertAssignment_HealthPrepopulated_ShortCircuits(t *testing.T) {
	fake := &fakeDescriber{resp: emptyGroupResp("test-group")}
	c := newAssignmentConsumer(fake)
	// Simulate a message already processed.
	c.health["t"] = &topicHealth{Topic: "t", MessagesOK: 1}

	c.assertAssignment(context.Background())

	if err := c.AssignmentError(); err != nil {
		t.Errorf("unexpected AssignmentError: %v", err)
	}
	if atomic.LoadInt32(&fake.calls) != 0 {
		t.Errorf("describer called %d times, want 0 (short-circuit)", fake.calls)
	}
}

func TestAssertAssignment_CtxCancel_ExitsClean(t *testing.T) {
	c := newAssignmentConsumer(&fakeDescriber{resp: emptyGroupResp("test-group")})
	c.cfg.AssignmentDeadline = 10 * time.Second // long, so ctx cancel wins
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	done := make(chan struct{})
	go func() {
		c.assertAssignment(ctx)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("assertAssignment did not exit on ctx cancel")
	}
	if err := c.AssignmentError(); err != nil {
		t.Errorf("unexpected AssignmentError on ctx cancel: %v", err)
	}
}
