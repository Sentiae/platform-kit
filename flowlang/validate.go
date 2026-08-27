package flowlang

import (
	"fmt"
	"sort"

	"github.com/sentiae/platform-kit/flowlang/types"
	"github.com/sentiae/platform-kit/nodemanifest"
)

// Manifests is the pinned node contract set a document is read against, keyed
// by the pin literal `@scope/name@semver`.
type Manifests map[string]*nodemanifest.Manifest

// index is a document's resolved identity map. Duplicates resolve to the LAST
// declaration (§3.8) — the duplicate itself is a diagnostic, but the file still
// has to mean ONE thing while the reader repairs it.
type index struct {
	pins   map[string]string
	bySlug map[string]*Node
}

func newIndex(doc *Doc) *index {
	idx := &index{pins: map[string]string{}, bySlug: map[string]*Node{}}
	for _, u := range doc.Uses {
		idx.pins[u.Alias] = u.Pin
	}
	for i := range doc.Nodes {
		idx.bySlug[doc.Nodes[i].Slug] = &doc.Nodes[i]
	}
	return idx
}

// manifestOf resolves one node's pinned contract. A node whose alias is not a
// `use`, or whose pin is not in the set, has NO manifest — and §3.5 stands
// every contract-dependent check down for it rather than inventing a verdict.
func (idx *index) manifestOf(n *Node, m Manifests) (*nodemanifest.Manifest, string) {
	pin, ok := idx.pins[n.Alias]
	if !ok {
		return nil, ""
	}
	return m[pin], pin
}

