package atlas_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/sentiae/platform-kit/atlas"
)

// testModel is a minimal GORM model for testing the loader.
type testModel struct {
	ID   uint   `gorm:"primarykey"`
	Name string `gorm:"size:255"`
}

func (testModel) TableName() string { return "test_models" }

func TestLoadAndWrite_Postgres(t *testing.T) {
	var buf bytes.Buffer
	err := atlas.LoadAndWrite(&buf, "postgres", &testModel{})
	if err != nil {
		t.Fatalf("LoadAndWrite failed: %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, "test_models") {
		t.Errorf("expected DDL to contain table name 'test_models', got:\n%s", output)
	}
	if !strings.Contains(output, "CREATE") {
		t.Errorf("expected DDL to contain CREATE statement, got:\n%s", output)
	}
}

func TestLoadAndWrite_InvalidDialect(t *testing.T) {
	var buf bytes.Buffer
	err := atlas.LoadAndWrite(&buf, "invalid_dialect", &testModel{})
	if err == nil {
		t.Fatal("expected error for invalid dialect, got nil")
	}
}

func TestLoadAndWrite_NoModels(t *testing.T) {
	var buf bytes.Buffer
	err := atlas.LoadAndWrite(&buf, "postgres")
	// Loading no models should succeed (empty schema) or error — either is acceptable
	// but it should not panic
	_ = err
}

func TestLoadAndWrite_MultipleModels(t *testing.T) {
	type secondModel struct {
		ID    uint   `gorm:"primarykey"`
		Email string `gorm:"size:255;uniqueIndex"`
	}

	var buf bytes.Buffer
	err := atlas.LoadAndWrite(&buf, "postgres", &testModel{}, &secondModel{})
	if err != nil {
		t.Fatalf("LoadAndWrite failed: %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, "test_models") {
		t.Errorf("expected DDL to contain 'test_models', got:\n%s", output)
	}
	if !strings.Contains(output, "second_models") {
		t.Errorf("expected DDL to contain 'second_models', got:\n%s", output)
	}
}
