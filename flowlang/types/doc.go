// Package types owns structural assignability between two node port schemas —
// DESIGN.md §6, the rule that decides whether a wire may exist.
//
// It is one of TWO implementations of one truth table (the other is
// portal/src/shared/canvas/core/ports.ts); both are pinned to the same golden
// fixture corpus, because a verdict that differs between the editor and the
// build is a wire the user drew and the platform then refused.
//
// The four verdicts are not a scale of confidence. `unknown` means "the source
// promises nothing here", which is a different repair (insert a validator) from
// `incompatible` ("these shapes cannot meet"), which is why the silent
// coercions the six-string vocabulary used to allow are gone.
package types
