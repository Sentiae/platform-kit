package nodemanifest

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Parse reads a manifest's JSON SHAPE. It refuses only what cannot be stored
// (invalid JSON, a non-object document, a value of the wrong JSON type) and
// RECORDS everything else — unknown keys, absent required keys, keyword values
// whose JSON type is wrong — so Validate stays the single place that decides
// whether a manifest may be published.
func Parse(b []byte) (*Manifest, error) {
	var root any
	if err := json.Unmarshal(b, &root); err != nil {
		return nil, &Problem{Path: "", Code: CodeJSONInvalid, Message: fmt.Sprintf(msgJSONInvalid, err)}
	}
	obj, ok := root.(map[string]any)
	if !ok {
		return nil, &Problem{Path: "", Code: CodeNotObject, Message: msgNotObject}
	}
	p := &parser{m: &Manifest{}}
	if err := p.manifest(obj); err != nil {
		return nil, err
	}
	sort.Strings(p.m.unknown)
	return p.m, nil
}

type parser struct {
	m *Manifest
}

var topLevelKeys = map[string]bool{
	"$defs": true, "$schema": true, "capabilities": true, "category": true, "config": true,
	"display": true, "implementations": true, "inputs": true, "name": true, "outputs": true,
	"resources": true, "role": true, "secrets": true, "shape": true,
}

// requiredTopLevel is every top-level key except `$defs`.
var requiredTopLevel = []string{
	"$schema", "capabilities", "category", "config", "display", "implementations",
	"inputs", "name", "outputs", "resources", "role", "secrets", "shape",
}

var typeRefKeys = map[string]bool{
	"$ref": true, "type": true, "items": true, "properties": true, "required": true,
	"additionalProperties": true, "const": true, "enum": true, "format": true,
	"default": true, "description": true,
}

