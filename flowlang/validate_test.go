package flowlang

import (
	"strings"
	"testing"

	"github.com/sentiae/platform-kit/nodemanifest"
)

// anyOutManifest is the one contract the corpus does not carry: a node with no
// inputs whose single output promises NOTHING. It is what makes `wire_type_unknown`
// and the zero-input runnability rule testable at all.
const anyOutManifest = `{
  "$schema": "https://sentiae.com/schemas/node-manifest/v1.json",
  "capabilities": {
    "egress": []
  },
  "category": "transform",
  "config": {
    "additionalProperties": false,
    "properties": {},
    "type": "object"
  },
  "display": {
    "description": "Emits an unconstrained value.",
    "icon": "box",
    "name": "Any out"
  },
  "implementations": {
    "go": {
      "entry": "go/node.go",
      "lockfiles": [
        "go/go.mod",
        "go/go.sum"
      ]
    }
  },
  "inputs": [],
  "name": "@test/any-out",
  "outputs": [
    {
      "name": "out",
      "required": true,
      "schema": {}
    }
  ],
  "resources": {
    "memoryMiB": 64,
    "timeoutMs": 5000
  },
  "role": null,
  "secrets": [],
  "shape": "inline"
}
`

// testManifests is the corpus set plus @test/any-out@1.0.0.
func testManifests(t *testing.T) Manifests {
	t.Helper()
	m := corpusManifests(t)
	man, problems := nodemanifest.Load([]byte(anyOutManifest))
	if len(problems) > 0 {
		t.Fatalf("@test/any-out is not publishable: %v", problems)
	}
	m["@test/any-out@1.0.0"] = man
	return m
}

func flowText(lines ...string) string { return strings.Join(lines, "\n") + "\n" }

