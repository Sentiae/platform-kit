package flowlang

// Severity is a diagnostic's weight. Only SeverityError stops a schedule.
type Severity string

// The three severities.
const (
	SeverityError   Severity = "error"
	SeverityWarning Severity = "warning"
	SeverityInfo    Severity = "info"
)

// Diagnostic is one positioned finding. It is DATA, not an error value: a
// reader repairing a file wants every objection at once, and a language server
// wants them addressable by line and code — neither is served by a wrapped
// error chain.
type Diagnostic struct {
	Severity Severity
	Line     int // 1-based; document-level findings use 1
	Code     string
	Message  string
}

// The parse codes.
const (
	CodeBOM                  = "bom"
	CodeCRLF                 = "crlf"
	CodeEmptyFile            = "empty_file"
	CodeFinalNewline         = "final_newline"
	CodeUnsupportedVersion   = "unsupported_version"
	CodeMissingHeader        = "missing_header"
	CodeUnknownStatement     = "unknown_statement"
	CodeExpectedToken        = "expected_token"
	CodeBadIdent             = "bad_ident"
	CodeBadPin               = "bad_pin"
	CodeBadType              = "bad_type"
	CodeBadString            = "bad_string"
	CodeBadNumber            = "bad_number"
	CodeBadInlineJSON        = "bad_inline_json"
	CodeBadIndent            = "bad_indent"
	CodeHeredocOpener        = "heredoc_opener"
	CodeHeredocPrefix        = "heredoc_prefix"
	CodeHeredocUnterminated  = "heredoc_unterminated"
	CodeSectionOrder         = "section_order"
	CodeUnclosedBlock        = "unclosed_block"
	CodeTrailingInput        = "trailing_input"
	CodeDuplicateLayoutBlock = "duplicate_layout_block"
)

// The validate codes.
const (
	CodeDuplicateAlias        = "duplicate_alias"
	CodeUnknownPin            = "unknown_pin"
	CodeUndeclaredAlias       = "undeclared_alias"
	CodeDuplicateSlug         = "duplicate_slug"
	CodeDuplicateConfigKey    = "duplicate_config_key"
	CodeUnknownConfigKey      = "unknown_config_key"
	CodeConfigValueMismatch   = "config_value_mismatch"
	CodeConfigRequiredMissing = "config_required_missing"
	CodeDuplicatePort         = "duplicate_port"
	CodePortOutUnknown        = "port_out_unknown"
	CodePortInIsOutput        = "port_in_is_output"
	CodePromotionKeyMismatch  = "promotion_key_mismatch"
	CodePromotionUnknownKey   = "promotion_unknown_key"
	CodePortTypeWidens        = "port_type_widens"
	CodeTypeAliasUnknown      = "type_alias_unknown"
	CodeTypeDefUnknown        = "type_def_unknown"
	CodeSchemaTypeOverride    = "schema_type_override"
	CodeDuplicateWire         = "duplicate_wire"
	CodeWireSourceUnknownNode = "wire_source_unknown_node"
	CodeWireSourceUnknownPort = "wire_source_unknown_port"
	CodeWireTargetUnknownNode = "wire_target_unknown_node"
	CodeWireTargetUnknownPort = "wire_target_unknown_port"
	CodeWireFanIn             = "wire_fan_in"
	CodeWireTypeIncompatible  = "wire_type_incompatible"
	CodeWireTypeUnknown       = "wire_type_unknown"
	CodeCycle                 = "cycle"
	CodeRequiredInputUnwired  = "required_input_unwired"
	CodeTriggerInputWired     = "trigger_input_wired"
	CodeMultipleTriggers      = "multiple_triggers"
	CodeLayoutUnknownNode     = "layout_unknown_node"
	CodeDuplicateLayout       = "duplicate_layout"
	CodeFreeInputUndeclared   = "free_input_undeclared"
	CodeFireAndForget         = "fire_and_forget"
)

