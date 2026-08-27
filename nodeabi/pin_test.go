package nodeabi

import "testing"

// TestParsePin proves the pin grammar is the anchored §3.1 regex: exact
// semver, lower-case scope and name, leading `@` mandatory. Each invalid row
// is its own control — it is a spelling a looser regex would accept.
func TestParsePin(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want Pin
		bad  bool
	}{
		{"valid", "@sentiae/webhook-trigger@1.0.0", Pin{Scope: "sentiae", Name: "webhook-trigger", Semver: "1.0.0"}, false},
		{"upper_case_scope", "@Acme/x@1.0.0", Pin{}, true},
		{"short_semver", "@acme/x@1.0", Pin{}, true},
		{"semver_range", "@acme/x@^1.0.0", Pin{}, true},
		{"no_at", "acme/x@1.0.0", Pin{}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParsePin(tt.in)
			if tt.bad {
				if err == nil {
					t.Fatalf("ParsePin(%q) = %#v, want an error", tt.in, got)
				}
				if want := "invalid node pin " + q(tt.in); err.Error() != want {
					t.Fatalf("ParsePin(%q) error = %q, want %q", tt.in, err.Error(), want)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParsePin(%q) error = %v", tt.in, err)
			}
			if got != tt.want {
				t.Fatalf("ParsePin(%q) = %#v, want %#v", tt.in, got, tt.want)
			}
			if got.String() != tt.in {
				t.Fatalf("Pin.String() = %q, want %q", got.String(), tt.in)
			}
		})
	}
}

// TestParseQualifiedName proves the version-less identity parses and that a
// pin literal is NOT a qualified name (the two token classes stay distinct).
func TestParseQualifiedName(t *testing.T) {
	scope, name, err := ParseQualifiedName("@acme/secure-http")
	if err != nil {
		t.Fatalf("ParseQualifiedName error = %v", err)
	}
	if scope != "acme" || name != "secure-http" {
		t.Fatalf("ParseQualifiedName = %q/%q, want acme/secure-http", scope, name)
	}
	if _, _, err := ParseQualifiedName("@acme/secure-http@2.1.0"); err == nil {
		t.Fatal("ParseQualifiedName accepted a pin literal")
	}
	if got := RepoRef(scope, name); got != "acme/secure-http.node" {
		t.Fatalf("RepoRef = %q, want acme/secure-http.node", got)
	}
}
