package types

import (
	"encoding/json"
	"os"
	"sort"
	"testing"

	"github.com/sentiae/platform-kit/nodemanifest"
)

// assignableCase is one golden row of ../testdata/assignable.json. The same
// bytes drive the TypeScript suite; a verdict that differs between them is a
// wire the editor draws and the build refuses.
type assignableCase struct {
	Claim      string                `json:"claim"`
	Name       string                `json:"name"`
	SameDefs   bool                  `json:"sameDefs"`
	Source     *nodemanifest.TypeRef `json:"source"`
	SourceDefs nodemanifest.Defs     `json:"sourceDefs"`
	Target     *nodemanifest.TypeRef `json:"target"`
	TargetDefs nodemanifest.Defs     `json:"targetDefs"`
	Want       Verdict               `json:"want"`
}

// mandatoryCases is §5.2's closed list. The suite asserts SET EQUALITY with the
// fixture before it runs a single row: a corpus that silently lost a row would
// otherwise turn a deleted rule into a passing suite.
var mandatoryCases = []string{
	"equal_string", "both_unconstrained", "target_unconstrained", "source_unconstrained",
	"array_items_unknown", "integer_to_number", "number_to_integer", "boolean_to_string",
	"enum_subset", "enum_superset", "const_in_enum", "const_not_in_enum",
	"format_equal_narrower", "format_to_none", "none_to_format", "array_items_assignable",
	"scalar_to_array", "object_required_present", "object_optional_to_required",
	"object_listed_property_incompatible", "object_nested_unknown",
	"object_closed_target_open_source", "object_closed_both_subset",
	"object_closed_both_extra_prop", "ref_cycle_same_shape", "ref_cycle_different_names",
	"string_to_number", "number_to_string", "null_to_string", "boolean_exact",
	"enum_to_plain", "plain_to_enum", "format_equal", "format_mismatch",
	"array_items_incompatible", "array_to_scalar", "object_required_missing",
	"object_extra_prop_open_target", "ref_same_def_same_manifest", "ref_structural_equal",
	"ref_cycle_incompatible", "annotations_ignored",
}

func loadAssignableCases(t *testing.T) []assignableCase {
	t.Helper()
	b, err := os.ReadFile("../testdata/assignable.json")
	if err != nil {
		t.Fatalf("read corpus: %v", err)
	}
	var doc struct {
		Cases []assignableCase `json:"cases"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatalf("decode corpus: %v", err)
	}
	return doc.Cases
}

// TestFixtures_Assignable runs every golden row.
func TestFixtures_Assignable(t *testing.T) {
	cases := loadAssignableCases(t)

	got := make([]string, 0, len(cases))
	for _, c := range cases {
		got = append(got, c.Name)
	}
	want := append([]string(nil), mandatoryCases...)
	sort.Strings(got)
	sort.Strings(want)
	if len(got) != len(want) {
		t.Fatalf("corpus has %d cases, §5.2 pins %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("case name set differs at %d: corpus %q, §5.2 %q", i, got[i], want[i])
		}
	}

	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			ctx := Context{SourceDefs: c.SourceDefs, TargetDefs: c.TargetDefs}
			if c.SameDefs {
				ctx.TargetDefs = c.SourceDefs
				ctx.SameManifest = true
			}
			res := Assignable(c.Source, c.Target, ctx)
			if res.Verdict != c.Want {
				t.Fatalf("%s: got %q (%s), want %q — %s",
					c.Name, res.Verdict, res.Reason, c.Want, c.Claim)
			}
			switch res.Verdict {
			case VerdictUnknown, VerdictIncompatible:
				if res.Reason == "" {
					t.Fatalf("%s: verdict %q carries no reason", c.Name, res.Verdict)
				}
			default:
				if res.Reason != "" {
					t.Fatalf("%s: verdict %q carries reason %q", c.Name, res.Verdict, res.Reason)
				}
			}
		})
	}
}

// TestAssignable_RefUnrollsBeforeIdentity pins §5.2's rev-3 ordering: the one
// unrolling happens BEFORE any identity or equality shortcut, so two same-named
// refs in one manifest whose def is missing refuse rather than pass. The corpus
// cannot distinguish the two orderings, which is why this row is written here.
func TestAssignable_RefUnrollsBeforeIdentity(t *testing.T) {
	ref := &nodemanifest.TypeRef{Ref: "#/$defs/Missing"}
	got := Assignable(ref, ref, Context{
		SourceDefs:   nodemanifest.Defs{},
		TargetDefs:   nodemanifest.Defs{},
		SameManifest: true,
	})
	want := Result{Verdict: VerdictIncompatible, Reason: "unresolved $ref #/$defs/Missing"}
	if got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

// TestAssignable_ReasonsPropagate pins the two things a reader repairs a wire
// with: WHERE the mismatch is, and — when several parts fail — which one the
// message names.
func TestAssignable_ReasonsPropagate(t *testing.T) {
	t.Run("nested_property_path", func(t *testing.T) {
		source := object(props{"a": object(props{"b": prim("string")}, "b")}, "a")
		target := object(props{"a": object(props{"b": prim("integer")}, "b")}, "a")
		got := Assignable(source, target, Context{})
		want := Result{
			Verdict: VerdictIncompatible,
			Reason:  `at "a.b": string cannot feed integer`,
		}
		if got != want {
			t.Fatalf("got %+v, want %+v", got, want)
		}
	})

	t.Run("required_parts_report_in_byte_order", func(t *testing.T) {
		// The target requires [b, a] in DECLARATION order and the source requires
		// neither, so both parts fail; §5.2 rule 10(a) iterates in byte order, so
		// the message names "a".
		source := object(props{"a": prim("string"), "b": prim("string")})
		target := &nodemanifest.TypeRef{
			Type:       "object",
			Properties: map[string]*nodemanifest.TypeRef{"a": prim("string"), "b": prim("string")},
			Required:   []string{"b", "a"},
		}
		got := Assignable(source, target, Context{})
		want := Result{
			Verdict: VerdictIncompatible,
			Reason:  `required property "a" is not required by the source`,
		}
		if got != want {
			t.Fatalf("got %+v, want %+v", got, want)
		}
	})
}

type props map[string]*nodemanifest.TypeRef

func prim(t string) *nodemanifest.TypeRef { return &nodemanifest.TypeRef{Type: t} }

func object(p props, required ...string) *nodemanifest.TypeRef {
	return &nodemanifest.TypeRef{Type: "object", Properties: p, Required: required}
}
