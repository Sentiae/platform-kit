package testutil

import (
	"context"
	"testing"
	"time"

	tckafka "github.com/testcontainers/testcontainers-go/modules/kafka"
)

// KafkaResult holds connection details for a test Kafka container.
type KafkaResult struct {
	// Brokers is the list of broker addresses (typically one element).
	Brokers []string
}

// NewTestKafka starts a temporary Kafka (KRaft mode) container and returns
// its broker addresses. The container is terminated via t.Cleanup.
//
// Example:
//
//	k := testutil.NewTestKafka(t)
//	writer := &kafka.Writer{Addr: kafka.TCP(k.Brokers...)}
func NewTestKafka(t *testing.T) KafkaResult {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	ctr, err := tckafka.Run(ctx,
		"confluentinc/confluent-local:7.5.0",
		tckafka.WithClusterID("testutil-cluster"),
	)
	if err != nil {
		t.Fatalf("testutil.NewTestKafka: start kafka container: %v", err)
	}

	t.Cleanup(func() {
		if err := ctr.Terminate(context.Background()); err != nil {
			t.Logf("testutil.NewTestKafka: terminate kafka container: %v", err)
		}
	})

	brokers, err := ctr.Brokers(ctx)
	if err != nil {
		t.Fatalf("testutil.NewTestKafka: get brokers: %v", err)
	}

	return KafkaResult{Brokers: brokers}
}
