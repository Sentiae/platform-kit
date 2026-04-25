package kafka

import (
	"strings"
	"testing"

	"github.com/segmentio/kafka-go"
)

func errTopicAlreadyExistsForTest() error { return kafka.TopicAlreadyExists }

// KnownTopics is consumed by EnsureTopics. Guard the registry surface so
// taxonomy additions stay discoverable through the EnsureTopics path.
func TestKnownTopics_RegistryDerived(t *testing.T) {
	got := KnownTopics("sentiae")
	if len(got) == 0 {
		t.Fatal("KnownTopics returned empty list — registry must be initialised at init")
	}

	want := []string{
		"sentiae.chat.message",
		"sentiae.chat.channel",
		"sentiae.foundry.flow",
		"sentiae.flow.step",
		"sentiae.identity.user",
		"sentiae.work.feature",
	}

	have := make(map[string]struct{}, len(got))
	for _, t := range got {
		have[t] = struct{}{}
	}

	for _, w := range want {
		if _, ok := have[w]; !ok {
			t.Errorf("KnownTopics missing %q — every topic in the canonical "+
				"pipeline must be discoverable through KnownTopics so EnsureTopics "+
				"pre-creates it on publisher init", w)
		}
	}

	for _, topic := range got {
		if !strings.HasPrefix(topic, "sentiae.") {
			t.Errorf("KnownTopics returned %q without configured prefix", topic)
		}
	}
}

// Smoke check: TopicAlreadyExists detection used by EnsureTopics' idempotency.
func TestIsTopicAlreadyExists(t *testing.T) {
	if !isTopicAlreadyExists(errTopicAlreadyExistsForTest()) {
		t.Error("isTopicAlreadyExists should return true for kafka.TopicAlreadyExists")
	}
	if isTopicAlreadyExists(nil) {
		t.Error("isTopicAlreadyExists should return false for nil")
	}
}
