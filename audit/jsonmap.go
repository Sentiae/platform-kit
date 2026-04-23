package audit

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"errors"
)

// Value implements the driver.Valuer interface so GORM can persist the
// metadata map into a jsonb column.
func (m JSONMap) Value() (driver.Value, error) {
	if m == nil {
		return nil, nil
	}
	return json.Marshal(m)
}

// Scan implements the sql.Scanner interface so GORM can load jsonb
// data back into the map type.
func (m *JSONMap) Scan(value any) error {
	if value == nil {
		*m = nil
		return nil
	}
	var raw []byte
	switch v := value.(type) {
	case []byte:
		raw = v
	case string:
		raw = []byte(v)
	default:
		return errors.New("audit.JSONMap: unsupported Scan value type")
	}
	if len(raw) == 0 {
		*m = nil
		return nil
	}
	return json.Unmarshal(raw, m)
}

// Noop is a Recorder that discards everything. Use in tests / offline
// paths where audit is optional.
type Noop struct{}

// Record always returns nil.
func (Noop) Record(_ context.Context, _ ActorRef, _ string, _ TargetRef, _ map[string]any) error {
	return nil
}