// TestValidate_Codes pins one document per Validate code, with the line and the
// verbatim message. Each row is its own control: the code it names appears
// nowhere else in the table, so deleting the check that emits it turns exactly
// that row red.
func TestValidate_Codes(t *testing.T) {
	manifests := testManifests(t)

	tests := []struct {
		name string
		text string
		// want is the finding the row exists to pin; absent inverts the row into
		// "this code must NOT be emitted here".
		want   Diagnostic
		absent bool
	}{
		{
			name: "duplicate_alias",
			text: flowText(
				`flow "f" v2`,
				``,
				`use secure_http = @acme/secure-http@2.1.0`,
				`use secure_http = @acme/secure-http@2.1.0`,
			),
			want: e(4, CodeDuplicateAlias, `duplicate use alias "secure_http"`),
		},
		{
			name: "unknown_pin",
			text: flowText(
				`flow "f" v2`,
				``,
				`use ghost = @acme/nope@9.9.9`,
			),
			want: e(3, CodeUnknownPin, `unknown node version "@acme/nope@9.9.9" for alias "ghost"`),
		},
		{
			name: "undeclared_alias",
			text: flowText(
				`flow "f" v2`,
				``,
				`node n: missing {`,
				`}`,
			),
			want: e(3, CodeUndeclaredAlias, `node "n" uses undeclared alias "missing"`),
		},
		{
			name: "duplicate_slug",
			text: flowText(
				`flow "f" v2`,
				``,
				`use webhook_trigger = @sentiae/webhook-trigger@1.0.0`,
				``,
				`node intake: webhook_trigger {`,
				`}`,
				``,
				`node intake: webhook_trigger {`,
				`}`,
			),
			want: e(8, CodeDuplicateSlug, `duplicate node slug "intake"`),
		},
		{
			name: "duplicate_config_key",
			text: flowText(
				`flow "f" v2`,
				``,
				`use secure_http = @acme/secure-http@2.1.0`,
				``,
				`node w: secure_http {`,
				"\turl = \"https://api.example.net/a\"",
				"\turl = \"https://api.example.net/b\"",
				`}`,
			),
			want: e(7, CodeDuplicateConfigKey, `duplicate config key "url" on "w"`),
		},
		{
			name: "unknown_config_key",
			text: flowText(
				`flow "f" v2`,
				``,
				`use secure_http = @acme/secure-http@2.1.0`,
				``,
				`node w: secure_http {`,
				"\tbogus = 1",
				"\turl = \"https://api.example.net/a\"",
				`}`,
			),
			want: e(6, CodeUnknownConfigKey, `config key "bogus" on "w" is not declared by @acme/secure-http@2.1.0`),
		},
		{
			name: "config_value_mismatch",
			text: flowText(
				`flow "f" v2`,
				``,
				`use secure_http = @acme/secure-http@2.1.0`,
				``,
				`node w: secure_http {`,
				"\tretries = \"x\"",
				"\turl = \"https://api.example.net/a\"",
				`}`,
			),
			want: e(6, CodeConfigValueMismatch, `config "w.retries" does not conform: expected integer, got string`),
		},
		{
			name: "config_value_mismatch_nested_reason",
			text: flowText(
				`flow "f" v2`,
				``,
				`use secure_http = @acme/secure-http@2.1.0`,
				``,
				`node w: secure_http {`,
				"\ttags = [1]",
				"\turl = \"https://api.example.net/a\"",
				`}`,
			),
			want: e(6, CodeConfigValueMismatch, `config "w.tags" does not conform: at "items[0]": expected string, got number`),
		},
		{
			name: "config_required_missing",
			text: flowText(
				`flow "f" v2`,
				``,
				`use secure_http = @acme/secure-http@2.1.0`,
				``,
				`node w: secure_http {`,
				`}`,
			),
			want: e(5, CodeConfigRequiredMissing, `required config "w.url" has no value and no default`),
		},
		{
			name: "duplicate_port",
			text: flowText(
				`flow "f" v2`,
				``,
				`use secure_http = @acme/secure-http@2.1.0`,
				``,
				`node w: secure_http {`,
				"\turl = \"https://api.example.net/a\"",
				"\tport in payload label \"A\"",
				"\tport in payload label \"B\"",
				`}`,
			),
			want: e(8, CodeDuplicatePort, `duplicate port "payload" on "w"`),
		},
		{
			name: "port_out_unknown",
			text: flowText(
				`flow "f" v2`,
				``,
				`use secure_http = @acme/secure-http@2.1.0`,
				``,
				`node w: secure_http {`,
				"\turl = \"https://api.example.net/a\"",
				"\tport out nope label \"N\"",
				`}`,
			),
			want: e(7, CodePortOutUnknown, `port out "nope" is not an output of "w"`),
		},
		{
			name: "port_in_is_output",
			text: flowText(
				`flow "f" v2`,
				``,
				`use secure_http = @acme/secure-http@2.1.0`,
				``,
				`node w: secure_http {`,
				"\turl = \"https://api.example.net/a\"",
				"\tport in result",
				`}`,
			),
			want: e(7, CodePortInIsOutput, `port in "result" is an output of "w"`),
		},
		{
			name: "promotion_key_mismatch",
			text: flowText(
				`flow "f" v2`,
				``,
				`use secure_http = @acme/secure-http@2.1.0`,
				``,
				`node w: secure_http {`,
				"\turl = \"https://api.example.net/a\"",
				"\tport in method = config.url",
				`}`,
			),
			want: e(7, CodePromotionKeyMismatch, `promoted port "method" must expose config key "method", not "url"`),
		},
		{
			name: "promotion_unknown_key",
			text: flowText(
				`flow "f" v2`,
				``,
				`use secure_http = @acme/secure-http@2.1.0`,
				``,
				`node w: secure_http {`,
				"\turl = \"https://api.example.net/a\"",
				"\tport in ghost = config.ghost",
				`}`,
			),
			want: e(7, CodePromotionUnknownKey, `promoted port "ghost" has no config value or default on "w"`),
		},
		{
			name: "port_type_widens",
			text: flowText(
				`flow "f" v2`,
				``,
				`use secure_http = @acme/secure-http@2.1.0`,
				``,
				`node w: secure_http {`,
				"\turl = \"https://api.example.net/a\"",
				"\tport in method: string = config.method",
				`}`,
			),
			want: e(7, CodePortTypeWidens, `port type widens config.method`),
		},
		{
			name: "port_type_widens_absent_when_the_type_is_exact",
			text: flowText(
				`flow "f" v2`,
				``,
				`use secure_http = @acme/secure-http@2.1.0`,
				``,
				`node w: secure_http {`,
				"\turl = \"https://api.example.net/a\"",
				"\tport in ends_with_lf: string = config.ends_with_lf",
				`}`,
			),
			want:   e(7, CodePortTypeWidens, `port type widens config.ends_with_lf`),
			absent: true,
		},
		{
			name: "type_alias_unknown",
			text: flowText(
				`flow "f" v2`,
				``,
				`use secure_http = @acme/secure-http@2.1.0`,
				``,
				`node w: secure_http {`,
				"\turl = \"https://api.example.net/a\"",
				"\tport in cached: ghost.Result",
				`}`,
			),
			want: e(7, CodeTypeAliasUnknown, `type "ghost.Result" names alias "ghost", which is not a use`),
		},
		{
			name: "type_def_unknown",
			text: flowText(
				`flow "f" v2`,
				``,
				`use secure_http = @acme/secure-http@2.1.0`,
				``,
				`node w: secure_http {`,
				"\turl = \"https://api.example.net/a\"",
				"\tport in cached: secure_http.Missing",
				`}`,
			),
			want: e(7, CodeTypeDefUnknown, `type "secure_http.Missing" is not defined by @acme/secure-http@2.1.0`),
		},
		{
			name: "schema_type_override",
			text: flowText(
				`flow "f" v2`,
				``,
				`use secure_http = @acme/secure-http@2.1.0`,
				``,
				`node w: secure_http {`,
				"\turl = \"https://api.example.net/a\"",
				"\tport in payload: object",
				`}`,
			),
			want: e(7, CodeSchemaTypeOverride, `port "payload" is declared by @acme/secure-http@2.1.0; the manifest owns its type (label-only override allowed)`),
		},
		{
			name: "duplicate_wire",
			text: flowText(
				`flow "f" v2`,
				``,
				`use secure_http = @acme/secure-http@2.1.0`,
				`use webhook_trigger = @sentiae/webhook-trigger@1.0.0`,
				``,
				`node intake: webhook_trigger {`,
				`}`,
				``,
				`node w: secure_http {`,
				"\turl = \"https://api.example.net/a\"",
				`}`,
				``,
				`wire intake.body -> w.payload`,
				`wire intake.body -> w.payload`,
			),
			want: e(14, CodeDuplicateWire, `duplicate wire`),
		},
		{
			name: "wire_source_unknown_node",
			text: flowText(
				`flow "f" v2`,
				``,
				`use secure_http = @acme/secure-http@2.1.0`,
				``,
				`node w: secure_http {`,
				"\turl = \"https://api.example.net/a\"",
				`}`,
				``,
				`wire ghost.out -> w.payload`,
			),
			want: e(9, CodeWireSourceUnknownNode, `wire source "ghost" is not a node`),
		},
		{
			name: "wire_source_unknown_port",
			text: flowText(
				`flow "f" v2`,
				``,
				`use secure_http = @acme/secure-http@2.1.0`,
				`use webhook_trigger = @sentiae/webhook-trigger@1.0.0`,
				``,
				`node intake: webhook_trigger {`,
				`}`,
				``,
				`node w: secure_http {`,
				"\turl = \"https://api.example.net/a\"",
				`}`,
				``,
				`wire intake.nope -> w.payload`,
			),
			want: e(13, CodeWireSourceUnknownPort, `wire source "intake.nope" is not a port of "intake"`),
		},
		{
			name: "wire_target_unknown_node",
			text: flowText(
				`flow "f" v2`,
				``,
				`use webhook_trigger = @sentiae/webhook-trigger@1.0.0`,
				``,
				`node intake: webhook_trigger {`,
				`}`,
				``,
				`wire intake.body -> ghost.in`,
			),
			want: e(8, CodeWireTargetUnknownNode, `wire target "ghost" is not a node`),
		},
		{
			name: "wire_target_unknown_port",
			text: flowText(
				`flow "f" v2`,
				``,
				`use secure_http = @acme/secure-http@2.1.0`,
				`use webhook_trigger = @sentiae/webhook-trigger@1.0.0`,
				``,
				`node intake: webhook_trigger {`,
				`}`,
				``,
				`node w: secure_http {`,
				"\turl = \"https://api.example.net/a\"",
				`}`,
				``,
				`wire intake.body -> w.nope`,
			),
			want: e(13, CodeWireTargetUnknownPort, `wire target "w.nope" is not a port of "w"`),
		},
		{
			name: "wire_fan_in",
			text: flowText(
				`flow "f" v2`,
				``,
				`use secure_http = @acme/secure-http@2.1.0`,
				`use webhook_trigger = @sentiae/webhook-trigger@1.0.0`,
				``,
				`node intake: webhook_trigger {`,
				`}`,
				``,
				`node w: secure_http {`,
				"\turl = \"https://api.example.net/a\"",
				`}`,
				``,
				`wire intake.body -> w.payload`,
				`wire intake.method -> w.payload`,
			),
			want: e(14, CodeWireFanIn, `input "w.payload" has more than one wire; fan-in is a merge node`),
		},
		{
			name: "wire_type_incompatible",
			text: flowText(
				`flow "f" v2`,
				``,
				`use secure_http = @acme/secure-http@2.1.0`,
				``,
				`node source: secure_http {`,
				"\turl = \"https://api.example.net/a\"",
				`}`,
				``,
				`node sink: secure_http {`,
				"\turl = \"https://api.example.net/b\"",
				"\tport in tools: integer",
				`}`,
				``,
				`wire source.result -> sink.tools`,
			),
			want: e(14, CodeWireTypeIncompatible, `"source.result" cannot feed "sink.tools": object cannot feed integer`),
		},
		{
			name: "wire_type_unknown",
			text: flowText(
				`flow "f" v2`,
				``,
				`use any_out = @test/any-out@1.0.0`,
				`use secure_http = @acme/secure-http@2.1.0`,
				``,
				`node source: any_out {`,
				`}`,
				``,
				`node w: secure_http {`,
				"\turl = \"https://api.example.net/a\"",
				`}`,
				``,
				`wire source.out -> w.payload`,
			),
			want: e(13, CodeWireTypeUnknown, `source "source.out" is unconstrained; insert @sentiae/validate`),
		},
		{
			name: "cycle",
			text: flowText(
				`flow "f" v2`,
				``,
				`use branch_node = @sentiae/branch@1.0.0`,
				``,
				`node a: branch_node {`,
				`}`,
				``,
				`node b: branch_node {`,
				`}`,
				``,
				`wire a.on_true -> b.value`,
				`wire b.on_true -> a.value`,
			),
			want: e(5, CodeCycle, `cycle through "a"`),
		},
		{
			name: "required_input_unwired",
			text: flowText(
				`flow "f" v2`,
				``,
				`use secure_http = @acme/secure-http@2.1.0`,
				``,
				`node w: secure_http {`,
				"\turl = \"https://api.example.net/a\"",
				`}`,
			),
			want: e(5, CodeRequiredInputUnwired, `required input "w.payload" has no wire`),
		},
		{
			name: "trigger_input_wired",
			text: flowText(
				`flow "f" v2`,
				``,
				`use secure_http = @acme/secure-http@2.1.0`,
				`use webhook_trigger = @sentiae/webhook-trigger@1.0.0`,
				``,
				`node intake: webhook_trigger {`,
				`}`,
				``,
				`node w: secure_http {`,
				"\turl = \"https://api.example.net/a\"",
				`}`,
				``,
				`wire intake.body -> w.payload`,
				`wire w.result -> intake.body`,
			),
			want: e(14, CodeTriggerInputWired, `trigger "intake" cannot take a wire`),
		},
		{
			name: "multiple_triggers",
			text: flowText(
				`flow "f" v2`,
				``,
				`use webhook_trigger = @sentiae/webhook-trigger@1.0.0`,
				``,
				`node first: webhook_trigger {`,
				`}`,
				``,
				`node second: webhook_trigger {`,
				`}`,
			),
			want: e(8, CodeMultipleTriggers, `flow has more than one trigger ("first", "second")`),
		},
		{
			name: "layout_unknown_node",
			text: flowText(
				`flow "f" v2`,
				``,
				`layout {`,
				"\tghost @ 1,2",
				`}`,
			),
			want: e(4, CodeLayoutUnknownNode, `layout names "ghost", which is not a node`),
		},
		{
			name: "duplicate_layout",
			text: flowText(
				`flow "f" v2`,
				``,
				`use webhook_trigger = @sentiae/webhook-trigger@1.0.0`,
				``,
				`node intake: webhook_trigger {`,
				`}`,
				``,
				`layout {`,
				"\tintake @ 1,2",
				"\tintake @ 3,4",
				`}`,
			),
			want: e(10, CodeDuplicateLayout, `duplicate layout entry for "intake"`),
		},
		{
			name: "free_input_undeclared",
			text: flowText(
				`flow "f" v2`,
				``,
				`use secure_http = @acme/secure-http@2.1.0`,
				``,
				`node w: secure_http {`,
				"\turl = \"https://api.example.net/a\"",
				"\tport in tools: string[] label \"Tools\"",
				`}`,
			),
			want: Diagnostic{Severity: SeverityWarning, Line: 7, Code: CodeFreeInputUndeclared,
				Message: `input "w.tools" is not declared by @acme/secure-http@2.1.0; its value is passed through unvalidated`},
		},
		{
			name: "fire_and_forget",
			text: flowText(`flow "f" v2`),
			want: Diagnostic{Severity: SeverityInfo, Line: 1, Code: CodeFireAndForget, Message: msgFireAndForget},
		},
		{
			name: "fire_and_forget_absent_with_a_respond_node",
			text: flowText(
				`flow "f" v2`,
				``,
				`use respond = @sentiae/respond@1.0.0`,
				``,
				`node out: respond {`,
				`}`,
			),
			want:   Diagnostic{Severity: SeverityInfo, Line: 1, Code: CodeFireAndForget, Message: msgFireAndForget},
			absent: true,
		},
	}

	// §3.5's cascade suppression, asserted once: a node with no manifest emits
	// its STRUCTURAL findings and nothing that would need the contract.
	t.Run("cascade_suppression_without_a_manifest", func(t *testing.T) {
		text := flowText(
			`flow "f" v2`,
			``,
			`node w: missing {`,
			"\tbogus = 1",
			"\tport in tools: string[]",
			"\tport in method = config.url",
			`}`,
		)
		doc, diags := Parse(text)
		if doc == nil || len(diags) != 0 {
			t.Fatalf("Parse: doc=%v diags=%+v", doc != nil, diags)
		}
		got := Validate(doc, manifests)
		var codes []string
		for _, d := range got {
			codes = append(codes, d.Code)
		}
		want := []string{CodeFireAndForget, CodeUndeclaredAlias, CodePromotionKeyMismatch}
		if len(codes) != len(want) {
			t.Fatalf("codes = %v, want %v", codes, want)
		}
		for i := range want {
			if codes[i] != want[i] {
				t.Fatalf("codes = %v, want %v", codes, want)
			}
		}
	})

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			doc, diags := Parse(tt.text)
			if doc == nil {
				t.Fatalf("Parse refused: %+v", diags)
			}
			if len(diags) != 0 {
				t.Fatalf("row text does not parse cleanly: %+v", diags)
			}
			got := Validate(doc, manifests)
			if len(got) == 0 {
				t.Fatalf("Validate reported nothing at all")
			}
			hits := 0
			for _, d := range got {
				if d == tt.want {
					hits++
				}
			}
			if tt.absent {
				for _, d := range got {
					if d.Code == tt.want.Code {
						t.Fatalf("code %q must not be emitted here, got %+v (all: %+v)", tt.want.Code, d, got)
					}
				}
				return
			}
			if hits != 1 {
				t.Fatalf("want exactly one %+v, got %d in %+v", tt.want, hits, got)
			}
		})
	}
}

