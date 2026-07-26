package otel

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"sync"
	"time"
)

// DefaultErrorWindow is the throttle window for OTel export failures: the first
// failure of a signature logs immediately, further identical failures are
// counted and re-reported at most once per window.
const DefaultErrorWindow = 5 * time.Minute

// maxErrorSignatures bounds the throttle's memory. Error strings are attacker-
// independent but not bounded (endpoints, peer addresses); past the cap every
// new signature collapses into the single otherSignature bucket rather than
// growing the map forever (so the map holds at most cap+1 entries).
const maxErrorSignatures = 64

// otherSignature is the overflow bucket used once maxErrorSignatures is reached.
const otherSignature = "<other>"

// digitsRx collapses varying numbers (ports, retry delays, byte counts) so that
// "retry after 3s" and "retry after 7s" share one signature.
var digitsRx = regexp.MustCompile(`[0-9]+`)

// errorThrottle is the global OTel error handler installed by Init.
//
// Why it exists: the OTel SDK's default handler is log.Print, which Go 1.21+
// routes through slog at INFO — so a service that has silently stopped
// exporting telemetry looks exactly like a healthy one to error-level alerting.
//
// Why it throttles: a collector that is down fails on EVERY export interval in
// EVERY service, forever. An unthrottled Error line per interval is a log flood
// that trains humans to ignore errors, which trades one false-green for
// another. So: first failure loud, then one line per window carrying the count
// of what was suppressed, then a recovery line when the failures stop.
//
// It is also the loop-breaker for the log signal specifically: a service whose
// logger tees into OTLP turns "log an export failure" into another export, so an
// unthrottled handler could feed itself. One line per window per signature
// bounds that.
type errorThrottle struct {
	window time.Duration
	now    func() time.Time
	log    func(level slog.Level, msg string, args ...any)

	mu    sync.Mutex
	state map[string]*errorState
}

type errorState struct {
	firstSeen  time.Time
	lastSeen   time.Time
	lastLogged time.Time
	// suppressed counts occurrences since the last logged line.
	suppressed int
	// total counts every occurrence since the signature was first seen.
	total int
}

func newErrorThrottle(window time.Duration, now func() time.Time, log func(slog.Level, string, ...any)) *errorThrottle {
	if window <= 0 {
		window = DefaultErrorWindow
	}
	if now == nil {
		now = time.Now
	}
	return &errorThrottle{window: window, now: now, log: log, state: map[string]*errorState{}}
}

// Handle implements otel.ErrorHandler. It reports at Error level, throttled per
// error signature.
func (t *errorThrottle) Handle(err error) {
	if err == nil {
		return
	}
	sig := signature(err)

	t.mu.Lock()
	if _, ok := t.state[sig]; !ok && len(t.state) >= maxErrorSignatures {
		sig = otherSignature
	}
	now := t.now()
	st, known := t.state[sig]
	if !known {
		st = &errorState{firstSeen: now}
		t.state[sig] = st
	}
	st.lastSeen = now
	st.total++

	logNow := !known || now.Sub(st.lastLogged) >= t.window
	suppressed := st.suppressed
	if logNow {
		st.lastLogged = now
		st.suppressed = 0
	} else {
		st.suppressed++
	}
	window := t.window
	t.mu.Unlock()

	if !logNow {
		return
	}
	t.log(slog.LevelError, "otel export failed",
		"err", err,
		"suppressed", suppressed,
		"throttle_window", window.String(),
	)
}

// sweep emits a recovery line for every signature that has been quiet for a
// full window, and drops its state so the next failure is loud again. Recovery
// is Info, not Error: the alert clears on the absence of the Error line, while
// this line is what tells a human it came back and how much was dropped.
func (t *errorThrottle) sweep() {
	type recovered struct {
		sig        string
		total      int
		suppressed int
		quietFor   time.Duration
		failingFor time.Duration
	}
	var out []recovered

	t.mu.Lock()
	now := t.now()
	for sig, st := range t.state {
		if now.Sub(st.lastSeen) < t.window {
			continue
		}
		out = append(out, recovered{
			sig:        sig,
			total:      st.total,
			suppressed: st.suppressed,
			quietFor:   now.Sub(st.lastSeen),
			failingFor: st.lastSeen.Sub(st.firstSeen),
		})
		delete(t.state, sig)
	}
	t.mu.Unlock()

	for _, r := range out {
		t.log(slog.LevelInfo, "otel export recovered",
			"signature", r.sig,
			"failures", r.total,
			"unlogged_failures", r.suppressed,
			"failing_for", r.failingFor.String(),
			"quiet_for", r.quietFor.String(),
		)
	}
}

// runSweeper ticks sweep until ctx is done, so recovery is reported even though
// the SDK never tells us an export succeeded (there is no success callback).
func (t *errorThrottle) runSweeper(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			t.log(slog.LevelError, "otel error-handler sweeper panicked", "panic", fmt.Sprint(r))
		}
	}()
	interval := t.window / 2
	if interval <= 0 {
		interval = DefaultErrorWindow / 2
	}
	tick := time.NewTicker(interval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			t.sweep()
		}
	}
}

// signature reduces an error to a stable throttle key: its concrete type plus
// its first line with numbers collapsed. Same failure mode => same key, so the
// throttle collapses a storm without hiding a genuinely different failure.
func signature(err error) string {
	msg := err.Error()
	if i := strings.IndexByte(msg, '\n'); i >= 0 {
		msg = msg[:i]
	}
	if len(msg) > 200 {
		msg = msg[:200]
	}
	return fmt.Sprintf("%T|%s", err, digitsRx.ReplaceAllString(msg, "N"))
}
