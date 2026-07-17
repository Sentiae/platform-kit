package posture

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// pass is a control assertion that always holds.
func pass(context.Context) error { return nil }

// failWith returns a control assertion that always fails with err.
func failWith(err error) func(context.Context) error {
	return func(context.Context) error { return err }
}

func TestKindString(t *testing.T) {
	tests := []struct {
		name string
		k    Kind
		want string
	}{
		{"control", KindControl, "control"},
		{"exemption", KindExemption, "exemption"},
		{"none", KindNone, "none"},
		{"unknown", Kind(99), "Kind(99)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.k.String(); got != tt.want {
				t.Fatalf("Kind.String() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestResultString(t *testing.T) {
	tests := []struct {
		name string
		r    Result
		want string
	}{
		{"not_run", ResultNotRun, "not_run"},
		{"passed", ResultPassed, "passed"},
		{"failed", ResultFailed, "failed"},
		{"unknown", Result(99), "Result(99)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.r.String(); got != tt.want {
				t.Fatalf("Result.String() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestDeclareConstructionErrors(t *testing.T) {
	tests := []struct {
		name    string
		decls   []Declaration
		wantErr error
	}{
		{
			name:    "empty declare",
			decls:   nil,
			wantErr: ErrEmptyDeclare,
		},
		{
			name:    "nil Assert control",
			decls:   []Declaration{Control{Name: "rls"}},
			wantErr: ErrInvalidDeclaration,
		},
		{
			name:    "empty control name",
			decls:   []Declaration{Control{Name: "  ", Assert: pass}},
			wantErr: ErrInvalidDeclaration,
		},
		{
			name:    "exemption empty reason",
			decls:   []Declaration{Exempt("rls", "", pass)},
			wantErr: ErrInvalidDeclaration,
		},
		{
			name:    "exemption nil trigger",
			decls:   []Declaration{Exempt("rls", "stateless", nil)},
			wantErr: ErrInvalidDeclaration,
		},
		{
			name:    "None empty reason",
			decls:   []Declaration{None("  ")},
			wantErr: ErrInvalidDeclaration,
		},
		{
			name: "None mixed with control",
			decls: []Declaration{
				None("stateless"),
				Control{Name: "rls", Assert: pass},
			},
			wantErr: ErrInvalidDeclaration,
		},
		{
			name: "duplicate name",
			decls: []Declaration{
				Control{Name: "rls", Assert: pass},
				Control{Name: "rls", Assert: pass},
			},
			wantErr: ErrInvalidDeclaration,
		},
		{
			name:    "nil declaration",
			decls:   []Declaration{nil},
			wantErr: ErrInvalidDeclaration,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			set, err := Declare(tt.decls...)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("Declare() err = %v, want errors.Is %v", err, tt.wantErr)
			}
			if set != nil {
				t.Fatalf("Declare() returned a non-nil Set on error: %+v", set)
			}
		})
	}
}

func TestDeclareAggregatesAllConstructionErrors(t *testing.T) {
	// Two malformed declarations: an operator wants to see BOTH, not just the first.
	_, err := Declare(
		Control{Name: "a"},    // nil Assert
		Exempt("b", "", pass), // empty reason
	)
	if !errors.Is(err, ErrInvalidDeclaration) {
		t.Fatalf("err = %v, want ErrInvalidDeclaration", err)
	}
	msg := err.Error()
	if !strings.Contains(msg, `"a"`) || !strings.Contains(msg, `"b"`) {
		t.Fatalf("aggregated construction error missing a declaration: %q", msg)
	}
}

func TestHoldPasses(t *testing.T) {
	set, err := Declare(
		Control{Name: "rls-enforced", Assert: pass},
		Control{Name: "jwks-reachable", Assert: pass},
	)
	if err != nil {
		t.Fatalf("Declare() err = %v", err)
	}
	if err := set.Hold(context.Background()); err != nil {
		t.Fatalf("Hold() err = %v, want nil", err)
	}
	for _, s := range set.Report() {
		if s.Result != ResultPassed {
			t.Fatalf("control %q Result = %v, want passed", s.Name, s.Result)
		}
	}
}

func TestHoldAggregatesAllFailures(t *testing.T) {
	errA := errors.New("role has bypassrls")
	errB := errors.New("jwks unreachable")

	set, err := Declare(
		Control{Name: "rls-enforced", Assert: failWith(errA)},
		Control{Name: "jwks-reachable", Assert: failWith(errB)},
		Control{Name: "mesh-svid", Assert: pass},
	)
	if err != nil {
		t.Fatalf("Declare() err = %v", err)
	}

	holdErr := set.Hold(context.Background())
	if !errors.Is(holdErr, ErrPostureNotHeld) {
		t.Fatalf("Hold() err = %v, want ErrPostureNotHeld", holdErr)
	}
	// ALL failures must be named, not just the first.
	if !errors.Is(holdErr, errA) {
		t.Fatalf("Hold() err does not wrap errA: %v", holdErr)
	}
	if !errors.Is(holdErr, errB) {
		t.Fatalf("Hold() err does not wrap errB: %v", holdErr)
	}
	msg := holdErr.Error()
	if !strings.Contains(msg, "rls-enforced") || !strings.Contains(msg, "jwks-reachable") {
		t.Fatalf("Hold() err missing a failing control name: %q", msg)
	}

	// Report reflects per-declaration results.
	want := map[string]Result{
		"rls-enforced":   ResultFailed,
		"jwks-reachable": ResultFailed,
		"mesh-svid":      ResultPassed,
	}
	for _, s := range set.Report() {
		if s.Result != want[s.Name] {
			t.Fatalf("control %q Result = %v, want %v", s.Name, s.Result, want[s.Name])
		}
		if s.Result == ResultFailed && s.Err == nil {
			t.Fatalf("control %q failed but Status.Err is nil", s.Name)
		}
	}
}

func TestMustHoldEqualsHold(t *testing.T) {
	set, err := Declare(Control{Name: "x", Assert: failWith(errors.New("boom"))})
	if err != nil {
		t.Fatalf("Declare() err = %v", err)
	}
	if err := set.MustHold(context.Background()); !errors.Is(err, ErrPostureNotHeld) {
		t.Fatalf("MustHold() err = %v, want ErrPostureNotHeld", err)
	}
}

func TestExemptionValidTriggerHolds(t *testing.T) {
	// llm-gateway's RLS exemption: valid while DatabaseURL is empty.
	databaseURL := ""
	trigger := func(context.Context) error {
		if databaseURL != "" {
			return errors.New("DatabaseURL is set; RLS exemption no longer valid")
		}
		return nil
	}
	set, err := Declare(Exempt("rls", "llm-gateway holds no tenant data", trigger))
	if err != nil {
		t.Fatalf("Declare() err = %v", err)
	}
	if err := set.Hold(context.Background()); err != nil {
		t.Fatalf("Hold() err = %v, want nil (exemption valid)", err)
	}
	rep := set.Report()
	if len(rep) != 1 || rep[0].Kind != KindExemption || rep[0].Result != ResultPassed {
		t.Fatalf("Report() = %+v, want one passed exemption", rep)
	}
	if rep[0].Reason == "" {
		t.Fatalf("exemption Status.Reason is empty")
	}
}

func TestExemptionFiredTriggerFailsHold(t *testing.T) {
	// The moment DatabaseURL is set, the waiver's premise is gone -> boot failure.
	databaseURL := "postgres://x"
	trigger := func(context.Context) error {
		if databaseURL != "" {
			return errors.New("DatabaseURL is set; RLS exemption no longer valid")
		}
		return nil
	}
	set, err := Declare(Exempt("rls", "llm-gateway holds no tenant data", trigger))
	if err != nil {
		t.Fatalf("Declare() err = %v", err)
	}
	holdErr := set.Hold(context.Background())
	if !errors.Is(holdErr, ErrPostureNotHeld) {
		t.Fatalf("Hold() err = %v, want ErrPostureNotHeld", holdErr)
	}
	if !strings.Contains(holdErr.Error(), "rls") {
		t.Fatalf("Hold() err missing exemption name: %q", holdErr.Error())
	}
	if set.Report()[0].Result != ResultFailed {
		t.Fatalf("fired exemption Result = %v, want failed", set.Report()[0].Result)
	}
}

func TestNoneHoldsAndReports(t *testing.T) {
	set, err := Declare(None("stateless edge proxy; no tenant data, no auth surface"))
	if err != nil {
		t.Fatalf("Declare() err = %v", err)
	}
	if err := set.Hold(context.Background()); err != nil {
		t.Fatalf("Hold() err = %v, want nil", err)
	}
	rep := set.Report()
	if len(rep) != 1 {
		t.Fatalf("Report() len = %d, want 1", len(rep))
	}
	if rep[0].Kind != KindNone {
		t.Fatalf("Report()[0].Kind = %v, want none", rep[0].Kind)
	}
	if rep[0].Result != ResultPassed {
		t.Fatalf("Report()[0].Result = %v, want passed", rep[0].Result)
	}
	if rep[0].Reason == "" {
		t.Fatalf("None Status.Reason is empty; the opt-out must be visible")
	}
}

func TestReportBeforeHoldIsNotRun(t *testing.T) {
	set, err := Declare(Control{Name: "rls", Assert: pass})
	if err != nil {
		t.Fatalf("Declare() err = %v", err)
	}
	for _, s := range set.Report() {
		if s.Result != ResultNotRun {
			t.Fatalf("before Hold, control %q Result = %v, want not_run", s.Name, s.Result)
		}
	}
}

func TestHoldRecoversControlPanic(t *testing.T) {
	set, err := Declare(Control{Name: "panicky", Assert: func(context.Context) error {
		panic("kaboom")
	}})
	if err != nil {
		t.Fatalf("Declare() err = %v", err)
	}
	holdErr := set.Hold(context.Background())
	if !errors.Is(holdErr, ErrPostureNotHeld) {
		t.Fatalf("Hold() err = %v, want ErrPostureNotHeld", holdErr)
	}
	if !strings.Contains(holdErr.Error(), "panicked") {
		t.Fatalf("Hold() err missing panic detail: %q", holdErr.Error())
	}
}

func TestHoldCancelledContextFails(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already dead before Hold runs

	set, err := Declare(Control{Name: "rls", Assert: pass})
	if err != nil {
		t.Fatalf("Declare() err = %v", err)
	}
	if err := set.Hold(ctx); !errors.Is(err, ErrPostureNotHeld) {
		t.Fatalf("Hold() on cancelled ctx err = %v, want ErrPostureNotHeld", err)
	}
}

func TestHoldBoundsCtxIgnoringControl(t *testing.T) {
	// A control that ignores ctx and blocks must not hang boot past the deadline.
	block := make(chan struct{})
	t.Cleanup(func() { close(block) })

	set, err := Declare(Control{Name: "hangs", Assert: func(context.Context) error {
		<-block // never released within the test's deadline
		return nil
	}})
	if err != nil {
		t.Fatalf("Declare() err = %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	start := time.Now()
	holdErr := set.Hold(ctx)
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("Hold() took %v; deadline did not bound a ctx-ignoring control", elapsed)
	}
	if !errors.Is(holdErr, ErrPostureNotHeld) {
		t.Fatalf("Hold() err = %v, want ErrPostureNotHeld", holdErr)
	}
}

func TestHoldStopsAfterDeadlineExhausted(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()

	block := make(chan struct{})
	t.Cleanup(func() { close(block) })

	set, err := Declare(
		Control{Name: "hangs", Assert: func(context.Context) error { <-block; return nil }},
		Control{Name: "never-reached", Assert: pass},
	)
	if err != nil {
		t.Fatalf("Declare() err = %v", err)
	}
	if err := set.Hold(ctx); !errors.Is(err, ErrPostureNotHeld) {
		t.Fatalf("Hold() err = %v, want ErrPostureNotHeld", err)
	}
	rep := set.Report()
	byName := map[string]Result{}
	for _, s := range rep {
		byName[s.Name] = s.Result
	}
	if byName["hangs"] != ResultFailed {
		t.Fatalf("hangs Result = %v, want failed", byName["hangs"])
	}
	if byName["never-reached"] != ResultNotRun {
		t.Fatalf("never-reached Result = %v, want not_run (deadline was exhausted)", byName["never-reached"])
	}
}

// TestStatefulControlViaMethodValue proves the struct-with-func-field shape
// admits a struct-with-state through a method value, per the package doc.
type jwksChecker struct{ healthy bool }

func (j jwksChecker) Assert(context.Context) error {
	if !j.healthy {
		return errors.New("jwks endpoint unhealthy")
	}
	return nil
}

func TestStatefulControlViaMethodValue(t *testing.T) {
	healthy := jwksChecker{healthy: true}
	set, err := Declare(Control{Name: "jwks", Assert: healthy.Assert})
	if err != nil {
		t.Fatalf("Declare() err = %v", err)
	}
	if err := set.Hold(context.Background()); err != nil {
		t.Fatalf("Hold() err = %v, want nil", err)
	}
}
