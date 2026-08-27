package nodemanifest

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/sentiae/platform-kit/nodeabi"
)

// The closed manifest problem codes.
const (
	CodeJSONInvalid                 = "json_invalid"
	CodeNotObject                   = "not_object"
	CodeTypeMismatch                = "type_mismatch"
	CodeMissingKey                  = "missing_key"
	CodeKeywordUnknown              = "keyword_unknown"
	CodeSchemaURL                   = "schema_url"
	CodeNameInvalid                 = "name_invalid"
	CodeCategoryInvalid             = "category_invalid"
	CodeRoleInvalid                 = "role_invalid"
	CodeDisplayNameRequired         = "display_name_required"
	CodeDefNameInvalid              = "def_name_invalid"
	CodeTypeInvalid                 = "type_invalid"
	CodeRefInvalid                  = "ref_invalid"
	CodeRefUnresolved               = "ref_unresolved"
	CodeFormatInvalid               = "format_invalid"
	CodeFormatWithoutString         = "format_without_string"
	CodeItemsWithoutArray           = "items_without_array"
	CodeObjectKeywordWithoutObject  = "object_keyword_without_object"
	CodeRequiredNotProperty         = "required_not_property"
	CodeAdditionalPropertiesNotBool = "additional_properties_not_bool"
	CodeEnumEmpty                   = "enum_empty"
	CodeEnumDuplicate               = "enum_duplicate"
	CodeDefaultOutsideConfig        = "default_outside_config"
	CodeDefaultMismatch             = "default_mismatch"
	CodeConfigNotObject             = "config_not_object"
	CodeConfigNotClosed             = "config_not_closed"
	CodePortNameInvalid             = "port_name_invalid"
	CodePortDuplicate               = "port_duplicate"
	CodeTriggerHasInputs            = "trigger_has_inputs"
	CodeRespondOutputsShape         = "respond_outputs_shape"
	CodeRespondNoInputs             = "respond_no_inputs"
	CodeImplementationsEmpty        = "implementations_empty"
	CodeImplementationUnknown       = "implementation_unknown"
	CodeImplementationEntry         = "implementation_entry"
	CodeImplementationLockfiles     = "implementation_lockfiles"
	CodeEgressPatternInvalid        = "egress_pattern_invalid"
	CodeResourcesInvalid            = "resources_invalid"
	CodeSecretNameInvalid           = "secret_name_invalid"
	CodeSecretDuplicate             = "secret_duplicate"
	CodeShapeInvalid                = "shape_invalid"
)

// The verbatim message texts.
const (
	msgJSONInvalid                 = "manifest is not valid JSON: %s"
	msgNotObject                   = "manifest must be a JSON object"
	msgTypeMismatch                = "expected %s"
	msgMissingKey                  = "missing required key"
	msgKeywordUnknown              = "unknown keyword %s"
	msgSchemaURL                   = "$schema must be https://sentiae.com/schemas/node-manifest/v1.json"
	msgNameInvalid                 = "name must match @scope/name (lower-case letters, digits, hyphens)"
	msgCategoryInvalid             = "category must be one of trigger, http, transform, branch, data, code, ai, output, vendor"
	msgRoleInvalid                 = `role must be null, "trigger" or "respond"`
	msgDisplayNameRequired         = "display.name must be non-empty"
	msgDefNameInvalid              = "$defs key must match [A-Z][A-Za-z0-9]{0,63}"
	msgTypeInvalid                 = "type must be one of null, boolean, integer, number, string, object, array"
	msgRefInvalid                  = "$ref must match #/$defs/<DefName>"
	msgRefUnresolved               = "$ref %s does not resolve in $defs"
	msgFormatInvalid               = "format must be one of uuid, uri, date-time"
	msgFormatWithoutString         = "format requires type: string"
	msgItemsWithoutArray           = "items requires type: array"
	msgObjectKeywordWithoutObject  = "%s requires type: object"
	msgRequiredNotProperty         = "required entry %s is not a property"
	msgAdditionalPropertiesNotBool = "additionalProperties must be a boolean"
	msgEnumEmpty                   = "enum must not be empty"
	msgEnumDuplicate               = "enum has a duplicate value"
	msgDefaultOutsideConfig        = "default is allowed only on config.properties.* roots"
	msgDefaultMismatch             = "default does not conform: %s"
	msgConfigNotObject             = "config must have type: object"
	msgConfigNotClosed             = "config must set additionalProperties: false"
	msgPortNameInvalid             = "port name must match [a-z][a-z0-9_]{0,63}"
	msgPortDuplicate               = "duplicate port name %s"
	msgTriggerHasInputs            = "a trigger declares no inputs"
	msgRespondOutputsShape         = `a respond node's outputs are exactly one required port "response" with the SDK response schema`
	msgRespondNoInputs             = "a respond node declares at least one input"
	msgImplementationsEmpty        = "implementations must name at least one of go, typescript"
	msgImplementationUnknown       = "unknown implementation %s"
	msgImplementationEntry         = "entry must be %s"
	msgImplementationLockfiles     = "lockfiles must be %s"
	msgEgressPatternInvalid        = "egress pattern %s is invalid"
	msgResourcesInvalid            = "memoryMiB must be an integer in [16, 8192] and timeoutMs an integer in [1, 3600000]"
	msgSecretNameInvalid           = "secret name must match [a-z][a-z0-9_]{0,63}"
	msgSecretDuplicate             = "duplicate secret name %s"
	msgShapeInvalid                = `shape must be "inline" or "standalone_service"`
)

