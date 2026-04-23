package webhook

import (
	"testing"
	"time"
)

func TestComputeHMACSHA256(t *testing.T) {
	// Well-known HMAC-SHA256 test vector.
	got := ComputeHMACSHA256("secret", "hello")
	want := "88aab3ede8d3adf94d26ab90d3bafd4a2083070c3bcce9c014ee04a443847c0b"
	if got != want {
		t.Errorf("ComputeHMACSHA256 = %q, want %q", got, want)
	}
	if ComputeHMACSHA256("", "") == ComputeHMACSHA256("x", "") {
		t.Error("different secrets must produce different signatures")
	}
}

func TestBackoffDuration(t *testing.T) {
	cases := []struct {
		attempt int
		want    time.Duration
	}{
		{0, 1 * time.Minute},
		{1, 1 * time.Minute},
		{2, 5 * time.Minute},
		{3, 25 * time.Minute},
		{4, 2 * time.Hour},
		{5, 10 * time.Hour},
		{99, 10 * time.Hour},
	}
	for _, c := range cases {
		if got := BackoffDuration(c.attempt); got != c.want {
			t.Errorf("BackoffDuration(%d) = %s, want %s", c.attempt, got, c.want)
		}
	}
}

func TestIsRetryable(t *testing.T) {
	cases := []struct {
		code int
		want bool
	}{
		{200, false}, {201, false}, {204, false},
		{400, false}, {401, false}, {403, false}, {404, false},
		{408, true}, {429, true},
		{500, true}, {502, true}, {503, true}, {504, true},
	}
	for _, c := range cases {
		if got := IsRetryable(c.code); got != c.want {
			t.Errorf("IsRetryable(%d) = %v, want %v", c.code, got, c.want)
		}
	}
}