// Validate reports every semantic finding of a document against its pinned
// manifests, sorted by (Line, Code).
func Validate(doc *Doc, m Manifests) []Diagnostic {
	if doc == nil {
		return nil
	}
	var out []Diagnostic
	add := func(sev Severity, line int, code, message string) {
		if line == 0 {
			line = 1
		}
		out = append(out, Diagnostic{Severity: sev, Line: line, Code: code, Message: message})
	}
	bad := func(line int, code, message string) { add(SeverityError, line, code, message) }

	idx := newIndex(doc)

	seenAlias := map[string]bool{}
	for _, u := range doc.Uses {
		if seenAlias[u.Alias] {
			bad(u.Line, CodeDuplicateAlias, fmt.Sprintf(msgDuplicateAlias, u.Alias))
		}
		seenAlias[u.Alias] = true
		if _, ok := m[u.Pin]; !ok {
			bad(u.Line, CodeUnknownPin, fmt.Sprintf(msgUnknownPin, u.Pin, u.Alias))
		}
	}

	// Wires are indexed before the node pass: a promoted key fed by a wire is
	// not a missing required config.
	wiredInto := map[string]bool{}
	for _, w := range doc.Wires {
		wiredInto[w.To+"."+w.ToPort] = true
	}

	seenSlug := map[string]bool{}
	var triggers []*Node
	for i := range doc.Nodes {
		n := &doc.Nodes[i]
		if seenSlug[n.Slug] {
			bad(n.Line, CodeDuplicateSlug, fmt.Sprintf(msgDuplicateSlug, n.Slug))
		}
		seenSlug[n.Slug] = true
		if _, ok := idx.pins[n.Alias]; !ok {
			bad(n.Line, CodeUndeclaredAlias, fmt.Sprintf(msgUndeclaredAlias, n.Slug, n.Alias))
		}
		man, pin := idx.manifestOf(n, m)
		if man != nil && man.IsTrigger() {
			triggers = append(triggers, n)
		}

		configKeys := map[string]bool{}
		for _, c := range n.Config {
			if configKeys[c.Key] {
				bad(c.Line, CodeDuplicateConfigKey, fmt.Sprintf(msgDuplicateConfigKey, c.Key, n.Slug))
			}
			configKeys[c.Key] = true
		}

		seenPort := map[string]bool{}
		for _, p := range n.Ports {
			key := PortTriviaKey(p.Dir, p.ID)
			if seenPort[key] {
				bad(p.Line, CodeDuplicatePort, fmt.Sprintf(msgDuplicatePort, p.ID, n.Slug))
			}
			seenPort[key] = true

			// Structural: a promotion states its own key, and a type expression
			// names an alias and a def. Neither needs THIS node's manifest.
			if p.Dir == "in" && p.ConfigKey != "" && p.ConfigKey != p.ID {
				bad(p.Line, CodePromotionKeyMismatch,
					fmt.Sprintf(msgPromotionKeyMismatch, p.ID, p.ID, p.ConfigKey))
			}
			if d := typeExprProblem(idx, m, p); d != nil {
				out = append(out, *d)
			}

			if man == nil {
				continue
			}
			switch {
			case p.Dir == "out":
				if man.Output(p.ID) == nil {
					bad(p.Line, CodePortOutUnknown, fmt.Sprintf(msgPortOutUnknown, p.ID, n.Slug))
				}
			case p.ConfigKey != "":
				prop, declared := man.ConfigProperty(p.ConfigKey)
				if !configKeys[p.ConfigKey] && !declared {
					bad(p.Line, CodePromotionUnknownKey,
						fmt.Sprintf(msgPromotionUnknownKey, p.ID, n.Slug))
				}
				if p.Type != "" && declared && widens(idx, m, p, prop, man, pin) {
					bad(p.Line, CodePortTypeWidens, fmt.Sprintf(msgPortTypeWidens, p.ConfigKey))
				}
			case man.Input(p.ID) != nil:
				if p.Type != "" {
					bad(p.Line, CodeSchemaTypeOverride,
						fmt.Sprintf(msgSchemaTypeOverride, p.ID, pin))
				}
			case man.Output(p.ID) != nil:
				bad(p.Line, CodePortInIsOutput, fmt.Sprintf(msgPortInIsOutput, p.ID, n.Slug))
			default:
				add(SeverityWarning, p.Line, CodeFreeInputUndeclared,
					fmt.Sprintf(msgFreeInputUndeclared, n.Slug, p.ID, pin))
			}
		}

		if man == nil {
			continue
		}

		for _, c := range n.Config {
			prop, declared := man.ConfigProperty(c.Key)
			if !declared {
				bad(c.Line, CodeUnknownConfigKey,
					fmt.Sprintf(msgUnknownConfigKey, c.Key, n.Slug, pin))
				continue
			}
			if ok, reason := nodemanifest.Conforms(c.Value, prop, man.Defs); !ok {
				bad(c.Line, CodeConfigValueMismatch,
					fmt.Sprintf(msgConfigValueMismatch, n.Slug, c.Key, reason))
			}
		}

		if man.Config != nil {
			required := append([]string(nil), man.Config.Required...)
			sort.Strings(required)
			for _, key := range required {
				if configKeys[key] {
					continue
				}
				if _, has := man.ConfigDefault(key); has {
					continue
				}
				if isPromoted(n, key) && wiredInto[n.Slug+"."+key] {
					continue
				}
				bad(n.Line, CodeConfigRequiredMissing,
					fmt.Sprintf(msgConfigRequiredMissing, n.Slug, key))
			}
		}

		for _, in := range man.Inputs {
			if !in.Required || wiredInto[n.Slug+"."+in.Name] {
				continue
			}
			bad(n.Line, CodeRequiredInputUnwired,
				fmt.Sprintf(msgRequiredInputUnwired, n.Slug, in.Name))
		}
	}

	if len(triggers) > 1 {
		bad(triggers[1].Line, CodeMultipleTriggers,
			fmt.Sprintf(msgMultipleTriggers, triggers[0].Slug, triggers[1].Slug))
	}

	seenWire := map[string]bool{}
	fanIn := map[string]int{}
	for _, w := range doc.Wires {
		key := WireAnchorKey(w)
		if seenWire[key] {
			bad(w.Line, CodeDuplicateWire, msgDuplicateWire)
		}
		seenWire[key] = true

		from, fromOK := idx.bySlug[w.From]
		to, toOK := idx.bySlug[w.To]
		if !fromOK {
			bad(w.Line, CodeWireSourceUnknownNode, fmt.Sprintf(msgWireSourceUnknownNode, w.From))
		}
		if !toOK {
			bad(w.Line, CodeWireTargetUnknownNode, fmt.Sprintf(msgWireTargetUnknownNode, w.To))
		}
		if !fromOK || !toOK {
			continue
		}

		fromMan, _ := idx.manifestOf(from, m)
		toMan, _ := idx.manifestOf(to, m)

		sourceResolved := false
		if fromMan != nil {
			if fromMan.Output(w.FromPort) == nil {
				bad(w.Line, CodeWireSourceUnknownPort,
					fmt.Sprintf(msgWireSourceUnknownPort, w.From, w.FromPort, w.From))
			} else {
				sourceResolved = true
			}
		}
		targetResolved := false
		if toMan != nil {
			if toMan.Input(w.ToPort) == nil && !declaresInput(to, w.ToPort) {
				bad(w.Line, CodeWireTargetUnknownPort,
					fmt.Sprintf(msgWireTargetUnknownPort, w.To, w.ToPort, w.To))
			} else {
				targetResolved = true
			}
			if toMan.IsTrigger() {
				bad(w.Line, CodeTriggerInputWired, fmt.Sprintf(msgTriggerInputWired, w.To))
			}
		}
		if !sourceResolved || !targetResolved {
			continue
		}

		fk := w.To + "." + w.ToPort
		fanIn[fk]++
		if fanIn[fk] > 1 {
			bad(w.Line, CodeWireFanIn, fmt.Sprintf(msgWireFanIn, w.To, w.ToPort))
		}

		src, srcDefs, srcPin, srcOK := effectiveSchema(doc, idx, m, w.From, w.FromPort, map[string]bool{})
		tgt, tgtDefs, tgtPin, tgtOK := effectiveSchema(doc, idx, m, w.To, w.ToPort, map[string]bool{})
		if !srcOK || !tgtOK {
			continue
		}
		res := types.Assignable(src, tgt, types.Context{
			SourceDefs:   srcDefs,
			TargetDefs:   tgtDefs,
			SameManifest: srcPin == tgtPin,
		})
		switch res.Verdict {
		case types.VerdictIncompatible:
			bad(w.Line, CodeWireTypeIncompatible, fmt.Sprintf(msgWireTypeIncompatible,
				w.From, w.FromPort, w.To, w.ToPort, res.Reason))
		case types.VerdictUnknown:
			bad(w.Line, CodeWireTypeUnknown, fmt.Sprintf(msgWireTypeUnknown, w.From, w.FromPort))
		}
	}

	if slug, line, ok := firstCycleNode(doc, idx); ok {
		bad(line, CodeCycle, fmt.Sprintf(msgCycle, slug))
	}

	laidOut := map[string]bool{}
	for _, l := range doc.Layout {
		if laidOut[l.Slug] {
			bad(l.Line, CodeDuplicateLayout, fmt.Sprintf(msgDuplicateLayout, l.Slug))
		}
		laidOut[l.Slug] = true
		if _, ok := idx.bySlug[l.Slug]; !ok {
			bad(l.Line, CodeLayoutUnknownNode, fmt.Sprintf(msgLayoutUnknownNode, l.Slug))
		}
	}

	// The handler's answer is a property of the WHOLE document, including the
	// document with no nodes at all: a flow that names no respond node answers
	// 202, and the reader is told so rather than discovering it in production.
	respond := false
	for i := range doc.Nodes {
		if man, _ := idx.manifestOf(&doc.Nodes[i], m); man != nil && man.IsRespond() {
			respond = true
			break
		}
	}
	if !respond {
		add(SeverityInfo, 1, CodeFireAndForget, msgFireAndForget)
	}

	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Line != out[j].Line {
			return out[i].Line < out[j].Line
		}
		return out[i].Code < out[j].Code
	})
	return out
}

