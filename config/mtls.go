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
// Unset or unrecognized values return MTLSModeOff, so the mesh stays in
// today's insecure-by-default posture until explicitly switched on.
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
