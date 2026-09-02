package flowlang

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/sentiae/platform-kit/nodemanifest"
)

// lowerCoverageFlow carries the three features no corpus fixture that SCHEDULES
// has: a disabled node, a promoted port, and a declaration order that is not the
// execution order. It is written here rather than added to the golden corpus
// because only a control needs it — the corpus and its four mirrors stay
// byte-identical (R-8).
//
// Its shape is load-bearing and asserted before use: `dormant` is disabled and
// must vanish under Lower; `worker` survives with a promotion, so the promoted
// round trip has something to carry; and the nodes are declared
// reply, dormant, worker, intake — the reverse of the order they run in.
func lowerCoverageFlow() string {
	return flowText(
		`flow "Lower controls" v2`,
		``,
		`use respond = @sentiae/respond@1.0.0`,
		`use secure_http = @acme/secure-http@2.1.0`,
		`use webhook_trigger = @sentiae/webhook-trigger@1.0.0`,
		``,
		`node reply: respond {`,
		`}`,
		``,
		`node dormant: secure_http disabled {`,
		"\turl = \"https://example.test/dormant\"",
		"\tport in headers = config.headers",
		`}`,
		``,
		`node worker: secure_http {`,
		"\turl = \"https://example.test/worker\"",
		"\tport in headers = config.headers",
		`}`,
		``,
		`node intake: webhook_trigger {`,
		`}`,
		``,
		`wire worker.result -> reply.body`,
		`wire intake.headers -> dormant.headers`,
		`wire intake.headers -> dormant.payload`,
		`wire intake.headers -> worker.headers`,
		`wire intake.headers -> worker.payload`,
	)
}

// coveragePlan schedules lowerCoverageFlow and asserts the preconditions every
// control below depends on. A control built on a document that stopped having a
// disabled node — or stopped scheduling at all — would pass while proving
// nothing, so the shape is checked here rather than assumed.
func coveragePlan(t *testing.T, m Manifests) *Plan {
	t.Helper()
	doc, diags := Parse(lowerCoverageFlow())
	if doc == nil || len(diags) != 0 {
		t.Fatalf("coverage flow does not parse: doc=%v diags=%+v", doc != nil, diags)
	}
	for _, d := range Validate(doc, m) {
		if d.Severity == SeverityError {
			t.Fatalf("coverage flow has error-severity diagnostics: %+v", Validate(doc, m))
		}
	}
	p := mustPlan(t, lowerCoverageFlow(), m)
	if !p.Nodes["dormant"].Disabled {
		t.Fatal("precondition: the coverage flow must carry a disabled node")
	}
	if len(p.Nodes["worker"].Promoted) == 0 {
		t.Fatal("precondition: the coverage flow's surviving node must carry a promotion")
	}
	if reflect.DeepEqual(p.Sequence, p.Order) {
		t.Fatalf("precondition: the coverage flow's document order must differ from its execution order (both %v)", p.Order)
	}
	return p
}

// schedulableCorpus is the corpus fixtures that produce a plan. The others carry
// deliberate error-severity diagnostics, so Schedule returns nothing to lower.
func schedulableCorpus(t *testing.T, m Manifests) map[string]*Plan {
	t.Helper()
	out := map[string]*Plan{}
	for _, path := range corpusFlows(t) {
		doc, diags := Parse(readFixture(t, path))
		if doc == nil || len(diags) != 0 {
			t.Fatalf("%s: Parse: doc=%v diags=%+v", path, doc != nil, diags)
		}
		if p, _ := Schedule(doc, m); p != nil {
			out[filepath.Base(path)] = p
		}
	}
	// Positive anchor: an empty set would make every loop below vacuously green.
	for _, want := range []string{"05_multi_respond.flow", "08_validated_intake.flow", "09_phase4_acceptance.flow"} {
		if _, ok := out[want]; !ok {
			t.Fatalf("anchor: %s must schedule", want)
		}
	}
	return out
}

