// Package timetravel — DiffSnapshots computes the field-level
// difference between two point-in-time snapshots of the same entity.
//
// The recorder writes one row per mutation into entity_snapshots, and
// each row carries the entity's JSON payload at ValidFrom. DiffSnapshots
// loads the snapshots active at the two requested times, unmarshals
// both payloads into generic map[string]any trees, and walks them
// recursively to produce a flat list of FieldDiff entries. Nested
// objects surface as dotted paths (e.g. "metadata.owner.email"); array
// entries surface as "items[0].status".
//
// The implementation deliberately avoids pulling in a third-party
// JSON-diff library so the platform-kit stays dependency-light and the
// exact diff semantics are visible to callers. Service-specific ops
// (like reordering arrays) can wrap this with their own post-processor.
package timetravel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"time"

	"gorm.io/gorm"
)

// FieldDiff is one changed field between two entity snapshots. Path is
// a dotted/bracketed JSON path rooted at the payload object; OldValue
// and NewValue carry the decoded JSON values (primitive, []any, or
// map[string]any). For fields added only in the To snapshot, OldValue
// is nil; for fields removed, NewValue is nil.
type FieldDiff struct {
	Path     string `json:"path"`
	OldValue any    `json:"old_value"`
	NewValue any    `json:"new_value"`
	// Op is one of "added", "removed", "changed" so renderers can
	// color-code without re-deriving from OldValue/NewValue presence.
	Op string `json:"op"`
}

// SnapshotDiff is the full response payload returned by DiffSnapshots.
// From and To are the raw snapshot rows that were chosen by ValidFrom
// ≤ requested ≤ ValidTo; FieldDiffs is the sorted flat diff.
type SnapshotDiff struct {
	Kind       string         `json:"kind"`
	EntityID   string         `json:"entity_id"`
	From       EntitySnapshot `json:"from"`
	To         EntitySnapshot `json:"to"`
	RequestedFrom time.Time   `json:"requested_from"`
	RequestedTo   time.Time   `json:"requested_to"`
	FieldDiffs []FieldDiff    `json:"field_diffs"`
}

// SnapshotLoader is the narrow contract DiffSnapshots needs. The
// GORMRecorder in this package satisfies it automatically; callers
// backed by other stores (e.g. Kafka+consumer writing to a different
// schema) can supply a shim.
type SnapshotLoader interface {
	// LoadSnapshotAt returns the snapshot row whose validity window
	// covers at — i.e. ValidFrom <= at AND (ValidTo IS NULL OR ValidTo > at).
	// ErrSnapshotNotFound is returned when no row covers the point.
	LoadSnapshotAt(ctx context.Context, kind, id string, at time.Time) (*EntitySnapshot, error)
}

// ErrSnapshotNotFound is returned by SnapshotLoader implementations
// when no row covers the requested point-in-time.
var ErrSnapshotNotFound = errors.New("entity snapshot not found at requested time")

// LoadSnapshotAt implements SnapshotLoader on the GORMRecorder so
// callers can reuse a single Recorder instance for both write and read.
func (r *GORMRecorder) LoadSnapshotAt(ctx context.Context, kind, id string, at time.Time) (*EntitySnapshot, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("gorm recorder not configured")
	}
	var row EntitySnapshot
	err := r.db.WithContext(ctx).
		Where("kind = ? AND entity_id = ? AND valid_from <= ? AND (valid_to IS NULL OR valid_to > ?)",
			kind, id, at, at).
		Order("valid_from DESC").
		First(&row).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrSnapshotNotFound
		}
		return nil, fmt.Errorf("load snapshot: %w", err)
	}
	return &row, nil
}

// DiffSnapshots loads the snapshots covering from and to, unmarshals
// their JSON payloads, and returns a flat FieldDiff list ordered by
// path. Identical snapshots produce an empty FieldDiffs slice. Callers
// providing a nil loader receive an error.
func DiffSnapshots(ctx context.Context, loader SnapshotLoader, kind, id string, from, to time.Time) (*SnapshotDiff, error) {
	if loader == nil {
		return nil, errors.New("snapshot loader is required")
	}
	if kind == "" || id == "" {
		return nil, errors.New("kind and id are required")
	}
	fromSnap, err := loader.LoadSnapshotAt(ctx, kind, id, from)
	if err != nil {
		return nil, fmt.Errorf("from snapshot: %w", err)
	}
	toSnap, err := loader.LoadSnapshotAt(ctx, kind, id, to)
	if err != nil {
		return nil, fmt.Errorf("to snapshot: %w", err)
	}
	var fromPayload, toPayload any
	if len(fromSnap.Payload) > 0 {
		if uerr := json.Unmarshal(fromSnap.Payload, &fromPayload); uerr != nil {
			return nil, fmt.Errorf("decode from payload: %w", uerr)
		}
	}
	if len(toSnap.Payload) > 0 {
		if uerr := json.Unmarshal(toSnap.Payload, &toPayload); uerr != nil {
			return nil, fmt.Errorf("decode to payload: %w", uerr)
		}
	}
	diffs := diffValues("", fromPayload, toPayload)
	sort.Slice(diffs, func(i, j int) bool { return diffs[i].Path < diffs[j].Path })
	return &SnapshotDiff{
		Kind:          kind,
		EntityID:      id,
		From:          *fromSnap,
		To:            *toSnap,
		RequestedFrom: from,
		RequestedTo:   to,
		FieldDiffs:    diffs,
	}, nil
}

