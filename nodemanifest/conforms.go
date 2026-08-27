package nodemanifest

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
	"time"
)

var (
	uuidRx = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	uriRx  = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9+.-]*:`)
)

// Conforms reports whether a decoded JSON value satisfies a schema, and why
// not. The reason text is a contract: `config_value_mismatch` and
// `default_mismatch` interpolate it verbatim.
func Conforms(value any, t *TypeRef, defs Defs) (bool, string) {
	return conforms(value, t, defs, "")
}

func conforms(value any, t *TypeRef, defs Defs, path string) (bool, string) {
	if t == nil {
		return true, ""
	}
	fail := func(reason string) (bool, string) {
		if path == "" {
			return false, reason
		}
		return false, fmt.Sprintf("at %q: %s", path, reason)
	}
	if t.Ref != "" {
		target, ok := defs[defName(t.Ref)]
		if !ok {
			return fail(fmt.Sprintf("unresolved $ref %s", t.Ref))
		}
		return conforms(value, target, defs, path)
	}
	if t.Type != "" {
		kind := jsonKind(value)
		switch t.Type {
		case "integer":
			if kind != "number" {
				return fail(fmt.Sprintf("expected %s, got %s", t.Type, kind))
			}
			// Integrality is a property of the double, not of int64: 1e20 has
			// no fractional part and IS an integer, but does not survive an
			// int64 round trip.
			f, _ := value.(float64)
			if math.IsInf(f, 0) || math.IsNaN(f) || math.Trunc(f) != f {
				return fail("expected integer, got number")
			}
		default:
			if kind != t.Type {
				return fail(fmt.Sprintf("expected %s, got %s", t.Type, kind))
			}
		}
	}
	if len(t.Const) > 0 {
		if !rawEquals(t.Const, value) {
			return fail("value differs from const")
		}
	}
	if len(t.Enum) > 0 {
		found := false
		for _, e := range t.Enum {
			if rawEquals(e, value) {
				found = true
				break
			}
		}
		if !found {
			return fail("value is not in enum")
		}
	}
	if t.Format != "" {
		s, ok := value.(string)
		if !ok || !formatOK(t.Format, s) {
			return fail(fmt.Sprintf("invalid %s", t.Format))
		}
	}
	switch t.Type {
	case "array":
		items, ok := value.([]any)
		if !ok {
			break
		}
		for i, item := range items {
			if ok, reason := conforms(item, t.Items, defs, join(path, fmt.Sprintf("items[%d]", i))); !ok {
				return false, reason
			}
		}
	case "object":
		obj, ok := value.(map[string]any)
		if !ok {
			break
		}
		for _, p := range sorted(t.Required) {
			if _, ok := obj[p]; !ok {
				return fail(fmt.Sprintf("missing required property %q", p))
			}
		}
		if t.AdditionalProperties != nil && !*t.AdditionalProperties {
			for _, k := range sortedKeys(obj) {
				if _, ok := t.Properties[k]; !ok {
					return fail(fmt.Sprintf("unexpected property %q", k))
				}
			}
		}
		for _, k := range sortedKeys(obj) {
			sub, ok := t.Properties[k]
			if !ok {
				continue
			}
			if ok, reason := conforms(obj[k], sub, defs, join(path, k)); !ok {
				return false, reason
			}
		}
	}
	return true, ""
}

func join(path, seg string) string {
	if path == "" {
		return seg
	}
	return path + "." + seg
}

func sorted(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}

func sortedKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// defName is the def a `#/$defs/Name` reference names.
func defName(ref string) string {
	return strings.TrimPrefix(ref, "#/$defs/")
}

func jsonKind(v any) string {
	switch v.(type) {
	case nil:
		return "null"
	case bool:
		return "boolean"
	case float64, json.Number:
		return "number"
	case string:
		return "string"
	case map[string]any:
		return "object"
	case []any:
		return "array"
	default:
		return "unknown"
	}
}

// rawEquals compares a schema literal with a decoded value by canonical JSON —
// the one equality this package uses, so `1` and `1.0` never differ.
func rawEquals(raw json.RawMessage, value any) bool {
	var decoded any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return false
	}
	a, err := CanonicalJSON(decoded)
	if err != nil {
		return false
	}
	b, err := CanonicalJSON(value)
	if err != nil {
		return false
	}
	return string(a) == string(b)
}

func formatOK(format, s string) bool {
	switch format {
	case "uuid":
		return uuidRx.MatchString(s)
	case "uri":
		return uriRx.MatchString(s)
	case "date-time":
		_, err := time.Parse(time.RFC3339, s)
		return err == nil
	}
	return false
}