func wireEdges(w WirePlan) []Edge {
	out := make([]Edge, 0, len(w.Edges))
	for _, e := range w.Edges {
		out = append(out, Edge{From: e.From, FromPort: e.FromPort, To: e.To, ToPort: e.ToPort})
	}
	return out
}

// T0.1 — TestLower_WireRoundTrip proves the projection is lossless for
// everything an executor reads: a plan that goes out over the wire and comes
// back must schedule identically, or the compiler and the runtime are running
// two different flows.
//
// It also pins what makes the round trip possible at all: a lowered plan's
// Sequence IS its Order. Document order is the scheduled plan's business, and a
// document may legally declare its nodes out of dependency order — the coverage
// flow does — so a lowered plan that carried document order would be refused by
// its own reader.
//
// CONTROL: skip rebuilding Promoted in PlanFromWire (drop the copy loop) and the
// coverage row goes red — it is the only plan here carrying a promotion.
// CONTROL: make Lower filter p.Sequence instead of assigning Order, and the
// coverage row is refused with "nodes are not in a topological order".
func TestLower_WireRoundTrip(t *testing.T) {
	m := corpusManifests(t)
	plans := schedulableCorpus(t, m)
	plans["<coverage>"] = coveragePlan(t, m)

	for name, p := range plans {
		t.Run(name, func(t *testing.T) {
			lowered := Lower(p)
			if !reflect.DeepEqual(orEmpty(lowered.Sequence), orEmpty(lowered.Order)) {
				t.Fatalf("a lowered plan must carry a topological order: Sequence %v, Order %v",
					lowered.Sequence, lowered.Order)
			}
			w := ToWire(lowered)
			rp, err := PlanFromWire(w.Nodes, wireEdges(w))
			if err != nil {
				t.Fatalf("PlanFromWire refused a plan this package produced: %v (carried %v)", err, w.Order)
			}
			if !reflect.DeepEqual(rp.Order, lowered.Order) {
				t.Errorf("Order: got %v, want %v", rp.Order, lowered.Order)
			}
			if !reflect.DeepEqual(rp.Edges, lowered.Edges) {
				t.Errorf("Edges: got %+v, want %+v", rp.Edges, lowered.Edges)
			}
			if rp.Trigger != lowered.Trigger {
				t.Errorf("Trigger: got %q, want %q", rp.Trigger, lowered.Trigger)
			}
			if !reflect.DeepEqual(orEmpty(rp.Responds), orEmpty(lowered.Responds)) {
				t.Errorf("Responds: got %v, want %v", rp.Responds, lowered.Responds)
			}
			if len(rp.Nodes) != len(lowered.Nodes) {
				t.Fatalf("node count: got %d, want %d", len(rp.Nodes), len(lowered.Nodes))
			}
			for slug, want := range lowered.Nodes {
				got := rp.Nodes[slug]
				if !reflect.DeepEqual(orEmpty(got.Required), orEmpty(want.Required)) {
					t.Errorf("%s Required: got %v, want %v", slug, got.Required, want.Required)
				}
				if got.Role != want.Role {
					t.Errorf("%s Role: got %q, want %q", slug, got.Role, want.Role)
				}
				if !reflect.DeepEqual(orEmpty(got.Upstream), orEmpty(want.Upstream)) {
					t.Errorf("%s Upstream: got %v, want %v", slug, got.Upstream, want.Upstream)
				}
				if !reflect.DeepEqual(orEmptyMap(got.Promoted), orEmptyMap(want.Promoted)) {
					t.Errorf("%s Promoted: got %v, want %v", slug, got.Promoted, want.Promoted)
				}
			}
		})
	}
}

