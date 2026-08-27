package flowlang

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/sentiae/platform-kit/flowlang/types"
)

const tab = "\t"

// maxIdent is §3.1's identifier ceiling.
const maxIdent = 64

// syntaxError is one line's objection. §3.5: a line yields AT MOST one finding,
// so the scanner latches the first and every later call is a no-op — which is
// what makes "the first failure on that line" a property of the type rather
// than of every call site remembering to check.
type syntaxError struct {
	line    int
	code    string
	message string
}

func (e *syntaxError) Error() string { return e.code + ": " + e.message }

type scan struct {
	s    string
	line int
	i    int
	err  *syntaxError
}

func newScan(s string, line int) *scan { return &scan{s: s, line: line} }

func (sc *scan) fail(code, message string) {
	if sc.err == nil {
		sc.err = &syntaxError{line: sc.line, code: code, message: message}
	}
}

func (sc *scan) failed() bool { return sc.err != nil }

func (sc *scan) done() bool { return sc.i >= len(sc.s) }

func (sc *scan) peek() byte {
	if sc.done() {
		return 0
	}
	return sc.s[sc.i]
}

func (sc *scan) startsWith(tok string) bool {
	return strings.HasPrefix(sc.s[sc.i:], tok)
}

func (sc *scan) eat(tok string) bool {
	if sc.failed() || !sc.startsWith(tok) {
		return false
	}
	sc.i += len(tok)
	return true
}

func (sc *scan) expect(tok string) {
	if sc.failed() {
		return
	}
	if !sc.eat(tok) {
		sc.fail(CodeExpectedToken, fmt.Sprintf(msgExpectedToken, jsonString(tok)))
	}
}

func (sc *scan) space() { sc.expect(" ") }

var identRx = regexp.MustCompile(`^[a-z_][a-z0-9_]*`)

func (sc *scan) ident() string {
	if sc.failed() {
		return ""
	}
	m := identRx.FindString(sc.s[sc.i:])
	if m == "" {
		sc.fail(CodeBadIdent, msgBadIdent)
		return ""
	}
	if len(m) > maxIdent {
		sc.fail(CodeBadIdent, msgIdentTooLong)
		return ""
	}
	sc.i += len(m)
	return m
}

// name is a key or port id: the bare form, or the quoted form for anything else.
func (sc *scan) name() string {
	if sc.failed() {
		return ""
	}
	if sc.peek() == '"' {
		return sc.str()
	}
	return sc.ident()
}

func (sc *scan) str() string {
	if sc.failed() {
		return ""
	}
	if sc.peek() != '"' {
		sc.fail(CodeBadString, msgBadString)
		return ""
	}
	j := sc.i + 1
	for {
		if j >= len(sc.s) {
			sc.fail(CodeBadString, msgUnterminatedString)
			return ""
		}
		switch sc.s[j] {
		case '\\':
			j += 2
			continue
		case '"':
			raw := sc.s[sc.i : j+1]
			var v string
			if err := json.Unmarshal([]byte(raw), &v); err != nil {
				sc.fail(CodeBadString, msgInvalidEscape)
				return ""
			}
			sc.i = j + 1
			return v
		}
		j++
	}
}

var intRx = regexp.MustCompile(`^-?[0-9]+`)

func (sc *scan) integer() int {
	if sc.failed() {
		return 0
	}
	m := intRx.FindString(sc.s[sc.i:])
	if m == "" {
		sc.fail(CodeBadNumber, msgBadInteger)
		return 0
	}
	n, err := strconv.Atoi(m)
	if err != nil {
		sc.fail(CodeBadNumber, msgBadInteger)
		return 0
	}
	sc.i += len(m)
	return n
}

