package flowlang

import (
	"reflect"
	"testing"
)

func e(line int, code, message string) Diagnostic {
	return Diagnostic{Severity: SeverityError, Line: line, Code: code, Message: message}
}

// TestParse_Refusals pins the five findings that return NO document. They are
// the cases where the bytes are not a v2 flow at all: half-reading them would
// hand the caller a document the file does not describe.
func TestParse_Refusals(t *testing.T) {
	tests := []struct {
		name string
		text string
		want Diagnostic
	}{
		{"bom", "\ufeffflow \"f\" v2\n", e(1, CodeBOM, msgBOM)},
		{"cr", "flow \"f\" v2\r\n", e(1, CodeCRLF, msgCRLF)},
		{"empty", "", e(1, CodeEmptyFile, msgEmptyFile)},
		{"no_final_newline", "flow \"f\" v2", e(1, CodeFinalNewline, msgFinalNewline)},
		{"double_final_newline", "flow \"f\" v2\n\n", e(2, CodeFinalNewline, msgFinalNewline)},
		{"v1", "flow \"f\" v1\n", e(1, CodeUnsupportedVersion, "platform-kit/flowlang reads v2 only (file is v1)")},
		{"v3", "flow \"f\" v3\n", e(1, CodeUnsupportedVersion, "platform-kit/flowlang reads v2 only (file is v3)")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			doc, diags := Parse(tt.text)
			if doc != nil {
				t.Fatalf("Parse returned a document; a refusal returns none")
			}
			if len(diags) != 1 {
				t.Fatalf("got %d diagnostics %+v, want exactly the refusal", len(diags), diags)
			}
			if diags[0] != tt.want {
				t.Fatalf("got %+v, want %+v", diags[0], tt.want)
			}
		})
	}
}

