package types

import (
	"errors"
	"strings"
	"testing"

	"github.com/sentiae/platform-kit/nodemanifest"
)

// TestParseTypeExpr proves the v2 type grammar: the six primitives, one level
// of `[]` on a primitive only, and `<alias>.<DefName>` with a lower-case alias
// and an upper-case def. `any` is refused with its OWN message, because
// "omitted means the pin's constraint" is a different statement from "anything"
// and the author must be told which one they wanted.
func TestParseTypeExpr(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    TypeExpr
		wantErr error
	}{
		{name: "prim", in: "string", want: TypeExpr{Kind: KindPrim, Prim: "string"}},
		{name: "prim_array", in: "string[]", want: TypeExpr{Kind: KindPrim, Prim: "string", Array: true}},
		{name: "ref", in: "hello.Greeting", want: TypeExpr{Kind: KindRef, Alias: "hello", Def: "Greeting"}},
		{name: "any_is_never_spelled", in: "any", wantErr: ErrAnySpelled},
		{name: "upper_case_alias", in: "Hello.Greeting", wantErr: ErrBadType},
		{name: "lower_case_def", in: "hello.greeting", wantErr: ErrBadType},
		{name: "array_of_ref", in: "hello.Greeting[]", wantErr: ErrBadType},
		{name: "def_name_too_long", in: "hello.G" + strings.Repeat("x", 64), wantErr: ErrTypeTooLong},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseTypeExpr(tt.in)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("ParseTypeExpr(%q) error = %v, want %v", tt.in, err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseTypeExpr(%q) error = %v", tt.in, err)
			}
			if got != tt.want {
				t.Fatalf("ParseTypeExpr(%q) = %#v, want %#v", tt.in, got, tt.want)
			}
			// One spelling per meaning: the parse round-trips to its own text.
			if got.String() != tt.in {
				t.Fatalf("String() = %q, want %q", got.String(), tt.in)
			}
		})
	}
}

// TestFromTypeExpr proves the map from a type expression to the schema it
// names — the bridge that lets a `.flow` port line be type-checked against a
// manifest without the file ever DEFINING a type.
func TestFromTypeExpr(t *testing.T) {
	tests := []struct {
		in   string
		want *nodemanifest.TypeRef
	}{
		{"string", &nodemanifest.TypeRef{Type: "string"}},
		{"number", &nodemanifest.TypeRef{Type: "number"}},
		{"integer", &nodemanifest.TypeRef{Type: "integer"}},
		{"boolean", &nodemanifest.TypeRef{Type: "boolean"}},
		{"object", &nodemanifest.TypeRef{Type: "object"}},
		{"array", &nodemanifest.TypeRef{Type: "array"}},
		{"boolean[]", &nodemanifest.TypeRef{Type: "array", Items: &nodemanifest.TypeRef{Type: "boolean"}}},
		{"secure_http.Result", &nodemanifest.TypeRef{Ref: "#/$defs/Result"}},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			expr, err := ParseTypeExpr(tt.in)
			if err != nil {
				t.Fatalf("ParseTypeExpr(%q): %v", tt.in, err)
			}
			got, err := nodemanifest.CanonicalJSON(FromTypeExpr(expr))
			if err != nil {
				t.Fatalf("CanonicalJSON: %v", err)
			}
			want, err := nodemanifest.CanonicalJSON(tt.want)
			if err != nil {
				t.Fatalf("CanonicalJSON: %v", err)
			}
			if string(got) != string(want) {
				t.Fatalf("FromTypeExpr(%q) =\n%s\nwant\n%s", tt.in, got, want)
			}
		})
	}
}
