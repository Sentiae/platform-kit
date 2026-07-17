// Package posture implements L1 (Declare) and L3 (Prove at boot) of D-162a:
// no security control may be selected by the ABSENCE of a value.
//
// A service enumerates its security controls AS CODE and, at boot, every
// declared control must prove itself or the process must refuse to serve. The
// governing principle: a control that cannot prove itself at boot must PREVENT
// boot. This is the generalization of the single prove-at-boot control that
// tenantdb.AssertPosture already implements -- that function adapts into a
// Control in one line and its logic does not change.
//
// # The Control shape: a struct with a func field, not an interface
//
// A Control is a struct carrying a Name and an Assert func rather than an
// interface with Name()/Assert() methods. This lets BOTH common producers
// implement it with zero ceremony:
//
//   - a closure: posture.Control{Name: "x", Assert: func(ctx) error { ... }}
//   - an existing plain function (e.g. tenantdb.AssertPosture):
//     posture.Control{Name: "rls", Assert: func(ctx) error { return tenantdb.AssertPosture(db, want) }}
//   - a stateful checker via a method value, which closes over its receiver:
//     posture.Control{Name: "jwks", Assert: verifier.Assert}
//
// An interface would force the two most common cases (closures and existing
// funcs) through an adapter type such as ControlFunc, while only helping the
// rare stateful case -- which the method value already covers. So the struct
// is the shape that both a closure and a struct-with-state satisfy cleanly.
//
// # Fail-closed applies to this package's OWN design
//
//   - A Control with a nil Assert is a construction error (ErrInvalidDeclaration),
//     never a silently-passing control -- that would be the disease inside the cure.
//   - Declare with zero declarations is a construction error (ErrEmptyDeclare). A
//     service with genuinely no security controls must say so explicitly via
//     None(reason): "no controls" is thereby a DECLARED, NAMED, reasoned state a
//     Report can show, not an implicit no-op selected by absence -- which is the
//     exact failure D-162a exists to make impossible.
//   - An exemption is first-class and visible: a named waiver whose trigger
//     asserts the condition under which the waiver is still valid. A trigger that
//     fires (returns an error) is a boot failure, exactly like a failed control.
//
// # This package NEVER exits or logs
//
// Per root CLAUDE.md §12 and §30.3 only main.go may os.Exit / log.Fatal. Hold
// and MustHold return an aggregated error; the caller (main.go) fatals on it.
// MustHold's name signals intent ("this MUST hold or we do not serve") -- it
// does not itself exit. The package returns DATA and never logs; a log line on
// failure is the caller's job via the returned error. It is a leaf dependency:
// stdlib only, so it can sit at the composition root of every service.
package posture

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
)

// Errors returned by this package. Compare with errors.Is.
var (
	// ErrEmptyDeclare is returned by Declare when given zero declarations. A
	// service with no security controls must declare None(reason) explicitly.
	ErrEmptyDeclare = errors.New("posture: no declarations (declare at least one control, or posture.None(reason) explicitly)")
	// ErrInvalidDeclaration is returned by Declare for a malformed declaration:
	// a nil Assert/trigger, an empty name, an empty exemption reason, a duplicate
	// name, or a None mixed with real controls.
	ErrInvalidDeclaration = errors.New("posture: invalid declaration")
	// ErrPostureNotHeld is returned by Hold/MustHold when one or more declared
	// controls or exemptions failed to prove themselves. The process must not serve.
	ErrPostureNotHeld = errors.New("posture: declared security posture not held")
)

// Kind classifies a declaration in a Report.
type Kind int

const (
	// KindControl is a security control that must prove itself at boot.
	KindControl Kind = iota
	// KindExemption is a named, reasoned waiver of a control; its trigger asserts
	// the condition under which the waiver is still valid.
	KindExemption
	// KindNone is the explicit "this service intentionally declares no controls"
	// state, carrying a reason so absence-of-controls is visible, not implicit.
	KindNone
)

// String renders the kind for reports and error messages.
func (k Kind) String() string {
	switch k {
	case KindControl:
		return "control"
	case KindExemption:
		return "exemption"
	case KindNone:
		return "none"
	default:
		return fmt.Sprintf("Kind(%d)", int(k))
	}
}

// Result is the outcome of evaluating a declaration during Hold.
type Result int

const (
	// ResultNotRun is the zero value: Hold has not evaluated this declaration
	// (either Hold has not run, or a prior control exhausted the boot deadline).
	ResultNotRun Result = iota
	// ResultPassed means the declaration proved itself.
	ResultPassed
	// ResultFailed means the declaration failed; the process must not serve.
	ResultFailed
)

// String renders the result for reports.
func (r Result) String() string {
	switch r {
	case ResultNotRun:
		return "not_run"
	case ResultPassed:
		return "passed"
	case ResultFailed:
		return "failed"
	default:
		return fmt.Sprintf("Result(%d)", int(r))
	}
}

