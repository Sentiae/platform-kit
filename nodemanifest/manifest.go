package nodemanifest

import (
	"encoding/json"
	"sort"

	"github.com/sentiae/platform-kit/nodeabi"
)

// TypeRef is the closed JSON-Schema 2020-12 subset a node declares its port
// and config shapes in. `{}` (every field zero) is EXPLICITLY unconstrained;
// a nil *TypeRef is "not known to this client", which is a different verdict.
type TypeRef struct {
	Ref                  string              `json:"$ref,omitempty"`
	Type                 string              `json:"type,omitempty"`
	Items                *TypeRef            `json:"items,omitempty"`
	Properties           map[string]*TypeRef `json:"properties,omitempty"`
	Required             []string            `json:"required,omitempty"`
	AdditionalProperties *bool               `json:"additionalProperties,omitempty"`
	Const                json.RawMessage     `json:"const,omitempty"`
	Enum                 []json.RawMessage   `json:"enum,omitempty"`
	Format               string              `json:"format,omitempty"`
	Default              json.RawMessage     `json:"default,omitempty"`
	Description          string              `json:"description,omitempty"`
}

// Defs is a manifest's `$defs` block.
type Defs map[string]*TypeRef

// IsUnconstrained reports the `{}` schema. A nil receiver is NOT unconstrained
// — it is unknown, and the two must never collapse into one verdict.
func (t *TypeRef) IsUnconstrained() bool {
	if t == nil {
		return false
	}
	return t.Ref == "" && t.Type == "" && t.Items == nil && len(t.Properties) == 0 &&
		len(t.Required) == 0 && t.AdditionalProperties == nil && len(t.Const) == 0 &&
		len(t.Enum) == 0 && t.Format == "" && len(t.Default) == 0 && t.Description == ""
}

// Clone deep-copies the schema.
func (t *TypeRef) Clone() *TypeRef {
	if t == nil {
		return nil
	}
	out := *t
	out.Items = t.Items.Clone()
	if t.Properties != nil {
		out.Properties = make(map[string]*TypeRef, len(t.Properties))
		for k, v := range t.Properties {
			out.Properties[k] = v.Clone()
		}
	}
	if t.Required != nil {
		out.Required = append([]string(nil), t.Required...)
	}
	if t.AdditionalProperties != nil {
		b := *t.AdditionalProperties
		out.AdditionalProperties = &b
	}
	out.Const = cloneRaw(t.Const)
	if t.Enum != nil {
		out.Enum = make([]json.RawMessage, len(t.Enum))
		for i, e := range t.Enum {
			out.Enum[i] = cloneRaw(e)
		}
	}
	out.Default = cloneRaw(t.Default)
	return &out
}

func cloneRaw(r json.RawMessage) json.RawMessage {
	if r == nil {
		return nil
	}
	return append(json.RawMessage(nil), r...)
}

// StripAnnotations deep-copies the schema with `default` and `description`
// cleared at every level. Annotations describe a schema; they never constrain
// it, so assignability must not see them.
func StripAnnotations(t *TypeRef) *TypeRef {
	c := t.Clone()
	stripAnnotations(c)
	return c
}

func stripAnnotations(t *TypeRef) {
	if t == nil {
		return
	}
	t.Default = nil
	t.Description = ""
	stripAnnotations(t.Items)
	for _, v := range t.Properties {
		stripAnnotations(v)
	}
}

// Capabilities is the manifest's capability block.
type Capabilities struct {
	Egress []string `json:"egress"`
}

// Display is the palette-facing presentation of a node.
type Display struct {
	Description string `json:"description"`
	Icon        string `json:"icon"`
	Name        string `json:"name"`
}

// Implementation names one language's entry point and lockfiles.
type Implementation struct {
	Entry     string   `json:"entry"`
	Lockfiles []string `json:"lockfiles"`
}

// Port is one declared input or output.
type Port struct {
	Description string   `json:"description,omitempty"`
	Name        string   `json:"name"`
	Required    bool     `json:"required"`
	Schema      *TypeRef `json:"schema"`
}