// SchemaURL is the one `$schema` a v1 manifest may carry.
const SchemaURL = "https://sentiae.com/schemas/node-manifest/v1.json"

var (
	manifestNameRx = regexp.MustCompile(`^@([a-z0-9][a-z0-9-]*)/([a-z0-9][a-z0-9-]*)$`)
	defNameRx      = regexp.MustCompile(`^[A-Z][A-Za-z0-9]{0,63}$`)
	refRx          = regexp.MustCompile(`^#/\$defs/[A-Z][A-Za-z0-9]{0,63}$`)
	portNameRx     = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
)

var scalarTypes = map[string]bool{
	"null": true, "boolean": true, "integer": true, "number": true,
	"string": true, "object": true, "array": true,
}

var formats = map[string]bool{"uuid": true, "uri": true, "date-time": true}

var implementationShape = map[string]Implementation{
	"go":         {Entry: "go/node.go", Lockfiles: []string{"go/go.mod", "go/go.sum"}},
	"typescript": {Entry: "typescript/src/node.ts", Lockfiles: []string{"typescript/package-lock.json"}},
}

// Load parses and validates in one call. A parse error is returned as the ONE
// problem it is, with no manifest — a document Parse refused has no shape for
// Validate to read.
func Load(b []byte) (*Manifest, []Problem) {
	m, err := Parse(b)
	if err != nil {
		var p *Problem
		if ok := asProblem(err, &p); ok {
			return nil, []Problem{*p}
		}
		return nil, []Problem{{Code: CodeJSONInvalid, Message: fmt.Sprintf(msgJSONInvalid, err)}}
	}
	return m, Validate(m)
}

func asProblem(err error, out **Problem) bool {
	p, ok := err.(*Problem)
	if ok {
		*out = p
	}
	return ok
}

