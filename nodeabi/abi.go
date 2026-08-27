package nodeabi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// The ABI identity and the envelope limits every implementation shares.
const (
	// ABI is the value of the `abi` key in both documents.
	ABI = "sentiae.node/v1"
	// MaxStdoutBytes bounds a RESULT document.
	MaxStdoutBytes = 8 << 20
	// MaxLogEntries bounds `logs`.
	MaxLogEntries = 256
	// MaxLogEntryBytes bounds one log message.
	MaxLogEntryBytes = 4096
	// MaxStderrBytes bounds the stderr a runtime keeps from a node process.
	MaxStderrBytes = 64 << 10
	// HandlePrefix opens every opaque secret handle.
	HandlePrefix = "handle:"
	// MinHandleLen is the shortest acceptable handle, in bytes.
	MinHandleLen = 24
	// BrokerSocketPath is where the per-run secret broker is bind-mounted.
	BrokerSocketPath = "/run/sentiae/broker.sock"
)

// Invocation identifies one node invocation inside one run.
type Invocation struct {
	ID    string `json:"id"`
	Node  string `json:"node"`
	RunID string `json:"run_id"`
}

// Egress carries the capability-bound proxy credentials (DESIGN.md §3.6).
type Egress struct {
	Proxy string `json:"proxy"`
	Token string `json:"token"`
}

// Call is the CALL document. Fields are declared in sorted-key order so a
// compact encoding of this struct is already canonical.
type Call struct {
	ABI        string                     `json:"abi"`
	Config     map[string]json.RawMessage `json:"config"`
	Egress     *Egress                    `json:"egress,omitempty"`
	Inputs     map[string]json.RawMessage `json:"inputs"`
	Invocation Invocation                 `json:"invocation"`
	Node       string                     `json:"node"`
	Secrets    map[string]string          `json:"secrets"`
}

// LogEntry is one structured log line a node emitted.
type LogEntry struct {
	Fields  map[string]any `json:"fields,omitempty"`
	Level   string         `json:"level"`
	Message string         `json:"message"`
}

// Error is the author-returned failure of a node.
type Error struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}

// Result is the RESULT document.
type Result struct {
	ABI       string                     `json:"abi"`
	Emitted   []string                   `json:"emitted"`
	Error     *Error                     `json:"error,omitempty"`
	Logs      []LogEntry                 `json:"logs"`
	Outputs   map[string]json.RawMessage `json:"outputs"`
	Status    string                     `json:"status"`
	Truncated bool                       `json:"truncated,omitempty"`
}

// DeclaredOutput is one manifest output, as ValidateResult needs it.
type DeclaredOutput struct {
	Name     string
	Required bool
}

// The two RESULT status values.
const (
	StatusOK    = "ok"
	StatusError = "error"
)

