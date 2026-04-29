package nudgeevents

import (
	"context"
	"encoding/json"
	"testing"
)

type fakeRegistrar struct {
	calls map[string]string
}

func (f *fakeRegistrar) RegisterSchema(_ context.Context, subject, schema string) (int, error) {
	if f.calls == nil {
		f.calls = make(map[string]string)
	}
	f.calls[subject] = schema
	return 1, nil
}

func TestRegisterAllTypes(t *testing.T) {
	r := &fakeRegistrar{}
	if err := Register(context.Background(), r); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if got, want := len(r.calls), len(AllTypes()); got != want {
		t.Fatalf("registered %d subjects, want %d", got, want)
	}
	for _, tp := range AllTypes() {
		schema, ok := r.calls[string(tp)]
		if !ok {
			t.Fatalf("missing subject %s", tp)
		}
		var parsed map[string]any
		if err := json.Unmarshal([]byte(schema), &parsed); err != nil {
			t.Fatalf("schema for %s is not valid JSON: %v", tp, err)
		}
		if parsed["type"] != "object" {
			t.Fatalf("schema for %s missing object type", tp)
		}
	}
}

func TestSchemasCoverAllTypes(t *testing.T) {
	for _, tp := range AllTypes() {
		if _, ok := Schemas[tp]; !ok {
			t.Fatalf("Schemas missing entry for %s", tp)
		}
	}
}