// Validate returns every publication objection, sorted by (Path, Code). An
// empty result means the manifest may be published.
func Validate(m *Manifest) []Problem {
	v := &validator{m: m}
	v.out = append(v.out, m.shape...)
	for _, ptr := range m.unknown {
		v.add(ptr, CodeKeywordUnknown, fmt.Sprintf(msgKeywordUnknown, q(pointerKey(ptr))))
	}

	if m.Schema != SchemaURL {
		v.add("/$schema", CodeSchemaURL, msgSchemaURL)
	}
	if !manifestNameRx.MatchString(m.Name) {
		v.add("/name", CodeNameInvalid, msgNameInvalid)
	}
	if !containsString(Categories, m.Category) {
		v.add("/category", CodeCategoryInvalid, msgCategoryInvalid)
	}
	if m.Role != nil && *m.Role != "trigger" && *m.Role != "respond" {
		v.add("/role", CodeRoleInvalid, msgRoleInvalid)
	}
	if m.Display.Name == "" {
		v.add("/display/name", CodeDisplayNameRequired, msgDisplayNameRequired)
	}
	if m.Shape != "inline" && m.Shape != "standalone_service" {
		v.add("/shape", CodeShapeInvalid, msgShapeInvalid)
	}

	for _, name := range sortedDefNames(m.Defs) {
		ptr := "/$defs/" + escapePointer(name)
		if !defNameRx.MatchString(name) {
			v.add(ptr, CodeDefNameInvalid, msgDefNameInvalid)
		}
		v.typeRef(m.Defs[name], ptr, false)
	}

	if m.Config != nil {
		if m.Config.Type != "object" {
			v.add("/config", CodeConfigNotObject, msgConfigNotObject)
		}
		if m.Config.AdditionalProperties == nil || *m.Config.AdditionalProperties {
			v.add("/config", CodeConfigNotClosed, msgConfigNotClosed)
		}
		v.typeRef(m.Config, "/config", false)
	}

	v.ports(m.Inputs, "/inputs")
	v.ports(m.Outputs, "/outputs")

	if m.IsTrigger() && len(m.Inputs) != 0 {
		v.add("/inputs", CodeTriggerHasInputs, msgTriggerHasInputs)
	}
	if m.IsRespond() {
		if len(m.Inputs) == 0 {
			v.add("/inputs", CodeRespondNoInputs, msgRespondNoInputs)
		}
		if !respondOutputsOK(m.Outputs) {
			v.add("/outputs", CodeRespondOutputsShape, msgRespondOutputsShape)
		}
	}

	if len(m.Implementations) == 0 {
		v.add("/implementations", CodeImplementationsEmpty, msgImplementationsEmpty)
	}
	for _, name := range m.ImplementationNames() {
		ptr := "/implementations/" + escapePointer(name)
		want, ok := implementationShape[name]
		if !ok {
			v.add(ptr, CodeImplementationUnknown, fmt.Sprintf(msgImplementationUnknown, q(name)))
			continue
		}
		got := m.Implementations[name]
		if got.Entry != want.Entry {
			v.add(ptr+"/entry", CodeImplementationEntry, fmt.Sprintf(msgImplementationEntry, q(want.Entry)))
		}
		if !equalStrings(got.Lockfiles, want.Lockfiles) {
			v.add(ptr+"/lockfiles", CodeImplementationLockfiles, fmt.Sprintf(msgImplementationLockfiles, inlineStrings(want.Lockfiles)))
		}
	}

	for i, p := range m.Capabilities.Egress {
		if err := ValidateEgressPattern(p); err != nil {
			v.add(fmt.Sprintf("/capabilities/egress/%d", i), CodeEgressPatternInvalid, fmt.Sprintf(msgEgressPatternInvalid, q(p)))
		}
	}

	if m.Resources.MemoryMiB < 16 || m.Resources.MemoryMiB > 8192 ||
		m.Resources.TimeoutMs < 1 || m.Resources.TimeoutMs > 3600000 {
		v.add("/resources", CodeResourcesInvalid, msgResourcesInvalid)
	}

	seenSecret := map[string]bool{}
	for i, s := range m.Secrets {
		ptr := fmt.Sprintf("/secrets/%d/name", i)
		if !portNameRx.MatchString(s.Name) {
			v.add(ptr, CodeSecretNameInvalid, msgSecretNameInvalid)
		}
		if seenSecret[s.Name] {
			v.add(ptr, CodeSecretDuplicate, fmt.Sprintf(msgSecretDuplicate, q(s.Name)))
		}
		seenSecret[s.Name] = true
	}

	sort.SliceStable(v.out, func(i, j int) bool {
		if v.out[i].Path != v.out[j].Path {
			return v.out[i].Path < v.out[j].Path
		}
		return v.out[i].Code < v.out[j].Code
	})
	return v.out
}

type validator struct {
	m   *Manifest
	out []Problem
}

func (v *validator) add(path, code, message string) {
	v.out = append(v.out, Problem{Path: path, Code: code, Message: message})
}

func (v *validator) ports(ports []Port, base string) {
	seen := map[string]bool{}
	for i, p := range ports {
		ptr := fmt.Sprintf("%s/%d", base, i)
		if !portNameRx.MatchString(p.Name) {
			v.add(ptr+"/name", CodePortNameInvalid, msgPortNameInvalid)
		}
		if seen[p.Name] {
			v.add(ptr+"/name", CodePortDuplicate, fmt.Sprintf(msgPortDuplicate, q(p.Name)))
		}
		seen[p.Name] = true
		v.typeRef(p.Schema, ptr+"/schema", false)
	}
}

