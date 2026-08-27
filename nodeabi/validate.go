package nodeabi

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
)

// The closed ABI problem codes.
const (
	CodeJSONInvalid            = "json_invalid"
	CodeEnvelopeUnknownField   = "envelope_unknown_field"
	CodeEnvelopeMissingField   = "envelope_missing_field"
	CodeABIMismatch            = "abi_mismatch"
	CodeNodePinInvalid         = "node_pin_invalid"
	CodeSecretHandleInvalid    = "secret_handle_invalid"
	CodeEgressProxyInvalid     = "egress_proxy_invalid"
	CodeStatusInvalid          = "status_invalid"
	CodeOutputsOnError         = "outputs_on_error"
	CodeOutputsMissing         = "outputs_missing"
	CodeErrorOnOK              = "error_on_ok"
	CodeErrorCodeMissing       = "error_code_missing"
	CodeEmittedMissing         = "emitted_missing"
	CodeEmittedDuplicate       = "emitted_duplicate"
	CodeEmittedUnsorted        = "emitted_unsorted"
	CodeEmittedUndeclared      = "emitted_undeclared"
	CodeEmittedOutputsMismatch = "emitted_outputs_mismatch"
	CodeRequiredOutputMissing  = "required_output_missing"
	CodeStdoutOverflow         = "stdout_overflow"
	CodeLogsOverflow           = "logs_overflow"
	CodeLogEntryOverflow       = "log_entry_overflow"
	CodeLogLevelInvalid        = "log_level_invalid"
)

// The verbatim message texts. They are constants because both SDKs and the
// runtime assert on them: a reworded message is a contract change.
const (
	msgJSONInvalid           = "document is not valid JSON: %s"
	msgEnvelopeUnknownField  = "unknown field %s"
	msgEnvelopeMissingField  = "missing field %s"
	msgABIMismatch           = "abi must be sentiae.node/v1"
	msgNodePinInvalid        = "node must be a pin (@scope/name@x.y.z)"
	msgSecretHandleInvalid   = "secret %s must be an opaque handle"
	msgEgressProxyInvalid    = "egress.proxy must be http://host:port"
	msgStatusInvalid         = "status must be ok or error"
	msgOutputsOnError        = "outputs is not allowed on status error"
	msgOutputsMissing        = "outputs is required on status ok"
	msgErrorOnOK             = "error is not allowed on status ok"
	msgErrorCodeMissing      = "error.code must be non-empty"
	msgEmittedMissing        = "emitted is required"
	msgEmittedDuplicate      = "emitted has a duplicate %s"
	msgEmittedUnsorted       = "emitted must be sorted"
	msgEmittedUndeclared     = "emitted names undeclared output %s"
	msgEmittedOutputsMismat  = "emitted must equal the keys of outputs"
	msgRequiredOutputMissing = "required output %s not emitted"
	msgStdoutOverflow        = "result exceeds 8 MiB"
	msgLogsOverflow          = "logs exceed 256 entries"
	msgLogEntryOverflow      = "log message exceeds 4096 bytes"
	msgLogLevelInvalid       = "log level must be debug, info, warn or error"
)

// ValidationError is one refusal of an ABI document. Path is an RFC 6901
// pointer into the document, empty for the document as a whole.
type ValidationError struct {
	Code    string
	Path    string
	Message string
}

func (e *ValidationError) Error() string { return e.Code + ": " + e.Message }

var logLevels = keySet("debug", "info", "warn", "error")

// The CALL wire shapes. Every field is a pointer because the envelope's
// contract is PRESENCE: a required field written as JSON `null` carries no
// value, and the caller must be told the field is missing rather than handed a
// decoded zero that the runtime would then act on.
type callWire struct {
	ABI        *string                     `json:"abi"`
	Config     *map[string]json.RawMessage `json:"config"`
	Egress     *egressWire                 `json:"egress"`
	Inputs     *map[string]json.RawMessage `json:"inputs"`
	Invocation *invocationWire             `json:"invocation"`
	Node       *string                     `json:"node"`
	Secrets    *map[string]string          `json:"secrets"`
}

type invocationWire struct {
	ID    *string `json:"id"`
	Node  *string `json:"node"`
	RunID *string `json:"run_id"`
}

