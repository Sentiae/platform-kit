package config

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

type reloadableCfg struct {
	LogLevel  string `reloadable:"true"`
	Port      string // NOT reloadable — secrets/binding
	Nested    reloadableNested
	NestedPtr *reloadableNested
}

type reloadableNested struct {
	RateLimit int `reloadable:"true"`
	Broker    string
}

func TestApplyReloadable_OnlySwapsTaggedFields(t *testing.T) {
	dst := &reloadableCfg{
		LogLevel: "info",
		Port:     "8080",
		Nested:   reloadableNested{RateLimit: 10, Broker: "kafka-a"},
	}
	src := &reloadableCfg{
		LogLevel: "debug",
		Port:     "9090",
		Nested:   reloadableNested{RateLimit: 99, Broker: "kafka-b"},
	}

	if err := ApplyReloadable(dst, src); err != nil {
		t.Fatalf("ApplyReloadable: %v", err)
	}

	if dst.LogLevel != "debug" {
		t.Errorf("LogLevel should have hot-reloaded to debug, got %q", dst.LogLevel)
	}
	if dst.Port != "8080" {
		t.Errorf("Port should NOT have changed, got %q", dst.Port)
	}
	if dst.Nested.RateLimit != 99 {
		t.Errorf("Nested.RateLimit should have reloaded to 99, got %d", dst.Nested.RateLimit)
	}
	if dst.Nested.Broker != "kafka-a" {
		t.Errorf("Nested.Broker should NOT have changed, got %q", dst.Nested.Broker)
	}
}

func TestApplyReloadable_NilPointerFields(t *testing.T) {
	dst := &reloadableCfg{}
	src := &reloadableCfg{LogLevel: "warn"}
	if err := ApplyReloadable(dst, src); err != nil {
		t.Fatalf("ApplyReloadable nil ptr: %v", err)
	}
	if dst.LogLevel != "warn" {
		t.Errorf("expected warn, got %q", dst.LogLevel)
	}
}

func TestApplyReloadable_TypeMismatch(t *testing.T) {
	type other struct{}
	if err := ApplyReloadable(&reloadableCfg{}, &other{}); err == nil {
		t.Error("expected type-mismatch error")
	}
}

// Writes a temp config file and re-reads it on change. We don't actually
// fire fsnotify here (that's covered by the HotReloader unit test); this
// exercises InstallHotReload's wiring.
func TestInstallHotReload_ReloaderConstructs(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("log_level: info\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	dst := &reloadableCfg{LogLevel: "info"}

	ctx, cancel := context.WithCancel(context.Background())
	stop, err := InstallHotReload(ctx, dst, func() (any, error) {
		return &reloadableCfg{LogLevel: "debug"}, nil
	}, []string{path}, 100)
	if err != nil {
		t.Fatalf("InstallHotReload: %v", err)
	}
	defer stop()
	cancel()
}
