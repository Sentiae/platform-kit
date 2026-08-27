package nodemanifest

import (
	"encoding/json"
	"testing"
)

// TestStripAnnotations proves the strip is DEEP and non-destructive: nested
// `default`/`description` are cleared inside items and inside properties, and
// the source schema still carries them afterwards (a shallow or in-place
// strip would leave a nested annotation behind, or mutate the manifest).
func TestStripAnnotations(t *testing.T) {
	src := ref(t, `{
		"type":"object",
		"description":"root",
		"properties":{
			"a":{"type":"array","description":"a","items":{"type":"string","default":"x","description":"item"}},
			"b":{"type":"string","default":"y"}
		},
		"required":["a"],
		"additionalProperties":false
	}`)
	before, err := CanonicalJSON(src)
	if err != nil {
		t.Fatalf("CanonicalJSON: %v", err)
	}

	got := StripAnnotations(src)
	if got.Description != "" {
		t.Fatalf("root description = %q, want empty", got.Description)
	}
	if d := got.Properties["a"].Description; d != "" {
		t.Fatalf("properties.a description = %q, want empty", d)
	}
	items := got.Properties["a"].Items
	if items.Description != "" || len(items.Default) != 0 {
		t.Fatalf("nested items kept annotations: %#v", items)
	}
	if len(got.Properties["b"].Default) != 0 {
		t.Fatalf("properties.b kept its default")
	}
	// Positive anchor: the constraints survived the strip.
	if got.Type != "object" || got.Properties["a"].Items.Type != "string" || len(got.Required) != 1 {
		b, _ := json.Marshal(got)
		t.Fatalf("strip removed constraints: %s", b)
	}

	after, err := CanonicalJSON(src)
	if err != nil {
		t.Fatalf("CanonicalJSON: %v", err)
	}
	if string(before) != string(after) {
		t.Fatalf("source mutated:\n%s\n%s", before, after)
	}
}

// TestIsUnconstrained proves `{}` and nil are DIFFERENT verdicts: `{}` is an
// author saying "anything", nil is this client saying "I do not know".
func TestIsUnconstrained(t *testing.T) {
	if !(&TypeRef{}).IsUnconstrained() {
		t.Fatal("{} is not reported unconstrained")
	}
	var nilRef *TypeRef
	if nilRef.IsUnconstrained() {
		t.Fatal("nil reported unconstrained")
	}
	if (&TypeRef{Type: "string"}).IsUnconstrained() {
		t.Fatal("{type:string} reported unconstrained")
	}
	if (&TypeRef{Description: "just words"}).IsUnconstrained() {
		t.Fatal("a described schema reported unconstrained before annotation stripping")
	}
}
