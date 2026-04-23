package timetravel

import (
	"context"
	"testing"
	"time"
)

func TestDiffPayloads_NoChange(t *testing.T) {
	from := map[string]any{"name": "Alpha", "count": float64(3)}
	to := map[string]any{"name": "Alpha", "count": float64(3)}
	diffs := DiffPayloads(from, to)
	if len(diffs) != 0 {
		t.Fatalf("expected no diffs, got %d: %+v", len(diffs), diffs)
	}
}

func TestDiffPayloads_SingleFieldChange(t *testing.T) {
	from := map[string]any{"name": "Alpha", "count": float64(3)}
	to := map[string]any{"name": "Beta", "count": float64(3)}
	diffs := DiffPayloads(from, to)
	if len(diffs) != 1 {
		t.Fatalf("expected 1 diff, got %d: %+v", len(diffs), diffs)
	}
	d := diffs[0]
	if d.Path != "name" {
		t.Fatalf("path: got %q want %q", d.Path, "name")
	}
	if d.OldValue != "Alpha" || d.NewValue != "Beta" {
		t.Fatalf("values: got %+v", d)
	}
	if d.Op != "changed" {
		t.Fatalf("op: got %q want changed", d.Op)
	}
}

func TestDiffPayloads_MultipleFieldChanges(t *testing.T) {
	from := map[string]any{
		"name":   "Alpha",
		"status": "draft",
		"tags":   []any{"a", "b"},
	}
	to := map[string]any{
		"name":   "Alpha",
		"status": "published",
		"tags":   []any{"a", "b", "c"},
		"owner":  "alice",
	}
	diffs := DiffPayloads(from, to)
	if len(diffs) != 3 {
		t.Fatalf("expected 3 diffs, got %d: %+v", len(diffs), diffs)
	}
	paths := make(map[string]FieldDiff, len(diffs))
	for _, d := range diffs {
		paths[d.Path] = d
	}
	if d, ok := paths["status"]; !ok || d.Op != "changed" || d.OldValue != "draft" || d.NewValue != "published" {
		t.Fatalf("status diff wrong: %+v", d)
	}
	if d, ok := paths["owner"]; !ok || d.Op != "added" || d.OldValue != nil || d.NewValue != "alice" {
		t.Fatalf("owner diff wrong: %+v", d)
	}
	if d, ok := paths["tags[2]"]; !ok || d.Op != "added" || d.NewValue != "c" {
		t.Fatalf("tags[2] diff wrong: %+v", d)
	}
}

func TestDiffPayloads_NestedJSONChange(t *testing.T) {
	from := map[string]any{
		"metadata": map[string]any{
			"owner": map[string]any{
				"name":  "alice",
				"email": "a@example.com",
			},
			"tier": "gold",
		},
	}
	to := map[string]any{
		"metadata": map[string]any{
			"owner": map[string]any{
				"name":  "alice",
				"email": "alice@example.com",
			},
			"tier": "platinum",
		},
	}
	diffs := DiffPayloads(from, to)
	if len(diffs) != 2 {
		t.Fatalf("expected 2 diffs, got %d: %+v", len(diffs), diffs)
	}
	var gotEmail, gotTier bool
	for _, d := range diffs {
		if d.Path == "metadata.owner.email" {
			gotEmail = true
			if d.OldValue != "a@example.com" || d.NewValue != "alice@example.com" {
				t.Fatalf("email diff values: %+v", d)
			}
		}
		if d.Path == "metadata.tier" {
			gotTier = true
			if d.OldValue != "gold" || d.NewValue != "platinum" {
				t.Fatalf("tier diff values: %+v", d)
			}
		}
	}
	if !gotEmail || !gotTier {
		t.Fatalf("missing expected nested diffs: %+v", diffs)
	}
}

func TestDiffPayloads_RemovedField(t *testing.T) {
	from := map[string]any{"name": "Alpha", "archived": false}
	to := map[string]any{"name": "Alpha"}
	diffs := DiffPayloads(from, to)
	if len(diffs) != 1 {
		t.Fatalf("expected 1 diff, got %d: %+v", len(diffs), diffs)
	}
	if diffs[0].Path != "archived" || diffs[0].Op != "removed" {
		t.Fatalf("diff: %+v", diffs[0])
	}
}

func TestDiffSnapshots_EndToEnd(t *testing.T) {
	db := newTestDB(t)
	rec := NewGORMRecorder(db, "ops-service", nil)
	ctx := context.Background()

	t0 := time.Now().UTC()
	if err := rec.RecordEntity(ctx, "deployment", "dep-7", map[string]any{
		"status":  "pending",
		"version": "1.0.0",
	}); err != nil {
		t.Fatalf("first record: %v", err)
	}
	// Give the in-memory clock a beat so valid_to/valid_from windows don't collide.
	time.Sleep(10 * time.Millisecond)
	t1 := time.Now().UTC()
	if err := rec.RecordEntity(ctx, "deployment", "dep-7", map[string]any{
		"status":  "succeeded",
		"version": "1.0.1",
	}); err != nil {
		t.Fatalf("second record: %v", err)
	}
	time.Sleep(10 * time.Millisecond)
	t2 := time.Now().UTC()

	diff, err := DiffSnapshots(ctx, rec, "deployment", "dep-7", t0.Add(5*time.Millisecond), t2)
	if err != nil {
		t.Fatalf("DiffSnapshots: %v", err)
	}
	if diff.Kind != "deployment" || diff.EntityID != "dep-7" {
		t.Fatalf("kind/id wrong on response: %+v", diff)
	}
	if !diff.RequestedFrom.Before(t1) || !diff.RequestedTo.After(t1) {
		t.Fatalf("requested bounds wrong: %v..%v (t1=%v)", diff.RequestedFrom, diff.RequestedTo, t1)
	}
	if len(diff.FieldDiffs) != 2 {
		t.Fatalf("expected 2 field diffs, got %d: %+v", len(diff.FieldDiffs), diff.FieldDiffs)
	}
	paths := map[string]FieldDiff{}
	for _, d := range diff.FieldDiffs {
		paths[d.Path] = d
	}
	if paths["status"].OldValue != "pending" || paths["status"].NewValue != "succeeded" {
		t.Fatalf("status diff wrong: %+v", paths["status"])
	}
	if paths["version"].OldValue != "1.0.0" || paths["version"].NewValue != "1.0.1" {
		t.Fatalf("version diff wrong: %+v", paths["version"])
	}
}