// Declaration is a sealed interface: exactly a Control, an Exemption, or a None.
// Its methods are unexported, so only this package can implement it -- callers
// construct declarations via the Control struct literal, Exempt, or None.
type Declaration interface {
	name() string
	kind() Kind
	reason() string
	// assert evaluates the declaration; a non-nil error means boot must not proceed.
	assert(ctx context.Context) error
	// validate is the construction-time check run by Declare.
	validate() error
}

// Control is a named security control with a boot-time proof. Assert must be
// non-nil (a nil Assert is a construction error) and should honor the ctx
// deadline, which Hold propagates from the caller's boot budget.
type Control struct {
	Name   string
	Assert func(ctx context.Context) error
}

func (c Control) name() string   { return c.Name }
func (c Control) kind() Kind     { return KindControl }
func (c Control) reason() string { return "" }

func (c Control) assert(ctx context.Context) error { return c.Assert(ctx) }

func (c Control) validate() error {
	if strings.TrimSpace(c.Name) == "" {
		return fmt.Errorf("%w: control has an empty name", ErrInvalidDeclaration)
	}
	if c.Assert == nil {
		return fmt.Errorf("%w: control %q has a nil Assert", ErrInvalidDeclaration, c.Name)
	}
	return nil
}

// exemption is a named, reasoned waiver of a control. Its trigger asserts the
// invalidation condition: a trigger that returns an error means the waiver's
// premise no longer holds, which is a boot failure.
type exemption struct {
	nm      string
	rsn     string
	trigger func(ctx context.Context) error
}

// Exempt declares a first-class waiver of a control. name and reason must be
// non-empty (a waiver with no stated reason is the invisible-off this package
// prevents), and trigger must be non-nil. trigger asserts the condition under
// which the exemption is still valid: it returns nil while the waiver holds and
// an error the moment it does not (e.g. llm-gateway's RLS exemption returns an
// error once DatabaseURL != ""). A firing trigger fails Hold exactly like a
// failed control.
func Exempt(name, reason string, trigger func(ctx context.Context) error) Declaration {
	return exemption{nm: name, rsn: reason, trigger: trigger}
}

func (e exemption) name() string   { return e.nm }
func (e exemption) kind() Kind     { return KindExemption }
func (e exemption) reason() string { return e.rsn }

func (e exemption) assert(ctx context.Context) error {
	if err := e.trigger(ctx); err != nil {
		return fmt.Errorf("exemption %q no longer valid (waiver reason: %s): %w", e.nm, e.rsn, err)
	}
	return nil
}

func (e exemption) validate() error {
	if strings.TrimSpace(e.nm) == "" {
		return fmt.Errorf("%w: exemption has an empty name", ErrInvalidDeclaration)
	}
	if strings.TrimSpace(e.rsn) == "" {
		return fmt.Errorf("%w: exemption %q has an empty reason (a waiver must state why)", ErrInvalidDeclaration, e.nm)
	}
	if e.trigger == nil {
		return fmt.Errorf("%w: exemption %q has a nil trigger", ErrInvalidDeclaration, e.nm)
	}
	return nil
}

// none is the explicit "no controls" declaration. It always holds but appears
// in a Report as a named, reasoned KindNone entry so a genuinely stateless
// service's opt-out is visible rather than an implicit empty no-op.
type none struct{ rsn string }

// None declares that a service intentionally has no security controls, carrying
// a reason. It must be the ONLY declaration in a Set (mixing it with real
// controls is a construction error).
func None(reason string) Declaration { return none{rsn: reason} }

func (n none) name() string                     { return "(no controls)" }
func (n none) kind() Kind                       { return KindNone }
func (n none) reason() string                   { return n.rsn }
func (n none) assert(ctx context.Context) error { return nil }

func (n none) validate() error {
	if strings.TrimSpace(n.rsn) == "" {
		return fmt.Errorf("%w: None has an empty reason (state why the service has no controls)", ErrInvalidDeclaration)
	}
	return nil
}

// Status is one row of a Report: a declaration and its last evaluation result.
type Status struct {
	Name   string
	Kind   Kind
	Reason string // set for exemptions and None; empty for controls
	Result Result
	Err    error // non-nil only when Result == ResultFailed
}

// Set is a service's declared security posture: the controls, exemptions and
// (optional) None it enumerates at wiring time, plus the last result of each.
// Build one with Declare. It is safe for concurrent Hold/Report calls.
type Set struct {
	mu    sync.RWMutex
	decls []Declaration // immutable after Declare
	last  []Result      // parallel to decls
	errs  []error       // parallel to decls; nil unless the matching Result is ResultFailed
}

