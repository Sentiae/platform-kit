package flowlang

import (
	"encoding/json"
	"sort"

	"github.com/sentiae/platform-kit/flowlang/types"
	"github.com/sentiae/platform-kit/nodemanifest"
)

// Canonicalize returns a NEW document in §3.3's canonical form: `use` sorted by
// alias, config lines sorted by key with default-equal lines omitted, ports in
// contract order, wires sorted by their endpoint tuple, layout in node order.
//
// A node whose pin is not in the manifest set keeps its config and port order
// untouched: without the contract there is no "restates the default" to remove,
// and reordering on a guess would rewrite lines the reader did not change.
func Canonicalize(doc *Doc, m Manifests) *Doc {
	if doc == nil {
		return nil
	}
	idx := newIndex(doc)
	out := &Doc{
		Name:       doc.Name,
		Version:    doc.Version,
		Uses:       append([]Use(nil), doc.Uses...),
		Wires:      append([]Wire(nil), doc.Wires...),
		FileTrivia: doc.FileTrivia,
	}

	sort.SliceStable(out.Uses, func(i, j int) bool { return out.Uses[i].Alias < out.Uses[j].Alias })

	out.Nodes = make([]Node, 0, len(doc.Nodes))
	for i := range doc.Nodes {
		out.Nodes = append(out.Nodes, canonicalNode(doc.Nodes[i], idx, m))
	}

	sort.SliceStable(out.Wires, func(i, j int) bool {
		a, b := out.Wires[i], out.Wires[j]
		if a.From != b.From {
			return a.From < b.From
		}
		if a.FromPort != b.FromPort {
			return a.FromPort < b.FromPort
		}
		if a.To != b.To {
			return a.To < b.To
		}
		return a.ToPort < b.ToPort
	})

	order := make(map[string]int, len(doc.Nodes))
	for i := range doc.Nodes {
		if _, seen := order[doc.Nodes[i].Slug]; !seen {
			order[doc.Nodes[i].Slug] = i
		}
	}
	out.Layout = append([]Layout(nil), doc.Layout...)
	sort.SliceStable(out.Layout, func(i, j int) bool {
		a, aok := order[out.Layout[i].Slug]
		b, bok := order[out.Layout[j].Slug]
		if aok != bok {
			// A layout entry naming no node is a diagnostic, not a position:
			// it keeps its relative place at the end rather than sorting as 0.
			return aok
		}
		return a < b
	})

	return out
}

func canonicalNode(n Node, idx *index, m Manifests) Node {
	man, pin := idx.manifestOf(&n, m)
	out := n
	out.Config = append([]Config(nil), n.Config...)
	out.Ports = append([]Port(nil), n.Ports...)
	out.Trivia = cloneNodeTrivia(n.Trivia)
	if man == nil {
		return out
	}

	if out.Title != nil && *out.Title == man.Display.Name {
		out.Title = nil
	}

	sort.SliceStable(out.Config, func(i, j int) bool { return out.Config[i].Key < out.Config[j].Key })
	out.Ports = canonicalPorts(out.Ports, man)
	for i := range out.Ports {
		p := &out.Ports[i]
		if p.ConfigKey == "" || p.Type == "" {
			continue
		}
		prop, declared := man.ConfigProperty(p.ConfigKey)
		if !declared {
			continue
		}
		schema, defs, srcPin, ok := typeExprSchema(idx, m, p.Type, pin, man.Defs)
		if !ok {
			continue
		}
		res := types.Assignable(schema, prop, types.Context{
			SourceDefs: defs, TargetDefs: man.Defs, SameManifest: srcPin == pin,
		})
		if res.Verdict == types.VerdictExact {
			p.Type = ""
		}
	}

	kept := make([]Config, 0, len(out.Config))
	var pending []string
	for _, c := range out.Config {
		if !equalsDefault(man, c) {
			if len(pending) > 0 {
				prependConfigLeading(out.Trivia, c.Key, pending)
				pending = nil
			}
			kept = append(kept, c)
			continue
		}
		pending = append(pending, triviaLines(nodeConfigTrivia(out.Trivia, c.Key))...)
		dropConfigTrivia(out.Trivia, c.Key)
	}
	out.Config = kept
	if len(pending) > 0 {
		if len(out.Ports) > 0 {
			p := out.Ports[0]
			prependPortLeading(out.Trivia, PortTriviaKey(p.Dir, p.ID), pending)
		} else {
			if out.Trivia == nil {
				out.Trivia = &NodeTrivia{}
			}
			out.Trivia.BodyTail = append(append([]string(nil), pending...), out.Trivia.BodyTail...)
		}
	}
	return out
}