func (p *parser) manifest(obj map[string]any) error {
	p.unknownKeys(obj, topLevelKeys, "")
	for _, k := range requiredTopLevel {
		if _, ok := obj[k]; !ok {
			p.record(Problem{Path: "/" + k, Code: CodeMissingKey, Message: msgMissingKey})
		}
	}

	if v, ok := obj["$defs"]; ok {
		defs, err := asObject(v, "/$defs")
		if err != nil {
			return err
		}
		p.m.Defs = Defs{}
		for _, name := range keysOf(defs) {
			ptr := "/$defs/" + escapePointer(name)
			t, err := p.typeRef(defs[name], ptr)
			if err != nil {
				return err
			}
			p.m.Defs[name] = t
		}
	}
	if v, ok := obj["$schema"]; ok {
		s, err := asString(v, "/$schema")
		if err != nil {
			return err
		}
		p.m.Schema = s
	}
	if v, ok := obj["capabilities"]; ok {
		caps, err := asObject(v, "/capabilities")
		if err != nil {
			return err
		}
		p.unknownKeys(caps, map[string]bool{"egress": true}, "/capabilities")
		if e, ok := caps["egress"]; ok {
			list, err := asStringArray(e, "/capabilities/egress")
			if err != nil {
				return err
			}
			p.m.Capabilities.Egress = list
		} else {
			p.record(Problem{Path: "/capabilities/egress", Code: CodeMissingKey, Message: msgMissingKey})
		}
	}
	if v, ok := obj["category"]; ok {
		s, err := asString(v, "/category")
		if err != nil {
			return err
		}
		p.m.Category = s
	}
	if v, ok := obj["config"]; ok {
		t, err := p.typeRef(v, "/config")
		if err != nil {
			return err
		}
		p.m.Config = t
	}
	if v, ok := obj["display"]; ok {
		d, err := asObject(v, "/display")
		if err != nil {
			return err
		}
		p.unknownKeys(d, map[string]bool{"description": true, "icon": true, "name": true}, "/display")
		for _, k := range []string{"description", "icon", "name"} {
			raw, ok := d[k]
			if !ok {
				p.record(Problem{Path: "/display/" + k, Code: CodeMissingKey, Message: msgMissingKey})
				continue
			}
			s, err := asString(raw, "/display/"+k)
			if err != nil {
				return err
			}
			switch k {
			case "description":
				p.m.Display.Description = s
			case "icon":
				p.m.Display.Icon = s
			case "name":
				p.m.Display.Name = s
			}
		}
	}
	if v, ok := obj["implementations"]; ok {
		impls, err := asObject(v, "/implementations")
		if err != nil {
			return err
		}
		p.m.Implementations = map[string]Implementation{}
		for _, name := range keysOf(impls) {
			ptr := "/implementations/" + escapePointer(name)
			body, err := asObject(impls[name], ptr)
			if err != nil {
				return err
			}
			p.unknownKeys(body, map[string]bool{"entry": true, "lockfiles": true}, ptr)
			var impl Implementation
			if raw, ok := body["entry"]; ok {
				s, err := asString(raw, ptr+"/entry")
				if err != nil {
					return err
				}
				impl.Entry = s
			} else {
				p.record(Problem{Path: ptr + "/entry", Code: CodeMissingKey, Message: msgMissingKey})
			}
			if raw, ok := body["lockfiles"]; ok {
				list, err := asStringArray(raw, ptr+"/lockfiles")
				if err != nil {
					return err
				}
				impl.Lockfiles = list
			} else {
				p.record(Problem{Path: ptr + "/lockfiles", Code: CodeMissingKey, Message: msgMissingKey})
			}
			p.m.Implementations[name] = impl
		}
	}
	for _, key := range []string{"inputs", "outputs"} {
		v, ok := obj[key]
		if !ok {
			continue
		}
		arr, err := asArray(v, "/"+key)
		if err != nil {
			return err
		}
		ports := make([]Port, 0, len(arr))
		for i, item := range arr {
			ptr := fmt.Sprintf("/%s/%d", key, i)
			port, err := p.port(item, ptr)
			if err != nil {
				return err
			}
			ports = append(ports, port)
		}
		if key == "inputs" {
			p.m.Inputs = ports
		} else {
			p.m.Outputs = ports
		}
	}
	if v, ok := obj["name"]; ok {
		s, err := asString(v, "/name")
		if err != nil {
			return err
		}
		p.m.Name = s
	}
	if v, ok := obj["resources"]; ok {
		r, err := asObject(v, "/resources")
		if err != nil {
			return err
		}
		p.unknownKeys(r, map[string]bool{"memoryMiB": true, "timeoutMs": true}, "/resources")
		for _, k := range []string{"memoryMiB", "timeoutMs"} {
			raw, ok := r[k]
			if !ok {
				p.record(Problem{Path: "/resources/" + k, Code: CodeMissingKey, Message: msgMissingKey})
				continue
			}
			n, err := asInteger(raw, "/resources/"+k)
			if err != nil {
				return err
			}
			if k == "memoryMiB" {
				p.m.Resources.MemoryMiB = n
			} else {
				p.m.Resources.TimeoutMs = n
			}
		}
	}
	if v, ok := obj["role"]; ok {
		switch t := v.(type) {
		case nil:
			p.m.Role = nil
		case string:
			s := t
			p.m.Role = &s
		default:
			return &Problem{Path: "/role", Code: CodeTypeMismatch, Message: fmt.Sprintf(msgTypeMismatch, "null or string")}
		}
	}
	if v, ok := obj["secrets"]; ok {
		arr, err := asArray(v, "/secrets")
		if err != nil {
			return err
		}
		secrets := make([]Secret, 0, len(arr))
		for i, item := range arr {
			ptr := fmt.Sprintf("/secrets/%d", i)
			body, err := asObject(item, ptr)
			if err != nil {
				return err
			}
			p.unknownKeys(body, map[string]bool{"name": true, "required": true}, ptr)
			var s Secret
			if raw, ok := body["name"]; ok {
				v, err := asString(raw, ptr+"/name")
				if err != nil {
					return err
				}
				s.Name = v
			} else {
				p.record(Problem{Path: ptr + "/name", Code: CodeMissingKey, Message: msgMissingKey})
			}
			if raw, ok := body["required"]; ok {
				v, err := asBool(raw, ptr+"/required")
				if err != nil {
					return err
				}
				s.Required = v
			} else {
				p.record(Problem{Path: ptr + "/required", Code: CodeMissingKey, Message: msgMissingKey})
			}
			secrets = append(secrets, s)
		}
		p.m.Secrets = secrets
	}
	if v, ok := obj["shape"]; ok {
		s, err := asString(v, "/shape")
		if err != nil {
			return err
		}
		p.m.Shape = s
	}
	return nil
}

