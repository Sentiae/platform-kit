package entitlement

// ToolDescriptor is the minimal shape platform-kit uses to gate a
// tool by entitlement. Real Eve tool descriptors live in foundry +
// the MCP layer; this is the projection we need for filtering.
// (Paradigm Shift §I.4)
type ToolDescriptor struct {
	Name               string
	RequiredEntitlement Entitlement // empty = always available
}

// FilterTools returns only the tools the supplied set has access to.
// Tools without a RequiredEntitlement are returned unchanged.
//
// Empty input slice yields empty output. Nil set yields tools that
// require no entitlement.
func FilterTools(s *Set, tools []ToolDescriptor) []ToolDescriptor {
	out := make([]ToolDescriptor, 0, len(tools))
	for _, t := range tools {
		if t.RequiredEntitlement == "" {
			out = append(out, t)
			continue
		}
		if s != nil && s.Has(t.RequiredEntitlement) {
			out = append(out, t)
		}
	}
	return out
}
