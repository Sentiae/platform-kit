package nodemanifest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// esNumber spells a float the way ECMAScript's Number::toString does, so the
// Go and the TypeScript writers of a canonical document agree byte for byte.
// Decimal notation covers 1e-6 ≤ |f| < 1e21 (ECMAScript switches to the
// exponential form exactly outside that window); Go's exponent carries a
// leading zero the ECMAScript form does not.
func esNumber(f float64) string {
	if f == 0 {
		return "0"
	}
	abs := math.Abs(f)
	if abs >= 1e-6 && abs < 1e21 {
		return strconv.FormatFloat(f, 'f', -1, 64)
	}
	return stripExponentZeros(strconv.FormatFloat(f, 'e', -1, 64))
}

// stripExponentZeros turns Go's `1e-07` into ECMAScript's `1e-7`.
func stripExponentZeros(s string) string {
	i := strings.IndexByte(s, 'e')
	if i < 0 {
		return s
	}
	mantissa, exp := s[:i], s[i+1:]
	sign := ""
	if len(exp) > 0 && (exp[0] == '+' || exp[0] == '-') {
		sign, exp = string(exp[0]), exp[1:]
	}
	for len(exp) > 1 && exp[0] == '0' {
		exp = exp[1:]
	}
	return mantissa + "e" + sign + exp
}

// CanonicalJSON is the one normal form: keys sorted bytewise at every level,
// two-space indent, `<`/`>`/`&` unescaped, numbers spelled by esNumber, and
// exactly one trailing LF.
func CanonicalJSON(v any) ([]byte, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var decoded any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, err
	}
	normalized, err := normalizeNumbers(decoded)
	if err != nil {
		return nil, err
	}
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(normalized); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// canonicalString is CanonicalJSON with the trailing LF stripped — the form
// every diagnostic message interpolates, because a message is one line.
func canonicalString(v any) string {
	b, err := CanonicalJSON(v)
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	return string(bytes.TrimSuffix(b, []byte("\n")))
}

func normalizeNumbers(v any) (any, error) {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			n, err := normalizeNumbers(val)
			if err != nil {
				return nil, err
			}
			out[k] = n
		}
		return out, nil
	case []any:
		out := make([]any, len(t))
		for i, val := range t {
			n, err := normalizeNumbers(val)
			if err != nil {
				return nil, err
			}
			out[i] = n
		}
		return out, nil
	case float64:
		if math.IsNaN(t) || math.IsInf(t, 0) {
			return nil, fmt.Errorf("json: unsupported value: %v", t)
		}
		return json.Number(esNumber(t)), nil
	default:
		return v, nil
	}
}

// Canonicalize rewrites any JSON document in the canonical form. It does not
// validate — Load does that.
func Canonicalize(b []byte) ([]byte, error) {
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		return nil, err
	}
	return CanonicalJSON(v)
}
