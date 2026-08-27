package flowlang

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/sentiae/platform-kit/nodemanifest"
)

// ErrUnsupportedVersion is returned by Serialize for a document that is not v2.
var ErrUnsupportedVersion = errors.New("flowlang: writes v2 only")

// ErrWirePortless is returned by Serialize for a wire missing a port on either
// end. A v2 wire is data and names both ends; there is no continuation
// spelling to fall back to.
var ErrWirePortless = errors.New("flowlang: a wire names a port on both ends")

// Serialize emits a document as `.flow` text. It is pure: the same model plus
// the same trivia yields the same bytes, and it emits every comment the model
// holds. It does NOT reorder — Canonicalize owns the canonical order, so a
// caller can round-trip a file byte-for-byte without a manifest set.
func Serialize(doc *Doc) (string, error) {
	if doc.Version != Version {
		return "", fmt.Errorf("%w (document is v%d)", ErrUnsupportedVersion, doc.Version)
	}
	for _, w := range doc.Wires {
		if w.FromPort == "" || w.ToPort == "" {
			return "", fmt.Errorf("%w: %q -> %q", ErrWirePortless, w.From, w.To)
		}
	}

	var units [][]string
	var cur []string
	flush := func() {
		if len(cur) > 0 {
			units = append(units, cur)
			cur = nil
		}
	}
	written := make([]bool, len(doc.FileTrivia.Free))
	freeAfter := func(key string) {
		for i, g := range doc.FileTrivia.Free {
			if g.After != key || written[i] {
				continue
			}
			written[i] = true
			lines := leadingLines(g.Lines, "")
			if len(lines) == 0 {
				continue
			}
			flush()
			units = append(units, lines)
		}
	}

	freeAfter("")

	header := doc.FileTrivia.Header
	cur = append(cur, leadingLines(triviaLeading(header), "")...)
	cur = append(cur, "flow "+jsonString(doc.Name)+" v"+strconv.Itoa(doc.Version)+
		trailingSuffix(triviaTrailing(header)))
	flush()
	freeAfter("header")

	for _, u := range doc.Uses {
		t := doc.FileTrivia.Use[u.Alias]
		cur = append(cur, leadingLines(triviaLeading(t), "")...)
		cur = append(cur, "use "+u.Alias+" = "+u.Pin+trailingSuffix(triviaTrailing(t)))
		freeAfter("use:" + u.Alias)
	}
	flush()

	for _, n := range doc.Nodes {
		cur = append(cur, nodeLines(n)...)
		flush()
		freeAfter("node:" + n.Slug)
	}

	for _, w := range doc.Wires {
		cur = append(cur, leadingLines(triviaLeading(w.Trivia), "")...)
		cur = append(cur, "wire "+endpointToken(w.From, w.FromPort)+" -> "+
			endpointToken(w.To, w.ToPort)+trailingSuffix(triviaTrailing(w.Trivia)))
		freeAfter(WireAnchorKey(w))
	}
	flush()

	if len(doc.Layout) > 0 {
		lt := doc.FileTrivia.Layout
		cur = append(cur, leadingLines(triviaLeading(lt), "")...)
		cur = append(cur, "layout {"+trailingSuffix(triviaTrailing(lt)))
		for _, l := range doc.Layout {
			cur = append(cur, leadingLines(triviaLeading(l.Trivia), tab)...)
			width := ""
			if l.W != nil {
				width = " w " + strconv.Itoa(*l.W)
			}
			cur = append(cur, tab+l.Slug+" @ "+strconv.Itoa(l.X)+","+strconv.Itoa(l.Y)+
				width+trailingSuffix(triviaTrailing(l.Trivia)))
		}
		cur = append(cur, leadingLines(doc.FileTrivia.LayoutTail, tab)...)
		cur = append(cur, "}"+trailingSuffix(doc.FileTrivia.LayoutClose))
		flush()
		freeAfter("layout")
	}
	flush()

	// The never-drop sweep. A free-floating group whose anchor construct is gone
	// has no home left in the template, and the one thing it may never do is
	// vanish: it lands at the file tail, where the diff shows it as a MOVE.
	for i, g := range doc.FileTrivia.Free {
		if written[i] {
			continue
		}
		written[i] = true
		if lines := leadingLines(g.Lines, ""); len(lines) > 0 {
			units = append(units, lines)
		}
	}

	blocks := make([]string, 0, len(units))
	for _, u := range units {
		blocks = append(blocks, strings.Join(u, "\n"))
	}
	return strings.Join(blocks, "\n\n") + "\n", nil
}