// allFire runs the plan's own wave rule with every port of every run node
// firing, and returns the trace of nodes that ran plus the flow's result case.
// It is the oracle for T0.2: it uses ONLY Next/Result, so it observes the plan
// exactly the way the runtime and the code generator do.
func allFire(p *Plan) ([]string, ResultCase) {
	status := map[string]Status{}
	fired := map[string]map[string]bool{}
	var trace []string
	for {
		run, skip := p.Next(status, fired)
		if len(run) == 0 && len(skip) == 0 {
			break
		}
		for _, slug := range run {
			trace = append(trace, slug)
			status[slug] = StatusDone
			ports := map[string]bool{}
			for _, o := range p.Nodes[slug].Outputs {
				ports[o] = true
			}
			for _, e := range p.Edges {
				if e.From == slug {
					ports[e.FromPort] = true
				}
			}
			fired[slug] = ports
		}
		for _, slug := range skip {
			status[slug] = StatusSkipped
		}
	}
	c, _ := p.Result(fired)
	return trace, c
}

// T0.2 — TestLower_DisabledIsUnobservable proves a disabled node is not merely
// skipped but GONE: the lowered plan runs the same nodes, in the same order,
// and answers the same way. "Present but never runs" is exactly the state in
// which two executors drift, because each has to remember to skip it.
//
// CONTROL: keep the disabled node in Lower (drop the `!n.Disabled` filter) and
// the anchor below fails — dormant survives and the node counts no longer differ.
func TestLower_DisabledIsUnobservable(t *testing.T) {
	m := corpusManifests(t)
	p := coveragePlan(t, m)
	lowered := Lower(p)

	// Anchor: lowering this plan must actually remove something.
	if len(lowered.Nodes) != len(p.Nodes)-1 {
		t.Fatalf("anchor: Lower must drop the disabled node: %d nodes before, %d after", len(p.Nodes), len(lowered.Nodes))
	}
	if _, ok := lowered.Nodes["dormant"]; ok {
		t.Fatal("anchor: the disabled node survived Lower")
	}
	for _, e := range lowered.Edges {
		if e.From == "dormant" || e.To == "dormant" {
			t.Errorf("an edge incident to the disabled node survived: %+v", e)
		}
	}

	beforeTrace, beforeCase := allFire(p)
	afterTrace, afterCase := allFire(lowered)
	var wantTrace []string
	for _, slug := range beforeTrace {
		if slug != "dormant" {
			wantTrace = append(wantTrace, slug)
		}
	}
	if !reflect.DeepEqual(afterTrace, wantTrace) {
		t.Errorf("run trace: got %v, want %v (the original minus the disabled node)", afterTrace, wantTrace)
	}
	if afterCase != beforeCase {
		t.Errorf("result case: got %q, want %q", afterCase, beforeCase)
	}
}

