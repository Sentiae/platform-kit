package nodemanifest

import (
	"fmt"
	"regexp"
	"strings"
)

// labelRx is one DNS label of an egress pattern: lower-case only, because the
// caller IDNA/lower-cases the host before it ever reaches MatchEgress.
var labelRx = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// ipv4Rx catches a dotted quad, which is denied as a pattern: an allowlist
// entry names a NAME, and the proxy denies IP-literal hosts anyway.
var ipv4Rx = regexp.MustCompile(`^[0-9.]+$`)

// ValidateEgressPattern accepts `*`, `*.<domain>.<tld>` and an exact host.
func ValidateEgressPattern(p string) error {
	bad := fmt.Errorf("egress pattern %s is invalid", q(p))
	if p == "" {
		return bad
	}
	if strings.ContainsAny(p, "[]:/ \t\n\r") {
		return bad
	}
	if p == "*" {
		return nil
	}
	rest := p
	wildcard := false
	if strings.HasPrefix(p, "*.") {
		wildcard = true
		rest = p[2:]
	}
	if strings.Contains(rest, "*") {
		return bad
	}
	if ipv4Rx.MatchString(rest) {
		return bad
	}
	labels := strings.Split(rest, ".")
	if wildcard && len(labels) < 2 {
		return bad
	}
	for _, l := range labels {
		if !labelRx.MatchString(l) {
			return bad
		}
	}
	return nil
}

// MatchEgress reports whether any pattern admits the host. `*.example.com`
// admits exactly one extra label — never the apex, never a deeper name.
func MatchEgress(patterns []string, host string) bool {
	for _, p := range patterns {
		if p == "*" {
			return true
		}
		if strings.HasPrefix(p, "*.") {
			suffix := p[1:] // ".example.com"
			if !strings.HasSuffix(host, suffix) {
				continue
			}
			head := host[:len(host)-len(suffix)]
			if head != "" && !strings.Contains(head, ".") {
				return true
			}
			continue
		}
		if p == host {
			return true
		}
	}
	return false
}

// q renders x as a JSON string with `"` quotes and no HTML escaping.
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