// TestParse_Codes pins one minimal document per remaining parse code, with the
// FULL finding list: a partial assertion would pass on a parser that recovered
// by swallowing the rest of the file.
func TestParse_Codes(t *testing.T) {
	const head = "flow \"f\" v2\n\nuse a = @x/y@1.0.0\n\n"

	tests := []struct {
		name string
		text string
		want []Diagnostic
	}{
		{
			"missing_header",
			"# only a comment\n",
			[]Diagnostic{e(1, CodeMissingHeader, msgMissingHeader)},
		},
		{
			"unknown_statement",
			"flow \"f\" v2\nbogus thing\n",
			[]Diagnostic{e(2, CodeUnknownStatement, `unknown statement "bogus"`)},
		},
		{
			"expected_token_wire_needs_a_port",
			"flow \"f\" v2\nwire a -> b\n",
			[]Diagnostic{e(2, CodeExpectedToken, `expected "."`)},
		},
		{
			"expected_token_value",
			head + "node n: a {\n\tk = @\n}\n",
			[]Diagnostic{e(6, CodeExpectedToken, msgExpectedValue)},
		},
		{
			"bad_ident",
			"flow \"f\" v2\nuse 9x = @x/y@1.0.0\n",
			[]Diagnostic{e(2, CodeBadIdent, msgBadIdent)},
		},
		{
			"bad_pin",
			"flow \"f\" v2\nuse a = legacy-id\n",
			[]Diagnostic{e(2, CodeBadPin, msgBadPin)},
		},
		{
			"bad_type_any_is_never_spelled",
			head + "node n: a {\n\tport in p: any\n}\n",
			[]Diagnostic{e(6, CodeBadType, `"any" is never spelled; omit the type`)},
		},
		{
			"bad_type_not_a_type_expression",
			head + "node n: a {\n\tport in p: Widget\n}\n",
			[]Diagnostic{e(6, CodeBadType, "expected a type (string, number, integer, boolean, object, array, <prim>[] or <alias>.<DefName>)")},
		},
		{
			"bad_string",
			head + "node n: a {\n\tport out p label bare\n}\n",
			[]Diagnostic{e(6, CodeBadString, msgBadString)},
		},
		{
			"bad_string_unterminated",
			head + "node n: a {\n\tk = \"abc\n}\n",
			[]Diagnostic{e(6, CodeBadString, msgUnterminatedString)},
		},
		{
			"bad_string_invalid_escape",
			head + "node n: a {\n\tk = \"a\\qb\"\n}\n",
			[]Diagnostic{e(6, CodeBadString, msgInvalidEscape)},
		},
		{
			"bad_number_expected_an_integer",
			"flow \"f\" v2\n\nlayout {\n\tn @ x,1\n}\n",
			[]Diagnostic{e(4, CodeBadNumber, msgBadInteger)},
		},
		{
			"bad_inline_json_unterminated_object",
			head + "node n: a {\n\tk = {\"a\": 1\n}\n",
			[]Diagnostic{e(6, CodeBadInlineJSON, msgUnterminatedObject)},
		},
		{
			"bad_inline_json_invalid",
			head + "node n: a {\n\tk = {\"a\": }\n}\n",
			[]Diagnostic{e(6, CodeBadInlineJSON, msgInvalidInlineJSON)},
		},
		{
			"bad_indent",
			head + "node n: a {\nk = 1\n}\n",
			[]Diagnostic{e(6, CodeBadIndent, msgBadIndent)},
		},
		{
			"heredoc_opener",
			head + "node n: a {\n\tk = <<< trailing\n}\n",
			[]Diagnostic{e(6, CodeHeredocOpener, msgHeredocOpener)},
		},
		{
			"heredoc_prefix",
			head + "node n: a {\n\tk = <<<\nunindented\n\t>>>\n}\n",
			[]Diagnostic{
				e(7, CodeHeredocPrefix, msgHeredocPrefix),
				e(8, CodeBadIdent, msgBadIdent),
			},
		},
		{
			"heredoc_unterminated",
			head + "node n: a {\n\tk = <<<\n\t\tbody\n",
			[]Diagnostic{
				e(6, CodeHeredocUnterminated, msgHeredocUnterminated),
				e(7, CodeUnclosedBlock, msgUnclosedNode),
			},
		},
		{
			"section_order_use_after_node",
			head + "node n: a {\n}\n\nuse b = @x/z@1.0.0\n",
			[]Diagnostic{e(8, CodeSectionOrder, msgUseBeforeNode)},
		},
		{
			// The out-of-order `node` line is skipped whole: no block is opened,
			// so its closing brace is read at file scope and is its own finding
			// on its own line. §3.5 bounds a line, not a document.
			"section_order_node_after_wire",
			head + "node n: a {\n}\n\nwire n.o -> n.i\n\nnode m: a {\n}\n",
			[]Diagnostic{
				e(10, CodeSectionOrder, msgNodeBeforeWire),
				e(11, CodeUnknownStatement, `unknown statement "}"`),
			},
		},
		{
			"section_order_wire_after_layout",
			"flow \"f\" v2\n\nlayout {\n}\n\nwire a.b -> c.d\n",
			[]Diagnostic{e(6, CodeSectionOrder, msgWireBeforeLayout)},
		},
		{
			"unclosed_block_node",
			head + "node n: a {\n",
			[]Diagnostic{e(5, CodeUnclosedBlock, msgUnclosedNode)},
		},
		{
			"unclosed_block_layout",
			"flow \"f\" v2\n\nlayout {\n",
			[]Diagnostic{e(3, CodeUnclosedBlock, msgUnclosedLayout)},
		},
		{
			"trailing_input_v1_placement_id",
			"flow \"f\" v2\n\nlayout {\n\tn @ 1,2 (abc)\n}\n",
			[]Diagnostic{e(4, CodeTrailingInput, msgTrailingInput)},
		},
		{
			"duplicate_layout_block",
			"flow \"f\" v2\n\nlayout {\n}\n\nlayout {\n}\n",
			[]Diagnostic{
				e(6, CodeDuplicateLayoutBlock, msgDuplicateLayoutBlock),
				e(7, CodeUnknownStatement, `unknown statement "}"`),
			},
		},
		{
			// A malformed out-of-order line yields ONE finding: the section-order
			// refusal is the line's first failure, and the line is not re-scanned.
			"section_order_use_after_node_malformed",
			head + "node n: a {\n}\n\nuse 9x = @x/y@1.0.0\n",
			[]Diagnostic{e(8, CodeSectionOrder, msgUseBeforeNode)},
		},
		{
			"section_order_wire_after_layout_malformed",
			"flow \"f\" v2\n\nlayout {\n}\n\nwire a -> b\n",
			[]Diagnostic{e(6, CodeSectionOrder, msgWireBeforeLayout)},
		},
		{
			// The end-of-file findings collide with a line that already failed:
			// `unclosed_block` would land on line 6, which already carries the
			// scan failure, and `missing_header` on line 1.
			"eof_collision_unclosed_block",
			head + "node n: a {\n\tk = @\n",
			[]Diagnostic{e(6, CodeExpectedToken, msgExpectedValue)},
		},
		{
			"eof_collision_missing_header",
			"bogus\n",
			[]Diagnostic{e(1, CodeExpectedToken, `expected "flow"`)},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			doc, diags := Parse(tt.text)
			if doc == nil {
				t.Fatalf("Parse refused; these are collected findings, not refusals: %+v", diags)
			}
			if !reflect.DeepEqual(diags, tt.want) {
				t.Fatalf("got %+v, want %+v", diags, tt.want)
			}
		})
	}

	// A skipped line contributes NO construct to the document: a reader that
	// recorded the out-of-order `use` would hand the resolver an alias the file
	// is not allowed to declare there. Each case carries the in-order construct
	// of the same kind as its positive anchor.
	skips := []struct {
		name  string
		text  string
		want  []Diagnostic
		check func(t *testing.T, doc *Doc)
	}{
		{
			name: "use_after_node",
			text: "flow \"f\" v2\n\nuse a = @x/y@1.0.0\n\nnode n: a {\n}\n\nuse b = @x/z@1.0.0\n",
			want: []Diagnostic{e(8, CodeSectionOrder, msgUseBeforeNode)},
			check: func(t *testing.T, doc *Doc) {
				if len(doc.Uses) != 1 || doc.Uses[0].Alias != "a" {
					t.Fatalf("uses = %+v, want only the in-order alias a", doc.Uses)
				}
			},
		},
		{
			name: "node_after_wire",
			text: "flow \"f\" v2\n\nuse a = @x/y@1.0.0\n\nnode n: a {\n}\n\nwire n.o -> n.i\n\nnode m: a {\n}\n",
			want: []Diagnostic{
				e(10, CodeSectionOrder, msgNodeBeforeWire),
				e(11, CodeUnknownStatement, `unknown statement "}"`),
			},
			check: func(t *testing.T, doc *Doc) {
				if len(doc.Nodes) != 1 || doc.Nodes[0].Slug != "n" {
					t.Fatalf("nodes = %+v, want only the in-order node n", doc.Nodes)
				}
			},
		},
		{
			name: "wire_after_layout",
			text: "flow \"f\" v2\n\nuse a = @x/y@1.0.0\n\nnode n: a {\n}\n\nwire n.o -> n.i\n\nlayout {\n\tn @ 1,2\n}\n\nwire n.o -> n.p\n",
			want: []Diagnostic{e(14, CodeSectionOrder, msgWireBeforeLayout)},
			check: func(t *testing.T, doc *Doc) {
				if len(doc.Wires) != 1 || doc.Wires[0].ToPort != "i" {
					t.Fatalf("wires = %+v, want only the in-order wire", doc.Wires)
				}
			},
		},
		{
			name: "second_layout_block",
			text: "flow \"f\" v2\n\nlayout {\n\tn @ 1,2\n}\n\nlayout {\n\tm @ 3,4\n}\n",
			want: []Diagnostic{
				e(7, CodeDuplicateLayoutBlock, msgDuplicateLayoutBlock),
				e(8, CodeUnknownStatement, `unknown statement "m"`),
				e(9, CodeUnknownStatement, `unknown statement "}"`),
			},
			check: func(t *testing.T, doc *Doc) {
				if len(doc.Layout) != 1 || doc.Layout[0].Slug != "n" {
					t.Fatalf("layout = %+v, want only the first block's entry", doc.Layout)
				}
			},
		},
	}
	for _, tt := range skips {
		t.Run("skipped_"+tt.name, func(t *testing.T) {
			doc, diags := Parse(tt.text)
			if doc == nil {
				t.Fatalf("Parse refused: %+v", diags)
			}
			if !reflect.DeepEqual(diags, tt.want) {
				t.Fatalf("got %+v, want %+v", diags, tt.want)
			}
			tt.check(t, doc)
		})
	}
}

