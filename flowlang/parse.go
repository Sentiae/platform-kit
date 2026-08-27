package flowlang

import (
	"fmt"
	"strings"
)

// Parse reads canonical `.flow` v2 text.
//
// Every finding is reported (never first-error-only) so a reader repairing the
// file gets every objection at once, and the partially-understood document is
// still returned. The five encoding/version refusals are the exception: they
// mean the bytes are not a v2 document at all, so there is nothing to return
// but the refusal.
func Parse(text string) (*Doc, []Diagnostic) {
	var diags []Diagnostic
	// §3.5: one line yields AT MOST one finding. The latch lives here rather
	// than at each call site so no path — a phase failure, the scanner, or the
	// end-of-file checks — can add a second finding to a line that already has
	// one.
	failed := map[int]bool{}
	fail := func(line int, code, message string) {
		if failed[line] {
			return
		}
		failed[line] = true
		diags = append(diags, Diagnostic{Severity: SeverityError, Line: line, Code: code, Message: message})
	}
	refuse := func(line int, code, message string) (*Doc, []Diagnostic) {
		return nil, []Diagnostic{{Severity: SeverityError, Line: line, Code: code, Message: message}}
	}

	if strings.HasPrefix(text, "\ufeff") {
		return refuse(1, CodeBOM, msgBOM)
	}
	if i := strings.IndexByte(text, '\r'); i >= 0 {
		return refuse(strings.Count(text[:i], "\n")+1, CodeCRLF, msgCRLF)
	}
	if text == "" {
		return refuse(1, CodeEmptyFile, msgEmptyFile)
	}
	if !strings.HasSuffix(text, "\n") {
		return refuse(strings.Count(text, "\n")+1, CodeFinalNewline, msgFinalNewline)
	}
	lines := strings.Split(text, "\n")
	lines = lines[:len(lines)-1]
	if lines[len(lines)-1] == "" {
		return refuse(len(lines), CodeFinalNewline, msgFinalNewline)
	}

	doc := &Doc{Version: Version}

	var group []string
	anchor := ""
	phase := 0 // 0 header · 1 use · 2 node · 3 wire · 5 layout seen
	mode := "file"
	var node *Node
	headerSeen := false

	freeGroup := func() {
		if len(group) == 0 {
			return
		}
		doc.FileTrivia.Free = append(doc.FileTrivia.Free, FreeComment{After: anchor, Lines: group})
		group = nil
	}
	takeLeading := func() []string {
		g := group
		group = nil
		return g
	}
	trivia := func(trailing *string) *Trivia {
		lead := takeLeading()
		if len(lead) == 0 && trailing == nil {
			return nil
		}
		return &Trivia{Leading: lead, Trailing: trailing}
	}

	for i := 0; i < len(lines); {
		raw := lines[i]
		lineNo := i + 1
		i++

		if strings.TrimSpace(raw) == "" {
			// Blank lines are structural. At FILE scope a blank after a comment
			// group is what turns it from leading trivia into a free-floating
			// comment; inside a body it is only spacing and normalizes away.
			if mode == "file" {
				freeGroup()
			}
			continue
		}
		if isCommentLine(raw) {
			group = append(group, commentText(strings.TrimLeft(raw, " \t")))
			continue
		}

		if mode == "node" {
			if strings.HasPrefix(raw, "}") {
				sc := newScan(raw, lineNo)
				sc.expect("}")
				closeText := sc.end()
				if sc.failed() {
					fail(sc.err.line, sc.err.code, sc.err.message)
					continue
				}
				attachNodeClose(node, takeLeading(), closeText)
				doc.Nodes = append(doc.Nodes, *node)
				anchor = "node:" + node.Slug
				node = nil
				mode = "file"
				continue
			}
			if !strings.HasPrefix(raw, tab) {
				fail(lineNo, CodeBadIndent, msgBadIndent)
				continue
			}
			if strings.HasPrefix(raw, tab+"port in ") || strings.HasPrefix(raw, tab+"port out ") {
				p, trailing, err := parsePortLine(raw, lineNo)
				if err != nil {
					fail(err.line, err.code, err.message)
					continue
				}
				node.Ports = append(node.Ports, p)
				attachBodyTrivia(node, "ports", PortTriviaKey(p.Dir, p.ID), trivia(trailing))
				continue
			}
			key, value, heredoc, trailing, err := parseConfigLine(raw, lineNo)
			if err != nil {
				fail(err.line, err.code, err.message)
				continue
			}
			if heredoc {
				text, next, herr := readHeredoc(lines, i, lineNo)
				if herr != nil {
					// §3.5: one line yields at most ONE finding and is then skipped.
					// A bad prefix line is skipped and reading resumes after it; an
					// unterminated heredoc has consumed the rest of the file, so
					// there is no line left to resume on.
					fail(herr.line, herr.code, herr.message)
					if herr.code == CodeHeredocUnterminated {
						i = len(lines)
					} else {
						i = herr.line
					}
					continue
				}
				i = next
				node.Config = append(node.Config, Config{Key: key, Value: text, Line: lineNo})
				attachBodyTrivia(node, "config", key, trivia(nil))
				continue
			}
			node.Config = append(node.Config, Config{Key: key, Value: value, Line: lineNo})
			attachBodyTrivia(node, "config", key, trivia(trailing))
			continue
		}

		if mode == "layout" {
			if strings.HasPrefix(raw, "}") {
				sc := newScan(raw, lineNo)
				sc.expect("}")
				closeText := sc.end()
				if sc.failed() {
					fail(sc.err.line, sc.err.code, sc.err.message)
					continue
				}
				doc.FileTrivia.LayoutTail = takeLeading()
				doc.FileTrivia.LayoutClose = closeText
				anchor = "layout"
				mode = "file"
				continue
			}
			l, trailing, err := parseLayoutLine(raw, lineNo)
			if err != nil {
				fail(err.line, err.code, err.message)
				continue
			}
			l.Trivia = trivia(trailing)
			doc.Layout = append(doc.Layout, l)
			continue
		}

		// ── file scope ──────────────────────────────────────────────────────
		if !headerSeen {
			name, version, trailing, err := parseHeader(raw, lineNo)
			if err != nil {
				fail(err.line, err.code, err.message)
				continue
			}
			if version != Version {
				return refuse(lineNo, CodeUnsupportedVersion, fmt.Sprintf(msgUnsupportedVersion, version))
			}
			doc.Name = name
			doc.Version = version
			doc.FileTrivia.Header = trivia(trailing)
			headerSeen = true
			anchor = "header"
			phase = 1
			continue
		}

		switch {
		case strings.HasPrefix(raw, "use "):
			if phase > 1 {
				fail(lineNo, CodeSectionOrder, msgUseBeforeNode)
				continue
			}
			alias, pin, trailing, err := parseUse(raw, lineNo)
			if err != nil {
				fail(err.line, err.code, err.message)
				continue
			}
			doc.Uses = append(doc.Uses, Use{Alias: alias, Pin: pin, Line: lineNo})
			if t := trivia(trailing); t != nil {
				if doc.FileTrivia.Use == nil {
					doc.FileTrivia.Use = map[string]*Trivia{}
				}
				doc.FileTrivia.Use[alias] = t
			}
			anchor = "use:" + alias

		case strings.HasPrefix(raw, "node "):
			if phase > 2 {
				fail(lineNo, CodeSectionOrder, msgNodeBeforeWire)
				continue
			}
			opened, err := parseNodeOpen(raw, lineNo)
			if err != nil {
				fail(err.line, err.code, err.message)
				continue
			}
			if lead := takeLeading(); len(lead) > 0 {
				if opened.Trivia == nil {
					opened.Trivia = &NodeTrivia{}
				}
				opened.Trivia.Leading = lead
			}
			node = opened
			mode = "node"
			phase = 2

		case strings.HasPrefix(raw, "wire "):
			if phase > 3 {
				fail(lineNo, CodeSectionOrder, msgWireBeforeLayout)
				continue
			}
			w, trailing, err := parseWire(raw, lineNo)
			if err != nil {
				fail(err.line, err.code, err.message)
				continue
			}
			w.Trivia = trivia(trailing)
			doc.Wires = append(doc.Wires, w)
			anchor = WireAnchorKey(w)
			phase = 3

		case strings.HasPrefix(raw, "layout"):
			if phase > 4 {
				fail(lineNo, CodeDuplicateLayoutBlock, msgDuplicateLayoutBlock)
				continue
			}
			sc := newScan(raw, lineNo)
			sc.expect("layout")
			sc.space()
			sc.expect("{")
			open := sc.end()
			if sc.failed() {
				fail(sc.err.line, sc.err.code, sc.err.message)
				continue
			}
			doc.FileTrivia.Layout = trivia(open)
			mode = "layout"
			phase = 5

		default:
			fail(lineNo, CodeUnknownStatement, fmt.Sprintf(msgUnknownStatement, jsonString(firstWord(raw))))
		}
	}

	if mode == "node" {
		fail(len(lines), CodeUnclosedBlock, msgUnclosedNode)
	}
	if mode == "layout" {
		fail(len(lines), CodeUnclosedBlock, msgUnclosedLayout)
	}
	if !headerSeen {
		fail(1, CodeMissingHeader, msgMissingHeader)
	}
	freeGroup()

	return doc, diags
}