func (p *parser) port(v any, ptr string) (Port, error) {
	body, err := asObject(v, ptr)
	if err != nil {
		return Port{}, err
	}
	p.unknownKeys(body, map[string]bool{"description": true, "name": true, "required": true, "schema": true}, ptr)
	var port Port
	if raw, ok := body["description"]; ok {
		s, err := asString(raw, ptr+"/description")
		if err != nil {
			return Port{}, err
		}
		port.Description = s
	}
	if raw, ok := body["name"]; ok {
		s, err := asString(raw, ptr+"/name")
		if err != nil {
			return Port{}, err
		}
		port.Name = s
	} else {
		p.record(Problem{Path: ptr + "/name", Code: CodeMissingKey, Message: msgMissingKey})
	}
	if raw, ok := body["required"]; ok {
		b, err := asBool(raw, ptr+"/required")
		if err != nil {
			return Port{}, err
		}
		port.Required = b
	} else {
		p.record(Problem{Path: ptr + "/required", Code: CodeMissingKey, Message: msgMissingKey})
	}
	if raw, ok := body["schema"]; ok {
		t, err := p.typeRef(raw, ptr+"/schema")
		if err != nil {
			return Port{}, err
		}
		port.Schema = t
	} else {
		p.record(Problem{Path: ptr + "/schema", Code: CodeMissingKey, Message: msgMissingKey})
	}
	return port, nil
}

// typeRef reads one schema. The CONTAINER must be an object (a shape Parse
// cannot store otherwise); the KEYWORDS are lenient — a keyword whose JSON
// type is wrong is recorded with the publication code that names it, so
// `{"additionalProperties": {"type": "string"}}` reaches Validate as
// additional_properties_not_bool rather than dying as a decode error.
func (p *parser) typeRef(v any, ptr string) (*TypeRef, error) {
	body, err := asObject(v, ptr)
	if err != nil {
		return nil, err
	}
	p.unknownTypeRefKeys(body, ptr)
	t := &TypeRef{}
	if raw, ok := body["$ref"]; ok {
		s, isString := raw.(string)
		if !isString {
			p.record(Problem{Path: ptr + "/$ref", Code: CodeRefInvalid, Message: msgRefInvalid})
		} else {
			t.Ref = s
		}
	}
	if raw, ok := body["type"]; ok {
		s, isString := raw.(string)
		if !isString {
			p.record(Problem{Path: ptr + "/type", Code: CodeTypeInvalid, Message: msgTypeInvalid})
		} else {
			t.Type = s
		}
	}
	if raw, ok := body["items"]; ok {
		sub, err := p.typeRef(raw, ptr+"/items")
		if err != nil {
			return nil, err
		}
		t.Items = sub
	}
	if raw, ok := body["properties"]; ok {
		props, err := asObject(raw, ptr+"/properties")
		if err != nil {
			return nil, err
		}
		t.Properties = map[string]*TypeRef{}
		for _, name := range keysOf(props) {
			sub, err := p.typeRef(props[name], ptr+"/properties/"+escapePointer(name))
			if err != nil {
				return nil, err
			}
			t.Properties[name] = sub
		}
	}
	if raw, ok := body["required"]; ok {
		list, err := asStringArray(raw, ptr+"/required")
		if err != nil {
			return nil, err
		}
		t.Required = list
	}
	if raw, ok := body["additionalProperties"]; ok {
		b, isBool := raw.(bool)
		if !isBool {
			p.record(Problem{Path: ptr + "/additionalProperties", Code: CodeAdditionalPropertiesNotBool, Message: msgAdditionalPropertiesNotBool})
		} else {
			t.AdditionalProperties = &b
		}
	}
	if raw, ok := body["const"]; ok {
		enc, err := encodeValue(raw, ptr+"/const")
		if err != nil {
			return nil, err
		}
		t.Const = enc
	}
	if raw, ok := body["enum"]; ok {
		arr, err := asArray(raw, ptr+"/enum")
		if err != nil {
			return nil, err
		}
		t.Enum = make([]json.RawMessage, 0, len(arr))
		for i, item := range arr {
			enc, err := encodeValue(item, fmt.Sprintf("%s/enum/%d", ptr, i))
			if err != nil {
				return nil, err
			}
			t.Enum = append(t.Enum, enc)
		}
	}
	if raw, ok := body["format"]; ok {
		s, isString := raw.(string)
		if !isString {
			p.record(Problem{Path: ptr + "/format", Code: CodeFormatInvalid, Message: msgFormatInvalid})
		} else {
			t.Format = s
		}
	}
	if raw, ok := body["default"]; ok {
		enc, err := encodeValue(raw, ptr+"/default")
		if err != nil {
			return nil, err
		}
		t.Default = enc
	}
	if raw, ok := body["description"]; ok {
		s, err := asString(raw, ptr+"/description")
		if err != nil {
			return nil, err
		}
		t.Description = s
	}
	return t, nil
}

