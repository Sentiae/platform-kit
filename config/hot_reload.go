package config

import (
	"fmt"
	"log"
	"reflect"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

// NOTE on usage:
//
//   - OnReload on config.Options is the ergonomic way to wire hot-reload
//     from a service.Load call site.  Internally it builds a
//     HotReloader and runs Start in a background goroutine.
//
//   - MustStartHotReloader is the standalone helper for services that
//     manage their own config reload lifecycle (e.g. services that
//     re-Load only specific reloadable fields without a full struct
//     rebuild). See the function comment for a usage sketch.
//
//   - ApplyReloadable is the safety valve: it copies only fields
//     tagged `reloadable:"true"` so secrets and pool sizes never
//     hot-swap under live traffic.


// HotReloader watches config files for changes and invokes a
// callback when a modification is detected. Only safe-to-reload
// values (feature flags, log levels, non-secret strings) should
// be driven by hot-reload; connection-pool sizes and secrets
// require a restart.
type HotReloader struct {
	watcher  *fsnotify.Watcher
	paths    []string
	onChange func()
	debounce time.Duration
	mu       sync.Mutex
	lastFire time.Time
}

// NewHotReloader builds a config hot-reloader. Pass the file paths
// to watch and a callback that re-parses the config into the live
// struct. The debounce prevents rapid-fire reloads when editors do
// a write+rename dance.
func NewHotReloader(paths []string, onChange func(), debounce time.Duration) (*HotReloader, error) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	if debounce <= 0 {
		debounce = 2 * time.Second
	}
	for _, p := range paths {
		if err := watcher.Add(p); err != nil {
			log.Printf("hot-reload: cannot watch %s: %v (skipped)", p, err)
		}
	}
	return &HotReloader{
		watcher:  watcher,
		paths:    paths,
		onChange: onChange,
		debounce: debounce,
	}, nil
}

// Start runs the watcher loop. Blocks until the context/watcher is
// closed; call in a goroutine.
func (h *HotReloader) Start() {
	log.Printf("config hot-reload watching %d paths (debounce=%s)", len(h.paths), h.debounce)
	for {
		select {
		case event, ok := <-h.watcher.Events:
			if !ok {
				return
			}
			if event.Op&(fsnotify.Write|fsnotify.Create) == 0 {
				continue
			}
			h.mu.Lock()
			if time.Since(h.lastFire) < h.debounce {
				h.mu.Unlock()
				continue
			}
			h.lastFire = time.Now()
			h.mu.Unlock()
			log.Printf("config hot-reload: %s changed, reloading", event.Name)
			h.onChange()

		case err, ok := <-h.watcher.Errors:
			if !ok {
				return
			}
			log.Printf("config hot-reload: watcher error: %v", err)
		}
	}
}

// Close stops the file watcher.
func (h *HotReloader) Close() error {
	return h.watcher.Close()
}

// MustStartHotReloader builds and starts a HotReloader in a goroutine.
//
// It's a convenience wrapper used by service bootstrap code that wants
// config hot-reload without threading the Options.OnReload callback
// through every Load call.  It watches the given paths, calls
// onChange whenever a file mutates (debounced), and panics on
// watcher-construction failures (startup-time error).
//
// Typical wiring in a service main.go:
//
//	cfg := loadConfig()
//	config.MustStartHotReloader(
//	    []string{"./configs/config.yaml"},
//	    func() { cfg = loadConfig() },
//	    0, // default 2s debounce
//	)
//
// Only fields whose struct tag is `reloadable:"true"` will actually
// be swapped into the live config when the caller pairs this with
// ApplyReloadable; the watcher itself is agnostic.
func MustStartHotReloader(paths []string, onChange func(), debounce time.Duration) *HotReloader {
	h, err := NewHotReloader(paths, onChange, debounce)
	if err != nil {
		panic(fmt.Sprintf("config: start hot-reloader: %v", err))
	}
	go h.Start()
	return h
}

// ApplyReloadable copies fields marked `reloadable:"true"` from `src` into
// `dst`. Fields without the tag are ignored so secrets, pool sizes, and
// other restart-required values are never hot-swapped. Both arguments
// must be non-nil pointers to the same struct type.
//
// §1.5 gap-closure: HotReloader fires a callback on file change, but
// services previously re-parsed the whole struct, letting any field be
// swapped live. That violated our "connection pools and secrets require
// restart" contract. This helper enforces the whitelist.
//
// Usage:
//
//	cfg := &AppConfig{}
//	newCfg := &AppConfig{}
//	if err := loadInto(newCfg); err != nil { ... }
//	if err := config.ApplyReloadable(cfg, newCfg); err != nil { ... }
//
// Nested structs are walked recursively: a field tagged
// `reloadable:"true"` with struct type has all of its inner fields
// copied wholesale. Nested fields are otherwise only copied when they
// individually carry the tag.
func ApplyReloadable(dst, src any) error {
	if dst == nil || src == nil {
		return fmt.Errorf("hot-reload: dst and src must be non-nil")
	}
	dv := reflect.ValueOf(dst)
	sv := reflect.ValueOf(src)
	if dv.Kind() != reflect.Ptr || sv.Kind() != reflect.Ptr {
		return fmt.Errorf("hot-reload: dst and src must be pointers")
	}
	dv = dv.Elem()
	sv = sv.Elem()
	if dv.Type() != sv.Type() {
		return fmt.Errorf("hot-reload: dst (%s) and src (%s) must be the same type", dv.Type(), sv.Type())
	}
	if dv.Kind() != reflect.Struct {
		return fmt.Errorf("hot-reload: dst must point to a struct, got %s", dv.Kind())
	}
	applyReloadableStruct(dv, sv)
	return nil
}

// applyReloadableStruct walks `dst` and copies fields whose struct tag
// is `reloadable:"true"`. For tagged struct fields the whole subtree
// is copied; for untagged struct fields recursion continues so
// nested-tagged fields still get picked up.
func applyReloadableStruct(dst, src reflect.Value) {
	t := dst.Type()
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		if !field.IsExported() {
			continue
		}
		tag := field.Tag.Get("reloadable")
		df := dst.Field(i)
		sf := src.Field(i)
		if tag == "true" {
			if df.CanSet() {
				df.Set(sf)
			}
			continue
		}
		// No tag on this field — if it's a nested struct, recurse so
		// callers can tag individual leaves.
		if df.Kind() == reflect.Struct {
			applyReloadableStruct(df, sf)
		} else if df.Kind() == reflect.Ptr && !df.IsNil() && df.Elem().Kind() == reflect.Struct && !sf.IsNil() {
			applyReloadableStruct(df.Elem(), sf.Elem())
		}
	}
}
