package nodemanifest

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

const corpusDir = "../flowlang/testdata"

func readFixture(t *testing.T, rel string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(corpusDir, rel))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return b
}

// TestFixtures_Corpus_Canonical proves two things about the ONE golden corpus:
// every manifest in it is publishable AND already in canonical bytes, and every
// JSON document in the corpus (manifests, ABI documents, diagnostic sidecars,
// the assignability table) is canonical too — so a hand edit that re-indents a
// fixture is caught here as well as by the fixture hash.
func TestFixtures_Corpus_Canonical(t *testing.T) {
	manifests, err := filepath.Glob(filepath.Join(corpusDir, "manifests", "*.json"))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	sort.Strings(manifests)
	if len(manifests) < 4 {
		t.Fatalf("corpus has %d manifests, want at least 4", len(manifests))
	}
	for _, f := range manifests {
		t.Run("manifest/"+filepath.Base(f), func(t *testing.T) {
			b, err := os.ReadFile(f)
			if err != nil {
				t.Fatalf("read: %v", err)
			}
			m, problems := Load(b)
			if len(problems) != 0 {
				for _, p := range problems {
					t.Errorf("%s %s: %s", p.Path, p.Code, p.Message)
				}
				t.Fatalf("fixture is not publishable")
			}
			canonical, err := Canonicalize(b)
			if err != nil {
				t.Fatalf("Canonicalize: %v", err)
			}
			if string(canonical) != string(b) {
				t.Fatalf("fixture is not canonical bytes")
			}
			// The file name IS the pin: <scope>__<name>@<semver>.json.
			base := strings.TrimSuffix(filepath.Base(f), ".json")
			at := strings.LastIndex(base, "@")
			if at < 0 {
				t.Fatalf("fixture name %q carries no @semver", base)
			}
			if want := m.Scope() + "__" + m.PackageName(); base[:at] != want {
				t.Fatalf("file name identity %q, manifest says %q", base[:at], want)
			}
		})
	}

	var others []string
	for _, pattern := range []string{"abi/*.json", "assignable.json", "*.diag.json"} {
		found, err := filepath.Glob(filepath.Join(corpusDir, pattern))
		if err != nil {
			t.Fatalf("glob %s: %v", pattern, err)
		}
		others = append(others, found...)
	}
	sort.Strings(others)
	if len(others) < 13 {
		t.Fatalf("corpus has %d non-manifest JSON documents, want at least 13", len(others))
	}
	for _, f := range others {
		t.Run("json/"+filepath.Base(f), func(t *testing.T) {
			b, err := os.ReadFile(f)
			if err != nil {
				t.Fatalf("read: %v", err)
			}
			canonical, err := Canonicalize(b)
			if err != nil {
				t.Fatalf("Canonicalize: %v", err)
			}
			if string(canonical) != string(b) {
				t.Fatalf("fixture is not canonical bytes")
			}
		})
	}
}

func decodeManifest(t *testing.T, rel string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(readFixture(t, rel), &m); err != nil {
		t.Fatalf("decode %s: %v", rel, err)
	}
	return m
}

func deepCopy(t *testing.T, m map[string]any) map[string]any {
	t.Helper()
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("copy: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("copy: %v", err)
	}
	return out
}

// at walks a decoded manifest to the object holding the last segment.
func at(t *testing.T, m map[string]any, path ...string) map[string]any {
	t.Helper()
	cur := m
	for _, seg := range path {
		next, ok := cur[seg].(map[string]any)
		if !ok {
			t.Fatalf("path %v: %q is not an object", path, seg)
		}
		cur = next
	}
	return cur
}

