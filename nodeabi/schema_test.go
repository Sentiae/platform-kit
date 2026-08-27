package nodeabi_test

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/sentiae/platform-kit/nodeabi"
	"github.com/sentiae/platform-kit/nodemanifest"
)

const schemaFile = "sentiae.node.v1.json"

// TestSchemaEmbedded proves the ABI schema document is valid, pinned and
// CANONICAL, and that RequestSchema/ResponseSchema — which are cut from the
// EMBEDDED copy — are byte-identical to nodemanifest.CanonicalJSON of the
// members of the file on disk. That is what ties the embed to the file: if the
// two ever differ, or if either is re-indented, this fails.
//
// It lives in nodeabi_test because it imports nodemanifest, which imports
// nodeabi; an internal test file would be an import cycle.
func TestSchemaEmbedded(t *testing.T) {
	raw, err := os.ReadFile(schemaFile)
	if err != nil {
		t.Fatalf("read %s: %v", schemaFile, err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("embedded schema is not valid JSON: %v", err)
	}
	if got := doc["$id"]; got != "https://sentiae.com/schemas/node-abi/v1.json" {
		t.Fatalf("$id = %v, want https://sentiae.com/schemas/node-abi/v1.json", got)
	}
	canonical, err := nodemanifest.CanonicalJSON(doc)
	if err != nil {
		t.Fatalf("CanonicalJSON: %v", err)
	}
	if string(canonical) != string(raw) {
		t.Fatalf("%s is not canonical bytes", schemaFile)
	}

	defs, ok := doc["$defs"].(map[string]any)
	if !ok {
		t.Fatalf("$defs is not an object")
	}
	for _, tc := range []struct {
		name string
		got  json.RawMessage
	}{
		{"Request", nodeabi.RequestSchema()},
		{"Response", nodeabi.ResponseSchema()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			want, err := nodemanifest.CanonicalJSON(defs[tc.name])
			if err != nil {
				t.Fatalf("CanonicalJSON: %v", err)
			}
			if string(tc.got) != string(want) {
				t.Fatalf("%sSchema() =\n%s\nwant\n%s", tc.name, tc.got, want)
			}
		})
	}

	// The pinned standalone bytes, written out: a re-indent, a key reorder or a
	// missing trailing LF all land here rather than in a caller.
	wantRequest := "{\n  \"additionalProperties\": false,\n  \"properties\": {\n    \"body\": {},\n    \"headers\": {\n      \"type\": \"object\"\n    },\n    \"method\": {\n      \"type\": \"string\"\n    },\n    \"path\": {\n      \"type\": \"string\"\n    },\n    \"query\": {\n      \"type\": \"object\"\n    }\n  },\n  \"required\": [\n    \"headers\",\n    \"method\",\n    \"path\",\n    \"query\"\n  ],\n  \"type\": \"object\"\n}\n"
	if string(nodeabi.RequestSchema()) != wantRequest {
		t.Fatalf("RequestSchema() =\n%q\nwant\n%q", nodeabi.RequestSchema(), wantRequest)
	}
	wantResponse := "{\n  \"additionalProperties\": false,\n  \"properties\": {\n    \"body\": {},\n    \"headers\": {\n      \"type\": \"object\"\n    },\n    \"status\": {\n      \"type\": \"integer\"\n    }\n  },\n  \"required\": [\n    \"status\"\n  ],\n  \"type\": \"object\"\n}\n"
	if string(nodeabi.ResponseSchema()) != wantResponse {
		t.Fatalf("ResponseSchema() =\n%q\nwant\n%q", nodeabi.ResponseSchema(), wantResponse)
	}
}
