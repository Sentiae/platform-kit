package nodeabi

import (
	"encoding/json"
	"reflect"
	"testing"
)

// TestResult_MarshalRoundTrip proves the RESULT encoder holds the presence
// contract in BYTES, not just in the struct: keys in sorted-key order, no
// `error` on ok, no `outputs` on error, `logs` always written even when the
// field is nil, and Unmarshal(Marshal(r)) == r.
func TestResult_MarshalRoundTrip(t *testing.T) {
	ok := Result{
		ABI:     ABI,
		Emitted: []string{"result"},
		Outputs: map[string]json.RawMessage{"result": json.RawMessage(`{"status":202}`)},
		Status:  StatusOK,
	}
	b, err := json.Marshal(ok)
	if err != nil {
		t.Fatalf("Marshal(ok) error = %v", err)
	}
	want := `{"abi":"sentiae.node/v1","emitted":["result"],"logs":[],"outputs":{"result":{"status":202}},"status":"ok"}`
	if string(b) != want {
		t.Fatalf("Marshal(ok) =\n%s\nwant\n%s", b, want)
	}

	bad := Result{
		ABI:     ABI,
		Emitted: []string{},
		Error:   &Error{Code: "REMOTE_TIMEOUT", Message: "upstream request timed out", Retryable: true},
		Logs:    []LogEntry{{Level: "warn", Message: "retrying"}},
		Status:  StatusError,
	}
	b2, err := json.Marshal(bad)
	if err != nil {
		t.Fatalf("Marshal(error) error = %v", err)
	}
	want2 := `{"abi":"sentiae.node/v1","emitted":[],"error":{"code":"REMOTE_TIMEOUT","message":"upstream request timed out","retryable":true},"logs":[{"level":"warn","message":"retrying"}],"status":"error"}`
	if string(b2) != want2 {
		t.Fatalf("Marshal(error) =\n%s\nwant\n%s", b2, want2)
	}

	truncated := Result{ABI: ABI, Emitted: []string{}, Outputs: map[string]json.RawMessage{}, Status: StatusOK, Truncated: true}
	b3, err := json.Marshal(truncated)
	if err != nil {
		t.Fatalf("Marshal(truncated) error = %v", err)
	}
	want3 := `{"abi":"sentiae.node/v1","emitted":[],"logs":[],"outputs":{},"status":"ok","truncated":true}`
	if string(b3) != want3 {
		t.Fatalf("Marshal(truncated) =\n%s\nwant\n%s", b3, want3)
	}

	for name, r := range map[string]Result{"ok": ok, "error": bad, "truncated": truncated} {
		raw, err := json.Marshal(r)
		if err != nil {
			t.Fatalf("%s: Marshal error = %v", name, err)
		}
		var back Result
		if err := json.Unmarshal(raw, &back); err != nil {
			t.Fatalf("%s: Unmarshal error = %v", name, err)
		}
		// The encoder writes the empty collections the decoder then reads back,
		// so the fixpoint is over the ENCODED form, which is the wire.
		again, err := json.Marshal(back)
		if err != nil {
			t.Fatalf("%s: re-Marshal error = %v", name, err)
		}
		if string(again) != string(raw) {
			t.Fatalf("%s: round trip changed bytes:\n%s\n%s", name, raw, again)
		}
		if r.Logs != nil && !reflect.DeepEqual(back.Logs, r.Logs) {
			t.Fatalf("%s: logs = %#v, want %#v", name, back.Logs, r.Logs)
		}
		if !reflect.DeepEqual(back.Error, r.Error) {
			t.Fatalf("%s: error = %#v, want %#v", name, back.Error, r.Error)
		}
		if back.Status != r.Status || back.ABI != r.ABI || back.Truncated != r.Truncated {
			t.Fatalf("%s: scalars differ: %#v vs %#v", name, back, r)
		}
	}
}
