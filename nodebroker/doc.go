// Package nodebroker is the secret broker a node redeems its handles against:
// the unix-socket HTTP protocol, the three refusal codes both SDKs match on,
// and the handle grammar.
//
// It lives here, below the runtime and below the delivery smoke, because the
// SAME broker must answer in both places. A node bundle cannot tell the two
// apart and must not have to: a second implementation is a second set of
// refusal semantics, and the one thing a credential redemption may never do is
// mean different things to different callers (DESIGN.md §3.5).
//
// The broker never resolves a secret. It answers with what it was handed, once
// per handle — the resolution happens outside, and the handle is the only
// credential that crosses into the sandbox.
package nodebroker