// T0.3 — TestPlanFromWire_Refusals pins one row per sentinel. A plan arriving
// over the wire was validated by whoever compiled it, so each of these means the
// two sides disagree about the shape of the same flow — which must stop a run
// rather than be repaired inside it.
//
// CONTROL: drop the `Order == Sequence` comparison at the end of PlanFromWire
// and the "nodes out of topological order" row passes — red.
func TestPlanFromWire_Refusals(t *testing.T) {
	node := func(slug, role string, required, outputs []string) WireNode {
		return WireNode{Slug: slug, Role: role, Required: required, Outputs: outputs}
	}
	tests := []struct {
		name  string
		nodes []WireNode
		edges []Edge
		want  error
	}{
		{
			name:  "positive anchor: a well-formed plan is accepted",
			nodes: []WireNode{node("a", "trigger", nil, []string{"out"}), node("b", "respond", []string{"body"}, []string{"response"})},
			edges: []Edge{{From: "a", FromPort: "out", To: "b", ToPort: "body"}},
		},
		{
			name:  "an edge names an unknown node",
			nodes: []WireNode{node("a", "trigger", nil, []string{"out"})},
			edges: []Edge{{From: "a", FromPort: "out", To: "ghost", ToPort: "body"}},
			want:  ErrPlanUnknownNode,
		},
		{
			name:  "a slug appears twice",
			nodes: []WireNode{node("a", "trigger", nil, []string{"out"}), node("a", "", nil, nil)},
			want:  ErrPlanDuplicateNode,
		},
		{
			name:  "nodes out of topological order",
			nodes: []WireNode{node("b", "", nil, []string{"out"}), node("a", "", nil, []string{"out"})},
			edges: []Edge{{From: "a", FromPort: "out", To: "b", ToPort: "in"}},
			want:  ErrPlanNotTopological,
		},
		{
			name:  "an edge points at the trigger",
			nodes: []WireNode{node("a", "", nil, []string{"out"}), node("t", "trigger", nil, []string{"out"})},
			edges: []Edge{{From: "a", FromPort: "out", To: "t", ToPort: "in"}},
			want:  ErrPlanTriggerWired,
		},
		{
			name:  "a required input has no incoming edge",
			nodes: []WireNode{node("a", "trigger", nil, []string{"out"}), node("b", "", []string{"body"}, []string{"out"})},
			edges: []Edge{{From: "a", FromPort: "out", To: "b", ToPort: "other"}},
			want:  ErrPlanRequiredUnwired,
		},
		{
			name:  "a respond node declares no response output",
			nodes: []WireNode{node("a", "trigger", nil, []string{"out"}), node("b", "respond", []string{"body"}, []string{"something_else"})},
			edges: []Edge{{From: "a", FromPort: "out", To: "b", ToPort: "body"}},
			want:  ErrPlanRespondNoOutput,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, err := PlanFromWire(tt.nodes, tt.edges)
			if tt.want == nil {
				if err != nil {
					t.Fatalf("got %v, want a plan", err)
				}
				if p == nil {
					t.Fatal("got no plan and no error")
				}
				return
			}
			if err != tt.want {
				t.Fatalf("got %v, want %v", err, tt.want)
			}
			if p != nil {
				t.Error("a refused plan must be nil, never partially built")
			}
		})
	}
}

// T0.4 — TestPromotedKey pins the one place a lowered target port is read back
// as a config key. A prefix nobody can spell as a real port name is what keeps
// the two namespaces from colliding: portNameRx forbids a dot.
func TestPromotedKey(t *testing.T) {
	tests := []struct {
		in      string
		wantKey string
		wantOK  bool
	}{
		{"config.method", "method", true},
		{"config.a", "a", true},
		{"config.config.method", "config.method", true},
		{"config.", "", false},
		{"config", "", false},
		{"method", "", false},
		{"", "", false},
		{"Config.method", "", false},
		{"xconfig.method", "", false},
		{" config.method", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			key, ok := PromotedKey(tt.in)
			if ok != tt.wantOK || key != tt.wantKey {
				t.Fatalf("PromotedKey(%q) = (%q, %v), want (%q, %v)", tt.in, key, ok, tt.wantKey, tt.wantOK)
			}
		})
	}
	if PromotedPortPrefix != "config." {
		t.Fatalf("PromotedPortPrefix = %q, want %q", PromotedPortPrefix, "config.")
	}
}

// T0.5 — TestSchedule_SequenceIsDocumentOrder proves Sequence carries the
// DOCUMENT's order and not the execution order. The distinction is invisible on
// every corpus fixture — in each of them the two are identical — so it is proved
// on a document whose nodes are declared in the reverse of the order they run.
//
// CONTROL: assign `plan.Sequence = plan.Order` at the end of Schedule and the
// exact Sequence assertion below goes red.
func TestSchedule_SequenceIsDocumentOrder(t *testing.T) {
	p := coveragePlan(t, corpusManifests(t))

	wantSequence := []string{"reply", "dormant", "worker", "intake"}
	wantOrder := []string{"intake", "dormant", "worker", "reply"}
	if !reflect.DeepEqual(p.Sequence, wantSequence) {
		t.Errorf("Sequence: got %v, want %v (the order the nodes are DECLARED in)", p.Sequence, wantSequence)
	}
	if !reflect.DeepEqual(p.Order, wantOrder) {
		t.Errorf("Order: got %v, want %v (the order they RUN in)", p.Order, wantOrder)
	}
	if reflect.DeepEqual(p.Sequence, p.Order) {
		t.Fatal("Sequence and Order are identical here, so this test cannot tell them apart")
	}
}

