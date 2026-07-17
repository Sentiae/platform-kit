package kafka

import (
	"testing"
)

func TestIsDLQTopic(t *testing.T) {
	cases := map[string]bool{
		"sentiae.identity.user.dlq":       true,
		"sentiae.identity.user":           false,
		"sentiae.identity.user-dlq":       true,
		"sentiae.identity.user.dlq.retry": true,
		"":                                false,
	}
	for in, want := range cases {
		if got := IsDLQTopic(in); got != want {
			t.Errorf("IsDLQTopic(%q) = %v, want %v", in, got, want)
		}
	}
}
