package otel

import (
	"context"
	"strings"
	"testing"
)

func TestInit_NoEndpointIsNoop(t *testing.T) {
	shutdown, err := Init(context.Background(), Config{ServiceName: "svc"})
	if err != nil {
		t.Fatalf("Init with empty endpoint: unexpected err %v", err)
	}
	if shutdown == nil {
		t.Fatal("Init returned nil shutdown")
	}
	if err := shutdown(context.Background()); err != nil {
		t.Fatalf("noop shutdown: unexpected err %v", err)
	}
}

func TestSampler(t *testing.T) {
	tests := []struct {
		name     string
		ratio    float64
		wantDesc string // substring of Sampler.Description()
	}{
		{"zero -> always", 0, "AlwaysOnSampler"},
		{"negative -> always", -1, "AlwaysOnSampler"},
		{"one -> always", 1, "AlwaysOnSampler"},
		{"above one -> always", 2, "AlwaysOnSampler"},
		{"half -> ratio", 0.5, "TraceIDRatioBased{0.5}"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sampler(tt.ratio).Description()
			if !strings.Contains(got, tt.wantDesc) {
				t.Fatalf("sampler(%v).Description() = %q, want substring %q", tt.ratio, got, tt.wantDesc)
			}
		})
	}
}

func TestNewResource(t *testing.T) {
	tests := []struct {
		name       string
		cfg        Config
		wantName   string
		wantExtras map[string]string // attr key -> value that must be present
		absentKeys []string
	}{
		{
			name:       "name only",
			cfg:        Config{ServiceName: "identity-service"},
			wantName:   "identity-service",
			absentKeys: []string{"service.version", "deployment.environment"},
		},
		{
			name:       "empty name defaults",
			cfg:        Config{},
			wantName:   "unknown-service",
			absentKeys: []string{"service.version", "deployment.environment"},
		},
		{
			name:       "full",
			cfg:        Config{ServiceName: "codegen-service", ServiceVersion: "1.2.3", Environment: "dev"},
			wantName:   "codegen-service",
			wantExtras: map[string]string{"service.version": "1.2.3", "deployment.environment": "dev"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res, err := newResource(context.Background(), tt.cfg)
			if err != nil {
				t.Fatalf("newResource: %v", err)
			}
			attrs := map[string]string{}
			for _, kv := range res.Attributes() {
				attrs[string(kv.Key)] = kv.Value.AsString()
			}
			if attrs["service.name"] != tt.wantName {
				t.Fatalf("service.name = %q, want %q", attrs["service.name"], tt.wantName)
			}
			for k, v := range tt.wantExtras {
				if attrs[k] != v {
					t.Fatalf("attr %q = %q, want %q", k, attrs[k], v)
				}
			}
			for _, k := range tt.absentKeys {
				if _, ok := attrs[k]; ok {
					t.Fatalf("attr %q should be absent, got %q", k, attrs[k])
				}
			}
		})
	}
}

func TestSlogHandler_NeverNil(t *testing.T) {
	if SlogHandler("") == nil || SlogHandler("svc") == nil {
		t.Fatal("SlogHandler returned nil")
	}
}