// DiffPayloads is the raw map-level diff engine DiffSnapshots builds
// on. It's exported so callers that already have decoded payloads (e.g.
// snapshot tests, other services whose payloads are already in memory)
// can reuse the algorithm without round-tripping through the DB.
func DiffPayloads(from, to any) []FieldDiff {
	diffs := diffValues("", from, to)
	sort.Slice(diffs, func(i, j int) bool { return diffs[i].Path < diffs[j].Path })
	return diffs
}

// diffValues recursively compares two JSON values and appends FieldDiff
// entries to out. Maps are compared key-by-key (missing keys become
// added/removed); slices are compared position-by-position (length
// mismatch surfaces as added/removed on the trailing indices); scalars
// compare by Go ==, with JSON-decoded numbers (float64) and booleans
// behaving naturally.
func diffValues(path string, from, to any) []FieldDiff {
	// Fast path: fully equal values produce no diff.
	if jsonEqual(from, to) {
		return nil
	}
	// Type divergence counts as a single changed field (e.g. string -> object).
	fromMap, fromIsMap := from.(map[string]any)
	toMap, toIsMap := to.(map[string]any)
	if fromIsMap && toIsMap {
		return diffMaps(path, fromMap, toMap)
	}
	fromSlice, fromIsSlice := from.([]any)
	toSlice, toIsSlice := to.([]any)
	if fromIsSlice && toIsSlice {
		return diffSlices(path, fromSlice, toSlice)
	}
	return []FieldDiff{{
		Path:     pathOrRoot(path),
		OldValue: from,
		NewValue: to,
		Op:       opFor(from, to),
	}}
}

func diffMaps(path string, from, to map[string]any) []FieldDiff {
	keys := make(map[string]struct{}, len(from)+len(to))
	for k := range from {
		keys[k] = struct{}{}
	}
	for k := range to {
		keys[k] = struct{}{}
	}
	sorted := make([]string, 0, len(keys))
	for k := range keys {
		sorted = append(sorted, k)
	}
	sort.Strings(sorted)
	var out []FieldDiff
	for _, k := range sorted {
		fv, fOk := from[k]
		tv, tOk := to[k]
		child := joinPath(path, k)
		switch {
		case !fOk && tOk:
			out = append(out, FieldDiff{Path: child, OldValue: nil, NewValue: tv, Op: "added"})
		case fOk && !tOk:
			out = append(out, FieldDiff{Path: child, OldValue: fv, NewValue: nil, Op: "removed"})
		default:
			out = append(out, diffValues(child, fv, tv)...)
		}
	}
	return out
}

func diffSlices(path string, from, to []any) []FieldDiff {
	var out []FieldDiff
	maxLen := len(from)
	if len(to) > maxLen {
		maxLen = len(to)
	}
	for i := 0; i < maxLen; i++ {
		idxPath := path + "[" + strconv.Itoa(i) + "]"
		switch {
		case i >= len(from):
			out = append(out, FieldDiff{Path: idxPath, OldValue: nil, NewValue: to[i], Op: "added"})
		case i >= len(to):
			out = append(out, FieldDiff{Path: idxPath, OldValue: from[i], NewValue: nil, Op: "removed"})
		default:
			out = append(out, diffValues(idxPath, from[i], to[i])...)
		}
	}
	return out
}

// jsonEqual compares two JSON-decoded values. Re-marshals both sides
// and byte-compares so map key ordering doesn't spuriously diff.
func jsonEqual(a, b any) bool {
	if a == nil && b == nil {
		return true
	}
	ab, aerr := json.Marshal(a)
	bb, berr := json.Marshal(b)
	if aerr != nil || berr != nil {
		return false
	}
	if len(ab) != len(bb) {
		return false
	}
	for i := range ab {
		if ab[i] != bb[i] {
			return false
		}
	}
	return true
}

func joinPath(base, segment string) string {
	if base == "" {
		return segment
	}
	return base + "." + segment
}

func pathOrRoot(p string) string {
	if p == "" {
		return "$"
	}
	return p
}

func opFor(from, to any) string {
	switch {
	case from == nil && to != nil:
		return "added"
	case from != nil && to == nil:
		return "removed"
	default:
		return "changed"
	}
}
