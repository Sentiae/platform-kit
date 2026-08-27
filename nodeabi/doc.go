// Package nodeabi owns the `sentiae.node/v1` ABI: the CALL document a runtime
// hands a node process on stdin, the RESULT document the process writes to
// stdout, and the pin/qualified-name grammar both sides name a node with.
//
// DESIGN.md §3.3 (node ABI layer 2 — the wire, identical on Docker and
// Firecracker) and §3.5 (secret handles) are the design; this package is the
// one validator both the runtime and the two SDKs are checked against. It
// imports nothing outside the standard library on purpose: it is the bottom of
// the node stack, so nodemanifest, flowlang and the SDKs may all depend on it.
//
// The embedded sentiae.node.v1.json is documentation plus the source of
// RequestSchema and ResponseSchema; the validator here is hand-written, so the
// ABI carries no JSON-Schema engine dependency.
package nodeabi
