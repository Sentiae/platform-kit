package kafka

import "context"

// Ensure NoopPublisher implements Publisher.
var _ Publisher = (*NoopPublisher)(nil)

// NoopPublisher is a no-op publisher for use when Kafka is disabled.
type NoopPublisher struct{}

// NewNoopPublisher returns a publisher that silently discards all events.
func NewNoopPublisher() *NoopPublisher {
	return &NoopPublisher{}
}

// Publish does nothing and returns nil.
func (p *NoopPublisher) Publish(_ context.Context, _ string, _ EventData) error {
	return nil
}

// PublishBatch does nothing and returns nil.
func (p *NoopPublisher) PublishBatch(_ context.Context, _ []Event) error {
	return nil
}

// EnsureTopics does nothing and returns nil. The no-op publisher has no
// broker to talk to.
func (p *NoopPublisher) EnsureTopics(_ context.Context) error {
	return nil
}

// Close does nothing and returns nil.
func (p *NoopPublisher) Close() error {
	return nil
}
