package flowlang

import (
	"errors"
	"strings"
)

// PromotedPortPrefix marks a lowered wire that delivers into a node's CONFIG
// rather than into one of its manifest inputs. Lowering rewrites the target
// exactly once, here, so no consumer downstream has to carry the document's
// promotion table to know where a value lands.
const PromotedPortPrefix = "config."

// PromotedKey reports the config key a lowered target port delivers into.
func PromotedKey(toPort string) (key string, ok bool) {
	key, ok = strings.CutPrefix(toPort, PromotedPortPrefix)
	if !ok || key == "" {
		return "", false
	}
	return key, true
}

// The refusals PlanFromWire raises. A plan arriving over a wire has already
// been validated once by whoever compiled it, so every one of these means the
// two sides disagree about the shape of the SAME flow — which is exactly the
// condition that must stop a run rather than be repaired inside it.
var (
	ErrPlanUnknownNode     = errors.New("flowlang: edge names an unknown node")
	ErrPlanDuplicateNode   = errors.New("flowlang: duplicate node slug")
	ErrPlanNotTopological  = errors.New("flowlang: nodes are not in a topological order")
	ErrPlanTriggerWired    = errors.New("flowlang: trigger node has an incoming edge")
	ErrPlanRequiredUnwired = errors.New("flowlang: required input has no incoming edge")
	ErrPlanRespondNoOutput = errors.New("flowlang: respond node declares no response output")
)

// Lower turns a scheduled plan into the shape that travels: disabled nodes and
// every edge incident to them are gone, and a wire into a promoted port names
// the config key it fills. A disabled node is dropped HERE, once, rather than
// skipped by each consumer in turn — a node that cannot run must not be
// observable at all, because "present but never runs" is the state in which the
// runtime and the code generator drift apart.
func Lower(p *Plan) *Plan {
	if p == nil {
		return nil
	}
	out := &Plan{Nodes: map[string]PlanNode{}}

	live := map[string]bool{}
	for _, slug := range p.Sequence {
		if n, ok := p.Nodes[slug]; ok && !n.Disabled {
			live[slug] = true
		}
	}
	for _, slug := range p.Sequence {
		if !live[slug] {
			continue
		}
		n := p.Nodes[slug]
		n.Upstream = nil
		out.Nodes[slug] = n
		out.Sequence = append(out.Sequence, slug)
		if n.Role == "trigger" && out.Trigger == "" {
			out.Trigger = slug
		}
		if n.Role == "respond" {
			out.Responds = append(out.Responds, slug)
		}
	}

	upstreamSeen := map[string]bool{}
	for _, e := range p.Edges {
		if !live[e.From] || !live[e.To] {
			continue
		}
		if key, ok := out.Nodes[e.To].Promoted[e.ToPort]; ok {
			e.ToPort = PromotedPortPrefix + key
		}
		out.Edges = append(out.Edges, e)
		if !upstreamSeen[e.To+"<-"+e.From] {
			upstreamSeen[e.To+"<-"+e.From] = true
			n := out.Nodes[e.To]
			n.Upstream = append(n.Upstream, e.From)
			out.Nodes[e.To] = n
		}
	}

	out.Order = topoOrder(out.Sequence, out)
	// A lowered plan exists only to cross the wire, and the order it carries is
	// the Kahn tie-break the receiver reproduces — which is meaningful only over
	// an order that is already topological. Document order is the scheduled
	// plan's business; past this point it would just be an order with holes.
	out.Sequence = append([]string(nil), out.Order...)
	return out
}
