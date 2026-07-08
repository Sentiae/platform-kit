package grpcclient

import (
	"context"
	"testing"
)

// TestDial_OffModeInsecure proves that Mode "off" (and empty, and nil Source)
// dials with insecure credentials — today's behavior — and never touches
// SPIFFE (calling spiffe.ClientCreds with a nil source would panic). grpc.NewClient
// is lazy so no server is needed.
func TestDial_OffModeInsecure(t *testing.T) {
	cases := []struct {
		name string
		cfg  Config
	}{
		{"empty mode", Config{Endpoint: "passthrough:///x"}},
		{"off mode", Config{Endpoint: "passthrough:///x", Mode: "off"}},
		{"permissive but nil source", Config{Endpoint: "passthrough:///x", Mode: "permissive", ServerService: "foundry"}},
		{"strict but nil source", Config{Endpoint: "passthrough:///x", Mode: "strict", ServerService: "foundry"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			conn, err := Dial(context.Background(), tc.cfg)
			if err != nil {
				t.Fatalf("Dial: %v", err)
			}
			if conn == nil {
				t.Fatal("expected non-nil conn")
			}
			_ = conn.Close()
		})
	}
}