func firstWord(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if i := strings.IndexAny(trimmed, " \t"); i >= 0 {
		return trimmed[:i]
	}
	return trimmed
}

func attachNodeClose(n *Node, tail []string, closeText *string) {
	if len(tail) == 0 && closeText == nil {
		return
	}
	if n.Trivia == nil {
		n.Trivia = &NodeTrivia{}
	}
	n.Trivia.BodyTail = tail
	n.Trivia.Close = closeText
}

// attachBodyTrivia records one body construct's trivia on the node.
func attachBodyTrivia(n *Node, bucket, key string, t *Trivia) {
	if t == nil {
		return
	}
	if n.Trivia == nil {
		n.Trivia = &NodeTrivia{}
	}
	if bucket == "config" {
		if n.Trivia.Config == nil {
			n.Trivia.Config = map[string]*Trivia{}
		}
		n.Trivia.Config[key] = t
		return
	}
	if n.Trivia.Ports == nil {
		n.Trivia.Ports = map[string]*Trivia{}
	}
	n.Trivia.Ports[key] = t
}

func parseHeader(raw string, lineNo int) (name string, version int, trailing *string, err *syntaxError) {
	sc := newScan(raw, lineNo)
	sc.expect("flow")
	sc.space()
	name = sc.str()
	sc.space()
	sc.expect("v")
	version = sc.integer()
	if !sc.failed() && version < 1 {
		sc.fail(CodeBadNumber, msgBadVersion)
	}
	trailing = sc.end()
	return name, version, trailing, sc.err
}

