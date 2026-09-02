package flowlang

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// goldenPlan09SHA256 is the hash of testdata/plan/09_phase4_acceptance.plan.json.
//
// It is DERIVED from the file this package produces, never typed. Three
// repositories decode the same bytes — platform-kit here, the runtime's
// CreateGraph mapper, and the code generator's proto mapping — so the constant
// is what makes a silent divergence in any one of them fail in all three.
const goldenPlan09SHA256 = "2b5cd5be7d90f00f5540c9ee301b0047b9dd42eabb40f2b6a589e31a2cc555fd"

const goldenPlan09Path = "testdata/plan/09_phase4_acceptance.plan.json"

// T0.6 — TestGoldenPlan_09 pins the wire projection of the Phase 4 acceptance
// flow on three axes at once: that this package still PRODUCES those bytes, that
// the bytes on disk are the ones every other repository pinned, and that the
// document is key-sorted so a re-encode anywhere is byte-stable.
//
// CONTROL: flip one `to_port` value in the golden (e.g. reply's `body` to
// `bodyx`) and the produced-bytes comparison plus the hash both go red.
func TestGoldenPlan_09(t *testing.T) {
	want, err := os.ReadFile(goldenPlan09Path)
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}

	t.Run("the package still produces these bytes", func(t *testing.T) {
		m := corpusManifests(t)
		doc, diags := Parse(readFixture(t, filepath.Join("testdata", "09_phase4_acceptance.flow")))
		if doc == nil || len(diags) != 0 {
			t.Fatalf("Parse: doc=%v diags=%+v", doc != nil, diags)
		}
		plan, vdiags := Schedule(doc, m)
		if plan == nil {
			t.Fatalf("Schedule refused: %+v", vdiags)
		}
		got, err := json.MarshalIndent(ToWire(Lower(plan)), "", "  ")
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		got = append(got, '\n')
		if string(got) != string(want) {
			t.Fatalf("projection differs:\n--- got ---\n%s\n--- want ---\n%s", got, want)
		}
	})

	t.Run("the bytes are the ones every repository pinned", func(t *testing.T) {
		sum := sha256.Sum256(want)
		if got := hex.EncodeToString(sum[:]); got != goldenPlan09SHA256 {
			t.Fatalf("sha256 = %s, the pinned constant is %s (S1 and S5 pin the same value)", got, goldenPlan09SHA256)
		}
	})

	// Go marshals a map[string]any with its keys sorted, so re-encoding the
	// decoded document reproduces the file only if the file was already
	// key-sorted. A file that was not would still decode fine and still hash
	// consistently — this is the only check that catches it.
	t.Run("the document is key-sorted", func(t *testing.T) {
		var generic map[string]any
		if err := json.Unmarshal(want, &generic); err != nil {
			t.Fatalf("decode: %v", err)
		}
		got, err := json.MarshalIndent(generic, "", "  ")
		if err != nil {
			t.Fatalf("re-encode: %v", err)
		}
		got = append(got, '\n')
		if string(got) != string(want) {
			t.Fatalf("the golden is not key-sorted:\n--- re-encoded ---\n%s\n--- file ---\n%s", got, want)
		}
	})

	// A strict decode into the wire types proves the file carries exactly the
	// declared keys — the same check S1 and S5 run, whose control is renaming
	// one `to_port` to `toPort`.
	t.Run("a strict decode accepts exactly the declared keys", func(t *testing.T) {
		dec := json.NewDecoder(bytes.NewReader(want))
		dec.DisallowUnknownFields()
		var w WirePlan
		if err := dec.Decode(&w); err != nil {
			t.Fatalf("strict decode: %v", err)
		}
		if len(w.Nodes) != 5 || len(w.Edges) != 4 {
			t.Fatalf("decoded %d nodes and %d edges, want 5 and 4", len(w.Nodes), len(w.Edges))
		}
	})
}

// TestToWire_NeverNull proves the projection has no null-valued collections: a
// decoder on any of the three sides would otherwise need a nil branch per field,
// and one of them would eventually forget.
func TestToWire_NeverNull(t *testing.T) {
	empty, err := json.Marshal(ToWire(&Plan{Nodes: map[string]PlanNode{}}))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if got := string(empty); got != `{"edges":[],"nodes":[],"order":[],"responds":[],"trigger":""}` {
		t.Fatalf("empty plan projects as %s", got)
	}

	lone, err := json.Marshal(ToWire(&Plan{
		Sequence: []string{"a"},
		Order:    []string{"a"},
		Nodes:    map[string]PlanNode{"a": {Slug: "a", Alias: "x", Pin: "@s/x@1.0.0"}},
	}))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	const want = `{"edges":[],"nodes":[{"alias":"x","outputs":[],"pin":"@s/x@1.0.0","promoted":{},` +
		`"required":[],"role":"","slug":"a","upstream":[]}],"order":["a"],"responds":[],"trigger":""}`
	if got := string(lone); got != want {
		t.Fatalf("node with no collections projects as:\n%s\nwant:\n%s", got, want)
	}
}
