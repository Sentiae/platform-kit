package nodemanifest

import _ "embed"

// schemaJSON is the published JSON-Schema of `node.json`. It documents the
// contract for authors and external tooling; the validator above is
// hand-written, so this package carries no schema-engine dependency and the
// two can only drift past T1.10, which pins them to each other.
//
//go:embed node-manifest.v1.json
var schemaJSON []byte