func parseUse(raw string, lineNo int) (alias, pin string, trailing *string, err *syntaxError) {
	sc := newScan(raw, lineNo)
	sc.expect("use")
	sc.space()
	alias = sc.ident()
	sc.space()
	sc.expect("=")
	sc.space()
	pin = sc.pin()
	trailing = sc.end()
	return alias, pin, trailing, sc.err
}

func parseNodeOpen(raw string, lineNo int) (*Node, *syntaxError) {
	sc := newScan(raw, lineNo)
	sc.expect("node")
	sc.space()
	slug := sc.ident()
	sc.expect(":")
	sc.space()
	alias := sc.ident()
	sc.space()
	var title *string
	if !sc.failed() && sc.peek() == '"' {
		t := sc.str()
		title = &t
		sc.space()
	}
	disabled := false
	if !sc.failed() && sc.startsWith("disabled ") {
		sc.expect("disabled")
		sc.space()
		disabled = true
	}
	sc.expect("{")
	open := sc.end()
	if sc.failed() {
		return nil, sc.err
	}
	n := &Node{Slug: slug, Alias: alias, Title: title, Disabled: disabled, Line: lineNo}
	if open != nil {
		n.Trivia = &NodeTrivia{Open: open}
	}
	return n, nil
}

func parseConfigLine(raw string, lineNo int) (key string, value any, heredoc bool, trailing *string, err *syntaxError) {
	sc := newScan(raw, lineNo)
	sc.expect(tab)
	key = sc.name()
	sc.space()
	sc.expect("=")
	sc.space()
	if !sc.failed() && sc.startsWith("<<<") {
		sc.expect("<<<")
		if !sc.done() {
			sc.fail(CodeHeredocOpener, msgHeredocOpener)
		}
		return key, nil, true, nil, sc.err
	}
	value = sc.value()
	trailing = sc.end()
	return key, value, false, trailing, sc.err
}