func (p *parser) record(pr Problem) { p.m.shape = append(p.m.shape, pr) }

func (p *parser) unknownKeys(obj map[string]any, allowed map[string]bool, ptr string) {
	for _, k := range keysOf(obj) {
		if !allowed[k] {
			p.m.unknown = append(p.m.unknown, ptr+"/"+escapePointer(k))
		}
	}
}

func (p *parser) unknownTypeRefKeys(obj map[string]any, ptr string) {
	p.unknownKeys(obj, typeRefKeys, ptr)
}

func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func escapePointer(token string) string {
	return strings.ReplaceAll(strings.ReplaceAll(token, "~", "~0"), "/", "~1")
}

// pointerKey is the last reference token of a pointer, unescaped.
func pointerKey(ptr string) string {
	i := strings.LastIndexByte(ptr, '/')
	if i < 0 {
		return ptr
	}
	token := ptr[i+1:]
	return strings.ReplaceAll(strings.ReplaceAll(token, "~1", "/"), "~0", "~")
}

func encodeValue(v any, ptr string) (json.RawMessage, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, &Problem{Path: ptr, Code: CodeJSONInvalid, Message: fmt.Sprintf(msgJSONInvalid, err)}
	}
	return json.RawMessage(b), nil
}

func asObject(v any, ptr string) (map[string]any, error) {
	m, ok := v.(map[string]any)
	if !ok {
		return nil, &Problem{Path: ptr, Code: CodeTypeMismatch, Message: fmt.Sprintf(msgTypeMismatch, "object")}
	}
	return m, nil
}

func asArray(v any, ptr string) ([]any, error) {
	a, ok := v.([]any)
	if !ok {
		return nil, &Problem{Path: ptr, Code: CodeTypeMismatch, Message: fmt.Sprintf(msgTypeMismatch, "array")}
	}
	return a, nil
}

func asString(v any, ptr string) (string, error) {
	s, ok := v.(string)
	if !ok {
		return "", &Problem{Path: ptr, Code: CodeTypeMismatch, Message: fmt.Sprintf(msgTypeMismatch, "string")}
	}
	return s, nil
}

func asBool(v any, ptr string) (bool, error) {
	b, ok := v.(bool)
	if !ok {
		return false, &Problem{Path: ptr, Code: CodeTypeMismatch, Message: fmt.Sprintf(msgTypeMismatch, "boolean")}
	}
	return b, nil
}

func asInteger(v any, ptr string) (int, error) {
	f, ok := v.(float64)
	if !ok || f != float64(int64(f)) {
		return 0, &Problem{Path: ptr, Code: CodeTypeMismatch, Message: fmt.Sprintf(msgTypeMismatch, "integer")}
	}
	return int(f), nil
}

func asStringArray(v any, ptr string) ([]string, error) {
	arr, err := asArray(v, ptr)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(arr))
	for i, item := range arr {
		s, err := asString(item, fmt.Sprintf("%s/%d", ptr, i))
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, nil
}