var (
	numberRx = regexp.MustCompile(`^-?(?:0|[1-9][0-9]*)(?:\.[0-9]+)?(?:[eE][-+]?[0-9]+)?`)
	pinRx    = regexp.MustCompile(`^@[a-z0-9][a-z0-9-]*/[a-z0-9][a-z0-9-]*@[0-9]+\.[0-9]+\.[0-9]+`)
	typeRx   = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.]*(?:\[\])?`)
)

// pin reads a v2 node pin. v1's loose bare id is gone: a pin resolves to one
// immutable published version or the file does not say what it runs.
func (sc *scan) pin() string {
	if sc.failed() {
		return ""
	}
	m := pinRx.FindString(sc.s[sc.i:])
	if m == "" {
		sc.fail(CodeBadPin, msgBadPin)
		return ""
	}
	sc.i += len(m)
	return m
}

// typeExpr reads one §3.1 type expression and validates it eagerly, so a bad
// type is a POSITIONED parse finding rather than a surprise at validate time.
func (sc *scan) typeExpr() string {
	if sc.failed() {
		return ""
	}
	m := typeRx.FindString(sc.s[sc.i:])
	if m == "" {
		sc.fail(CodeBadType, types.ErrBadType.Error())
		return ""
	}
	if _, err := types.ParseTypeExpr(m); err != nil {
		sc.fail(CodeBadType, err.Error())
		return ""
	}
	sc.i += len(m)
	return m
}

// value reads a JSON value: string, number, literal, or canonical inline
// object/array.
func (sc *scan) value() any {
	if sc.failed() {
		return nil
	}
	switch sc.peek() {
	case '"':
		return sc.str()
	case '{', '[':
		return sc.inlineJSONValue()
	}
	if sc.eat("true") {
		return true
	}
	if sc.eat("false") {
		return false
	}
	if sc.eat("null") {
		return nil
	}
	m := numberRx.FindString(sc.s[sc.i:])
	if m == "" {
		sc.fail(CodeExpectedToken, msgExpectedValue)
		return nil
	}
	f, err := strconv.ParseFloat(m, 64)
	if err != nil {
		sc.fail(CodeExpectedToken, msgExpectedValue)
		return nil
	}
	sc.i += len(m)
	return f
}

// inlineJSONValue scans a balanced, string-aware object/array and decodes it.
func (sc *scan) inlineJSONValue() any {
	open := sc.peek()
	depth := 0
	inStr := false
	j := sc.i
	for ; j < len(sc.s); j++ {
		ch := sc.s[j]
		if inStr {
			switch ch {
			case '\\':
				j++
			case '"':
				inStr = false
			}
			continue
		}
		switch ch {
		case '"':
			inStr = true
		case '{', '[':
			depth++
		case '}', ']':
			depth--
		}
		if depth == 0 && (ch == '}' || ch == ']') {
			break
		}
	}
	if depth != 0 {
		if open == '{' {
			sc.fail(CodeBadInlineJSON, msgUnterminatedObject)
		} else {
			sc.fail(CodeBadInlineJSON, msgUnterminatedArray)
		}
		return nil
	}
	raw := sc.s[sc.i : j+1]
	var v any
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		sc.fail(CodeBadInlineJSON, msgInvalidInlineJSON)
		return nil
	}
	sc.i = j + 1
	return v
}

var trailingCommentRx = regexp.MustCompile(`^[ \t]+#`)

// end consumes the optional trailing comment and asserts end of line. Spacing
// before `#` is tolerated and normalizes on the next serialize.
func (sc *scan) end() *string {
	if sc.failed() || sc.done() {
		return nil
	}
	rest := sc.s[sc.i:]
	m := trailingCommentRx.FindString(rest)
	if m == "" {
		sc.fail(CodeTrailingInput, msgTrailingInput)
		return nil
	}
	text := commentText(rest[len(m)-1:])
	sc.i = len(sc.s)
	return &text
}

// commentText is the stored text of a comment: everything after `#`, outer
// spacing removed.
func commentText(raw string) string {
	return strings.Trim(strings.TrimPrefix(raw, "#"), " \t")
}

// isCommentLine reports a line whose first non-blank character opens a comment.
func isCommentLine(raw string) bool {
	return strings.HasPrefix(strings.TrimLeft(raw, " \t"), "#")
}
