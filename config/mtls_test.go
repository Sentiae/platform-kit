package config

import "testing"

func TestMTLSMode(t *testing.T) {
	tests := []struct {
		name string
		env  string
		set  bool
		want string
	}{
		{"unset defaults off", "", false, MTLSModeOff},
		{"empty off", "", true, MTLSModeOff},
		{"off", "off", true, MTLSModeOff},
		{"permissive", "permissive", true, MTLSModePermissive},
		{"strict", "strict", true, MTLSModeStrict},
		{"uppercase strict", "STRICT", true, MTLSModeStrict},
		{"whitespace permissive", "  permissive  ", true, MTLSModePermissive},
		{"unknown defaults off", "bogus", true, MTLSModeOff},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.set {
				t.Setenv("APP_GRPC_MTLS_MODE", tt.env)
			} else {
				t.Setenv("APP_GRPC_MTLS_MODE", "")
				// t.Setenv can't unset; empty is the unset-equivalent here.
			}
			if got := MTLSMode(); got != tt.want {
				t.Fatalf("MTLSMode() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSPIFFEEndpointSocket(t *testing.T) {
	t.Setenv("SPIFFE_ENDPOINT_SOCKET", "unix:///tmp/agent.sock")
	if got := SPIFFEEndpointSocket(); got != "unix:///tmp/agent.sock" {
		t.Fatalf("SPIFFEEndpointSocket() = %q", got)
	}
}