// Resources is the node's sandbox budget.
type Resources struct {
	MemoryMiB int `json:"memoryMiB"`
	TimeoutMs int `json:"timeoutMs"`
}

// Secret is one declared secret name.
type Secret struct {
	Name     string `json:"name"`
	Required bool   `json:"required"`
}

// Manifest is `node.json`. Fields are declared in sorted-key order so the
// struct's own encoding is already canonical.
type Manifest struct {
	Defs            Defs                      `json:"$defs,omitempty"`
	Schema          string                    `json:"$schema"`
	Capabilities    Capabilities              `json:"capabilities"`
	Category        string                    `json:"category"`
	Config          *TypeRef                  `json:"config"`
	Display         Display                   `json:"display"`
	Implementations map[string]Implementation `json:"implementations"`
	Inputs          []Port                    `json:"inputs"`
	Name            string                    `json:"name"`
	Outputs         []Port                    `json:"outputs"`
	Resources       Resources                 `json:"resources"`
	Role            *string                   `json:"role"`
	Secrets         []Secret                  `json:"secrets"`
	Shape           string                    `json:"shape"`

	// unknown holds the JSON pointers of keys Parse did not recognise, at every
	// object level. They are RECORDED rather than rejected so Parse stays a
	// shape reader and Validate stays the single place publication says no.
	unknown []string
	// shape holds the problems Parse observed but may not refuse on its own:
	// absent required keys and TypeRef keywords whose JSON type is wrong.
	shape []Problem
}

// Problem is one publication objection. Path is an RFC 6901 pointer from the
// document root.
type Problem struct {
	Path    string
	Code    string
	Message string
}

func (p *Problem) Error() string { return p.Code + " at " + p.Path + ": " + p.Message }

// Categories is the closed palette category list (node-service's domain).
var Categories = []string{"trigger", "http", "transform", "branch", "data", "code", "ai", "output", "vendor"}

// Scope is the manifest name's scope segment (`@acme/x` ⇒ `acme`).
func (m *Manifest) Scope() string {
	scope, _, err := nodeabi.ParseQualifiedName(m.Name)
	if err != nil {
		return ""
	}
	return scope
}

// PackageName is the manifest name's package segment (`@acme/x` ⇒ `x`).
func (m *Manifest) PackageName() string {
	_, name, err := nodeabi.ParseQualifiedName(m.Name)
	if err != nil {
		return ""
	}
	return name
}

// RepoRef is the node's repository path (`acme/x.node`).
func (m *Manifest) RepoRef() string {
	scope, name, err := nodeabi.ParseQualifiedName(m.Name)
	if err != nil {
		return ""
	}
	return nodeabi.RepoRef(scope, name)
}

// Input returns the declared input port, or nil.
func (m *Manifest) Input(name string) *Port { return findPort(m.Inputs, name) }

// Output returns the declared output port, or nil.
func (m *Manifest) Output(name string) *Port { return findPort(m.Outputs, name) }

func findPort(ports []Port, name string) *Port {
	for i := range ports {
		if ports[i].Name == name {
			return &ports[i]
		}
	}
	return nil
}

// ConfigProperty returns the declared schema of one config key.
func (m *Manifest) ConfigProperty(key string) (*TypeRef, bool) {
	if m.Config == nil || m.Config.Properties == nil {
		return nil, false
	}
	t, ok := m.Config.Properties[key]
	return t, ok
}

// ConfigDefault returns one config key's `default`, when it has one.
func (m *Manifest) ConfigDefault(key string) (json.RawMessage, bool) {
	t, ok := m.ConfigProperty(key)
	if !ok || t == nil || len(t.Default) == 0 {
		return nil, false
	}
	return t.Default, true
}

// ImplementationNames lists the implementation languages, sorted.
func (m *Manifest) ImplementationNames() []string {
	names := make([]string, 0, len(m.Implementations))
	for k := range m.Implementations {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}

// IsTrigger reports role == "trigger".
func (m *Manifest) IsTrigger() bool { return m.Role != nil && *m.Role == "trigger" }

// IsRespond reports role == "respond".
func (m *Manifest) IsRespond() bool { return m.Role != nil && *m.Role == "respond" }
