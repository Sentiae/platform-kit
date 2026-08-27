package flowlang

import (
	"path/filepath"
	"reflect"
	"testing"
)

func mustPlan(t *testing.T, text string, m Manifests) *Plan {
	t.Helper()
	doc, diags := Parse(text)
	if doc == nil || len(diags) != 0 {
		t.Fatalf("Parse: doc=%v diags=%+v", doc != nil, diags)
	}
	plan, vdiags := Schedule(doc, m)
	if plan == nil {
		t.Fatalf("Schedule refused: %+v", vdiags)
	}
	return plan
}

// TestSchedule_Order proves the order is a pure function of the file: every
// node appears once, every edge points forward, and twenty runs agree. Map
// iteration is random in Go, so an order derived from a map would pass once and
// fail in production.
func TestSchedule_Order(t *testing.T) {
	manifests := corpusManifests(t)
	for _, path := range corpusFlows(t) {
		t.Run(filepath.Base(path), func(t *testing.T) {
			text := readFixture(t, path)
			doc, diags := Parse(text)
			if doc == nil || len(diags) != 0 {
				t.Fatalf("Parse: doc=%v diags=%+v", doc != nil, diags)
			}
			plan, vdiags := Schedule(doc, manifests)
			if plan == nil {
				t.Fatalf("Schedule refused: %+v", vdiags)
			}
			if len(plan.Order) != len(doc.Nodes) {
				t.Fatalf("order %v covers %d of %d nodes", plan.Order, len(plan.Order), len(doc.Nodes))
			}
			at := make(map[string]int, len(plan.Order))
			for i, slug := range plan.Order {
				at[slug] = i
			}
			for _, edge := range plan.Edges {
				if at[edge.From] >= at[edge.To] {
					t.Fatalf("edge %s.%s -> %s.%s does not point forward in %v",
						edge.From, edge.FromPort, edge.To, edge.ToPort, plan.Order)
				}
			}
			for i := 0; i < 20; i++ {
				again := mustPlan(t, text, manifests)
				if !reflect.DeepEqual(again.Order, plan.Order) {
					t.Fatalf("run %d order %v differs from %v", i, again.Order, plan.Order)
				}
			}
		})
	}

	t.Run("02_order_intake_equals_document_order", func(t *testing.T) {
		plan := mustPlan(t, readFixture(t, "testdata/02_order_intake.flow"), manifests)
		want := []string{"intake", "fetch_orders", "response"}
		if !reflect.DeepEqual(plan.Order, want) {
			t.Fatalf("order = %v, want %v", plan.Order, want)
		}
	})

	// 02 has no tie to break — every wave has exactly one ready candidate — so
	// it cannot observe the tie-break rule at all. 05 can: after `choose`, both
	// respond nodes are ready at once, and the earliest in DOCUMENT order wins.
	t.Run("05_multi_respond_breaks_the_tie_by_document_order", func(t *testing.T) {
		plan := mustPlan(t, readFixture(t, "testdata/05_multi_respond.flow"), manifests)
		want := []string{"intake", "choose", "respond_false", "respond_true"}
		if !reflect.DeepEqual(plan.Order, want) {
			t.Fatalf("order = %v, want %v", plan.Order, want)
		}
	})
}

// predicateFlow carries every shape the wave rule has to separate: a trigger, a
// zero-input non-trigger, a node with a required input, and a disabled node
// whose input DOES fire.
func predicateFlow() string {
	return flowText(
		`flow "f" v2`,
		``,
		`use any_out = @test/any-out@1.0.0`,
		`use secure_http = @acme/secure-http@2.1.0`,
		`use webhook_trigger = @sentiae/webhook-trigger@1.0.0`,
		``,
		`node intake: webhook_trigger {`,
		`}`,
		``,
		`node source: any_out {`,
		`}`,
		``,
		`node worker: secure_http {`,
		"\turl = \"https://api.example.net/a\"",
		`}`,
		``,
		`node audit: secure_http disabled {`,
		"\turl = \"https://api.example.net/b\"",
		`}`,
		``,
		`wire intake.body -> audit.payload`,
		`wire intake.body -> worker.payload`,
	)
}

// TestSchedule_Predicates pins the ONE wave rule both the runtime and the code
// generator call.
func TestSchedule_Predicates(t *testing.T) {
	plan := mustPlan(t, predicateFlow(), testManifests(t))

	tests := []struct {
		name     string
		status   map[string]Status
		fired    map[string]map[string]bool
		wantRun  []string
		wantSkip []string
	}{
		{
			name:    "wave_one_runs_the_trigger_and_the_zero_input_node",
			status:  map[string]Status{},
			fired:   map[string]map[string]bool{},
			wantRun: []string{"intake", "source"},
		},
		{
			name:     "a_required_input_that_fired_runs_and_a_disabled_node_is_skipped",
			status:   map[string]Status{"intake": StatusDone, "source": StatusDone},
			fired:    map[string]map[string]bool{"intake": {"body": true}},
			wantRun:  []string{"worker"},
			wantSkip: []string{"audit"},
		},
		{
			name:     "a_required_input_that_never_fired_is_skipped",
			status:   map[string]Status{"intake": StatusDone, "source": StatusDone},
			fired:    map[string]map[string]bool{},
			wantSkip: []string{"worker", "audit"},
		},
		{
			name:     "a_skipped_upstream_makes_the_node_ready_and_skipped",
			status:   map[string]Status{"intake": StatusSkipped, "source": StatusDone},
			fired:    map[string]map[string]bool{},
			wantSkip: []string{"worker", "audit"},
		},
		{
			name:   "an_unsettled_upstream_leaves_the_node_out_of_the_wave",
			status: map[string]Status{"intake": StatusRunning, "source": StatusDone},
			fired:  map[string]map[string]bool{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			run, skip := plan.Next(tt.status, tt.fired)
			if !equalSlugs(run, tt.wantRun) {
				t.Fatalf("run = %v, want %v", run, tt.wantRun)
			}
			if !equalSlugs(skip, tt.wantSkip) {
				t.Fatalf("skip = %v, want %v", skip, tt.wantSkip)
			}
		})
	}
}

