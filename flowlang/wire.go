package flowlang

// WirePlan is the ONE wire projection of a lowered plan. Field declaration
// order IS the JSON key order (encoding/json emits fields in declaration
// order), and it is key-sorted deliberately: the golden document is compared
// byte-for-byte on three sides, so the encoder must produce the canonical form
// natively rather than depend on anyone re-sorting it.
type WirePlan struct {
	Edges    []WireEdge `json:"edges"`
	Nodes    []WireNode `json:"nodes"`
	Order    []string   `json:"order"`
	Responds []string   `json:"responds"`
	Trigger  string     `json:"trigger"`
}

// WireNode is one node as it travels. Nils are normalized away in ToWire, in
// one place, so no decoder needs a nil branch.
type WireNode struct {
	Alias    string            `json:"alias"`
	Outputs  []string          `json:"outputs"`
	Pin      string            `json:"pin"`
	Promoted map[string]string `json:"promoted"`
	Required []string          `json:"required"`
	Role     string            `json:"role"`
	Slug     string            `json:"slug"`
	Upstream []string          `json:"upstream"`
}

// WireEdge is one lowered wire.
type WireEdge struct {
	From     string `json:"from"`
	FromPort string `json:"from_port"`
	To       string `json:"to"`
	ToPort   string `json:"to_port"`
}

// ToWire projects a lowered plan. Nodes travel in Sequence order, which Lower
// has already made topological, because that order is the Kahn tie-break: it is
// what lets the receiver recompute the SAME execution order instead of trusting
// a transmitted one.
func ToWire(p *Plan) WirePlan {
	w := WirePlan{Edges: []WireEdge{}, Nodes: []WireNode{}, Order: []string{}, Responds: []string{}}
	if p == nil {
		return w
	}
	for _, e := range p.Edges {
		w.Edges = append(w.Edges, WireEdge{From: e.From, FromPort: e.FromPort, To: e.To, ToPort: e.ToPort})
	}
	for _, slug := range p.Sequence {
		n, ok := p.Nodes[slug]
		if !ok {
			continue
		}
		w.Nodes = append(w.Nodes, WireNode{
			Alias:    n.Alias,
			Outputs:  orEmpty(n.Outputs),
			Pin:      n.Pin,
			Promoted: orEmptyMap(n.Promoted),
			Required: orEmpty(n.Required),
			Role:     n.Role,
			Slug:     n.Slug,
			Upstream: orEmpty(n.Upstream),
		})
	}
	w.Order = orEmpty(p.Order)
	w.Responds = orEmpty(p.Responds)
	w.Trigger = p.Trigger
	return w
}

func orEmpty(in []string) []string {
	if in == nil {
		return []string{}
	}
	return in
}

func orEmptyMap(in map[string]string) map[string]string {
	if in == nil {
		return map[string]string{}
	}
	return in
}

// PlanFromWire rebuilds a plan from what arrived, and refuses rather than
// repairs. The carried node order is the Kahn tie-break, so the order this
// recomputes must equal the order it was handed: if it does not, the sender
// and the receiver would run the same flow in two different sequences.
func PlanFromWire(nodes []WireNode, edges []Edge) (*Plan, error) {
	plan := &Plan{Nodes: map[string]PlanNode{}}
	for _, n := range nodes {
		if _, dup := plan.Nodes[n.Slug]; dup {
			return nil, ErrPlanDuplicateNode
		}
		pn := PlanNode{
			Slug:     n.Slug,
			Alias:    n.Alias,
			Pin:      n.Pin,
			Role:     n.Role,
			Required: n.Required,
			Outputs:  n.Outputs,
		}
		if len(n.Promoted) > 0 {
			pn.Promoted = map[string]string{}
			for k, v := range n.Promoted {
				pn.Promoted[k] = v
			}
		}
		if pn.Role == "trigger" && plan.Trigger == "" {
			plan.Trigger = n.Slug
		}
		if pn.Role == "respond" {
			plan.Responds = append(plan.Responds, n.Slug)
			if !hasOutput(n.Outputs, "response") {
				return nil, ErrPlanRespondNoOutput
			}
		}
		plan.Nodes[n.Slug] = pn
		plan.Sequence = append(plan.Sequence, n.Slug)
	}

	upstreamSeen := map[string]bool{}
	for _, e := range edges {
		if _, ok := plan.Nodes[e.From]; !ok {
			return nil, ErrPlanUnknownNode
		}
		to, ok := plan.Nodes[e.To]
		if !ok {
			return nil, ErrPlanUnknownNode
		}
		if to.Role == "trigger" {
			return nil, ErrPlanTriggerWired
		}
		plan.Edges = append(plan.Edges, e)
		if !upstreamSeen[e.To+"<-"+e.From] {
			upstreamSeen[e.To+"<-"+e.From] = true
			to.Upstream = append(to.Upstream, e.From)
			plan.Nodes[e.To] = to
		}
	}

	for _, n := range nodes {
		for _, req := range n.Required {
			if !wired(plan.Edges, n.Slug, req) {
				return nil, ErrPlanRequiredUnwired
			}
		}
	}

	plan.Order = topoOrder(plan.Sequence, plan)
	if !sameOrder(plan.Order, plan.Sequence) {
		return nil, ErrPlanNotTopological
	}
	return plan, nil
}

func hasOutput(outputs []string, want string) bool {
	for _, o := range outputs {
		if o == want {
			return true
		}
	}
	return false
}

func wired(edges []Edge, slug, port string) bool {
	for _, e := range edges {
		if e.To == slug && e.ToPort == port {
			return true
		}
	}
	return false
}

func sameOrder(a, b []string) bool {
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
