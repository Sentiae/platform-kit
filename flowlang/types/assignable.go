package types

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/sentiae/platform-kit/nodemanifest"
)

// Verdict is one of the four answers a wire can get. They are NOT a scale of
// confidence: `unknown` names a missing promise (the repair is a validator),
// `incompatible` names two shapes that cannot meet (the repair is a different
// wire).
type Verdict string

// The four verdicts. The wire values are the serialized contract shared with
// the TypeScript implementation.
const (
	VerdictExact        Verdict = "exact"
	VerdictAssignable   Verdict = "assignable"
	VerdictUnknown      Verdict = "unknown"
	VerdictIncompatible Verdict = "incompatible"
)

// Result is one verdict and, when the verdict refuses or abstains, the sentence
// a diagnostic interpolates. Reason is non-empty iff Verdict is VerdictUnknown
// or VerdictIncompatible.
type Result struct {
	Verdict Verdict
	Reason  string
}

// Context carries the two manifests' `$defs` and whether both ports were pinned
// by the SAME manifest — the one fact ref identity depends on.
type Context struct {
	SourceDefs   nodemanifest.Defs
	TargetDefs   nodemanifest.Defs
	SameManifest bool
}

// reasonUnknownType is the answer for a port whose schema this client does not
// have. It is deliberately distinct from `{}`, which is a schema that promises
// nothing — "I do not know" and "there is nothing to know" are different repairs.
const reasonUnknownType = "port type is not known"

// refPair is one (source $ref, target $ref) couple on the recursion path. A
// pair may be unrolled once; re-entering it is what terminates a cyclic type.
type refPair struct{ source, target string }

// Assignable decides whether a value described by source may flow into a port
// described by target. DESIGN.md §6.
func Assignable(source, target *nodemanifest.TypeRef, ctx Context) Result {
	if source == nil || target == nil {
		return Result{Verdict: VerdictUnknown, Reason: reasonUnknownType}
	}
	return assign(
		nodemanifest.StripAnnotations(source),
		nodemanifest.StripAnnotations(target),
		ctx, nil, "",
	)
}

func assign(source, target *nodemanifest.TypeRef, ctx Context, path []refPair, prop string) Result {
	if source == nil {
		source = &nodemanifest.TypeRef{}
	}
	if target == nil {
		target = &nodemanifest.TypeRef{}
	}

	// Rules 1 and 2. The unrolling precedes every shortcut: no identity test and
	// no canonical-equality test is ever applied to a `$ref` object itself, so a
	// pair of same-named refs whose def is missing refuses instead of passing.
	if source.Ref != "" || target.Ref != "" {
		if source.Ref != "" && target.Ref != "" {
			pair := refPair{source: source.Ref, target: target.Ref}
			for _, p := range path {
				if p == pair {
					if source.Ref == target.Ref && ctx.SameManifest {
						return Result{Verdict: VerdictExact}
					}
					return Result{Verdict: VerdictAssignable}
				}
			}
			path = append(append([]refPair(nil), path...), pair)
		}
		if source.Ref != "" {
			body, ok := resolve(source.Ref, ctx.SourceDefs)
			if !ok {
				return incompatible(prop, "unresolved $ref "+source.Ref)
			}
			source = body
		}
		if target.Ref != "" {
			body, ok := resolve(target.Ref, ctx.TargetDefs)
			if !ok {
				return incompatible(prop, "unresolved $ref "+target.Ref)
			}
			target = body
		}
	}

	// Rule 3.
	if canonical(source) == canonical(target) {
		return Result{Verdict: VerdictExact}
	}
	// Rule 4.
	if target.IsUnconstrained() {
		return Result{Verdict: VerdictAssignable}
	}
	// Rule 5.
	if source.IsUnconstrained() {
		return Result{Verdict: VerdictUnknown, Reason: at(prop, "source is unconstrained")}
	}

	// Past rule 3 the two schemas provably differ somewhere, so no node below
	// can be `exact`: assignable is the floor for every remaining rule.
	floor := Result{Verdict: VerdictAssignable}

	// Rule 6 — type.
	sv, sHasValues := values(source)
	tv, tHasValues := values(target)
	switch {
	case source.Type == target.Type:
	case source.Type != "" && target.Type != "":
		if !(source.Type == "integer" && target.Type == "number") {
			return incompatible(prop, fmt.Sprintf("%s cannot feed %s", source.Type, target.Type))
		}
	default:
		if !sHasValues || !tHasValues {
			return incompatible(prop, fmt.Sprintf("%s cannot feed %s",
				typeName(source.Type), typeName(target.Type)))
		}
	}

	// Rule 7 — const/enum.
	switch {
	case !tHasValues:
	case !sHasValues:
		return incompatible(prop, "source has no value set for target enum")
	default:
		for _, v := range sv {
			if !containsValue(tv, v) {
				return incompatible(prop, fmt.Sprintf("source value %s is not in target enum", rawCanonical(v)))
			}
		}
	}

	// Rule 8 — format.
	if source.Format != target.Format && target.Format != "" {
		return incompatible(prop, fmt.Sprintf("format %s cannot feed format %s",
			formatName(source.Format), formatName(target.Format)))
	}

	// Rule 9 — array.
	if source.Type == "array" && target.Type == "array" {
		return worst(floor, assign(source.Items, target.Items, ctx, path, join(prop, "items")))
	}

	// Rule 10 — object.
	if source.Type == "object" && target.Type == "object" {
		return worst(floor, objectParts(source, target, ctx, path, prop)...)
	}

	// Rule 11.
	return floor
}

