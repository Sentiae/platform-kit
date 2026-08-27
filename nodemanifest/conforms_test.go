package nodemanifest

import (
	"encoding/json"
	"testing"
)

func ref(t *testing.T, raw string) *TypeRef {
	t.Helper()
	var out TypeRef
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatalf("bad schema literal %s: %v", raw, err)
	}
	return &out
}

func value(t *testing.T, raw string) any {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		t.Fatalf("bad value literal %s: %v", raw, err)
	}
	return v
}

// TestConforms proves the reason texts config_value_mismatch and
// default_mismatch interpolate, including the one that has to distinguish a
// fractional number from an integer and the nested `at "a.b": ` prefix. Each
// row is its own control: the value differs from a conforming one only in the
// fault the row names.
func TestConforms(t *testing.T) {
	defs := Defs{"Greeting": ref(t, `{"type":"object","properties":{"hi":{"type":"string"}},"required":["hi"],"additionalProperties":false}`)}
	tests := []struct {
		name       string
		value      string
		schema     string
		wantOK     bool
		wantReason string
	}{
		{"integer_accepts_whole_number", `1`, `{"type":"integer"}`, true, ""},
		{"integer_refuses_fraction", `1.5`, `{"type":"integer"}`, false, "expected integer, got number"},
		{"integer_accepts_integral_double_past_int64", `1e20`, `{"type":"integer"}`, true, ""},
		{"type_mismatch_names_the_kind", `"x"`, `{"type":"number"}`, false, "expected number, got string"},
		{"null_is_a_kind", `null`, `{"type":"string"}`, false, "expected string, got null"},
		{"enum_accepts_member", `"GET"`, `{"type":"string","enum":["GET","POST"]}`, true, ""},
		{"enum_refuses_stranger", `"PATCH"`, `{"type":"string","enum":["GET","POST"]}`, false, "value is not in enum"},
		{"const_accepts_equal", `3`, `{"type":"integer","const":3}`, true, ""},
		{"const_refuses_other", `4`, `{"type":"integer","const":3}`, false, "value differs from const"},
		{"required_property_missing", `{}`, `{"type":"object","properties":{"a":{"type":"string"}},"required":["a"]}`, false, `missing required property "a"`},
		{"closed_object_refuses_extra", `{"a":"x","b":1}`, `{"type":"object","properties":{"a":{"type":"string"}},"required":["a"],"additionalProperties":false}`, false, `unexpected property "b"`},
		{"format_uuid_ok", `"550e8400-e29b-41d4-a716-446655440000"`, `{"type":"string","format":"uuid"}`, true, ""},
		{"format_uuid_bad", `"nope"`, `{"type":"string","format":"uuid"}`, false, "invalid uuid"},
		{"format_uri_ok", `"https://api.example.net/orders"`, `{"type":"string","format":"uri"}`, true, ""},
		{"format_uri_bad", `"api.example.net/orders"`, `{"type":"string","format":"uri"}`, false, "invalid uri"},
		{"format_date_time_ok", `"2026-08-27T10:00:00Z"`, `{"type":"string","format":"date-time"}`, true, ""},
		{"format_date_time_bad", `"2026-08-27"`, `{"type":"string","format":"date-time"}`, false, "invalid date-time"},
		{"ref_resolves_through_defs", `{"hi":"there"}`, `{"$ref":"#/$defs/Greeting"}`, true, ""},
		{"ref_unresolved", `{}`, `{"$ref":"#/$defs/Missing"}`, false, "unresolved $ref #/$defs/Missing"},
		{"unconstrained_accepts_anything", `{"a":[1,"x",null]}`, `{}`, true, ""},
		{"nested_reason_carries_the_dotted_path", `{"a":{"b":1}}`, `{"type":"object","properties":{"a":{"type":"object","properties":{"b":{"type":"string"}}}}}`, false, `at "a.b": expected string, got number`},
		{"array_item_reason", `["x",2]`, `{"type":"array","items":{"type":"string"}}`, false, `at "items[1]": expected string, got number`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ok, reason := Conforms(value(t, tt.value), ref(t, tt.schema), defs)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v (reason %q), want %v", ok, reason, tt.wantOK)
			}
			if reason != tt.wantReason {
				t.Fatalf("reason = %q, want %q", reason, tt.wantReason)
			}
		})
	}
}
