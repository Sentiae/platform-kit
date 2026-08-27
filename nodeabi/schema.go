package nodeabi

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
)

// schemaJSON is the ABI's JSON-Schema document. It is documentation for the
// wire plus the single source of the Request and Response shapes; the
// validator above is hand-written so this package needs no schema engine.
//
//go:embed sentiae.node.v1.json
var schemaJSON []byte

// RequestSchema is the reserved trigger input's shape, as a standalone
// canonical JSON-Schema subset document (sorted keys, two-space indent, one
// trailing LF — byte-identical to nodemanifest.CanonicalJSON of the same value).
func RequestSchema() json.RawMessage { return defSchema("Request") }

// ResponseSchema is the shape a role=respond node's `response` output carries.
func ResponseSchema() json.RawMessage { return defSchema("Response") }

func defSchema(name string) json.RawMessage {
	var doc struct {
		Defs map[string]any `json:"$defs"`
	}
	if err := json.Unmarshal(schemaJSON, &doc); err != nil {
		panic(fmt.Sprintf("nodeabi: embedded schema is not valid JSON: %v", err))
	}
	v, ok := doc.Defs[name]
	if !ok {
		panic("nodeabi: embedded schema has no $defs." + name)
	}
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		panic(fmt.Sprintf("nodeabi: $defs.%s does not encode: %v", name, err))
	}
	return json.RawMessage(b.Bytes())
}