// readHeredoc reads a heredoc body starting at `start` (the line after the
// opener). Content is verbatim minus the two-tab prefix; the value carries no
// trailing LF, which is what removes the classic heredoc ambiguity instead of
// documenting it.
func readHeredoc(lines []string, start, openerLine int) (string, int, *syntaxError) {
	var body []string
	for i := start; i < len(lines); i++ {
		raw := lines[i]
		if raw == tab+">>>" {
			return strings.Join(body, "\n"), i + 1, nil
		}
		if raw == "" {
			body = append(body, "")
			continue
		}
		if !strings.HasPrefix(raw, tab+tab) {
			return "", 0, &syntaxError{line: i + 1, code: CodeHeredocPrefix, message: msgHeredocPrefix}
		}
		body = append(body, raw[2:])
	}
	return "", 0, &syntaxError{line: openerLine, code: CodeHeredocUnterminated, message: msgHeredocUnterminated}
}

func parsePortLine(raw string, lineNo int) (Port, *string, *syntaxError) {
	sc := newScan(raw, lineNo)
	sc.expect(tab)
	sc.expect("port")
	sc.space()
	dir := "in"
	if sc.eat("out") {
		dir = "out"
	} else {
		sc.expect("in")
	}
	sc.space()
	id := sc.name()

	if dir == "out" {
		sc.space()
		sc.expect("label")
		sc.space()
		label := sc.str()
		trailing := sc.end()
		if sc.failed() {
			return Port{}, nil, sc.err
		}
		return Port{Dir: dir, ID: id, Label: &label, Line: lineNo}, trailing, nil
	}

	p := Port{Dir: dir, ID: id, Line: lineNo}
	if sc.eat(":") {
		sc.space()
		p.Type = sc.typeExpr()
	}
	if !sc.failed() && sc.startsWith(" = config.") {
		sc.expect(" = config.")
		p.ConfigKey = sc.name()
	}
	if !sc.failed() && sc.startsWith(" label ") {
		sc.expect(" label ")
		label := sc.str()
		p.Label = &label
	}
	trailing := sc.end()
	if sc.failed() {
		return Port{}, nil, sc.err
	}
	return p, trailing, nil
}

func parseWire(raw string, lineNo int) (Wire, *string, *syntaxError) {
	sc := newScan(raw, lineNo)
	sc.expect("wire")
	sc.space()
	from := sc.ident()
	sc.expect(".")
	fromPort := sc.name()
	sc.space()
	sc.expect("->")
	sc.space()
	to := sc.ident()
	sc.expect(".")
	toPort := sc.name()
	trailing := sc.end()
	if sc.failed() {
		return Wire{}, nil, sc.err
	}
	return Wire{From: from, FromPort: fromPort, To: to, ToPort: toPort, Line: lineNo}, trailing, nil
}

func parseLayoutLine(raw string, lineNo int) (Layout, *string, *syntaxError) {
	sc := newScan(raw, lineNo)
	sc.expect(tab)
	slug := sc.ident()
	sc.space()
	sc.expect("@")
	sc.space()
	x := sc.integer()
	sc.expect(",")
	y := sc.integer()
	l := Layout{Slug: slug, X: x, Y: y, Line: lineNo}
	if !sc.failed() && sc.startsWith(" w ") {
		sc.expect(" w ")
		w := sc.integer()
		l.W = &w
	}
	trailing := sc.end()
	if sc.failed() {
		return Layout{}, nil, sc.err
	}
	return l, trailing, nil
}