// TestParse_Trivia pins the comment model against the fixture built to carry
// one comment at every anchor. It asserts by ANCHOR IDENTITY, not by counting:
// a parser that dropped `bodyTail` into the free list would keep the count.
func TestParse_Trivia(t *testing.T) {
	doc, diags := Parse(readFixture(t, "testdata/03_every_anchor.flow"))
	if doc == nil || len(diags) != 0 {
		t.Fatalf("Parse: doc=%v diags=%+v", doc != nil, diags)
	}

	wantLeading := func(name string, got []string, want ...string) {
		t.Helper()
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("%s leading = %q, want %q", name, got, want)
		}
	}
	wantTrailing := func(name string, got *string, want string) {
		t.Helper()
		if got == nil {
			t.Fatalf("%s has no trailing comment, want %q", name, want)
		}
		if *got != want {
			t.Fatalf("%s trailing = %q, want %q", name, *got, want)
		}
	}

	wantLeading("header", doc.FileTrivia.Header.Leading, "Header-leading comment.")
	wantTrailing("header", doc.FileTrivia.Header.Trailing, "Header trailing comment.")

	wantLeading("use secure_http", doc.FileTrivia.Use["secure_http"].Leading, "Leading secure-http use.")
	wantTrailing("use secure_http", doc.FileTrivia.Use["secure_http"].Trailing, "Secure-http use trailing comment.")
	wantLeading("use webhook_trigger", doc.FileTrivia.Use["webhook_trigger"].Leading, "Leading webhook use.")
	wantTrailing("use webhook_trigger", doc.FileTrivia.Use["webhook_trigger"].Trailing, "Webhook use trailing comment.")

	wantFree := []FreeComment{
		{After: "", Lines: []string{"File-head free comment."}},
		{After: "use:secure_http", Lines: []string{"Free comment between use declarations."}},
		{After: "node:intake", Lines: []string{"Free comment after intake."}},
		{After: "wire:intake.body->worker.payload", Lines: []string{"Free comment between wire declarations."}},
		{After: "layout", Lines: []string{"File-tail free comment."}},
	}
	if !reflect.DeepEqual(doc.FileTrivia.Free, wantFree) {
		t.Fatalf("free comments = %+v, want %+v", doc.FileTrivia.Free, wantFree)
	}

	intake, worker := doc.Nodes[0], doc.Nodes[1]
	if intake.Slug != "intake" || worker.Slug != "worker" {
		t.Fatalf("node slugs = %q, %q", intake.Slug, worker.Slug)
	}
	wantLeading("intake", intake.Trivia.Leading, "Leading intake node.")
	wantTrailing("intake open", intake.Trivia.Open, "Intake open trailing comment.")
	wantTrailing("intake close", intake.Trivia.Close, "Intake close trailing comment.")
	wantLeading("intake bodyTail", intake.Trivia.BodyTail, "Intake body-tail comment.")

	wantLeading("worker", worker.Trivia.Leading, "Leading worker node.")
	wantTrailing("worker open", worker.Trivia.Open, "Worker open trailing comment.")
	wantTrailing("worker close", worker.Trivia.Close, "Worker close trailing comment.")
	wantLeading("worker bodyTail", worker.Trivia.BodyTail, "Worker body-tail comment.")

	wantLeading("worker.message", worker.Trivia.Config["message"].Leading, "Leading message config.")
	if worker.Trivia.Config["message"].Trailing != nil {
		t.Fatal("a heredoc line carries no trailing comment")
	}
	wantLeading("worker.method", worker.Trivia.Config["method"].Leading,
		"Comment after the heredoc terminator; leading trivia for method.")
	wantTrailing("worker.method", worker.Trivia.Config["method"].Trailing, "Method trailing comment.")
	wantLeading("worker.url", worker.Trivia.Config["url"].Leading, "Leading URL config.")
	wantTrailing("worker.url", worker.Trivia.Config["url"].Trailing, "URL trailing comment.")

	wantLeading("worker port payload", worker.Trivia.Ports["in:payload"].Leading, "Leading schema-input override.")
	wantTrailing("worker port payload", worker.Trivia.Ports["in:payload"].Trailing, "Schema-input trailing comment.")
	wantLeading("worker port method", worker.Trivia.Ports["in:method"].Leading, "Leading promoted port.")
	wantTrailing("worker port method", worker.Trivia.Ports["in:method"].Trailing, "Promoted-port trailing comment.")
	wantLeading("worker port tools", worker.Trivia.Ports["in:tools"].Leading, "Leading free input.")
	wantTrailing("worker port tools", worker.Trivia.Ports["in:tools"].Trailing, "Free-input trailing comment.")

	wantLeading("wire 1", doc.Wires[0].Trivia.Leading, "Leading first wire.")
	wantTrailing("wire 1", doc.Wires[0].Trivia.Trailing, "First-wire trailing comment.")
	wantLeading("wire 2", doc.Wires[1].Trivia.Leading, "Leading second wire.")
	wantTrailing("wire 2", doc.Wires[1].Trivia.Trailing, "Second-wire trailing comment.")

	wantLeading("layout block", doc.FileTrivia.Layout.Leading, "Leading layout block.")
	wantTrailing("layout block", doc.FileTrivia.Layout.Trailing, "Layout open trailing comment.")
	wantLeading("layout tail", doc.FileTrivia.LayoutTail, "Layout body-tail comment.")
	wantTrailing("layout close", doc.FileTrivia.LayoutClose, "Layout close trailing comment.")

	wantLeading("layout intake", doc.Layout[0].Trivia.Leading, "Leading intake layout line.")
	wantTrailing("layout intake", doc.Layout[0].Trivia.Trailing, "Intake-layout trailing comment.")
	wantLeading("layout worker", doc.Layout[1].Trivia.Leading, "Leading worker layout line.")
	wantTrailing("layout worker", doc.Layout[1].Trivia.Trailing, "Worker-layout trailing comment.")
}
