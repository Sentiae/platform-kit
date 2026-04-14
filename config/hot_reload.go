package config

import (
	"log"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

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
