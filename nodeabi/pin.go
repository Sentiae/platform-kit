package nodeabi

import (
	"fmt"
	"regexp"
	"strings"
)

// pinRx is §3.1's anchored pin grammar: @scope/name@major.minor.patch, exact
// semver only — a pin resolves to one immutable published version.
var pinRx = regexp.MustCompile(`^@([a-z0-9][a-z0-9-]*)/([a-z0-9][a-z0-9-]*)@([0-9]+\.[0-9]+\.[0-9]+)$`)

// qualifiedRx is the same identity without the version.
var qualifiedRx = regexp.MustCompile(`^@([a-z0-9][a-z0-9-]*)/([a-z0-9][a-z0-9-]*)$`)

// Pin is one node version's file-level identity.
type Pin struct {
	Scope  string
	Name   string
	Semver string
}

// String renders the pin in its one canonical spelling.
func (p Pin) String() string {
	return "@" + p.Scope + "/" + p.Name + "@" + p.Semver
}

// ParsePin reads a pin literal.
func ParsePin(s string) (Pin, error) {
	m := pinRx.FindStringSubmatch(s)
	if m == nil {
		return Pin{}, fmt.Errorf("invalid node pin %s", q(s))
	}
	return Pin{Scope: m[1], Name: m[2], Semver: m[3]}, nil
}

// ParseQualifiedName reads a manifest `name` (`@scope/name`).
func ParseQualifiedName(s string) (scope, name string, err error) {
	m := qualifiedRx.FindStringSubmatch(s)
	if m == nil {
		return "", "", fmt.Errorf("invalid node pin %s", q(s))
	}
	return m[1], m[2], nil
}

// RepoRef is the node's repository path: `<scope>/<name>.node`.
func RepoRef(scope, name string) string {
	return scope + "/" + name + ".node"
}

// q renders x as a JSON string with `"` quotes and no HTML escaping — the one
// quoting helper every message in this package interpolates through.
func q(x string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range x {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		default:
			if r < 0x20 {
				fmt.Fprintf(&b, `\u%04x`, r)
				continue
			}
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}
