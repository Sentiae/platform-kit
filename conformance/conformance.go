// Package conformance mechanically falsifies a service's fail-closed wiring.
//
// The fail-open root disease (`#fail-open-is-the-library-contract`) lives in the
// config↔code WIRING: unit tests exercise the configured branch and pass, so a
// service that is SUPPOSED to refuse boot on a broken security control only
// reveals the miss when booted in a real environment. This harness makes that
// mechanical: for each declared control it applies a mutation that MUST break it,
// then asserts the boot REFUSES and the refusal NAMES the control (a refusal that
// doesn't say what it refused is itself a fail-open of observability).
//
// It is the tool that would have caught cont.³⁷'s misses — the pulse healthcheck
// that could-not-fail, the MTLSMode typo that silently disabled the mesh, the
// permissive-branch config misses — before deploy.
//
// Usage (from a service's conformance test, or a deploy-verify step):
//
//	res := conformance.Falsify(ctx, boot,
//	    conformance.Mutation{Name: "mtls-typo", Env: map[string]string{"APP_GRPC_MTLS_MODE": "stric"},
//	        WantRefusal: "APP_GRPC_MTLS_MODE"},
//	    conformance.Mutation{Name: "strict-nil-svid", Env: map[string]string{"APP_GRPC_MTLS_MODE": "strict"},
//	        WantRefusal: "strict"},
//	)
//	if !res.Pass { t.Fatalf("fail-closed wiring not proven: %s", res.Summary()) }
package conformance

import (
	"context"
	"fmt"
	"os"
	"strings"
)

// Mutation is a named perturbation that MUST break a specific fail-closed control.
// Env is applied around the boot; a "" value UNSETS the variable. WantRefusal is a
// substring the boot error MUST contain — the name of the control it refused on.
type Mutation struct {
	Name        string
	Env         map[string]string
	WantRefusal string
}

// BootFunc performs a service's fail-closed boot FROM SCRATCH: it must re-read
// config from the process environment and run its posture assertions, returning
// the boot error (or nil if it would serve). It must NOT bind real listeners or
// leave global state behind — the harness applies/restores env around each call
// and runs the calls serially (env is process-global).
type BootFunc func(ctx context.Context) error

// Outcome is one mutation's verdict.
type Outcome struct {
	Mutation string `json:"mutation"`
	Refused  bool   `json:"refused"` // boot returned an error (fail-closed held)
	Named    bool   `json:"named"`   // the refusal contained WantRefusal
	Err      string `json:"error,omitempty"`
	Pass     bool   `json:"pass"` // Refused && Named
}

// Result is a full falsification run.
type Result struct {
	BaselineBoots bool      `json:"baseline_boots"` // the un-mutated boot succeeded
	BaselineErr   string    `json:"baseline_error,omitempty"`
	Outcomes      []Outcome `json:"outcomes"`
	Pass          bool      `json:"pass"` // BaselineBoots && every Outcome.Pass
}

// Falsify runs boot once clean — the baseline MUST succeed, or the harness is
// just always-failing and proves nothing — then once per mutation, each of which
// MUST refuse and name its control. Any failing outcome (or a non-booting
// baseline) fails the run. Mutations run serially; env is restored after each.
func Falsify(ctx context.Context, boot BootFunc, mutations ...Mutation) Result {
	res := Result{Pass: true}

	// Baseline: a correctly-wired service boots clean with no mutation.
	if err := boot(ctx); err != nil {
		res.BaselineBoots = false
		res.BaselineErr = err.Error()
		res.Pass = false
		// Still run the mutations for a full report, but the run has already failed.
	} else {
		res.BaselineBoots = true
	}

	for _, m := range mutations {
		restore := applyEnv(m.Env)
		err := boot(ctx)
		restore()

		o := Outcome{Mutation: m.Name}
		if err != nil {
			o.Refused = true
			o.Err = err.Error()
			o.Named = m.WantRefusal == "" || strings.Contains(err.Error(), m.WantRefusal)
		}
		o.Pass = o.Refused && o.Named
		if !o.Pass {
			res.Pass = false
		}
		res.Outcomes = append(res.Outcomes, o)
	}
	return res
}

// Summary is a one-line human-readable verdict for test failure messages.
func (r Result) Summary() string {
	var b strings.Builder
	if !r.BaselineBoots {
		fmt.Fprintf(&b, "BASELINE DID NOT BOOT (%s); ", r.BaselineErr)
	}
	for _, o := range r.Outcomes {
		switch {
		case !o.Refused:
			fmt.Fprintf(&b, "[%s: FAIL-OPEN — booted despite mutation] ", o.Mutation)
		case !o.Named:
			fmt.Fprintf(&b, "[%s: refused but did not name the control: %q] ", o.Mutation, o.Err)
		default:
			fmt.Fprintf(&b, "[%s: ok] ", o.Mutation)
		}
	}
	return strings.TrimSpace(b.String())
}

// applyEnv sets/unsets the given variables and returns a restore func that puts
// the environment back exactly as it was (including previously-unset vars).
func applyEnv(env map[string]string) func() {
	type prev struct {
		val string
		set bool
	}
	saved := make(map[string]prev, len(env))
	for k, v := range env {
		old, ok := os.LookupEnv(k)
		saved[k] = prev{val: old, set: ok}
		if v == "" {
			_ = os.Unsetenv(k)
		} else {
			_ = os.Setenv(k, v)
		}
	}
	return func() {
		for k, p := range saved {
			if p.set {
				_ = os.Setenv(k, p.val)
			} else {
				_ = os.Unsetenv(k)
			}
		}
	}
}
