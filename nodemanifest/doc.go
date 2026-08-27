// Package nodemanifest owns `node.json`: the manifest at the root of every
// node repository (DESIGN.md §1.2), the JSON-Schema 2020-12 subset its port
// and config shapes are written in (DESIGN.md §6), and the publication-time
// rules that keep the subset closed.
//
// Two properties are load-bearing and every rule here exists to hold one:
//
//  1. The subset is CLOSED. An unknown keyword is refused at publication, never
//     silently read as "unconstrained" — a manifest that says more than the
//     platform understands would type-check wires against a shape nobody
//     enforces.
//  2. Manifests are CANONICAL bytes. Canonicalize is the one normal form
//     (sorted keys, two-space indent, LF, ECMAScript number spelling), so a
//     manifest diff is a decision and never a re-serialization.
package nodemanifest
