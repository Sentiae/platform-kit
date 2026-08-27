package types

import (
	"errors"
	"regexp"
	"strings"

	"github.com/sentiae/platform-kit/nodemanifest"
)

// The three bad_type messages, verbatim.
var (
	ErrBadType     = errors.New("expected a type (string, number, integer, boolean, object, array, <prim>[] or <alias>.<DefName>)")
	ErrAnySpelled  = errors.New(`"any" is never spelled; omit the type`)
	ErrTypeTooLong = errors.New("a type name exceeds 64 characters")
)

// The TypeExpr kinds.
const (
	KindPrim = "prim"
	KindRef  = "ref"
)

// TypeExpr is a `.flow` type expression: a primitive, an array of a primitive,
// or a reference into a pinned manifest's `$defs`. `any` is not one of them —
// an omitted type means "the pin's constraint", which is a different statement
// from "anything".
type TypeExpr struct {
	Kind  string
	Prim  string
	Array bool
	Alias string
	Def   string
}

var prims = map[string]bool{
	"string": true, "number": true, "integer": true,
	"boolean": true, "object": true, "array": true,
}

var refRx = regexp.MustCompile(`^([a-z_][a-z0-9_]*)\.([A-Z][A-Za-z0-9]*)$`)

// ParseTypeExpr reads one type expression.
func ParseTypeExpr(s string) (TypeExpr, error) {
	if s == "any" {
		return TypeExpr{}, ErrAnySpelled
	}
	base := strings.TrimSuffix(s, "[]")
	array := base != s
	if prims[base] {
		if len(base) > 64 {
			return TypeExpr{}, ErrTypeTooLong
		}
		return TypeExpr{Kind: KindPrim, Prim: base, Array: array}, nil
	}
	if array {
		// `<alias>.<DefName>[]` — an array OF a reference is not spellable.
		return TypeExpr{}, ErrBadType
	}
	m := refRx.FindStringSubmatch(s)
	if m == nil {
		return TypeExpr{}, ErrBadType
	}
	if len(m[1]) > 64 || len(m[2]) > 64 {
		return TypeExpr{}, ErrTypeTooLong
	}
	return TypeExpr{Kind: KindRef, Alias: m[1], Def: m[2]}, nil
}

// String renders the expression's one canonical spelling.
func (e TypeExpr) String() string {
	if e.Kind == KindRef {
		return e.Alias + "." + e.Def
	}
	if e.Array {
		return e.Prim + "[]"
	}
	return e.Prim
}

// FromTypeExpr maps a type expression to the schema it names.
func FromTypeExpr(e TypeExpr) *nodemanifest.TypeRef {
	if e.Kind == KindRef {
		return &nodemanifest.TypeRef{Ref: "#/$defs/" + e.Def}
	}
	if e.Array {
		return &nodemanifest.TypeRef{Type: "array", Items: &nodemanifest.TypeRef{Type: e.Prim}}
	}
	return &nodemanifest.TypeRef{Type: e.Prim}
}
