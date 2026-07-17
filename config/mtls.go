package config

import (
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
// An unrecognized non-empty value also returns MTLSModeOff, and that is a
// KNOWN GAP, not a design choice: a typo (APP_GRPC_MTLS_MODE=stric) silently
// disables mesh mTLS fleet-wide — the operator asked for a mode and got none,
// with no signal. This getter cannot close it. Rejecting a bad value belongs
// at boot, once, not in a getter called from 66 sites across 20 services; the
// closure is the boot-time posture assertion (D-162a L1/L3), which validates
// the variable and refuses to start on an unrecognized value.
//
// Tracked as #mtls-mode-typo-disables-mesh. Until that lands, an unrecognized
// value here means "off" and nothing says so at runtime.
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

// SPIFFEEndpointSocket returns the SPIFFE Workload API endpoint from
// SPIFFE_ENDPOINT_SOCKET (empty when unset). go-spiffe's workloadapi reads
// this variable directly; this passthrough lets services report/validate it.
func SPIFFEEndpointSocket() string {
	return os.Getenv("SPIFFE_ENDPOINT_SOCKET")
}
