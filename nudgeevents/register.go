package nudgeevents

import (
	"context"
	"fmt"
)

// SchemaRegistrar is the subset of platform-kit/kafka.SchemaRegistry that we
// need for boot-time registration. Defined locally to avoid an import cycle
// and to keep this package import-light.
type SchemaRegistrar interface {
	RegisterSchema(ctx context.Context, subject, schema string) (int, error)
}

// Register publishes every nudge event schema to the schema registry. Idempotent
// — the registry assigns the same id when called repeatedly with an unchanged
// schema. Call once on foundry-service boot.
//
// Subject convention: the CloudEvent type doubles as the subject so consumers
// can look up the schema by topic name without a separate mapping.
func Register(ctx context.Context, r SchemaRegistrar) error {
	for _, t := range AllTypes() {
		schema, ok := Schemas[t]
		if !ok {
			return fmt.Errorf("nudgeevents: missing schema for %s", t)
		}
		if _, err := r.RegisterSchema(ctx, string(t), schema); err != nil {
			return fmt.Errorf("nudgeevents: register %s: %w", t, err)
		}
	}
	return nil
}