func equalSlugs(got, want []string) bool {
	if len(got) == 0 && len(want) == 0 {
		return true
	}
	return reflect.DeepEqual(got, want)
}

// TestSchedule_FourCases pins how a flow answers its caller. "Ran and said
// nothing" and "has nothing to say" are different outcomes and the handler
// answers them differently.
func TestSchedule_FourCases(t *testing.T) {
	manifests := corpusManifests(t)

	t.Run("no_respond_node_is_fire_and_forget", func(t *testing.T) {
		plan := mustPlan(t, readFixture(t, "testdata/04_fire_and_forget.flow"), manifests)
		got, slug := plan.Result(map[string]map[string]bool{})
		if got != ResultFireAndForget || slug != "" {
			t.Fatalf("got (%q, %q), want (%q, \"\")", got, slug, ResultFireAndForget)
		}
	})

	plan := mustPlan(t, readFixture(t, "testdata/05_multi_respond.flow"), manifests)
	if len(plan.Responds) != 2 {
		t.Fatalf("responds = %v, want two", plan.Responds)
	}

	tests := []struct {
		name     string
		fired    map[string]map[string]bool
		wantCase ResultCase
		wantSlug string
	}{
		{"none_fired", map[string]map[string]bool{}, ResultNoResponse, ""},
		{
			"one_fired",
			map[string]map[string]bool{"respond_true": {"response": true}},
			ResultSingleResponse, "respond_true",
		},
		{
			"both_fired",
			map[string]map[string]bool{
				"respond_false": {"response": true},
				"respond_true":  {"response": true},
			},
			ResultMultipleResponses, "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, slug := plan.Result(tt.fired)
			if got != tt.wantCase || slug != tt.wantSlug {
				t.Fatalf("got (%q, %q), want (%q, %q)", got, slug, tt.wantCase, tt.wantSlug)
			}
		})
	}
}

// TestSchedule_RejectsInvalid pins the gate: an error-severity finding yields
// NO plan, while a warning still yields one. A flow that cannot be read
// correctly must not be run approximately.
func TestSchedule_RejectsInvalid(t *testing.T) {
	manifests := testManifests(t)

	refused := []struct {
		name string
		text string
		code string
	}{
		{
			name: "cycle",
			text: flowText(
				`flow "f" v2`,
				``,
				`use branch_node = @sentiae/branch@1.0.0`,
				``,
				`node a: branch_node {`,
				`}`,
				``,
				`node b: branch_node {`,
				`}`,
				``,
				`wire a.on_true -> b.value`,
				`wire b.on_true -> a.value`,
			),
			code: CodeCycle,
		},
		{
			name: "fan_in",
			text: flowText(
				`flow "f" v2`,
				``,
				`use secure_http = @acme/secure-http@2.1.0`,
				`use webhook_trigger = @sentiae/webhook-trigger@1.0.0`,
				``,
				`node intake: webhook_trigger {`,
				`}`,
				``,
				`node w: secure_http {`,
				"\turl = \"https://api.example.net/a\"",
				`}`,
				``,
				`wire intake.body -> w.payload`,
				`wire intake.method -> w.payload`,
			),
			code: CodeWireFanIn,
		},
	}
	for _, tt := range refused {
		t.Run(tt.name, func(t *testing.T) {
			doc, diags := Parse(tt.text)
			if doc == nil || len(diags) != 0 {
				t.Fatalf("Parse: doc=%v diags=%+v", doc != nil, diags)
			}
			plan, vdiags := Schedule(doc, manifests)
			if plan != nil {
				t.Fatalf("Schedule returned a plan for an invalid document")
			}
			found := false
			for _, d := range vdiags {
				if d.Code == tt.code {
					found = true
				}
			}
			if !found {
				t.Fatalf("diagnostics %+v do not carry %q", vdiags, tt.code)
			}
		})
	}

	t.Run("a_warning_only_document_still_plans", func(t *testing.T) {
		doc, diags := Parse(readFixture(t, "testdata/02_order_intake.flow"))
		if doc == nil || len(diags) != 0 {
			t.Fatalf("Parse: doc=%v diags=%+v", doc != nil, diags)
		}
		plan, vdiags := Schedule(doc, manifests)
		if plan == nil {
			t.Fatalf("Schedule refused a warning-only document: %+v", vdiags)
		}
		if len(vdiags) == 0 {
			t.Fatal("expected the free-input warning to still be reported")
		}
		for _, d := range vdiags {
			if d.Severity == SeverityError {
				t.Fatalf("unexpected error finding: %+v", d)
			}
		}
	})
}