// MarshalJSON writes the RESULT keys in sorted-key order and holds the
// presence contract: `outputs` exists iff the status is ok, `error` exists iff
// the status is error, `logs` and `emitted` are always written (empty arrays
// when there is nothing), `truncated` only when true.
func (r Result) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	write := func(key string, v any) error {
		if b.Len() > 1 {
			b.WriteByte(',')
		}
		k, err := json.Marshal(key)
		if err != nil {
			return err
		}
		b.Write(k)
		b.WriteByte(':')
		raw, err := json.Marshal(v)
		if err != nil {
			return err
		}
		b.Write(raw)
		return nil
	}
	if err := write("abi", r.ABI); err != nil {
		return nil, err
	}
	emitted := r.Emitted
	if emitted == nil {
		emitted = []string{}
	}
	if err := write("emitted", emitted); err != nil {
		return nil, err
	}
	if r.Status == StatusError {
		if err := write("error", r.Error); err != nil {
			return nil, err
		}
	}
	logs := r.Logs
	if logs == nil {
		logs = []LogEntry{}
	}
	if err := write("logs", logs); err != nil {
		return nil, err
	}
	if r.Status == StatusOK {
		outputs := r.Outputs
		if outputs == nil {
			outputs = map[string]json.RawMessage{}
		}
		if err := write("outputs", outputs); err != nil {
			return nil, err
		}
	}
	if err := write("status", r.Status); err != nil {
		return nil, err
	}
	if r.Truncated {
		if err := write("truncated", r.Truncated); err != nil {
			return nil, err
		}
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

// UnmarshalJSON decodes a RESULT strictly: an unknown key at the document, the
// error object or a log entry is refused, and the four presence rules of the
// envelope are checked here rather than downstream, so no decoded Result can
// hold a shape the ABI does not admit.
func (r *Result) UnmarshalJSON(b []byte) error {
	top, verr := objectKeys(b, "", resultKeys)
	if verr != nil {
		return verr
	}
	if raw, ok := top["error"]; ok && !isNull(raw) {
		if _, verr := objectKeys(raw, "/error", errorKeys); verr != nil {
			return verr
		}
	}
	if raw, ok := top["logs"]; ok && !isNull(raw) {
		var entries []json.RawMessage
		if err := json.Unmarshal(raw, &entries); err != nil {
			return &ValidationError{Code: CodeJSONInvalid, Path: "/logs", Message: fmt.Sprintf(msgJSONInvalid, err)}
		}
		for i, e := range entries {
			if _, verr := objectKeys(e, fmt.Sprintf("/logs/%d", i), logEntryKeys); verr != nil {
				return verr
			}
		}
	}

	var wire struct {
		ABI       *string                     `json:"abi"`
		Emitted   *[]string                   `json:"emitted"`
		Error     *Error                      `json:"error"`
		Logs      *[]LogEntry                 `json:"logs"`
		Outputs   *map[string]json.RawMessage `json:"outputs"`
		Status    *string                     `json:"status"`
		Truncated *bool                       `json:"truncated"`
	}
	if err := json.Unmarshal(b, &wire); err != nil {
		return &ValidationError{Code: CodeJSONInvalid, Message: fmt.Sprintf(msgJSONInvalid, err)}
	}

	out := Result{}
	if wire.ABI != nil {
		out.ABI = *wire.ABI
	}
	if wire.Emitted != nil {
		out.Emitted = *wire.Emitted
	}
	out.Error = wire.Error
	if wire.Logs != nil {
		out.Logs = *wire.Logs
	}
	if wire.Outputs != nil {
		out.Outputs = *wire.Outputs
	}
	if wire.Status != nil {
		out.Status = *wire.Status
	}
	if wire.Truncated != nil {
		out.Truncated = *wire.Truncated
	}

	switch out.Status {
	case StatusOK:
		if wire.Outputs == nil {
			return &ValidationError{Code: CodeOutputsMissing, Path: "/outputs", Message: msgOutputsMissing}
		}
		if wire.Error != nil {
			return &ValidationError{Code: CodeErrorOnOK, Path: "/error", Message: msgErrorOnOK}
		}
	case StatusError:
		if wire.Outputs != nil {
			return &ValidationError{Code: CodeOutputsOnError, Path: "/outputs", Message: msgOutputsOnError}
		}
		if wire.Error == nil {
			return &ValidationError{Code: CodeEnvelopeMissingField, Path: "/error", Message: fmt.Sprintf(msgEnvelopeMissingField, "/error")}
		}
	}

	*r = out
	return nil
}

var (
	resultKeys   = keySet("abi", "emitted", "error", "logs", "outputs", "status", "truncated")
	errorKeys    = keySet("code", "message", "retryable")
	logEntryKeys = keySet("fields", "level", "message")
	callKeys     = keySet("abi", "config", "egress", "inputs", "invocation", "node", "secrets")
	invKeys      = keySet("id", "node", "run_id")
	egressKeys   = keySet("proxy", "token")
)

func keySet(names ...string) map[string]bool {
	m := make(map[string]bool, len(names))
	for _, n := range names {
		m[n] = true
	}
	return m
}

func isNull(raw json.RawMessage) bool {
	return string(bytes.TrimSpace(raw)) == "null"
}

// objectKeys decodes one JSON object and refuses the first unknown key in byte
// order, naming it with its RFC 6901 pointer. Go's DisallowUnknownFields knows
// the key but not the path, and the ABI's contract is the path.
func objectKeys(raw json.RawMessage, ptr string, allowed map[string]bool) (map[string]json.RawMessage, *ValidationError) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, &ValidationError{Code: CodeJSONInvalid, Path: ptr, Message: fmt.Sprintf(msgJSONInvalid, err)}
	}
	names := make([]string, 0, len(m))
	for k := range m {
		if !allowed[k] {
			names = append(names, k)
		}
	}
	if len(names) > 0 {
		sort.Strings(names)
		p := ptr + "/" + escapePointer(names[0])
		return nil, &ValidationError{Code: CodeEnvelopeUnknownField, Path: p, Message: fmt.Sprintf(msgEnvelopeUnknownField, p)}
	}
	return m, nil
}

// escapePointer applies RFC 6901 escaping to one reference token.
func escapePointer(token string) string {
	return strings.ReplaceAll(strings.ReplaceAll(token, "~", "~0"), "/", "~1")
}