type egressWire struct {
	Proxy *string `json:"proxy"`
	Token *string `json:"token"`
}

// presence is one required field and whether the document carries it.
type presence struct {
	ptr     string
	present bool
}

// ValidateCall refuses a CALL document that the runtime may not hand a node.
// The order of the checks is part of the contract: a document with two faults
// always reports the same one.
func ValidateCall(b []byte) (*Call, *ValidationError) {
	top, verr := objectKeys(b, "", callKeys)
	if verr != nil {
		return nil, verr
	}
	if raw, ok := top["invocation"]; ok && !isNull(raw) {
		if _, verr := objectKeys(raw, "/invocation", invKeys); verr != nil {
			return nil, verr
		}
	}
	if raw, ok := top["egress"]; ok && !isNull(raw) {
		if _, verr := objectKeys(raw, "/egress", egressKeys); verr != nil {
			return nil, verr
		}
	}

	var wire callWire
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&wire); err != nil {
		return nil, &ValidationError{Code: CodeJSONInvalid, Message: fmt.Sprintf(msgJSONInvalid, err)}
	}

	if wire.ABI != nil && *wire.ABI != ABI {
		return nil, &ValidationError{Code: CodeABIMismatch, Path: "/abi", Message: msgABIMismatch}
	}

	inv := wire.Invocation
	missing := []presence{
		{"/abi", wire.ABI != nil},
		{"/invocation", inv != nil},
		{"/invocation/id", inv != nil && inv.ID != nil},
		{"/invocation/node", inv != nil && inv.Node != nil},
		{"/invocation/run_id", inv != nil && inv.RunID != nil},
		{"/node", wire.Node != nil},
		{"/inputs", wire.Inputs != nil},
		{"/config", wire.Config != nil},
		{"/secrets", wire.Secrets != nil},
	}
	if wire.Egress != nil {
		missing = append(missing,
			presence{"/egress/proxy", wire.Egress.Proxy != nil},
			presence{"/egress/token", wire.Egress.Token != nil},
		)
	}
	for _, m := range missing {
		if !m.present {
			return nil, &ValidationError{Code: CodeEnvelopeMissingField, Path: m.ptr, Message: fmt.Sprintf(msgEnvelopeMissingField, m.ptr)}
		}
	}

	// Every dereference below is guarded by the presence table above.
	call := Call{
		ABI:        *wire.ABI,
		Config:     *wire.Config,
		Inputs:     *wire.Inputs,
		Invocation: Invocation{ID: *inv.ID, Node: *inv.Node, RunID: *inv.RunID},
		Node:       *wire.Node,
		Secrets:    *wire.Secrets,
	}
	if wire.Egress != nil {
		call.Egress = &Egress{Proxy: *wire.Egress.Proxy, Token: *wire.Egress.Token}
	}

	if _, err := ParsePin(call.Node); err != nil {
		return nil, &ValidationError{Code: CodeNodePinInvalid, Path: "/node", Message: msgNodePinInvalid}
	}

	names := make([]string, 0, len(call.Secrets))
	for k := range call.Secrets {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, n := range names {
		v := call.Secrets[n]
		if len(v) < len(HandlePrefix) || v[:len(HandlePrefix)] != HandlePrefix || len(v) < MinHandleLen {
			p := "/secrets/" + escapePointer(n)
			return nil, &ValidationError{Code: CodeSecretHandleInvalid, Path: p, Message: fmt.Sprintf(msgSecretHandleInvalid, q(n))}
		}
	}

	if call.Egress != nil {
		if !validProxy(call.Egress.Proxy) {
			return nil, &ValidationError{Code: CodeEgressProxyInvalid, Path: "/egress/proxy", Message: msgEgressProxyInvalid}
		}
		if call.Egress.Token == "" {
			return nil, &ValidationError{Code: CodeEnvelopeMissingField, Path: "/egress/token", Message: fmt.Sprintf(msgEnvelopeMissingField, "/egress/token")}
		}
	}
	return &call, nil
}

