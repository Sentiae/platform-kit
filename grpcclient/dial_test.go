package grpcclient

import (
	"context"
	"strings"
	"testing"
)

// TestDial_TransportSecurity proves the fail-closed posture (D-162a L2):
// off/empty and permissive with a nil Source dial insecure (permissive is the
// declared escape hatch), while strict with a nil Source is REFUSED — Dial
// returns an error naming "strict" and never a conn, rather than silently
// dialing plaintext under a "strict" posture. grpc.NewClient is lazy so no
// server is needed; SPIFFE is never touched (a nil source would panic in
// spiffe.ClientCreds).
func TestDial_TransportSecurity(t *testing.T) {
	cases := []struct {
		name    string
		cfg     Config
		wantErr string // substring the error must contain; "" means expect a conn
	}{
		{"empty mode", Config{Endpoint: "passthrough:///x"}, ""},
		{"off mode", Config{Endpoint: "passthrough:///x", Mode: "off"}, ""},
		{"permissive but nil source", Config{Endpoint: "passthrough:///x", Mode: "permissive", ServerService: "foundry"}, ""},
		{"strict but nil source", Config{Endpoint: "passthrough:///x", Mode: "strict", ServerService: "foundry"}, "strict"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			conn, err := Dial(context.Background(), tc.cfg)
			if tc.wantErr != "" {
				if err == nil {
					_ = conn.Close()
					t.Fatalf("expected Dial to fail-closed, got nil error and conn %v", conn)
				}
				if conn != nil {
					_ = conn.Close()
					t.Fatalf("expected nil conn on fail-closed dial, got %v", conn)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error %q does not name %q", err.Error(), tc.wantErr)
				}
				return
			}
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
