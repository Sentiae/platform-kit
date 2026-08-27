package nodeabi

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

const fixtureDir = "../flowlang/testdata/abi"

type abiFixture struct {
	Claim           string           `json:"claim"`
	DeclaredOutputs []DeclaredOutput `json:"declaredOutputs"`
	Document        json.RawMessage  `json:"document"`
	Expect          struct {
		Code  string `json:"code"`
		Valid bool   `json:"valid"`
	} `json:"expect"`
	Kind string `json:"kind"`
}

// TestFixtures_ABI proves this validator agrees with the ONE golden corpus the
// two SDKs are also checked against: every CALL and RESULT fixture, both kinds,
// verdict and code. If Go and a SDK disagree, one of them fails here.
func TestFixtures_ABI(t *testing.T) {
	files, err := filepath.Glob(filepath.Join(fixtureDir, "*.json"))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	sort.Strings(files)
	if len(files) < 6 {
		t.Fatalf("corpus has %d abi fixtures, want at least 6", len(files))
	}
	kinds := map[string]int{}
	for _, f := range files {
		t.Run(filepath.Base(f), func(t *testing.T) {
			raw, err := os.ReadFile(f)
			if err != nil {
				t.Fatalf("read: %v", err)
			}
			var fx abiFixture
			if err := json.Unmarshal(raw, &fx); err != nil {
				t.Fatalf("decode fixture: %v", err)
			}
			kinds[fx.Kind]++
			var verr *ValidationError
			switch fx.Kind {
			case "call":
				_, verr = ValidateCall(fx.Document)
			case "result":
				_, verr = ValidateResult(fx.Document, fx.DeclaredOutputs)
			default:
				t.Fatalf("unknown fixture kind %q", fx.Kind)
			}
			if fx.Expect.Valid {
				if verr != nil {
					t.Fatalf("want valid, got %s at %q: %s", verr.Code, verr.Path, verr.Message)
				}
				return
			}
			if verr == nil {
				t.Fatalf("want %s, got valid", fx.Expect.Code)
			}
			if verr.Code != fx.Expect.Code {
				t.Fatalf("code = %q, want %q (message %q)", verr.Code, fx.Expect.Code, verr.Message)
			}
		})
	}
	// Positive anchor: the table really did exercise BOTH document kinds.
	if kinds["call"] == 0 || kinds["result"] == 0 {
		t.Fatalf("corpus kinds = %v, want at least one call and one result", kinds)
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

func okResult() map[string]any {
	return map[string]any{
		"abi":     ABI,
		"emitted": []any{"result"},
		"logs":    []any{},
		"outputs": map[string]any{"result": map[string]any{"status": 202}},
		"status":  "ok",
	}
}

func errResult() map[string]any {
	return map[string]any{
		"abi":     ABI,
		"emitted": []any{},
		"error":   map[string]any{"code": "BOOM", "message": "x", "retryable": false},
		"status":  "error",
	}
}

var declaredResult = []DeclaredOutput{{Name: "result", Required: true}}

// TestValidateResult_Codes proves every RESULT refusal code is reachable and
// carries its pinned message and path. Each row IS its own control: the
// document differs from a valid one only in the fault the row names.
func TestValidateResult_Codes(t *testing.T) {
	big := append([]byte(`{"abi":"sentiae.node/v1","status":"ok","emitted":[],"outputs":{},"logs":[],"pad":"`), make([]byte, MaxStdoutBytes)...)
	for i := len(`{"abi":"sentiae.node/v1","status":"ok","emitted":[],"outputs":{},"logs":[],"pad":"`); i < len(big); i++ {
		big[i] = 'x'
	}
	big = append(big, '"', '}')

	longMsg := strings.Repeat("m", MaxLogEntryBytes+1)
	logs := make([]any, MaxLogEntries+1)
	for i := range logs {
		logs[i] = map[string]any{"level": "info", "message": "x"}
	}

	tests := []struct {
		name     string
		doc      []byte
		declared []DeclaredOutput
		wantCode string
		wantPath string
		wantMsg  string
	}{
		{"json_invalid", []byte(`{`), nil, CodeJSONInvalid, "", ""},
		{"envelope_unknown_field_top", mustJSON(t, withKey(okResult(), "extra", 1)), declaredResult, CodeEnvelopeUnknownField, "/extra", "unknown field /extra"},
		{"envelope_unknown_field_error", mustJSON(t, withNested(errResult(), "error", "extra", 1)), declaredResult, CodeEnvelopeUnknownField, "/error/extra", "unknown field /error/extra"},
		{"envelope_unknown_field_log", mustJSON(t, withKey(okResult(), "logs", []any{map[string]any{"level": "info", "message": "x", "extra": 1}})), declaredResult, CodeEnvelopeUnknownField, "/logs/0/extra", "unknown field /logs/0/extra"},
		{"envelope_missing_field_error", mustJSON(t, withoutKey(errResult(), "error")), declaredResult, CodeEnvelopeMissingField, "/error", "missing field /error"},
		{"abi_mismatch", mustJSON(t, withKey(okResult(), "abi", "sentiae.node/v2")), declaredResult, CodeABIMismatch, "/abi", msgABIMismatch},
		{"status_invalid", mustJSON(t, withKey(withoutKey(okResult(), "outputs"), "status", "maybe")), declaredResult, CodeStatusInvalid, "/status", msgStatusInvalid},
		{"outputs_on_error", mustJSON(t, withKey(errResult(), "outputs", map[string]any{})), declaredResult, CodeOutputsOnError, "/outputs", msgOutputsOnError},
		{"outputs_missing", []byte(`{"abi":"sentiae.node/v1","status":"ok","emitted":[]}`), declaredResult, CodeOutputsMissing, "/outputs", msgOutputsMissing},
		{"error_on_ok", mustJSON(t, withKey(okResult(), "error", map[string]any{"code": "x", "message": "y", "retryable": false})), declaredResult, CodeErrorOnOK, "/error", msgErrorOnOK},
		{"error_code_missing", mustJSON(t, withNested(errResult(), "error", "code", "")), declaredResult, CodeErrorCodeMissing, "/error/code", msgErrorCodeMissing},
		{"emitted_missing", mustJSON(t, withoutKey(okResult(), "emitted")), declaredResult, CodeEmittedMissing, "/emitted", msgEmittedMissing},
		{"emitted_duplicate", mustJSON(t, withKey(okResult(), "emitted", []any{"result", "result"})), declaredResult, CodeEmittedDuplicate, "/emitted", "emitted has a duplicate result"},
		{"emitted_unsorted", mustJSON(t, withKey(withKey(okResult(), "emitted", []any{"result", "other"}), "outputs", map[string]any{"result": 1, "other": 2})), []DeclaredOutput{{Name: "result"}, {Name: "other"}}, CodeEmittedUnsorted, "/emitted", msgEmittedUnsorted},
		{"emitted_undeclared", mustJSON(t, withKey(okResult(), "emitted", []any{"ghost"})), declaredResult, CodeEmittedUndeclared, "/emitted", "emitted names undeclared output ghost"},
		{"emitted_outputs_mismatch", mustJSON(t, withKey(okResult(), "outputs", map[string]any{})), declaredResult, CodeEmittedOutputsMismatch, "/emitted", msgEmittedOutputsMismat},
		{"required_output_missing", mustJSON(t, withKey(withKey(okResult(), "emitted", []any{}), "outputs", map[string]any{})), declaredResult, CodeRequiredOutputMissing, "/emitted", `required output "result" not emitted`},
		{"stdout_overflow", big, declaredResult, CodeStdoutOverflow, "", msgStdoutOverflow},
		{"logs_overflow", mustJSON(t, withKey(okResult(), "logs", logs)), declaredResult, CodeLogsOverflow, "/logs", msgLogsOverflow},
		{"log_entry_overflow", mustJSON(t, withKey(okResult(), "logs", []any{map[string]any{"level": "info", "message": longMsg}})), declaredResult, CodeLogEntryOverflow, "/logs/0/message", msgLogEntryOverflow},
		{"log_level_invalid", mustJSON(t, withKey(okResult(), "logs", []any{map[string]any{"level": "trace", "message": "x"}})), declaredResult, CodeLogLevelInvalid, "/logs/0/level", msgLogLevelInvalid},
	}
	seen := map[string]bool{}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, verr := ValidateResult(tt.doc, tt.declared)
			if verr == nil {
				t.Fatalf("want %s, got valid", tt.wantCode)
			}
			if verr.Code != tt.wantCode {
				t.Fatalf("code = %q (%s), want %q", verr.Code, verr.Message, tt.wantCode)
			}
			if verr.Path != tt.wantPath {
				t.Fatalf("path = %q, want %q", verr.Path, tt.wantPath)
			}
			if tt.wantMsg != "" && verr.Message != tt.wantMsg {
				t.Fatalf("message = %q, want %q", verr.Message, tt.wantMsg)
			}
			if verr.Error() != verr.Code+": "+verr.Message {
				t.Fatalf("Error() = %q", verr.Error())
			}
		})
		seen[tt.wantCode] = true
	}
	// Positive anchor: the table covers every RESULT code this package can emit.
	for _, code := range []string{
		CodeJSONInvalid, CodeEnvelopeUnknownField, CodeEnvelopeMissingField, CodeABIMismatch,
		CodeStatusInvalid, CodeOutputsOnError, CodeOutputsMissing, CodeErrorOnOK, CodeErrorCodeMissing,
		CodeEmittedMissing, CodeEmittedDuplicate, CodeEmittedUnsorted, CodeEmittedUndeclared,
		CodeEmittedOutputsMismatch, CodeRequiredOutputMissing, CodeStdoutOverflow, CodeLogsOverflow,
		CodeLogEntryOverflow, CodeLogLevelInvalid,
	} {
		if !seen[code] {
			t.Fatalf("no row for result code %q", code)
		}
	}
}