// validProxy holds §5.5's proxy shape: an origin-form http URL and nothing
// else — no path, no query, no fragment, no userinfo, and an explicit port.
func validProxy(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	if u.Scheme != "http" || u.User != nil {
		return false
	}
	if u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
		return false
	}
	return u.Hostname() != "" && u.Port() != ""
}

// ValidateResult refuses a RESULT document a node process wrote. `declared` is
// the pinned manifest's outputs in declaration order.
func ValidateResult(b []byte, declared []DeclaredOutput) (*Result, *ValidationError) {
	if len(b) > MaxStdoutBytes {
		return nil, &ValidationError{Code: CodeStdoutOverflow, Message: msgStdoutOverflow}
	}
	var r Result
	if err := json.Unmarshal(b, &r); err != nil {
		var verr *ValidationError
		if errors.As(err, &verr) {
			return nil, verr
		}
		return nil, &ValidationError{Code: CodeJSONInvalid, Message: fmt.Sprintf(msgJSONInvalid, err)}
	}
	if r.ABI != ABI {
		return nil, &ValidationError{Code: CodeABIMismatch, Path: "/abi", Message: msgABIMismatch}
	}
	if r.Status != StatusOK && r.Status != StatusError {
		return nil, &ValidationError{Code: CodeStatusInvalid, Path: "/status", Message: msgStatusInvalid}
	}
	if r.Status == StatusError && r.Error != nil && r.Error.Code == "" {
		return nil, &ValidationError{Code: CodeErrorCodeMissing, Path: "/error/code", Message: msgErrorCodeMissing}
	}

	var top map[string]json.RawMessage
	if err := json.Unmarshal(b, &top); err != nil {
		return nil, &ValidationError{Code: CodeJSONInvalid, Message: fmt.Sprintf(msgJSONInvalid, err)}
	}
	if raw, ok := top["emitted"]; !ok || isNull(raw) {
		return nil, &ValidationError{Code: CodeEmittedMissing, Path: "/emitted", Message: msgEmittedMissing}
	}
	seen := make(map[string]bool, len(r.Emitted))
	for _, name := range r.Emitted {
		if seen[name] {
			return nil, &ValidationError{Code: CodeEmittedDuplicate, Path: "/emitted", Message: fmt.Sprintf(msgEmittedDuplicate, name)}
		}
		seen[name] = true
	}
	if !sort.StringsAreSorted(r.Emitted) {
		return nil, &ValidationError{Code: CodeEmittedUnsorted, Path: "/emitted", Message: msgEmittedUnsorted}
	}
	declaredNames := make(map[string]bool, len(declared))
	for _, d := range declared {
		declaredNames[d.Name] = true
	}
	for _, name := range r.Emitted {
		if !declaredNames[name] {
			return nil, &ValidationError{Code: CodeEmittedUndeclared, Path: "/emitted", Message: fmt.Sprintf(msgEmittedUndeclared, name)}
		}
	}
	if r.Status == StatusOK {
		keys := make([]string, 0, len(r.Outputs))
		for k := range r.Outputs {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		if !equalStrings(keys, r.Emitted) {
			return nil, &ValidationError{Code: CodeEmittedOutputsMismatch, Path: "/emitted", Message: msgEmittedOutputsMismat}
		}
		for _, d := range declared {
			if d.Required && !seen[d.Name] {
				return nil, &ValidationError{Code: CodeRequiredOutputMissing, Path: "/emitted", Message: fmt.Sprintf(msgRequiredOutputMissing, q(d.Name))}
			}
		}
	}

	if len(r.Logs) > MaxLogEntries {
		return nil, &ValidationError{Code: CodeLogsOverflow, Path: "/logs", Message: msgLogsOverflow}
	}
	for i, e := range r.Logs {
		if len(e.Message) > MaxLogEntryBytes {
			p := fmt.Sprintf("/logs/%d/message", i)
			return nil, &ValidationError{Code: CodeLogEntryOverflow, Path: p, Message: msgLogEntryOverflow}
		}
		if !logLevels[e.Level] {
			p := fmt.Sprintf("/logs/%d/level", i)
			return nil, &ValidationError{Code: CodeLogLevelInvalid, Path: p, Message: msgLogLevelInvalid}
		}
	}
	return &r, nil
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
