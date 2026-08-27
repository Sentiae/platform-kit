// Package flowlang is the `.flow` v2 LANGUAGE: lexer, parser, serializer,
// trivia model, canonical form, semantic validation and the execution plan —
// DESIGN.md §3.2–§3.4 and §6.
//
// It is one of TWO implementations of one contract (the other is the editor's
// portal/src/features/flow-editor/flow-lang.ts). The corpus under `testdata/`
// is the shared oracle: both must parse the same bytes into the same model,
// emit the same bytes back, and report the same diagnostics at the same lines.
// A divergence is a file the user edits in one place and the platform rejects
// in the other, which is why the fixtures are hashed rather than described.
//
// Three properties are load-bearing and every rule here exists to hold one:
//
//  1. Machine-written. The serializer is the only author; the parser accepts
//     exactly the canonical form and reports a positioned diagnostic for
//     anything else rather than guessing.
//  2. Byte-deterministic. One model plus one trivia set yields one byte
//     sequence, so every diff hunk is a semantic decision.
//  3. Comments survive. A comment is authored text whose home is a trivia
//     attachment to a construct; no serializer path rebuilds a construct's
//     line without it.
package flowlang
