// Package gitowner owns the derivation of the legacy uint32 git owner id from
// an owner login. It exists so the one derivation has one home: DESIGN.md
// §4.1 (source archive and node build) has codegen and the node build path
// creating repositories through the same legacy uint32 surface, and two copies
// of an id derivation are two ids the day one of them is "improved".
package gitowner