// canonicalPorts is §3.3's port order: label-only schema overrides (manifest
// inputs in manifest order, then manifest outputs in manifest order), then
// promotions in declaration order, then free inputs in declaration order.
func canonicalPorts(ports []Port, man *nodemanifest.Manifest) []Port {
	used := make([]bool, len(ports))
	out := make([]Port, 0, len(ports))

	take := func(match func(Port) bool) {
		for i, p := range ports {
			if used[i] || !match(p) {
				continue
			}
			used[i] = true
			out = append(out, p)
		}
	}
	for _, in := range man.Inputs {
		name := in.Name
		take(func(p Port) bool { return p.Dir == "in" && p.ConfigKey == "" && p.ID == name })
	}
	for _, o := range man.Outputs {
		name := o.Name
		take(func(p Port) bool { return p.Dir == "out" && p.ID == name })
	}
	take(func(p Port) bool { return p.Dir == "in" && p.ConfigKey != "" })
	for i, p := range ports {
		if !used[i] {
			used[i] = true
			out = append(out, p)
		}
	}
	return out
}

// equalsDefault reports a config line that restates the pinned default. §3.3:
// the file records DEPARTURE from the contract, so a line that agrees with it
// carries no decision and is not written.
func equalsDefault(man *nodemanifest.Manifest, c Config) bool {
	raw, has := man.ConfigDefault(c.Key)
	if !has {
		return false
	}
	var decoded any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return false
	}
	a, err := nodemanifest.CanonicalJSON(decoded)
	if err != nil {
		return false
	}
	b, err := nodemanifest.CanonicalJSON(c.Value)
	if err != nil {
		return false
	}
	return string(a) == string(b)
}

// triviaLines flattens one construct's comments into the lines a re-anchor
// carries: the leading group, then the trailing text as one more line. A
// comment whose construct is removed MOVES; it never disappears (D-369).
func triviaLines(t *Trivia) []string {
	if t == nil {
		return nil
	}
	out := append([]string(nil), t.Leading...)
	if t.Trailing != nil {
		out = append(out, *t.Trailing)
	}
	return out
}

func prependConfigLeading(nt *NodeTrivia, key string, lines []string) {
	if nt == nil {
		return
	}
	if nt.Config == nil {
		nt.Config = map[string]*Trivia{}
	}
	t := nt.Config[key]
	if t == nil {
		t = &Trivia{}
		nt.Config[key] = t
	}
	t.Leading = append(append([]string(nil), lines...), t.Leading...)
}

func prependPortLeading(nt *NodeTrivia, key string, lines []string) {
	if nt == nil {
		return
	}
	if nt.Ports == nil {
		nt.Ports = map[string]*Trivia{}
	}
	t := nt.Ports[key]
	if t == nil {
		t = &Trivia{}
		nt.Ports[key] = t
	}
	t.Leading = append(append([]string(nil), lines...), t.Leading...)
}

func dropConfigTrivia(nt *NodeTrivia, key string) {
	if nt == nil {
		return
	}
	delete(nt.Config, key)
}

func cloneNodeTrivia(t *NodeTrivia) *NodeTrivia {
	if t == nil {
		return nil
	}
	out := *t
	out.Leading = append([]string(nil), t.Leading...)
	out.BodyTail = append([]string(nil), t.BodyTail...)
	out.Config = cloneTriviaMap(t.Config)
	out.Ports = cloneTriviaMap(t.Ports)
	out.Layout = cloneTrivia(t.Layout)
	return &out
}

func cloneTriviaMap(in map[string]*Trivia) map[string]*Trivia {
	if in == nil {
		return nil
	}
	out := make(map[string]*Trivia, len(in))
	for k, v := range in {
		out[k] = cloneTrivia(v)
	}
	return out
}

func cloneTrivia(t *Trivia) *Trivia {
	if t == nil {
		return nil
	}
	out := *t
	out.Leading = append([]string(nil), t.Leading...)
	return &out
}