// typeRef walks one schema. `configRoot` marks the ONE position a `default` is
// legal: a direct child of `/config/properties`.
func (v *validator) typeRef(t *TypeRef, ptr string, configRoot bool) {
	if t == nil {
		return
	}
	if t.Ref != "" {
		if !refRx.MatchString(t.Ref) {
			v.add(ptr+"/$ref", CodeRefInvalid, msgRefInvalid)
		} else if _, ok := v.m.Defs[defName(t.Ref)]; !ok {
			v.add(ptr+"/$ref", CodeRefUnresolved, fmt.Sprintf(msgRefUnresolved, q(t.Ref)))
		}
	}
	if t.Type != "" && !scalarTypes[t.Type] {
		v.add(ptr+"/type", CodeTypeInvalid, msgTypeInvalid)
	}
	if t.Format != "" {
		if !formats[t.Format] {
			v.add(ptr+"/format", CodeFormatInvalid, msgFormatInvalid)
		}
		if t.Type != "string" {
			v.add(ptr+"/format", CodeFormatWithoutString, msgFormatWithoutString)
		}
	}
	if t.Items != nil && t.Type != "array" {
		v.add(ptr+"/items", CodeItemsWithoutArray, msgItemsWithoutArray)
	}
	for _, kw := range []string{"additionalProperties", "properties", "required"} {
		present := false
		switch kw {
		case "additionalProperties":
			present = t.AdditionalProperties != nil
		case "properties":
			present = t.Properties != nil
		case "required":
			present = t.Required != nil
		}
		if present && t.Type != "object" {
			v.add(ptr+"/"+kw, CodeObjectKeywordWithoutObject, fmt.Sprintf(msgObjectKeywordWithoutObject, q(kw)))
		}
	}
	for _, p := range t.Required {
		if _, ok := t.Properties[p]; !ok {
			v.add(ptr+"/required", CodeRequiredNotProperty, fmt.Sprintf(msgRequiredNotProperty, q(p)))
		}
	}
	if t.Enum != nil {
		if len(t.Enum) == 0 {
			v.add(ptr+"/enum", CodeEnumEmpty, msgEnumEmpty)
		}
		seen := map[string]bool{}
		for _, e := range t.Enum {
			key := canonicalRaw(e)
			if seen[key] {
				v.add(ptr+"/enum", CodeEnumDuplicate, msgEnumDuplicate)
				break
			}
			seen[key] = true
		}
	}
	if len(t.Default) > 0 {
		if !configRoot {
			v.add(ptr+"/default", CodeDefaultOutsideConfig, msgDefaultOutsideConfig)
		} else {
			var value any
			if err := json.Unmarshal(t.Default, &value); err != nil {
				v.add(ptr+"/default", CodeDefaultMismatch, fmt.Sprintf(msgDefaultMismatch, err))
			} else if ok, reason := Conforms(value, t, v.m.Defs); !ok {
				v.add(ptr+"/default", CodeDefaultMismatch, fmt.Sprintf(msgDefaultMismatch, reason))
			}
		}
	}
	if t.Items != nil {
		v.typeRef(t.Items, ptr+"/items", false)
	}
	for _, name := range sortedTypeRefNames(t.Properties) {
		child := ptr + "/properties/" + escapePointer(name)
		v.typeRef(t.Properties[name], child, ptr == "/config")
	}
}

func respondOutputsOK(outputs []Port) bool {
	if len(outputs) != 1 {
		return false
	}
	p := outputs[0]
	if p.Name != "response" || !p.Required {
		return false
	}
	got, err := CanonicalJSON(p.Schema)
	if err != nil {
		return false
	}
	return string(got) == string(nodeabi.ResponseSchema())
}

func canonicalRaw(raw json.RawMessage) string {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return string(raw)
	}
	return canonicalString(v)
}

func sortedDefNames(d Defs) []string {
	out := make([]string, 0, len(d))
	for k := range d {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedTypeRefNames(m map[string]*TypeRef) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// inlineStrings is §3.7's canonical inline JSON for a string array.
func inlineStrings(list []string) string {
	parts := make([]string, len(list))
	for i, s := range list {
		parts[i] = q(s)
	}
	return "[" + strings.Join(parts, ", ") + "]"
}