// TestValidate_WireInheritance pins DESIGN §6's projection: an untyped free
// input takes the schema of its ONE source, so the user is not asked to restate
// a type the wire already determined. The inheritance is a projection — it is
// never written to the file.
func TestValidate_WireInheritance(t *testing.T) {
	manifests := testManifests(t)

	doc := func(t *testing.T, toolsType, sourceEndpoint string, extraUse, extraNode []string) *Doc {
		t.Helper()
		lines := []string{
			`flow "f" v2`,
			``,
			`use secure_http = @acme/secure-http@2.1.0`,
			`use webhook_trigger = @sentiae/webhook-trigger@1.0.0`,
		}
		lines = append(lines, extraUse...)
		lines = append(lines,
			``,
			`node intake: webhook_trigger {`,
			`}`,
			``,
		)
		lines = append(lines, extraNode...)
		lines = append(lines,
			`node worker: secure_http {`,
			"\turl = \"https://api.example.net/a\"",
			"\tport in tools"+toolsType,
			`}`,
			``,
			`wire intake.headers -> worker.payload`,
			`wire `+sourceEndpoint+` -> worker.tools`,
		)
		parsed, diags := Parse(flowText(lines...))
		if parsed == nil || len(diags) != 0 {
			t.Fatalf("Parse: doc=%v diags=%+v", parsed != nil, diags)
		}
		return parsed
	}

	t.Run("untyped_free_input_inherits_its_source", func(t *testing.T) {
		d := doc(t, "", "intake.headers", nil, nil)
		for _, diag := range Validate(d, manifests) {
			if diag.Severity == SeverityError {
				t.Fatalf("unexpected error finding: %+v", diag)
			}
			if diag.Code == CodeWireTypeIncompatible || diag.Code == CodeWireTypeUnknown {
				t.Fatalf("unexpected type finding: %+v", diag)
			}
		}
		schema, defs, pin, ok := EffectiveSchema(d, manifests, "worker", "tools")
		if !ok {
			t.Fatal("EffectiveSchema did not resolve worker.tools")
		}
		if schema == nil || schema.Type != "object" || schema.Items != nil ||
			len(schema.Properties) != 0 || schema.Ref != "" {
			t.Fatalf("schema = %+v, want exactly {\"type\":\"object\"}", schema)
		}
		if pin != "@sentiae/webhook-trigger@1.0.0" {
			t.Fatalf("pin = %q, want the trigger's pin", pin)
		}
		if len(defs) != 0 {
			t.Fatalf("defs = %+v, want the trigger's (empty) $defs", defs)
		}
	})

	t.Run("declared_type_refuses_the_inherited_source", func(t *testing.T) {
		d := doc(t, ": integer", "intake.headers", nil, nil)
		want := e(15, CodeWireTypeIncompatible, `"intake.headers" cannot feed "worker.tools": object cannot feed integer`)
		if !hasDiagnostic(Validate(d, manifests), want) {
			t.Fatalf("want %+v in %+v", want, Validate(d, manifests))
		}
	})

	t.Run("unconstrained_source_into_a_declared_type_is_unknown", func(t *testing.T) {
		d := doc(t, ": integer", "source.out",
			[]string{`use any_out = @test/any-out@1.0.0`},
			[]string{`node source: any_out {`, `}`, ``})
		want := e(19, CodeWireTypeUnknown, `source "source.out" is unconstrained; insert @sentiae/validate`)
		if !hasDiagnostic(Validate(d, manifests), want) {
			t.Fatalf("want %+v in %+v", want, Validate(d, manifests))
		}
	})
}

func hasDiagnostic(list []Diagnostic, want Diagnostic) bool {
	for _, d := range list {
		if d == want {
			return true
		}
	}
	return false
}
