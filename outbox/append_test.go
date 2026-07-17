package outbox

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	pkkafka "github.com/sentiae/platform-kit/kafka"
)

// mustPayload marshals an EventData to the JSON bytes an outbox row carries.
func mustPayload(t *testing.T, data pkkafka.EventData) []byte {
	t.Helper()
	b, err := json.Marshal(data)
	if err != nil {
		t.Fatalf("marshal EventData: %v", err)
	}
	return b
}

// TestAppendValidateAtAppend proves the D-162b keystone: validation runs before
// any DB access, so a bad event fails without persisting. The repo is built with
// a nil DB — a rejection path must never reach it; a bug that let it through
// would panic here instead of silently passing.
func TestAppendValidateAtAppend(t *testing.T) {
	repo := NewRepo(nil, nil)
	ctx := context.Background()

	tests := []struct {
		name    string
		msg     Message
		wantErr bool
	}{
		{
			name: "unregistered event type rejected",
			msg: Message{
				Topic: "foo.bar.baz", // syntactically valid, not in the taxonomy
				Key:   "u1",
				Payload: mustPayload(t, pkkafka.EventData{
					ResourceType: "user",
					ResourceID:   "u1",
					Timestamp:    time.Now().UTC(),
				}),
			},
			wantErr: true,
		},
		{
			name: "malformed event type rejected",
			msg: Message{
				Topic:   "notanevent",
				Key:     "u1",
				Payload: mustPayload(t, pkkafka.EventData{ResourceType: "user", ResourceID: "u1", Timestamp: time.Now().UTC()}),
			},
			wantErr: true,
		},
		{
			name: "schema-invalid payload for registered type rejected",
			msg: Message{
				// identity.user.registered requires metadata.email; omit metadata.
				Topic: "identity.user.registered",
				Key:   "u1",
				Payload: mustPayload(t, pkkafka.EventData{
					ResourceType: "user",
					ResourceID:   "u1",
					Timestamp:    time.Now().UTC(),
				}),
			},
			wantErr: true,
		},
		{
			name: "payload that is not valid JSON rejected",
			msg: Message{
				Topic:   "identity.user.updated",
				Key:     "u1",
				Payload: []byte("{not json"),
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := repo.Append(ctx, tt.msg)
			if tt.wantErr && err == nil {
				t.Fatalf("Append(%q) = nil, want error", tt.msg.Topic)
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("Append(%q) = %v, want nil", tt.msg.Topic, err)
			}
		})
	}
}
