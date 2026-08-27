package flowlang

// Status is one node's state in a run.
type Status string

// The five node states.
const (
	StatusPending Status = "pending"
	StatusRunning Status = "running"
	StatusDone    Status = "done"
	StatusSkipped Status = "skipped"
	StatusFailed  Status = "failed"
)

// ResultCase is how a flow answered its caller.
type ResultCase string

// The four result cases.
const (
	ResultFireAndForget     ResultCase = "fire_and_forget"
	ResultSingleResponse    ResultCase = "single_response"
	ResultMultipleResponses ResultCase = "multiple_responses"
	ResultNoResponse        ResultCase = "no_response"
)

// PlanNode is one node's execution facts, resolved against its manifest.
type PlanNode struct {
	Slug     string
	Alias    string
	Pin      string
	Role     string
	Disabled bool
	// Required is the manifest inputs with `required: true`.
	Required []string
	// Promoted maps a promoted port id to the config key it exposes.
	Promoted map[string]string
	// Upstream is the slugs feeding this node, in document order, deduplicated.
	Upstream []string
}

// Edge is one resolved wire.
type Edge struct {
	From     string
	FromPort string
	To       string
	ToPort   string
}

// Plan is the execution shape of a valid document. It is the ONE place the
// order and the run/skip rule live: the runtime and the code generator both
// read it, so a flow cannot execute one way and compile another.
type Plan struct {
	// Order is a Kahn topological order; among ready candidates the earliest in
	// document order is taken first, so the order is a pure function of the file.
	Order    []string
	Nodes    map[string]PlanNode
	Edges    []Edge
	Trigger  string
	Responds []string
}

// Schedule validates a document and, when it holds, returns its execution plan.
// An error-severity diagnostic yields no plan: a flow that cannot be read
// correctly must not be run approximately.
func Schedule(doc *Doc, m Manifests) (*Plan, []Diagnostic) {
	diags := Validate(doc, m)
	for _, d := range diags {
		if d.Severity == SeverityError {
			return nil, diags
		}
	}

	idx := newIndex(doc)
	plan := &Plan{Nodes: map[string]PlanNode{}}

	slugs := make([]string, 0, len(doc.Nodes))
	for i := range doc.Nodes {
		n := &doc.Nodes[i]
		man, pin := idx.manifestOf(n, m)
		pn := PlanNode{Slug: n.Slug, Alias: n.Alias, Pin: pin, Disabled: n.Disabled}
		if man != nil {
			if man.Role != nil {
				pn.Role = *man.Role
			}
			for _, in := range man.Inputs {
				if in.Required {
					pn.Required = append(pn.Required, in.Name)
				}
			}
		}
		for _, p := range n.Ports {
			if p.Dir == "in" && p.ConfigKey != "" {
				if pn.Promoted == nil {
					pn.Promoted = map[string]string{}
				}
				pn.Promoted[p.ID] = p.ConfigKey
			}
		}
		plan.Nodes[n.Slug] = pn
		slugs = append(slugs, n.Slug)
		if pn.Role == "trigger" && plan.Trigger == "" {
			plan.Trigger = n.Slug
		}
		if pn.Role == "respond" {
			plan.Responds = append(plan.Responds, n.Slug)
		}
	}

	seenEdge := map[string]bool{}
	upstream := map[string][]string{}
	upstreamSeen := map[string]bool{}
	for _, w := range doc.Wires {
		key := WireAnchorKey(w)
		if seenEdge[key] {
			continue
		}
		seenEdge[key] = true
		plan.Edges = append(plan.Edges, Edge{From: w.From, FromPort: w.FromPort, To: w.To, ToPort: w.ToPort})
		if !upstreamSeen[w.To+"<-"+w.From] {
			upstreamSeen[w.To+"<-"+w.From] = true
			upstream[w.To] = append(upstream[w.To], w.From)
		}
	}
	for slug, ups := range upstream {
		pn := plan.Nodes[slug]
		pn.Upstream = ups
		plan.Nodes[slug] = pn
	}

	plan.Order = topoOrder(slugs, plan)
	return plan, diags
}

// topoOrder walks the graph in Kahn order, breaking every tie by document
// position. Determinism here is not a nicety: the generated code's file order
// is derived from it.
func topoOrder(slugs []string, plan *Plan) []string {
	indegree := map[string]int{}
	for _, s := range slugs {
		indegree[s] = len(plan.Nodes[s].Upstream)
	}
	downstream := map[string][]string{}
	for _, s := range slugs {
		for _, up := range plan.Nodes[s].Upstream {
			downstream[up] = append(downstream[up], s)
		}
	}
	done := map[string]bool{}
	order := make([]string, 0, len(slugs))
	for len(order) < len(slugs) {
		picked := ""
		for _, s := range slugs {
			if !done[s] && indegree[s] == 0 {
				picked = s
				break
			}
		}
		if picked == "" {
			break // unreachable for a validated document: a cycle is an error
		}
		done[picked] = true
		order = append(order, picked)
		for _, next := range downstream[picked] {
			indegree[next]--
		}
	}
	return order
}

// Ready reports that every upstream node has settled — done, skipped or failed.
func (p *Plan) Ready(slug string, status map[string]Status) bool {
	for _, up := range p.Nodes[slug].Upstream {
		switch status[up] {
		case StatusDone, StatusSkipped, StatusFailed:
		default:
			return false
		}
	}
	return true
}

// Runnable reports that a ready node actually has the values it needs. A node
// with no required input runs as soon as it is ready — an edge is active iff
// its source port FIRED, and a node that asks for nothing is waiting for
// nothing (DESIGN §3.4).
func (p *Plan) Runnable(slug string, status map[string]Status, fired map[string]map[string]bool) bool {
	n := p.Nodes[slug]
	if !p.Ready(slug, status) || n.Disabled {
		return false
	}
	if n.Role == "trigger" {
		return true
	}
	for _, req := range n.Required {
		satisfied := false
		for _, e := range p.Edges {
			if e.To == slug && e.ToPort == req && fired[e.From][e.FromPort] {
				satisfied = true
				break
			}
		}
		if !satisfied {
			return false
		}
	}
	return true
}

// Next is the ONE wave rule the runtime and the code generator both call: every
// unsettled node that is ready either runs or is skipped, and nothing else
// decides.
func (p *Plan) Next(status map[string]Status, fired map[string]map[string]bool) (run, skip []string) {
	for _, slug := range p.Order {
		switch status[slug] {
		case "", StatusPending:
		default:
			continue
		}
		if !p.Ready(slug, status) {
			continue
		}
		if p.Runnable(slug, status, fired) {
			run = append(run, slug)
			continue
		}
		skip = append(skip, slug)
	}
	return run, skip
}

// Result reports how the flow answered: no respond node at all is a
// fire-and-forget flow (202), a respond node whose `response` output never
// fired is a flow that ran and said nothing, and two are an ambiguity the
// caller has to see rather than a race the platform resolves silently.
func (p *Plan) Result(fired map[string]map[string]bool) (ResultCase, string) {
	if len(p.Responds) == 0 {
		return ResultFireAndForget, ""
	}
	var answered []string
	for _, slug := range p.Responds {
		if fired[slug]["response"] {
			answered = append(answered, slug)
		}
	}
	switch len(answered) {
	case 0:
		return ResultNoResponse, ""
	case 1:
		return ResultSingleResponse, answered[0]
	default:
		return ResultMultipleResponses, ""
	}
}
