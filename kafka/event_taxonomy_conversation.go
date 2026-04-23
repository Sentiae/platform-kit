package kafka

// Conversation domain event constants (§10.1 — Eve response rating).
//
// Kept in a sibling file so the main event_taxonomy.go doesn't grow
// unbounded and so conversation-service can evolve its event surface
// without rebasing on the headline registry file.

const (
	// EventConversationFeedbackSubmitted carries a human 👍/👎 rating on
	// an individual assistant (Eve) message. The foundry-service learning
	// loop subscribes to this topic to weight prompt-version quality
	// scores. See conversation-service/internal/adapter/handler/http/
	// message_feedback_handler.go for the producer.
	EventConversationFeedbackSubmitted = "conversation.feedback.submitted"
)

// registerConversationEvents injects the conversation-domain events into
// the shared registry at package init. Must not touch registryMu here —
// init() in event_taxonomy.go builds the registry serially before any
// goroutine can observe it, and this init() runs after because Go
// orders same-package init() functions by file name.
func init() {
	extras := []RegisteredEvent{
		{
			Type:        EventConversationFeedbackSubmitted,
			Domain:      "conversation",
			Description: "A user rated a specific Eve/assistant message (thumbs_up|thumbs_down|neutral).",
			Owner:       "conversation-service",
			Schema: dataSchema(
				"conversation.feedback.submitted",
				[]string{"message_id", "conversation_id", "user_id", "rating"},
				`"message_id":{"type":"string","minLength":1},`+
					`"conversation_id":{"type":"string","minLength":1},`+
					`"user_id":{"type":"string","minLength":1},`+
					`"rating":{"type":"string","enum":["thumbs_up","thumbs_down","neutral"]},`+
					`"note":{"type":"string"},`+
					`"submitted_at":{"type":"string"}`,
			),
		},
	}
	for _, e := range extras {
		registry[e.Type] = e
	}
}
