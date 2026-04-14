// Package debug — Phase 8 shared pprof mounting.
//
// Every Sentiae Go service benefits from on-demand pprof, but leaving
// it wired unconditionally leaks runtime internals. Gate it behind
// SENTIAE_DEBUG_PPROF=1 (or per-service overrides); callers decide
// which mux to register onto so HTTP services with chi and pure
// net/http services can both use it.
package debug

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/pprof"
	"os"
	"strings"
	"time"
)

// MountHTTPHandler registers the six standard pprof handlers on a
// `HandleFunc`-capable mux. Safe to call unconditionally — it no-ops
// when pprof is disabled.
func MountHTTPHandler(mux interface {
	HandleFunc(pattern string, handler func(http.ResponseWriter, *http.Request))
}, envVars ...string) {
	if !enabled(envVars...) {
		return
	}
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
}

// Enabled reports whether any of the supplied env-vars (or the
// shared SENTIAE_DEBUG_PPROF default) enables pprof.
func Enabled(envVars ...string) bool { return enabled(envVars...) }

func enabled(envVars ...string) bool {
	all := append([]string{}, envVars...)
	all = append(all, "SENTIAE_DEBUG_PPROF")
	for _, k := range all {
		v := strings.ToLower(strings.TrimSpace(os.Getenv(k)))
		if v == "1" || v == "true" || v == "yes" {
			return true
		}
	}
	return false
}

// StartPprofServer spawns a dedicated HTTP listener on the configured
// pprof port (default 6060) when SENTIAE_DEBUG_PPROF — or one of the
// caller-specified env vars — is truthy. Returns a shutdown function
// the main goroutine can invoke on SIGTERM.
//
// This is the zero-coupling option: services don't touch their main
// router; they just call StartPprofServer(ctx) after config is loaded.
func StartPprofServer(ctx context.Context, envVars ...string) (shutdown func() error) {
	if !enabled(envVars...) {
		return func() error { return nil }
	}
	addr := os.Getenv("SENTIAE_DEBUG_PPROF_ADDR")
	if addr == "" {
		addr = "127.0.0.1:6060"
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)

	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		slog.InfoContext(ctx, "pprof listening", "addr", addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.WarnContext(ctx, "pprof listener stopped", "err", err)
		}
	}()
	return func() error {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	}
}
