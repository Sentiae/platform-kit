package nodemanifest

import (
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// TestSchemaFile_ParityWithManifestStruct proves the published JSON-Schema and
// the Go struct describe the SAME document: same top-level keys, and the
// schema's `required` is every key except `$defs`. Without this the two drift
// silently — the schema is what external authors read, the struct is what the
// platform enforces.
func TestSchemaFile_ParityWithManifestStruct(t *testing.T) {
	var doc struct {
		Properties map[string]json.RawMessage `json:"properties"`
		Required   []string                   `json:"required"`
		ID         string                     `json:"$id"`
	}
	if err := json.Unmarshal(schemaJSON, &doc); err != nil {
		t.Fatalf("embedded schema is not valid JSON: %v", err)
	}
	if doc.ID != SchemaURL {
		t.Fatalf("$id = %q, want %q", doc.ID, SchemaURL)
	}

	structTags := jsonTags(reflect.TypeOf(Manifest{}))
	schemaKeys := make([]string, 0, len(doc.Properties))
	for k := range doc.Properties {
		schemaKeys = append(schemaKeys, k)
	}
	sort.Strings(schemaKeys)
	if !reflect.DeepEqual(structTags, schemaKeys) {
		t.Fatalf("schema properties = %v, Manifest json tags = %v", schemaKeys, structTags)
	}

	wantRequired := make([]string, 0, len(structTags))
	for _, k := range structTags {
		if k == "$defs" {
			continue
		}
		wantRequired = append(wantRequired, k)
	}
	gotRequired := append([]string(nil), doc.Required...)
	sort.Strings(gotRequired)
	if !reflect.DeepEqual(gotRequired, wantRequired) {
		t.Fatalf("schema required = %v, want %v", gotRequired, wantRequired)
	}
}

// jsonTags is the sorted JSON tag set of a struct's exported fields.
func jsonTags(t reflect.Type) []string {
	out := make([]string, 0, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if f.PkgPath != "" {
			continue
		}
		tag := f.Tag.Get("json")
		if tag == "" || tag == "-" {
			continue
		}
		out = append(out, strings.Split(tag, ",")[0])
	}
	sort.Strings(out)
	return out
}

// TestSchemaFile_Canonical proves the embedded schema is itself canonical
// bytes, so `node.json` tooling can round-trip it through Canonicalize.
func TestSchemaFile_Canonical(t *testing.T) {
	got, err := Canonicalize(schemaJSON)
	if err != nil {
		t.Fatalf("Canonicalize: %v", err)
	}
	if string(got) != string(schemaJSON) {
		t.Fatalf("embedded schema is not canonical")
	}
}
