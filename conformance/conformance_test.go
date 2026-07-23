package conformance

import (
	"context"
	"errors"
	"os"
	"testing"
)

// A correctly-wired boot: it re-reads the env and REFUSES (naming the control)
// when APP_MODE is an unrecognized value, and boots clean otherwise.
func failClosedBoot(_ context.Context) error {
	mode := os.Getenv("APP_MODE")
	switch mode {
	case "", "off", "strict":
		return nil // legal — boots
	default:
		return errors.New(`config: invalid APP_MODE "` + mode + `": must be one of "off","strict"`)
	}
}

// A fail-OPEN boot: it maps any unrecognized APP_MODE silently to "off" and boots
// anyway — the exact disease the harness exists to catch.
func failOpenBoot(_ context.Context) error { return nil }

var typoMutation = Mutation{
	Name:        "app-mode-typo",
	Env:         map[string]string{"APP_MODE": "stric"}, // a typo for "strict"
	WantRefusal: "APP_MODE",
}

func TestFalsify_PassesOnCorrectlyWiredService(t *testing.T) {
	res := Falsify(context.Background(), failClosedBoot, typoMutation)
	if !res.Pass {
		t.Fatalf("expected PASS on a fail-closed service, got: %s", res.Summary())
	}
	if !res.BaselineBoots {
		t.Fatalf("baseline should boot clean")
	}
	if len(res.Outcomes) != 1 || !res.Outcomes[0].Refused || !res.Outcomes[0].Named {
		t.Fatalf("expected the mutation refused+named, got %+v", res.Outcomes)
	}
}

// THE self-falsification: the harness MUST go red on a planted fail-open. If this
// ever passes, the harness is worthless — it would rubber-stamp the disease.
func TestFalsify_CatchesFailOpen(t *testing.T) {
	res := Falsify(context.Background(), failOpenBoot, typoMutation)
	if res.Pass {
		t.Fatalf("harness FAILED to catch a fail-open — it rubber-stamped the disease")
	}
	if res.Outcomes[0].Refused {
		t.Fatalf("fail-open boot must be recorded as NOT refused")
	}
}

// A refusal that does not NAME the control is itself a fail-open of observability.
func TestFalsify_RequiresNamedRefusal(t *testing.T) {
	unnamedBoot := func(_ context.Context) error {
		if os.Getenv("APP_MODE") == "stric" {
			return errors.New("boot failed") // refuses, but says nothing useful
		}
		return nil
	}
	res := Falsify(context.Background(), unnamedBoot, typoMutation)
	if res.Pass {
		t.Fatalf("expected FAIL: refusal did not name APP_MODE")
	}
	if !res.Outcomes[0].Refused || res.Outcomes[0].Named {
		t.Fatalf("expected refused=true named=false, got %+v", res.Outcomes[0])
	}
}

func TestFalsify_FailsWhenBaselineDoesNotBoot(t *testing.T) {
	alwaysRefuses := func(_ context.Context) error { return errors.New("always refuses") }
	res := Falsify(context.Background(), alwaysRefuses, typoMutation)
	if res.Pass || res.BaselineBoots {
		t.Fatalf("a service that won't even boot clean must not pass: %s", res.Summary())
	}
}

// Env is fully restored after each mutation (no leakage across calls).
func TestFalsify_RestoresEnv(t *testing.T) {
	os.Unsetenv("APP_MODE")
	_ = Falsify(context.Background(), failClosedBoot, typoMutation)
	if _, ok := os.LookupEnv("APP_MODE"); ok {
		t.Fatalf("APP_MODE leaked after Falsify — env not restored")
	}
}