// TestValidate_Codes proves every publication code is reachable, at the pointer
// and with the message the contract pins. Each row IS its own control: it
// mutates a manifest that validates clean into one that does not, in exactly
// one place, so deleting the rule the row names turns that row red.
func TestValidate_Codes(t *testing.T) {
	secureHTTP := decodeManifest(t, "manifests/acme__secure-http@2.1.0.json")
	respond := decodeManifest(t, "manifests/sentiae__respond@1.0.0.json")
	trigger := decodeManifest(t, "manifests/sentiae__webhook-trigger@1.0.0.json")

	// Positive anchor: the bases are clean, so every finding below is the row's.
	for name, base := range map[string]map[string]any{"secure-http": secureHTTP, "respond": respond, "webhook-trigger": trigger} {
		b, err := json.Marshal(base)
		if err != nil {
			t.Fatalf("%s: marshal: %v", name, err)
		}
		if _, problems := Load(b); len(problems) != 0 {
			t.Fatalf("%s base is not clean: %+v", name, problems)
		}
	}

	tests := []struct {
		name     string
		raw      string
		base     map[string]any
		mutate   func(m map[string]any)
		wantCode string
		wantPath string
		wantMsg  string
	}{
		{name: "json_invalid", raw: `{`, wantCode: CodeJSONInvalid, wantPath: ""},
		{name: "not_object", raw: `[]`, wantCode: CodeNotObject, wantPath: "", wantMsg: msgNotObject},
		{name: "type_mismatch", base: secureHTTP, mutate: func(m map[string]any) { m["category"] = float64(5) },
			wantCode: CodeTypeMismatch, wantPath: "/category", wantMsg: "expected string"},
		{name: "missing_key", base: secureHTTP, mutate: func(m map[string]any) { delete(m, "shape") },
			wantCode: CodeMissingKey, wantPath: "/shape", wantMsg: msgMissingKey},
		{name: "keyword_unknown", base: secureHTTP, mutate: func(m map[string]any) { m["extra"] = 1 },
			wantCode: CodeKeywordUnknown, wantPath: "/extra", wantMsg: `unknown keyword "extra"`},
		{name: "schema_url", base: secureHTTP, mutate: func(m map[string]any) { m["$schema"] = "https://example.com/other.json" },
			wantCode: CodeSchemaURL, wantPath: "/$schema", wantMsg: msgSchemaURL},
		{name: "name_invalid", base: secureHTTP, mutate: func(m map[string]any) { m["name"] = "@Acme/secure-http" },
			wantCode: CodeNameInvalid, wantPath: "/name", wantMsg: msgNameInvalid},
		{name: "category_invalid", base: secureHTTP, mutate: func(m map[string]any) { m["category"] = "nope" },
			wantCode: CodeCategoryInvalid, wantPath: "/category", wantMsg: msgCategoryInvalid},
		{name: "role_invalid", base: secureHTTP, mutate: func(m map[string]any) { m["role"] = "boss" },
			wantCode: CodeRoleInvalid, wantPath: "/role", wantMsg: msgRoleInvalid},
		{name: "display_name_required", base: secureHTTP, mutate: func(m map[string]any) { at(t, m, "display")["name"] = "" },
			wantCode: CodeDisplayNameRequired, wantPath: "/display/name", wantMsg: msgDisplayNameRequired},
		{name: "def_name_invalid", base: secureHTTP, mutate: func(m map[string]any) {
			defs := at(t, m, "$defs")
			defs["result"] = defs["Result"]
			delete(defs, "Result")
		}, wantCode: CodeDefNameInvalid, wantPath: "/$defs/result", wantMsg: msgDefNameInvalid},
		{name: "type_invalid", base: secureHTTP, mutate: func(m map[string]any) {
			at(t, m, "config", "properties", "url")["type"] = "text"
		}, wantCode: CodeTypeInvalid, wantPath: "/config/properties/url/type", wantMsg: msgTypeInvalid},
		{name: "ref_invalid", base: secureHTTP, mutate: func(m map[string]any) {
			m["outputs"].([]any)[0].(map[string]any)["schema"] = map[string]any{"$ref": "#/defs/Result"}
		}, wantCode: CodeRefInvalid, wantPath: "/outputs/0/schema/$ref", wantMsg: msgRefInvalid},
		{name: "ref_unresolved", base: secureHTTP, mutate: func(m map[string]any) {
			m["outputs"].([]any)[0].(map[string]any)["schema"] = map[string]any{"$ref": "#/$defs/Missing"}
		}, wantCode: CodeRefUnresolved, wantPath: "/outputs/0/schema/$ref", wantMsg: `$ref "#/$defs/Missing" does not resolve in $defs`},
		{name: "format_invalid", base: secureHTTP, mutate: func(m map[string]any) {
			at(t, m, "config", "properties", "url")["format"] = "email"
		}, wantCode: CodeFormatInvalid, wantPath: "/config/properties/url/format", wantMsg: msgFormatInvalid},
		{name: "format_without_string", base: secureHTTP, mutate: func(m map[string]any) {
			at(t, m, "config", "properties", "retries")["format"] = "uuid"
		}, wantCode: CodeFormatWithoutString, wantPath: "/config/properties/retries/format", wantMsg: msgFormatWithoutString},
		{name: "items_without_array", base: secureHTTP, mutate: func(m map[string]any) {
			at(t, m, "config", "properties", "url")["items"] = map[string]any{"type": "string"}
		}, wantCode: CodeItemsWithoutArray, wantPath: "/config/properties/url/items", wantMsg: msgItemsWithoutArray},
		{name: "object_keyword_without_object", base: secureHTTP, mutate: func(m map[string]any) {
			at(t, m, "config", "properties", "url")["additionalProperties"] = false
		}, wantCode: CodeObjectKeywordWithoutObject, wantPath: "/config/properties/url/additionalProperties", wantMsg: `"additionalProperties" requires type: object`},
		{name: "required_not_property", base: secureHTTP, mutate: func(m map[string]any) {
			at(t, m, "config")["required"] = []any{"ghost"}
		}, wantCode: CodeRequiredNotProperty, wantPath: "/config/required", wantMsg: `required entry "ghost" is not a property`},
		{name: "additional_properties_not_bool", base: secureHTTP, mutate: func(m map[string]any) {
			at(t, m, "config", "properties", "headers")["additionalProperties"] = map[string]any{"type": "string"}
		}, wantCode: CodeAdditionalPropertiesNotBool, wantPath: "/config/properties/headers/additionalProperties", wantMsg: msgAdditionalPropertiesNotBool},
		{name: "enum_empty", base: secureHTTP, mutate: func(m map[string]any) {
			p := at(t, m, "config", "properties", "method")
			p["enum"] = []any{}
			delete(p, "default")
		}, wantCode: CodeEnumEmpty, wantPath: "/config/properties/method/enum", wantMsg: msgEnumEmpty},
		{name: "enum_duplicate", base: secureHTTP, mutate: func(m map[string]any) {
			at(t, m, "config", "properties", "method")["enum"] = []any{"GET", "POST", "GET"}
		}, wantCode: CodeEnumDuplicate, wantPath: "/config/properties/method/enum", wantMsg: msgEnumDuplicate},
		{name: "default_outside_config", base: secureHTTP, mutate: func(m map[string]any) {
			m["inputs"].([]any)[0].(map[string]any)["schema"] = map[string]any{"type": "object", "default": map[string]any{}}
		}, wantCode: CodeDefaultOutsideConfig, wantPath: "/inputs/0/schema/default", wantMsg: msgDefaultOutsideConfig},
		{name: "default_outside_config_root", base: secureHTTP, mutate: func(m map[string]any) {
			at(t, m, "config")["default"] = map[string]any{"url": "https://example.com"}
		}, wantCode: CodeDefaultOutsideConfig, wantPath: "/config/default", wantMsg: msgDefaultOutsideConfig},
		{name: "default_mismatch", base: secureHTTP, mutate: func(m map[string]any) {
			at(t, m, "config", "properties", "retries")["default"] = "x"
		}, wantCode: CodeDefaultMismatch, wantPath: "/config/properties/retries/default", wantMsg: "default does not conform: expected integer, got string"},
		{name: "config_not_object", base: secureHTTP, mutate: func(m map[string]any) {
			m["config"] = map[string]any{"type": "string", "additionalProperties": false}
		}, wantCode: CodeConfigNotObject, wantPath: "/config", wantMsg: msgConfigNotObject},
		{name: "config_not_closed", base: secureHTTP, mutate: func(m map[string]any) {
			delete(at(t, m, "config"), "additionalProperties")
		}, wantCode: CodeConfigNotClosed, wantPath: "/config", wantMsg: msgConfigNotClosed},
		{name: "port_name_invalid", base: secureHTTP, mutate: func(m map[string]any) {
			m["inputs"].([]any)[0].(map[string]any)["name"] = "Payload"
		}, wantCode: CodePortNameInvalid, wantPath: "/inputs/0/name", wantMsg: msgPortNameInvalid},
		{name: "port_duplicate", base: secureHTTP, mutate: func(m map[string]any) {
			first := m["inputs"].([]any)[0]
			m["inputs"] = []any{first, first}
		}, wantCode: CodePortDuplicate, wantPath: "/inputs/1/name", wantMsg: `duplicate port name "payload"`},
		{name: "trigger_has_inputs", base: trigger, mutate: func(m map[string]any) {
			m["inputs"] = []any{map[string]any{"name": "extra", "required": false, "schema": map[string]any{}}}
		}, wantCode: CodeTriggerHasInputs, wantPath: "/inputs", wantMsg: msgTriggerHasInputs},
		{name: "respond_outputs_shape", base: respond, mutate: func(m map[string]any) {
			m["outputs"].([]any)[0].(map[string]any)["name"] = "reply"
		}, wantCode: CodeRespondOutputsShape, wantPath: "/outputs", wantMsg: msgRespondOutputsShape},
		{name: "respond_no_inputs", base: respond, mutate: func(m map[string]any) { m["inputs"] = []any{} },
			wantCode: CodeRespondNoInputs, wantPath: "/inputs", wantMsg: msgRespondNoInputs},
		{name: "implementations_empty", base: secureHTTP, mutate: func(m map[string]any) { m["implementations"] = map[string]any{} },
			wantCode: CodeImplementationsEmpty, wantPath: "/implementations", wantMsg: msgImplementationsEmpty},
		{name: "implementation_unknown", base: secureHTTP, mutate: func(m map[string]any) {
			at(t, m, "implementations")["rust"] = map[string]any{"entry": "rust/node.rs", "lockfiles": []any{"rust/Cargo.lock"}}
		}, wantCode: CodeImplementationUnknown, wantPath: "/implementations/rust", wantMsg: `unknown implementation "rust"`},
		{name: "implementation_entry", base: secureHTTP, mutate: func(m map[string]any) {
			at(t, m, "implementations", "go")["entry"] = "go/main.go"
		}, wantCode: CodeImplementationEntry, wantPath: "/implementations/go/entry", wantMsg: `entry must be "go/node.go"`},
		{name: "implementation_lockfiles", base: secureHTTP, mutate: func(m map[string]any) {
			at(t, m, "implementations", "go")["lockfiles"] = []any{"go/go.mod"}
		}, wantCode: CodeImplementationLockfiles, wantPath: "/implementations/go/lockfiles", wantMsg: `lockfiles must be ["go/go.mod", "go/go.sum"]`},
		{name: "egress_pattern_invalid", base: secureHTTP, mutate: func(m map[string]any) {
			at(t, m, "capabilities")["egress"] = []any{"*.com"}
		}, wantCode: CodeEgressPatternInvalid, wantPath: "/capabilities/egress/0", wantMsg: `egress pattern "*.com" is invalid`},
		{name: "resources_invalid", base: secureHTTP, mutate: func(m map[string]any) {
			at(t, m, "resources")["memoryMiB"] = float64(8)
		}, wantCode: CodeResourcesInvalid, wantPath: "/resources", wantMsg: msgResourcesInvalid},
		{name: "secret_name_invalid", base: secureHTTP, mutate: func(m map[string]any) {
			m["secrets"].([]any)[0].(map[string]any)["name"] = "API_TOKEN"
		}, wantCode: CodeSecretNameInvalid, wantPath: "/secrets/0/name", wantMsg: msgSecretNameInvalid},
		{name: "secret_duplicate", base: secureHTTP, mutate: func(m map[string]any) {
			first := m["secrets"].([]any)[0]
			m["secrets"] = []any{first, first}
		}, wantCode: CodeSecretDuplicate, wantPath: "/secrets/1/name", wantMsg: `duplicate secret name "api_token"`},
		{name: "shape_invalid", base: secureHTTP, mutate: func(m map[string]any) { m["shape"] = "weird" },
			wantCode: CodeShapeInvalid, wantPath: "/shape", wantMsg: msgShapeInvalid},
	}

	seen := map[string]bool{}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var raw []byte
			if tt.raw != "" {
				raw = []byte(tt.raw)
			} else {
				doc := deepCopy(t, tt.base)
				tt.mutate(doc)
				b, err := json.Marshal(doc)
				if err != nil {
					t.Fatalf("marshal: %v", err)
				}
				raw = b
			}
			_, problems := Load(raw)
			var hit *Problem
			for i := range problems {
				if problems[i].Code == tt.wantCode && problems[i].Path == tt.wantPath {
					hit = &problems[i]
					break
				}
			}
			if hit == nil {
				t.Fatalf("no %s at %q; got %+v", tt.wantCode, tt.wantPath, problems)
			}
			if tt.wantMsg != "" && hit.Message != tt.wantMsg {
				t.Fatalf("message = %q, want %q", hit.Message, tt.wantMsg)
			}
			if want := tt.wantCode + " at " + tt.wantPath + ": " + hit.Message; hit.Error() != want {
				t.Fatalf("Error() = %q, want %q", hit.Error(), want)
			}
			if !sortedProblems(problems) {
				t.Fatalf("problems are not sorted by (Path, Code): %+v", problems)
			}
		})
		seen[tt.wantCode] = true
	}

	// Positive anchor: the table names every code this package can emit.
	for _, code := range allCodes {
		if !seen[code] {
			t.Fatalf("no row for code %q", code)
		}
	}
}