// The verbatim parse messages. Every one is shared with the TypeScript reader;
// a synonym here is a message the two clients disagree on.
const (
	msgBOM                  = "file starts with a BOM; .flow is UTF-8 without a BOM"
	msgCRLF                 = "CR found; .flow uses LF newlines only"
	msgEmptyFile            = "file is empty"
	msgFinalNewline         = "file must end with exactly one LF"
	msgUnsupportedVersion   = "platform-kit/flowlang reads v2 only (file is v%d)"
	msgMissingHeader        = "missing `flow` header"
	msgUnknownStatement     = "unknown statement %s"
	msgExpectedToken        = "expected %s"
	msgExpectedValue        = "expected a value"
	msgBadIdent             = "expected an identifier ([a-z_][a-z0-9_]*)"
	msgIdentTooLong         = "identifier exceeds 64 characters"
	msgBadPin               = "expected a node pin (@scope/name@x.y.z)"
	msgBadString            = "expected a quoted string"
	msgUnterminatedString   = "unterminated string"
	msgInvalidEscape        = "invalid string escape"
	msgBadInteger           = "expected an integer"
	msgBadVersion           = "the language version must be a positive integer"
	msgUnterminatedObject   = "unterminated object"
	msgUnterminatedArray    = "unterminated array"
	msgInvalidInlineJSON    = "invalid inline JSON"
	msgBadIndent            = "a node body line must be indented with one tab"
	msgHeredocOpener        = "a heredoc opener ends the line"
	msgHeredocPrefix        = "a heredoc content line needs the two-tab prefix"
	msgHeredocUnterminated  = "unterminated heredoc (no `>>>` terminator)"
	msgUseBeforeNode        = "`use` lines must precede every node block"
	msgNodeBeforeWire       = "node blocks must precede every wire line"
	msgWireBeforeLayout     = "wire lines must precede the layout block"
	msgUnclosedNode         = "unclosed node block"
	msgUnclosedLayout       = "unclosed layout block"
	msgTrailingInput        = "unexpected trailing input"
	msgDuplicateLayoutBlock = "a file carries exactly one layout block"
)

// The verbatim validate messages.
const (
	msgDuplicateAlias        = `duplicate use alias "%s"`
	msgUnknownPin            = `unknown node version "%s" for alias "%s"`
	msgUndeclaredAlias       = `node "%s" uses undeclared alias "%s"`
	msgDuplicateSlug         = `duplicate node slug "%s"`
	msgDuplicateConfigKey    = `duplicate config key "%s" on "%s"`
	msgUnknownConfigKey      = `config key "%s" on "%s" is not declared by %s`
	msgConfigValueMismatch   = `config "%s.%s" does not conform: %s`
	msgConfigRequiredMissing = `required config "%s.%s" has no value and no default`
	msgDuplicatePort         = `duplicate port "%s" on "%s"`
	msgPortOutUnknown        = `port out "%s" is not an output of "%s"`
	msgPortInIsOutput        = `port in "%s" is an output of "%s"`
	msgPromotionKeyMismatch  = `promoted port "%s" must expose config key "%s", not "%s"`
	msgPromotionUnknownKey   = `promoted port "%s" has no config value or default on "%s"`
	msgPortTypeWidens        = "port type widens config.%s"
	msgTypeAliasUnknown      = `type "%s" names alias "%s", which is not a use`
	msgTypeDefUnknown        = `type "%s" is not defined by %s`
	msgSchemaTypeOverride    = `port "%s" is declared by %s; the manifest owns its type (label-only override allowed)`
	msgDuplicateWire         = "duplicate wire"
	msgWireSourceUnknownNode = `wire source "%s" is not a node`
	msgWireSourceUnknownPort = `wire source "%s.%s" is not a port of "%s"`
	msgWireTargetUnknownNode = `wire target "%s" is not a node`
	msgWireTargetUnknownPort = `wire target "%s.%s" is not a port of "%s"`
	msgWireFanIn             = `input "%s.%s" has more than one wire; fan-in is a merge node`
	msgWireTypeIncompatible  = `"%s.%s" cannot feed "%s.%s": %s`
	msgWireTypeUnknown       = `source "%s.%s" is unconstrained; insert @sentiae/validate`
	msgCycle                 = `cycle through "%s"`
	msgRequiredInputUnwired  = `required input "%s.%s" has no wire`
	msgTriggerInputWired     = `trigger "%s" cannot take a wire`
	msgMultipleTriggers      = `flow has more than one trigger ("%s", "%s")`
	msgLayoutUnknownNode     = `layout names "%s", which is not a node`
	msgDuplicateLayout       = `duplicate layout entry for "%s"`
	msgFreeInputUndeclared   = `input "%s.%s" is not declared by %s; its value is passed through unvalidated`
	msgFireAndForget         = "flow has no respond node; the handler answers 202 Accepted"
)