func okCall() map[string]any {
	return map[string]any{
		"abi":        ABI,
		"config":     map[string]any{},
		"inputs":     map[string]any{"value": map[string]any{"order_id": "ord-123"}},
		"invocation": map[string]any{"id": "inv-1", "node": "choose", "run_id": "run-1"},
		"node":       "@sentiae/branch@1.0.0",
		"secrets":    map[string]any{},
	}
}

func withKey(m map[string]any, k string, v any) map[string]any {
	out := map[string]any{}
	for kk, vv := range m {
		out[kk] = vv
	}
	out[k] = v
	return out
}

func withoutKey(m map[string]any, k string) map[string]any {
	out := map[string]any{}
	for kk, vv := range m {
		if kk == k {
			continue
		}
		out[kk] = vv
	}
	return out
}

func withNested(m map[string]any, outer, k string, v any) map[string]any {
	inner := map[string]any{}
	if cur, ok := m[outer].(map[string]any); ok {
		for kk, vv := range cur {
			inner[kk] = vv
		}
	}
	inner[k] = v
	return withKey(m, outer, inner)
}

// TestValidateCall_Codes proves every CALL refusal code is reachable with its
// pinned message and pointer, AND — the positive anchor — that the opaque
// payload maps are never inspected: an unknown key inside `inputs.x` is data.
func TestValidateCall_Codes(t *testing.T) {
	egress := map[string]any{"proxy": "http://10.200.0.2:3128", "token": "t-1"}
	tests := []struct {
		name     string
		doc      []byte
		wantCode string
		wantPath string
		wantMsg  string
	}{
		{"json_invalid", []byte(`{"abi":`), CodeJSONInvalid, "", ""},
		{"envelope_unknown_field_top", mustJSON(t, withKey(okCall(), "extra", 1)), CodeEnvelopeUnknownField, "/extra", "unknown field /extra"},
		{"envelope_unknown_field_invocation", mustJSON(t, withNested(okCall(), "invocation", "extra", 1)), CodeEnvelopeUnknownField, "/invocation/extra", "unknown field /invocation/extra"},
		{"envelope_unknown_field_egress", mustJSON(t, withNested(withKey(okCall(), "egress", egress), "egress", "extra", 1)), CodeEnvelopeUnknownField, "/egress/extra", "unknown field /egress/extra"},
		{"envelope_missing_field_abi", mustJSON(t, withoutKey(okCall(), "abi")), CodeEnvelopeMissingField, "/abi", "missing field /abi"},
		{"envelope_missing_field_invocation", mustJSON(t, withoutKey(okCall(), "invocation")), CodeEnvelopeMissingField, "/invocation", "missing field /invocation"},
		{"envelope_missing_field_invocation_run_id", mustJSON(t, withKey(okCall(), "invocation", map[string]any{"id": "i", "node": "n"})), CodeEnvelopeMissingField, "/invocation/run_id", "missing field /invocation/run_id"},
		{"envelope_missing_field_secrets", mustJSON(t, withoutKey(okCall(), "secrets")), CodeEnvelopeMissingField, "/secrets", "missing field /secrets"},
		{"envelope_missing_field_egress_token", mustJSON(t, withKey(okCall(), "egress", map[string]any{"proxy": "http://10.0.0.1:3128"})), CodeEnvelopeMissingField, "/egress/token", "missing field /egress/token"},
		{"envelope_missing_field_egress_token_empty", mustJSON(t, withKey(okCall(), "egress", map[string]any{"proxy": "http://10.0.0.1:3128", "token": ""})), CodeEnvelopeMissingField, "/egress/token", "missing field /egress/token"},
		// An explicit JSON `null` is not a value: every required field refuses
		// it as absence, at its own pointer, rather than decoding to a zero.
		{"null_abi", mustJSON(t, withKey(okCall(), "abi", nil)), CodeEnvelopeMissingField, "/abi", "missing field /abi"},
		{"null_invocation", mustJSON(t, withKey(okCall(), "invocation", nil)), CodeEnvelopeMissingField, "/invocation", "missing field /invocation"},
		{"null_invocation_id", mustJSON(t, withNested(okCall(), "invocation", "id", nil)), CodeEnvelopeMissingField, "/invocation/id", "missing field /invocation/id"},
		{"null_invocation_node", mustJSON(t, withNested(okCall(), "invocation", "node", nil)), CodeEnvelopeMissingField, "/invocation/node", "missing field /invocation/node"},
		{"null_invocation_run_id", mustJSON(t, withNested(okCall(), "invocation", "run_id", nil)), CodeEnvelopeMissingField, "/invocation/run_id", "missing field /invocation/run_id"},
		{"null_node", mustJSON(t, withKey(okCall(), "node", nil)), CodeEnvelopeMissingField, "/node", "missing field /node"},
		{"null_inputs", mustJSON(t, withKey(okCall(), "inputs", nil)), CodeEnvelopeMissingField, "/inputs", "missing field /inputs"},
		{"null_config", mustJSON(t, withKey(okCall(), "config", nil)), CodeEnvelopeMissingField, "/config", "missing field /config"},
		{"null_secrets", mustJSON(t, withKey(okCall(), "secrets", nil)), CodeEnvelopeMissingField, "/secrets", "missing field /secrets"},
		{"null_egress_proxy", mustJSON(t, withKey(okCall(), "egress", map[string]any{"proxy": nil, "token": "t"})), CodeEnvelopeMissingField, "/egress/proxy", "missing field /egress/proxy"},
		{"null_egress_token", mustJSON(t, withKey(okCall(), "egress", map[string]any{"proxy": "http://10.0.0.1:3128", "token": nil})), CodeEnvelopeMissingField, "/egress/token", "missing field /egress/token"},
		{"abi_mismatch", mustJSON(t, withKey(okCall(), "abi", "sentiae.node/v2")), CodeABIMismatch, "/abi", msgABIMismatch},
		{"node_pin_invalid", mustJSON(t, withKey(okCall(), "node", "@sentiae/branch")), CodeNodePinInvalid, "/node", msgNodePinInvalid},
		{"secret_handle_invalid_prefix", mustJSON(t, withKey(okCall(), "secrets", map[string]any{"api_token": "plaintext-secret-value-long"})), CodeSecretHandleInvalid, "/secrets/api_token", `secret "api_token" must be an opaque handle`},
		{"secret_handle_invalid_short", mustJSON(t, withKey(okCall(), "secrets", map[string]any{"api_token": "handle:short"})), CodeSecretHandleInvalid, "/secrets/api_token", `secret "api_token" must be an opaque handle`},
		{"egress_proxy_invalid_scheme", mustJSON(t, withKey(okCall(), "egress", map[string]any{"proxy": "https://10.0.0.1:3128", "token": "t"})), CodeEgressProxyInvalid, "/egress/proxy", msgEgressProxyInvalid},
		{"egress_proxy_invalid_path", mustJSON(t, withKey(okCall(), "egress", map[string]any{"proxy": "http://10.0.0.1:3128/go", "token": "t"})), CodeEgressProxyInvalid, "/egress/proxy", msgEgressProxyInvalid},
		{"egress_proxy_invalid_no_port", mustJSON(t, withKey(okCall(), "egress", map[string]any{"proxy": "http://proxy.internal", "token": "t"})), CodeEgressProxyInvalid, "/egress/proxy", msgEgressProxyInvalid},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, verr := ValidateCall(tt.doc)
			if verr == nil {
				t.Fatalf("want %s, got valid", tt.wantCode)
			}
			if verr.Code != tt.wantCode {
				t.Fatalf("code = %q (%s), want %q", verr.Code, verr.Message, tt.wantCode)
			}
			if verr.Path != tt.wantPath {
				t.Fatalf("path = %q, want %q", verr.Path, tt.wantPath)
			}
			if tt.wantMsg != "" && verr.Message != tt.wantMsg {
				t.Fatalf("message = %q, want %q", verr.Message, tt.wantMsg)
			}
		})
	}

	t.Run("opaque_payload_keys_are_data", func(t *testing.T) {
		doc := mustJSON(t, withKey(okCall(), "inputs", map[string]any{"x": map[string]any{"extra": 1, "anything": []any{1, 2}}}))
		call, verr := ValidateCall(doc)
		if verr != nil {
			t.Fatalf("want valid, got %s at %q: %s", verr.Code, verr.Path, verr.Message)
		}
		if _, ok := call.Inputs["x"]; !ok {
			t.Fatalf("inputs = %#v, want the opaque payload preserved", call.Inputs)
		}
	})

	t.Run("egress_accepted", func(t *testing.T) {
		doc := mustJSON(t, withKey(okCall(), "egress", egress))
		call, verr := ValidateCall(doc)
		if verr != nil {
			t.Fatalf("want valid, got %s at %q: %s", verr.Code, verr.Path, verr.Message)
		}
		if call.Egress == nil || call.Egress.Proxy != "http://10.200.0.2:3128" {
			t.Fatalf("egress = %#v", call.Egress)
		}
	})
}
