package secret

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
)

type fakeGetter struct {
	val string
	err error
	// captured
	gotPath, gotKey string
}

func (f *fakeGetter) GetSecret(_ context.Context, path, key string) (string, error) {
	f.gotPath, f.gotKey = path, key
	return f.val, f.err
}

func TestSecretValue_Redacts(t *testing.T) {
	v := SecretValue{value: "hunter2"}
	if v.Reveal() != "hunter2" {
		t.Fatalf("Reveal() = %q, want hunter2", v.Reveal())
	}
	// Every leak surface must redact.
	if got := v.String(); got != "[REDACTED]" {
		t.Fatalf("String() = %q", got)
	}
	if got := fmt.Sprintf("%s / %v", v, v); got != "[REDACTED] / [REDACTED]" {
		t.Fatalf("fmt = %q", got)
	}
	b, err := json.Marshal(struct{ S SecretValue }{v})
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != `{"S":"[REDACTED]"}` {
		t.Fatalf("json = %s", b)
	}
	if v.LogValue().String() != "[REDACTED]" {
		t.Fatalf("LogValue = %q", v.LogValue().String())
	}
}

func TestParseRef(t *testing.T) {
	tests := []struct {
		ref              string
		wantPath, wantFn string
		ok               bool
	}{
		{"sentiae/prod/app#db_password", "sentiae/prod/app", "db_password", true},
		{"a#b", "a", "b", true},
		{"a/b/c#field", "a/b/c", "field", true},
		{"nofield", "", "", false},
		{"#field", "", "", false},
		{"path#", "", "", false},
		{"", "", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.ref, func(t *testing.T) {
			p, f, ok := parseRef(tt.ref)
			if ok != tt.ok || p != tt.wantPath || f != tt.wantFn {
				t.Fatalf("parseRef(%q) = (%q,%q,%v), want (%q,%q,%v)", tt.ref, p, f, ok, tt.wantPath, tt.wantFn, tt.ok)
			}
		})
	}
}

func TestVaultResolver_Resolve(t *testing.T) {
	t.Run("happy path reveals + hits the right path/field", func(t *testing.T) {
		fg := &fakeGetter{val: "s3cr3t"}
		r := NewVaultResolver(fg)
		v, err := r.Resolve(context.Background(), "sentiae/prod/app#db_password", Principal{Service: "delivery"})
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		if v.Reveal() != "s3cr3t" {
			t.Fatalf("Reveal = %q", v.Reveal())
		}
		if fg.gotPath != "sentiae/prod/app" || fg.gotKey != "db_password" {
			t.Fatalf("vault got path=%q key=%q", fg.gotPath, fg.gotKey)
		}
	})

	t.Run("invalid ref", func(t *testing.T) {
		r := NewVaultResolver(&fakeGetter{})
		_, err := r.Resolve(context.Background(), "no-hash", Principal{Service: "delivery"})
		if !errors.Is(err, ErrInvalidSecretRef) {
			t.Fatalf("want ErrInvalidSecretRef, got %v", err)
		}
	})

	t.Run("not found maps to ErrSecretNotFound", func(t *testing.T) {
		r := NewVaultResolver(&fakeGetter{err: errors.New("secret not found at path")})
		_, err := r.Resolve(context.Background(), "a/b#c", Principal{Service: "runtime-fleet"})
		if !errors.Is(err, ErrSecretNotFound) {
			t.Fatalf("want ErrSecretNotFound, got %v", err)
		}
	})

	t.Run("other error wrapped, ref included, no value leak", func(t *testing.T) {
		r := NewVaultResolver(&fakeGetter{err: errors.New("connection refused")})
		_, err := r.Resolve(context.Background(), "a/b#c", Principal{Service: "node-service"})
		if err == nil || errors.Is(err, ErrSecretNotFound) {
			t.Fatalf("want a wrapped non-notfound error, got %v", err)
		}
	})
}