// T0.7 — TestWireTypeUnknown_ClearsWhenSourceConstrained proves the corpus `10`
// tripwire measures the SOURCE's constraint and nothing else. Asserting the
// diagnostic is present would pass on a validator that emitted it for any wire;
// the swap is the control: constrain the trigger's `body` and the finding must
// disappear, restore it and the finding must come back.
func TestWireTypeUnknown_ClearsWhenSourceConstrained(t *testing.T) {
	m := corpusManifests(t)
	text := readFixture(t, "testdata/10_port_narrowing_rejection.flow")
	doc, diags := Parse(text)
	if doc == nil || len(diags) != 0 {
		t.Fatalf("Parse: doc=%v diags=%+v", doc != nil, diags)
	}

	errorsOf := func(m Manifests) []Diagnostic {
		var out []Diagnostic
		for _, d := range Validate(doc, m) {
			if d.Severity == SeverityError {
				out = append(out, d)
			}
		}
		return out
	}

	before := errorsOf(m)
	if len(before) != 1 || before[0].Code != CodeWireTypeUnknown || before[0].Line != 12 {
		t.Fatalf("as committed: got %+v, want exactly one wire_type_unknown at line 12", before)
	}

	// The swap: give the trigger's `body` output a constrained schema. Nothing
	// else about the document changes, so the finding can only be about the
	// source's constraint.
	trigger := m["@sentiae/webhook-trigger@1.0.0"]
	var restore *nodemanifest.TypeRef
	swapped := false
	for i := range trigger.Outputs {
		if trigger.Outputs[i].Name == "body" {
			restore = trigger.Outputs[i].Schema
			trigger.Outputs[i].Schema = &nodemanifest.TypeRef{Type: "string"}
			swapped = true
		}
	}
	if !swapped {
		t.Fatal("the trigger manifest has no body output to swap")
	}
	if after := errorsOf(m); len(after) != 0 {
		t.Fatalf("with a constrained source: got %+v, want no error-severity findings", after)
	}

	for i := range trigger.Outputs {
		if trigger.Outputs[i].Name == "body" {
			trigger.Outputs[i].Schema = restore
		}
	}
	if again := errorsOf(m); len(again) != 1 || again[0].Code != CodeWireTypeUnknown || again[0].Line != 12 {
		t.Fatalf("restored: got %+v, want the wire_type_unknown at line 12 back", again)
	}
}

// TestCorpus10_SidecarMatchesValidate proves the committed sidecar for the
// tripwire is what Validate actually produces, so the fixture cannot rot into
// agreeing with a stale expectation.
func TestCorpus10_SidecarMatchesValidate(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("testdata", "10_port_narrowing_rejection.diag.json"))
	if err != nil {
		t.Fatalf("read sidecar: %v", err)
	}
	var got []struct {
		Code     string `json:"code"`
		Line     int    `json:"line"`
		Severity string `json:"severity"`
	}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("decode sidecar: %v", err)
	}
	if len(got) != 2 ||
		got[0].Severity != "info" || got[0].Line != 1 || got[0].Code != CodeFireAndForget ||
		got[1].Severity != "error" || got[1].Line != 12 || got[1].Code != CodeWireTypeUnknown {
		t.Fatalf("sidecar rows: got %+v, want [{info 1 fire_and_forget} {error 12 wire_type_unknown}]", got)
	}
	if !strings.HasSuffix(string(b), "]\n") {
		t.Error("the sidecar must end with a newline, as the generator writes it")
	}
}