// typeExprProblem resolves a port's type expression against the document's
// aliases. It is structural: it needs the ALIASED manifest, never the port's
// own node's.
func typeExprProblem(idx *index, m Manifests, p Port) *Diagnostic {
	if p.Type == "" {
		return nil
	}
	e, err := types.ParseTypeExpr(p.Type)
	if err != nil || e.Kind != types.KindRef {
		return nil
	}
	pin, ok := idx.pins[e.Alias]
	if !ok {
		return &Diagnostic{Severity: SeverityError, Line: p.Line, Code: CodeTypeAliasUnknown,
			Message: fmt.Sprintf(msgTypeAliasUnknown, p.Type, e.Alias)}
	}
	man := m[pin]
	if man == nil {
		return nil
	}
	if _, ok := man.Defs[e.Def]; !ok {
		return &Diagnostic{Severity: SeverityError, Line: p.Line, Code: CodeTypeDefUnknown,
			Message: fmt.Sprintf(msgTypeDefUnknown, p.Type, pin)}
	}
	return nil
}

// widens reports a promotion whose declared type is not exact or assignable TO
// the config property it exposes — a port that would accept values the node's
// own contract rejects.
func widens(idx *index, m Manifests, p Port, prop *nodemanifest.TypeRef, man *nodemanifest.Manifest, pin string) bool {
	schema, defs, srcPin, ok := typeExprSchema(idx, m, p.Type, pin, man.Defs)
	if !ok {
		return false
	}
	res := types.Assignable(schema, prop, types.Context{
		SourceDefs:   defs,
		TargetDefs:   man.Defs,
		SameManifest: srcPin == pin,
	})
	return res.Verdict != types.VerdictExact && res.Verdict != types.VerdictAssignable
}