// Declare builds a Set from a service's declarations, validating each at
// construction time. It returns an error (never a partial Set) if:
//   - given zero declarations (ErrEmptyDeclare) -- use None(reason) to opt out;
//   - any declaration is malformed: nil Assert/trigger, empty name, empty
//     exemption reason, duplicate name, or a None mixed with real controls
//     (ErrInvalidDeclaration).
//
// All construction errors are aggregated so an operator sees the whole list.
func Declare(decls ...Declaration) (*Set, error) {
	if len(decls) == 0 {
		return nil, ErrEmptyDeclare
	}

	var cerrs []error
	seen := make(map[string]struct{}, len(decls))
	hasNone := false

	for _, d := range decls {
		if d == nil {
			cerrs = append(cerrs, fmt.Errorf("%w: nil declaration", ErrInvalidDeclaration))
			continue
		}
		if err := d.validate(); err != nil {
			cerrs = append(cerrs, err)
			continue
		}
		if d.kind() == KindNone {
			hasNone = true
		}
		if _, dup := seen[d.name()]; dup {
			cerrs = append(cerrs, fmt.Errorf("%w: duplicate declaration name %q", ErrInvalidDeclaration, d.name()))
		}
		seen[d.name()] = struct{}{}
	}

	if hasNone && len(decls) > 1 {
		cerrs = append(cerrs, fmt.Errorf("%w: posture.None must be the only declaration, but it was declared alongside other controls", ErrInvalidDeclaration))
	}

	if len(cerrs) > 0 {
		return nil, errors.Join(cerrs...)
	}

	return &Set{
		decls: decls,
		last:  make([]Result, len(decls)),
		errs:  make([]error, len(decls)),
	}, nil
}

// Hold runs every declared control and exemption and returns an aggregated
// error naming ALL failures (not just the first), or nil if the whole posture
// holds. It records each result for Report.
//
// The ctx carries the caller's boot budget: Hold imposes NO per-control timeout
// of its own -- a magic number would be exactly the "plausible default" D-162a
// fights, and only the caller knows its boot budget (root §21: 30s total). Hold
// propagates ctx to every Assert (well-behaved controls honor it) AND bounds
// each control against ctx.Done, so a control that ignores ctx cannot hang boot
// past the caller's deadline nor hide the results already gathered. A control
// that exhausts the deadline is a failure; declarations after it are left
// ResultNotRun.
func (s *Set) Hold(ctx context.Context) error {
	// decls is immutable after Declare; snapshot the header so control execution
	// does not hold the lock (a control must never be able to deadlock Report).
	s.mu.RLock()
	decls := s.decls
	s.mu.RUnlock()

	results := make([]Result, len(decls))
	errs := make([]error, len(decls))
	var failures []error

	for i, d := range decls {
		err := runBounded(ctx, d.assert)
		if err != nil {
			results[i] = ResultFailed
			errs[i] = err
			failures = append(failures, fmt.Errorf("%s %q: %w", d.kind(), d.name(), err))
			if ctx.Err() != nil {
				// Boot deadline blown: remaining declarations cannot run. Leave
				// them ResultNotRun so the Report shows exactly what was proven.
				break
			}
			continue
		}
		results[i] = ResultPassed
	}

	s.mu.Lock()
	s.last = results
	s.errs = errs
	s.mu.Unlock()

	if len(failures) > 0 {
		return fmt.Errorf("%w (%d of %d failed): %w", ErrPostureNotHeld, len(failures), len(decls), errors.Join(failures...))
	}
	return nil
}

// MustHold runs every declared control and returns an aggregated error if the
// posture does not hold. Despite the name it does NOT exit or log -- per root
// CLAUDE.md §12 and §30.3 only main.go may os.Exit / log.Fatal. The name
// signals intent: main.go is expected to fatal on a non-nil return, because a
// posture that does not hold means the process must not serve.
func (s *Set) MustHold(ctx context.Context) error { return s.Hold(ctx) }

// Report enumerates every declared control and exemption with its last result,
// so "is anything fail-open?" is a query, not an investigation. Before Hold has
// run, every Result is ResultNotRun. Safe to call concurrently.
func (s *Set) Report() []Status {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]Status, len(s.decls))
	for i, d := range s.decls {
		out[i] = Status{
			Name:   d.name(),
			Kind:   d.kind(),
			Reason: d.reason(),
			Result: s.last[i],
			Err:    s.errs[i],
		}
	}
	return out
}

// runBounded runs one assertion bounded by ctx: it returns the assertion's
// error, or a deadline error if ctx expires first, so a control that ignores
// ctx cannot hang boot past the caller's deadline. The assertion runs in a
// goroutine with a recover (root §30.4) and a buffered channel, so if ctx wins
// the race the goroutine cannot block on send.
func runBounded(ctx context.Context, fn func(context.Context) error) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("boot deadline exceeded before the control ran: %w", err)
	}

	done := make(chan error, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				done <- fmt.Errorf("control panicked: %v", r)
			}
		}()
		done <- fn(ctx)
	}()

	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return fmt.Errorf("control did not complete before the boot deadline: %w", ctx.Err())
	}
}