// nodeLines emits one node block, from its open line to its `}`.
func nodeLines(n Node) []string {
	t := n.Trivia
	out := leadingLines(nodeLeading(t), "")

	title := ""
	if n.Title != nil {
		title = " " + jsonString(*n.Title)
	}
	disabled := ""
	if n.Disabled {
		disabled = " disabled"
	}
	out = append(out, "node "+n.Slug+": "+n.Alias+title+disabled+" {"+
		trailingSuffix(nodeOpen(t)))

	for _, c := range n.Config {
		ct := nodeConfigTrivia(t, c.Key)
		out = append(out, leadingLines(triviaLeading(ct), tab)...)
		if s, ok := c.Value.(string); ok && ElectsHeredoc(s) {
			// A heredoc spans lines, so it carries no trailing comment.
			out = append(out, tab+nameToken(c.Key)+" = <<<")
			for _, line := range strings.Split(s, "\n") {
				if line == "" {
					out = append(out, "")
					continue
				}
				out = append(out, tab+tab+line)
			}
			out = append(out, tab+">>>")
			continue
		}
		out = append(out, tab+nameToken(c.Key)+" = "+inlineJSON(c.Value)+
			trailingSuffix(triviaTrailing(ct)))
	}

	for _, p := range n.Ports {
		pt := nodePortTrivia(t, PortTriviaKey(p.Dir, p.ID))
		out = append(out, leadingLines(triviaLeading(pt), tab)...)
		out = append(out, tab+portLineText(p)+trailingSuffix(triviaTrailing(pt)))
	}

	out = append(out, leadingLines(nodeBodyTail(t), tab)...)
	return append(out, "}"+trailingSuffix(nodeClose(t)))
}

func portLineText(p Port) string {
	if p.Dir == "out" {
		label := p.ID
		if p.Label != nil {
			label = *p.Label
		}
		return "port out " + nameToken(p.ID) + " label " + jsonString(label)
	}
	text := "port in " + nameToken(p.ID)
	if p.Type != "" {
		text += ": " + p.Type
	}
	if p.ConfigKey != "" {
		text += " = config." + nameToken(p.ConfigKey)
	}
	if p.Label != nil {
		text += " label " + jsonString(*p.Label)
	}
	return text
}

// endpointToken is one wire end.
func endpointToken(slug, port string) string { return slug + "." + nameToken(port) }

// nameToken is a key or port id: bare when it is `ident`-shaped, else quoted.
func nameToken(s string) string {
	if identRx.FindString(s) == s && len(s) <= maxIdent && s != "" {
		return s
	}
	return jsonString(s)
}

// comment is the canonical emit of one comment: `# ` + verbatim text, bare `#`
// when the text is empty.
func comment(text string) string {
	if text == "" {
		return "#"
	}
	return "# " + text
}

func leadingLines(group []string, indent string) []string {
	if len(group) == 0 {
		return nil
	}
	out := make([]string, 0, len(group))
	for _, t := range group {
		out = append(out, indent+comment(t))
	}
	return out
}

func trailingSuffix(text *string) string {
	if text == nil {
		return ""
	}
	return " " + comment(*text)
}

func triviaLeading(t *Trivia) []string {
	if t == nil {
		return nil
	}
	return t.Leading
}

func triviaTrailing(t *Trivia) *string {
	if t == nil {
		return nil
	}
	return t.Trailing
}

func nodeLeading(t *NodeTrivia) []string {
	if t == nil {
		return nil
	}
	return t.Leading
}

func nodeOpen(t *NodeTrivia) *string {
	if t == nil {
		return nil
	}
	return t.Open
}

func nodeClose(t *NodeTrivia) *string {
	if t == nil {
		return nil
	}
	return t.Close
}

func nodeBodyTail(t *NodeTrivia) []string {
	if t == nil {
		return nil
	}
	return t.BodyTail
}

func nodeConfigTrivia(t *NodeTrivia, key string) *Trivia {
	if t == nil {
		return nil
	}
	return t.Config[key]
}

func nodePortTrivia(t *NodeTrivia, key string) *Trivia {
	if t == nil {
		return nil
	}
	return t.Ports[key]
}

// ElectsHeredoc reports whether a string takes the heredoc spelling. It is
// deterministic, so every string has exactly ONE canonical form: a value that
// ends in LF, or that carries a line reading as the terminator, falls back to a
// JSON string instead of needing an escape rule.
func ElectsHeredoc(v string) bool {
	if !strings.Contains(v, "\n") || strings.HasSuffix(v, "\n") {
		return false
	}
	for _, line := range strings.Split(v, "\n") {
		if line == ">>>" {
			return false
		}
	}
	return true
}

// jsonString renders a Go string as a JSON string with MINIMAL escaping: `"`,
// `\` and the control characters only. `/` and every non-ASCII rune stay
// verbatim, which is what keeps the bytes identical to TypeScript's
// JSON.stringify.
func jsonString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
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

// inlineJSON is the canonical single-line JSON of a config value: keys sorted
// bytewise, `", "` between members, `": "` after keys.
func inlineJSON(v any) string {
	switch t := v.(type) {
	case nil:
		return "null"
	case bool:
		return strconv.FormatBool(t)
	case string:
		return jsonString(t)
	case float64:
		return esNumber(t)
	case int:
		return strconv.Itoa(t)
	case []any:
		parts := make([]string, 0, len(t))
		for _, item := range t {
			parts = append(parts, inlineJSON(item))
		}
		return "[" + strings.Join(parts, ", ") + "]"
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(keys))
		for _, k := range keys {
			parts = append(parts, jsonString(k)+": "+inlineJSON(t[k]))
		}
		return "{" + strings.Join(parts, ", ") + "}"
	default:
		return canonicalScalar(v)
	}
}

// esNumber spells a float the ECMAScript way. It borrows nodemanifest's
// canonical encoder rather than restating the exponent rule, because two
// spellings of one number is exactly the drift this package exists to prevent.
func esNumber(f float64) string { return canonicalScalar(f) }

func canonicalScalar(v any) string {
	b, err := nodemanifest.CanonicalJSON(v)
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	return strings.TrimSuffix(string(b), "\n")
}