var allCodes = []string{
	CodeJSONInvalid, CodeNotObject, CodeTypeMismatch, CodeMissingKey, CodeKeywordUnknown,
	CodeSchemaURL, CodeNameInvalid, CodeCategoryInvalid, CodeRoleInvalid, CodeDisplayNameRequired,
	CodeDefNameInvalid, CodeTypeInvalid, CodeRefInvalid, CodeRefUnresolved, CodeFormatInvalid,
	CodeFormatWithoutString, CodeItemsWithoutArray, CodeObjectKeywordWithoutObject,
	CodeRequiredNotProperty, CodeAdditionalPropertiesNotBool, CodeEnumEmpty, CodeEnumDuplicate,
	CodeDefaultOutsideConfig, CodeDefaultMismatch, CodeConfigNotObject, CodeConfigNotClosed,
	CodePortNameInvalid, CodePortDuplicate, CodeTriggerHasInputs, CodeRespondOutputsShape,
	CodeRespondNoInputs, CodeImplementationsEmpty, CodeImplementationUnknown, CodeImplementationEntry,
	CodeImplementationLockfiles, CodeEgressPatternInvalid, CodeResourcesInvalid,
	CodeSecretNameInvalid, CodeSecretDuplicate, CodeShapeInvalid,
}

func sortedProblems(p []Problem) bool {
	for i := 1; i < len(p); i++ {
		if p[i-1].Path > p[i].Path {
			return false
		}
		if p[i-1].Path == p[i].Path && p[i-1].Code > p[i].Code {
			return false
		}
	}
	return true
}
