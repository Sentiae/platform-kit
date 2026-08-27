package flowlang

import (
	"testing"

	"github.com/sentiae/platform-kit/nodeabi"
)

func reverse[T any](in []T) []T {
	out := make([]T, len(in))
	for i := range in {
		out[len(in)-1-i] = in[i]
	}
	return out
}

// TestCanonicalize_Restores proves the canonical form is RECOVERED, not merely
// preserved: a document whose every ordered collection has been reversed comes
// back byte-identical to the fixture. That is the property that lets a machine
// author the file without the reader seeing re-flowed diffs.
func TestCanonicalize_Restores(t *testing.T) {
	manifests := corpusManifests(t)

	t.Run("reversed_collections_come_back", func(t *testing.T) {
		want := readFixture(t, "testdata/02_order_intake.flow")
		doc, diags := Parse(want)
		if doc == nil || len(diags) != 0 {
			t.Fatalf("Parse: doc=%v diags=%+v", doc != nil, diags)
		}
		doc.Uses = reverse(doc.Uses)
		doc.Wires = reverse(doc.Wires)
		doc.Layout = reverse(doc.Layout)
		for i := range doc.Nodes {
			doc.Nodes[i].Config = reverse(doc.Nodes[i].Config)
			doc.Nodes[i].Ports = reverse(doc.Nodes[i].Ports)
		}

		scrambled, err := Serialize(doc)
		if err != nil {
			t.Fatalf("Serialize: %v", err)
		}
		if scrambled == want {
			// Positive anchor: without it the assertion below would pass on a
			// `reverse` that silently did nothing.
			t.Fatal("reversing the collections did not change the bytes")
		}

		got, err := Serialize(Canonicalize(doc, manifests))
		if err != nil {
			t.Fatalf("Serialize: %v", err)
		}
		if got != want {
			t.Fatalf("canonicalize did not restore:\n--- got ---\n%s\n--- want ---\n%s", got, want)
		}
	})

	// D-369: a config line that restates the pinned default is not written, and
	// its comments RE-ANCHOR onto the next surviving construct. A comment whose
	// construct is removed moves; it never disappears.
	t.Run("default_equal_line_reanchors_its_comments", func(t *testing.T) {
		const in = "flow \"f\" v2\n\n" +
			"use secure_http = @acme/secure-http@2.1.0\n\n" +
			"node worker: secure_http {\n" +
			"\t# Leading for retries.\n" +
			"\tretries = 0 # Trailing on retries.\n" +
			"\turl = \"https://api.example.net/x\"\n" +
			"}\n"
		const want = "flow \"f\" v2\n\n" +
			"use secure_http = @acme/secure-http@2.1.0\n\n" +
			"node worker: secure_http {\n" +
			"\t# Leading for retries.\n" +
			"\t# Trailing on retries.\n" +
			"\turl = \"https://api.example.net/x\"\n" +
			"}\n"

		doc, diags := Parse(in)
		if doc == nil || len(diags) != 0 {
			t.Fatalf("Parse: doc=%v diags=%+v", doc != nil, diags)
		}
		got, err := Serialize(Canonicalize(doc, manifests))
		if err != nil {
			t.Fatalf("Serialize: %v", err)
		}
		if got != want {
			t.Fatalf("got\n%s\nwant\n%s", got, want)
		}
	})

	// The same rule with nothing left to carry the comments: they land on the
	// node's body tail rather than on the floor.
	t.Run("default_equal_line_with_no_successor_falls_to_body_tail", func(t *testing.T) {
		const in = "flow \"f\" v2\n\n" +
			"use webhook_trigger = @sentiae/webhook-trigger@1.0.0\n\n" +
			"node intake: webhook_trigger {\n" +
			"\t# Leading for method.\n" +
			"\tmethod = \"POST\"\n" +
			"}\n"
		const want = "flow \"f\" v2\n\n" +
			"use webhook_trigger = @sentiae/webhook-trigger@1.0.0\n\n" +
			"node intake: webhook_trigger {\n" +
			"\t# Leading for method.\n" +
			"}\n"

		doc, diags := Parse(in)
		if doc == nil || len(diags) != 0 {
			t.Fatalf("Parse: doc=%v diags=%+v", doc != nil, diags)
		}
		got, err := Serialize(Canonicalize(doc, manifests))
		if err != nil {
			t.Fatalf("Serialize: %v", err)
		}
		if got != want {
			t.Fatalf("got\n%s\nwant\n%s", got, want)
		}
	})
}

// TestSerialize_Refusals pins the two states the emitter will not write: a
// document of the wrong major, and a v2 wire that names no port. Both are
// model corruption, and writing them would produce a file this package's own
// parser refuses.
func TestSerialize_Refusals(t *testing.T) {
	t.Run("wrong_major", func(t *testing.T) {
		if _, err := Serialize(&Doc{Name: "f", Version: 1}); err == nil {
			t.Fatal("Serialize accepted a v1 document")
		}
		if _, err := Serialize(&Doc{Name: "f", Version: 2}); err != nil {
			t.Fatalf("Serialize refused a v2 document: %v", err)
		}
	})
	t.Run("portless_wire", func(t *testing.T) {
		doc := &Doc{Name: "f", Version: 2, Wires: []Wire{{From: "a", To: "b"}}}
		if _, err := Serialize(doc); err == nil {
			t.Fatal("Serialize accepted a portless wire")
		}
		doc.Wires[0].FromPort = "o"
		doc.Wires[0].ToPort = "i"
		if _, err := Serialize(doc); err != nil {
			t.Fatalf("Serialize refused a data wire: %v", err)
		}
	})
}

// TestIdentifiers pins the slug/alias minting the editor and the importer share.
// A slug is FILE IDENTITY: every wire, layout line and free comment names it, so
// a normalization that drifted between two clients would strand comments.
func TestIdentifiers(t *testing.T) {
	t.Run("normalize", func(t *testing.T) {
		tests := []struct{ name, in, want string }{
			{"lowercases", "Fetch Orders", "fetch_orders"},
			{"collapses_runs", "a---b", "a_b"},
			{"trims_edges", "__a__", "a"},
			{"empty_becomes_x", "!!!", "x"},
			{"leading_digit_is_prefixed", "1st step", "n_1st_step"},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				if got := NormalizeIdent(tt.in); got != tt.want {
					t.Fatalf("NormalizeIdent(%q) = %q, want %q", tt.in, got, tt.want)
				}
			})
		}
	})

	t.Run("unique", func(t *testing.T) {
		taken := map[string]bool{"a": true, "a_2": true}
		if got := UniqueIdent("a", taken); got != "a_3" {
			t.Fatalf("UniqueIdent = %q, want a_3", got)
		}
		if got := UniqueIdent("b", taken); got != "b" {
			t.Fatalf("UniqueIdent = %q, want b", got)
		}
	})

	t.Run("alias_for_pin", func(t *testing.T) {
		pin, err := nodeabi.ParsePin("@acme/secure-http@2.1.0")
		if err != nil {
			t.Fatalf("ParsePin: %v", err)
		}
		if got := AliasFor(pin); got != "secure_http" {
			t.Fatalf("AliasFor = %q, want secure_http", got)
		}
	})
}
