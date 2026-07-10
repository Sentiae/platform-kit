package tenantdb

import "testing"

func TestPostureString(t *testing.T) {
	tests := []struct {
		name string
		p    Posture
		want string
	}{
		{"enforced", PostureEnforced, "enforced"},
		{"bypass", PostureBypass, "bypass"},
		{"unknown", Posture(99), "Posture(99)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.p.String(); got != tt.want {
				t.Fatalf("Posture.String() = %q, want %q", got, tt.want)
			}
		})
	}
}
