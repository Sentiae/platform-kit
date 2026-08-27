package nodemanifest

import "testing"

// TestEgress proves the pattern grammar and the matcher: `*.domain.tld` admits
// exactly one extra label — never the apex it looks like a superset of, and
// never a deeper name — which is the whole security value of the wildcard.
func TestEgress(t *testing.T) {
	t.Run("patterns", func(t *testing.T) {
		tests := []struct {
			pattern string
			valid   bool
		}{
			{"*", true},
			{"*.example.com", true},
			{"api.example.net", true},
			{"example", true},
			{"a-b.example.com", true},
			{"", false},
			{"*.com", false},
			{"Example.com", false},
			{"example.com:443", false},
			{"https://example.com", false},
			{"example.com/path", false},
			{"10.0.0.1", false},
			{"*.*.example.com", false},
			{"exa mple.com", false},
			{"-example.com", false},
			{"example-.com", false},
			{"[::1]", false},
		}
		for _, tt := range tests {
			t.Run(tt.pattern, func(t *testing.T) {
				err := ValidateEgressPattern(tt.pattern)
				if tt.valid && err != nil {
					t.Fatalf("ValidateEgressPattern(%q) = %v, want nil", tt.pattern, err)
				}
				if !tt.valid {
					if err == nil {
						t.Fatalf("ValidateEgressPattern(%q) = nil, want an error", tt.pattern)
					}
					if want := "egress pattern " + q(tt.pattern) + " is invalid"; err.Error() != want {
						t.Fatalf("error = %q, want %q", err.Error(), want)
					}
				}
			})
		}
	})

	t.Run("match", func(t *testing.T) {
		tests := []struct {
			name     string
			patterns []string
			host     string
			want     bool
		}{
			{"wildcard_one_label", []string{"*.example.com"}, "a.example.com", true},
			{"wildcard_not_apex", []string{"*.example.com"}, "example.com", false},
			{"wildcard_not_deeper", []string{"*.example.com"}, "a.b.example.com", false},
			{"exact_matches", []string{"api.example.net"}, "api.example.net", true},
			{"exact_only", []string{"api.example.net"}, "other.example.net", false},
			{"star_matches_anything", []string{"*"}, "whatever.internal", true},
			{"empty_allowlist_denies", nil, "api.example.net", false},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				if got := MatchEgress(tt.patterns, tt.host); got != tt.want {
					t.Fatalf("MatchEgress(%v, %q) = %v, want %v", tt.patterns, tt.host, got, tt.want)
				}
			})
		}
	})
}
