package flowlang

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/sentiae/platform-kit/nodeabi"
)

// Version is the language major this package reads and writes. There is no
// tolerant mode: a file of any other major is refused rather than half-read.
const Version = 2

// Trivia is the comment set of ONE single-line construct. Text excludes the
// leading `#`.
type Trivia struct {
	// Leading is the contiguous comment lines directly above the construct.
	Leading []string
	// Trailing is the comment after the construct's last token, on the same
	// line. A nil pointer means "no comment"; a pointer to "" means a bare `#`.
	Trailing *string
}

// NodeTrivia is one node block's comment set, keyed so that a construct that
// moves in the canonical order carries its comments with it.
type NodeTrivia struct {
	Leading  []string
	Open     *string
	Close    *string
	BodyTail []string
	// Config is keyed by config key.
	Config map[string]*Trivia
	// Ports is keyed by PortTriviaKey(dir, id).
	Ports map[string]*Trivia
	// Layout is the node's own line inside the `layout` block. Parse records
	// that trivia on the Layout entry (mirroring flow-lang.ts); this field is
	// the projection-side home for the same text when trivia rides node data.
	Layout *Trivia
}

// FreeComment is a comment group with no construct of its own — it is anchored
// AFTER one. Deleting the anchor moves the group by one construct; it never
// deletes it.
type FreeComment struct {
	// After is `""` (file head), `header`, `use:<alias>`, `node:<slug>`,
	// `wire:<from>.<fromPort>-><to>.<toPort>` or `layout`.
	After string
	Lines []string
}

// FileTrivia is the file-scoped half of the comment model.
type FileTrivia struct {
	Header *Trivia
	// Use is keyed by `use` alias.
	Use         map[string]*Trivia
	Free        []FreeComment
	Layout      *Trivia
	LayoutClose *string
	LayoutTail  []string
}

// Use is one `use <alias> = <pin>` line.
type Use struct {
	Alias string
	Pin   string
	Line  int
}

// Config is one config line. Value is decoded JSON: string, float64, bool, nil,
// map[string]any or []any.
type Config struct {
	Key   string
	Value any
	Line  int
}

// Port is one `port in`/`port out` line inside a node body.
type Port struct {
	// Dir is "in" or "out".
	Dir string
	ID  string
	// Type is the type-expression text; "" means the line omitted it.
	Type string
	// ConfigKey is the config key a promotion exposes; "" means not a promotion.
	ConfigKey string
	Label     *string
	Line      int
}

// Node is one `node <slug>: <alias> { … }` block.
type Node struct {
	Slug     string
	Alias    string
	Title    *string
	Disabled bool
	Config   []Config
	Ports    []Port
	Trivia   *NodeTrivia
	Line     int
}

// Wire is one `wire <from>.<fromPort> -> <to>.<toPort>` line. A v2 wire is
// data and names both ports.
type Wire struct {
	From     string
	FromPort string
	To       string
	ToPort   string
	Trivia   *Trivia
	Line     int
}

// Layout is one line of the `layout` block.
type Layout struct {
	Slug   string
	X, Y   int
	W      *int
	Trivia *Trivia
	Line   int
}

// Doc is a parsed (or synthesized) `.flow` document.
type Doc struct {
	Name       string
	Version    int
	Uses       []Use
	Nodes      []Node
	Wires      []Wire
	Layout     []Layout
	FileTrivia FileTrivia
}

// PortTriviaKey is the trivia key of one port line.
func PortTriviaKey(dir, id string) string { return dir + ":" + id }

// WireAnchorKey is the free-comment anchor of one wire.
func WireAnchorKey(w Wire) string {
	return "wire:" + w.From + "." + w.FromPort + "->" + w.To + "." + w.ToPort
}

// AliasFor is the `use` alias a pin gets when the editor mints one.
func AliasFor(pin nodeabi.Pin) string { return NormalizeIdent(pin.Name) }

var nonIdentRx = regexp.MustCompile(`[^a-z0-9]+`)

// NormalizeIdent is §2.2's identifier normalization: lower-case, every run of
// non-`[a-z0-9]` collapsed to `_`, trimmed, never empty, never leading-digit.
func NormalizeIdent(text string) string {
	slug := strings.Trim(nonIdentRx.ReplaceAllString(strings.ToLower(text), "_"), "_")
	if slug == "" {
		return "x"
	}
	if slug[0] >= '0' && slug[0] <= '9' {
		return "n_" + slug
	}
	return slug
}

// UniqueIdent suffixes `_2`, `_3`… until base no longer collides.
func UniqueIdent(base string, taken map[string]bool) string {
	if !taken[base] {
		return base
	}
	for n := 2; ; n++ {
		candidate := base + "_" + strconv.Itoa(n)
		if !taken[candidate] {
			return candidate
		}
	}
}
