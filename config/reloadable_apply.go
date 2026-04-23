package config

import (
	"context"
	"log"
	"sync"
	"time"
)

// InstallHotReload wires a config-file watcher that re-reads `target`'s
// struct from disk on change and merges only fields tagged
// `reloadable:"true"` onto `dst`. Secrets, ports, broker lists, and other
// restart-required values stay untouched.
//
// Typical wiring in a service's main.go:
//
//	cfg := &AppConfig{}
//	config.MustLoad(cfg, opts)
//	config.InstallHotReload(ctx, cfg, func() (any, error) {
//	    newCfg := &AppConfig{}
//	    err := config.Load(newCfg, opts)
//	    return newCfg, err
//	}, []string{"./configs/config.yaml"}, 0)
//
// InstallHotReload returns a stop function that halts the watcher.
func InstallHotReload(
	ctx context.Context,
	dst any,
	reload func() (any, error),
	paths []string,
	debounce time.Duration,
) (stop func(), err error) {
	if debounce <= 0 {
		debounce = 2 * time.Second
	}

	var mu sync.Mutex
	onChange := func() {
		mu.Lock()
		defer mu.Unlock()

		src, rerr := reload()
		if rerr != nil {
			log.Printf("hot-reload: re-load failed: %v (skipping apply)", rerr)
			return
		}
		if err := ApplyReloadable(dst, src); err != nil {
			log.Printf("hot-reload: ApplyReloadable failed: %v", err)
			return
		}
		log.Printf("hot-reload: applied reloadable fields from %d source file(s)", len(paths))
	}

	reloader, err := NewHotReloader(paths, onChange, debounce)
	if err != nil {
		return nil, err
	}
	go reloader.Start()
	go func() {
		<-ctx.Done()
		_ = reloader.Close()
	}()
	return func() { _ = reloader.Close() }, nil
}