// objectParts evaluates §5.2 rule 10's parts (a), (b) and (c) in that order.
func objectParts(source, target *nodemanifest.TypeRef, ctx Context, path []refPair, prop string) []Result {
	var parts []Result

	sourceRequired := make(map[string]bool, len(source.Required))
	for _, p := range source.Required {
		sourceRequired[p] = true
	}
	for _, p := range sortedStrings(target.Required) {
		if !sourceRequired[p] {
			parts = append(parts, incompatible(prop,
				fmt.Sprintf("required property %q is not required by the source", p)))
			break
		}
	}

	if target.AdditionalProperties != nil && !*target.AdditionalProperties {
		if source.AdditionalProperties == nil || *source.AdditionalProperties {
			parts = append(parts, incompatible(prop, "target is closed but the source is open"))
		} else {
			for _, k := range sortedRefKeys(source.Properties) {
				if _, ok := target.Properties[k]; !ok {
					parts = append(parts, incompatible(prop,
						fmt.Sprintf("property %q is not accepted by the closed target", k)))
					break
				}
			}
		}
	}

	for _, p := range sortedRefKeys(target.Properties) {
		sub, ok := source.Properties[p]
		if !ok {
			continue
		}
		parts = append(parts, assign(sub, target.Properties[p], ctx, path, join(prop, p)))
	}
	return parts
}

// worst returns the strictest verdict among floor and parts, keeping the reason
// of the FIRST part that reaches it.
func worst(floor Result, parts ...Result) Result {
	best := floor
	for _, p := range parts {
		if rank(p.Verdict) > rank(best.Verdict) {
			best = p
		}
	}
	return best
}

func rank(v Verdict) int {
	switch v {
	case VerdictExact:
		return 0
	case VerdictAssignable:
		return 1
	case VerdictUnknown:
		return 2
	default:
		return 3
	}
}

func incompatible(prop, reason string) Result {
	return Result{Verdict: VerdictIncompatible, Reason: at(prop, reason)}
}

// at prefixes a reason with the property path it was observed at.
func at(prop, reason string) string {
	if prop == "" {
		return reason
	}
	return fmt.Sprintf("at %q: %s", prop, reason)
}

func join(prop, seg string) string {
	if prop == "" {
		return seg
	}
	return prop + "." + seg
}

func resolve(ref string, defs nodemanifest.Defs) (*nodemanifest.TypeRef, bool) {
	body, ok := defs[strings.TrimPrefix(ref, "#/$defs/")]
	if !ok || body == nil {
		return nil, false
	}
	return nodemanifest.StripAnnotations(body), true
}

// values is a schema's value set: `const` as a single value, else `enum`.
func values(t *nodemanifest.TypeRef) ([]json.RawMessage, bool) {
	if len(t.Const) > 0 {
		return []json.RawMessage{t.Const}, true
	}
	if len(t.Enum) > 0 {
		return t.Enum, true
	}
	return nil, false
}

func containsValue(set []json.RawMessage, v json.RawMessage) bool {
	want := rawCanonical(v)
	for _, e := range set {
		if rawCanonical(e) == want {
			return true
		}
	}
	return false
}

// rawCanonical is the canonical spelling of one JSON literal, single-line —
// the form a reason interpolates.
func rawCanonical(raw json.RawMessage) string {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return string(raw)
	}
	return canonicalValue(v)
}

func canonical(t *nodemanifest.TypeRef) string { return canonicalValue(t) }

func canonicalValue(v any) string {
	b, err := nodemanifest.CanonicalJSON(v)
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	return strings.TrimSuffix(string(b), "\n")
}

func typeName(t string) string {
	if t == "" {
		return "untyped"
	}
	return t
}

func formatName(f string) string {
	if f == "" {
		return "none"
	}
	return f
}

func sortedStrings(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}

func sortedRefKeys(m map[string]*nodemanifest.TypeRef) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
