package nodemanifest

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestCanonicalJSON proves the one normal form: keys sorted at EVERY depth,
// ECMAScript number spelling, `<`/`>`/`&` left alone (Go's encoder escapes
// them by default, which would make canonical bytes HTML-dependent), exactly
// one trailing LF and no trailing space on any line.
func TestCanonicalJSON(t *testing.T) {
	t.Run("sorts_keys_at_depth_3", func(t *testing.T) {
		in := map[string]any{
			"z": map[string]any{"z": map[string]any{"z": 1, "a": 2}, "a": 3},
			"a": 4,
		}
		got, err := CanonicalJSON(in)
		if err != nil {
			t.Fatalf("CanonicalJSON: %v", err)
		}
		want := "{\n  \"a\": 4,\n  \"z\": {\n    \"a\": 3,\n    \"z\": {\n      \"a\": 2,\n      \"z\": 1\n    }\n  }\n}\n"
		if string(got) != want {
			t.Fatalf("got\n%s\nwant\n%s", got, want)
		}
	})

	t.Run("numbers", func(t *testing.T) {
		tests := []struct {
			name string
			raw  string
			want string
		}{
			{"1e21", "1e21", "1e+21"},
			{"1e-7", "1e-7", "1e-7"},
			{"one_point_zero", "1.0", "1"},
			{"big_integer", "123456789012345680000", "123456789012345680000"},
			{"negative_zero", "-0", "0"},
			{"1e-6_stays_decimal", "1e-6", "0.000001"},
			{"5e-7_goes_exponential", "5e-7", "5e-7"},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				var v any
				if err := json.Unmarshal([]byte(tt.raw), &v); err != nil {
					t.Fatalf("unmarshal %s: %v", tt.raw, err)
				}
				got, err := CanonicalJSON(v)
				if err != nil {
					t.Fatalf("CanonicalJSON: %v", err)
				}
				if string(got) != tt.want+"\n" {
					t.Fatalf("CanonicalJSON(%s) = %q, want %q", tt.raw, got, tt.want+"\n")
				}
			})
		}
	})

	t.Run("html_characters_unescaped", func(t *testing.T) {
		got, err := CanonicalJSON(map[string]any{"k": "<>&"})
		if err != nil {
			t.Fatalf("CanonicalJSON: %v", err)
		}
		want := "{\n  \"k\": \"<>&\"\n}\n"
		if string(got) != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})

	t.Run("exactly_one_trailing_lf_and_no_trailing_space", func(t *testing.T) {
		got, err := CanonicalJSON(map[string]any{"a": []any{1, map[string]any{"b": 2}}})
		if err != nil {
			t.Fatalf("CanonicalJSON: %v", err)
		}
		s := string(got)
		if !strings.HasSuffix(s, "}\n") || strings.HasSuffix(s, "\n\n") {
			t.Fatalf("trailing newline wrong: %q", s)
		}
		for _, line := range strings.Split(strings.TrimSuffix(s, "\n"), "\n") {
			if line != strings.TrimRight(line, " \t") {
				t.Fatalf("trailing whitespace on %q", line)
			}
		}
	})

	t.Run("refuses_nan_and_inf", func(t *testing.T) {
		for _, v := range []float64{nan(), inf()} {
			if _, err := CanonicalJSON(map[string]any{"n": v}); err == nil {
				t.Fatalf("CanonicalJSON(%v) returned no error", v)
			}
		}
	})
}

func nan() float64 { var z float64; return z / z }
func inf() float64 { var z float64; return 1 / z }
