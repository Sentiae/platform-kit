package config

import (
	"fmt"
	"os"
	"strings"
)

// gRPC mesh mTLS modes, read from APP_GRPC_MTLS_MODE.
//
//   - off        — no mTLS; dial insecure (today's behavior; the default).
//   - permissive — present/verify SVIDs when available but tolerate plaintext.
//   - strict     — require mTLS on the mesh.
//
// The mode is a plain string so grpcclient.Config.Mode can consume it without
// a type dependency.
const (
	MTLSModeOff        = "off"
	MTLSModePermissive = "permissive"
	MTLSModeStrict     = "strict"
)

// MTLSMode returns the parsed gRPC mesh mTLS mode from APP_GRPC_MTLS_MODE.
//
// Unset returns MTLSModeOff. That is deliberate: the mesh stays in today's
// insecure-by-default posture until explicitly switched on, and flipping the
// unset case here would change every service's transport at once.
//
// An unrecognized non-empty value also returns MTLSModeOff, but that no longer
// silently disables the mesh: this getter is a post-boot reader called from
// ~66 sites across 20 services and cannot return an error, so the typo check
// lives at boot in ValidateMTLSMode, which config.Load calls before any service
// serves. A typo (APP_GRPC_MTLS_MODE=stric) now refuses boot rather than
// degrading to "off" with no signal; by the time this getter runs, the value
// has already been proven recognized. Closes #mtls-mode-typo-disables-mesh.
func MTLSMode() string {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("APP_GRPC_MTLS_MODE"))) {
	case MTLSModePermissive:
		return MTLSModePermissive
	case MTLSModeStrict:
		return MTLSModeStrict
	default:
		return MTLSModeOff
	}
}

// ValidateMTLSMode is the boot-time closure for APP_GRPC_MTLS_MODE (D-162a
// L1/L3: no security posture is selected by the ABSENCE — or misspelling — of a
// value). config.Load calls it so every service fails fast on an unrecognized
// mode instead of silently serving plaintext under a mistyped "strict".
//
//   - unset/empty → nil. "off" is the intended default; an operator who set
//     nothing asked for nothing, which is a recognized, legal state.
//   - off | permissive | strict (case-insensitive, trimmed) → nil.
//   - anything else → an error naming the bad value and the valid set.
//
// It reads APP_GRPC_MTLS_MODE directly (as MTLSMode does), independent of any
// service's config prefix, so the one mesh variable is validated identically
// fleet-wide.
func ValidateMTLSMode() error {
	raw := os.Getenv("APP_GRPC_MTLS_MODE")
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", MTLSModeOff, MTLSModePermissive, MTLSModeStrict:
		return nil
	default:
		return fmt.Errorf("invalid APP_GRPC_MTLS_MODE %q: must be one of %q, %q, %q (or unset for %q)",
			raw, MTLSModeOff, MTLSModePermissive, MTLSModeStrict, MTLSModeOff)
	}
}

// SPIFFEEndpointSocket returns the SPIFFE Workload API endpoint from
// SPIFFE_ENDPOINT_SOCKET (empty when unset). go-spiffe's workloadapi reads
// this variable directly; this passthrough lets services report/validate it.
func SPIFFEEndpointSocket() string {
	return os.Getenv("SPIFFE_ENDPOINT_SOCKET")
}