// typeExprSchema maps a type-expression text to the schema it names, with the
// `$defs` and pin the schema is resolved against.
func typeExprSchema(idx *index, m Manifests, text, ownPin string, ownDefs nodemanifest.Defs) (*nodemanifest.TypeRef, nodemanifest.Defs, string, bool) {
	e, err := types.ParseTypeExpr(text)
	if err != nil {
		return nil, nil, "", false
	}
	if e.Kind != types.KindRef {
		return types.FromTypeExpr(e), ownDefs, ownPin, true
	}
	pin, ok := idx.pins[e.Alias]
	if !ok {
		return nil, nil, "", false
	}
	man := m[pin]
	if man == nil {
		return nil, nil, "", false
	}
	if _, ok := man.Defs[e.Def]; !ok {
		return nil, nil, "", false
	}
	return types.FromTypeExpr(e), man.Defs, pin, true
}

func isPromoted(n *Node, key string) bool {
	for _, p := range n.Ports {
		if p.Dir == "in" && p.ConfigKey == key {
			return true
		}
	}
	return false
}

func declaresInput(n *Node, id string) bool {
	_, ok := declaredInput(n, id)
	return ok
}

// declaredInput finds the node's own `port in` line for an id. The FIRST wins:
// a duplicate is a diagnostic, and the file still has to mean one thing.
func declaredInput(n *Node, id string) (Port, bool) {
	for _, p := range n.Ports {
		if p.Dir == "in" && p.ID == id {
			return p, true
		}
	}
	return Port{}, false
}

// EffectiveSchema is §3.4: the schema a port ACTUALLY carries, which is the
// manifest's for a declared port, the config property's for a promotion, the
// declared type expression's for a typed free input, and — for an untyped free
// input with exactly one incoming wire — the source's own schema. That last
// case is a PROJECTION: it is never written to the file.
func EffectiveSchema(doc *Doc, m Manifests, slug, port string) (*nodemanifest.TypeRef, nodemanifest.Defs, string, bool) {
	return effectiveSchema(doc, newIndex(doc), m, slug, port, map[string]bool{})
}

func effectiveSchema(doc *Doc, idx *index, m Manifests, slug, port string, seen map[string]bool) (*nodemanifest.TypeRef, nodemanifest.Defs, string, bool) {
	key := slug + "." + port
	if seen[key] {
		return nil, nil, "", false
	}
	seen[key] = true

	n, ok := idx.bySlug[slug]
	if !ok {
		return nil, nil, "", false
	}
	man, pin := idx.manifestOf(n, m)
	if man == nil {
		return nil, nil, "", false
	}
	if in := man.Input(port); in != nil {
		return in.Schema, man.Defs, pin, true
	}
	if out := man.Output(port); out != nil {
		return out.Schema, man.Defs, pin, true
	}
	decl, ok := declaredInput(n, port)
	if !ok {
		return nil, nil, "", false
	}
	if decl.ConfigKey != "" {
		if decl.Type != "" {
			return typeExprSchema(idx, m, decl.Type, pin, man.Defs)
		}
		prop, declared := man.ConfigProperty(decl.ConfigKey)
		if !declared {
			return nil, nil, "", false
		}
		return prop, man.Defs, pin, true
	}
	if decl.Type != "" {
		return typeExprSchema(idx, m, decl.Type, pin, man.Defs)
	}
	if src, ok := soleWireInto(doc, slug, port); ok {
		return effectiveSchema(doc, idx, m, src.From, src.FromPort, seen)
	}
	return &nodemanifest.TypeRef{}, man.Defs, pin, true
}

// soleWireInto returns the one wire feeding a port, when there is exactly one.
// Fan-in is a diagnostic, not an inheritance rule.
func soleWireInto(doc *Doc, slug, port string) (Wire, bool) {
	var found Wire
	count := 0
	for _, w := range doc.Wires {
		if w.To == slug && w.ToPort == port {
			found = w
			count++
		}
	}
	return found, count == 1
}

// firstCycleNode returns the first node in DOCUMENT order that lies on a cycle.
// Reporting every member of every cycle buries the one edge the reader has to
// cut; reporting the first is a repair instruction.
func firstCycleNode(doc *Doc, idx *index) (string, int, bool) {
	adj := map[string][]string{}
	for _, w := range doc.Wires {
		if _, ok := idx.bySlug[w.From]; !ok {
			continue
		}
		if _, ok := idx.bySlug[w.To]; !ok {
			continue
		}
		adj[w.From] = append(adj[w.From], w.To)
	}
	for i := range doc.Nodes {
		n := &doc.Nodes[i]
		if reaches(adj, n.Slug, n.Slug, map[string]bool{}) {
			return n.Slug, n.Line, true
		}
	}
	return "", 0, false
}

func reaches(adj map[string][]string, from, target string, seen map[string]bool) bool {
	for _, next := range adj[from] {
		if next == target {
			return true
		}
		if seen[next] {
			continue
		}
		seen[next] = true
		if reaches(adj, next, target, seen) {
			return true
		}
	}
	return false
}
