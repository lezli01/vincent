package config

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

// debounce coalesces the event bursts editors produce for a single save
// (write+write+chmod, or rename-based atomic saves) into one reload.
const debounce = 100 * time.Millisecond

// Watch watches dir for changes to config.yaml and calls onReload with every
// new configuration that loads and validates. Invalid or unreadable configs
// are logged and dropped, so the caller's last good configuration stays
// active. Deleting the file reloads the built-in defaults (Load treats a
// missing file as defaults). The watch is on the directory, not the file, so
// rename-on-save editors and late file creation are handled.
//
// Each reload holds mu from before it reads config.yaml until onReload
// returns, so onReload runs with mu held and must not take it again. That
// is what lets a writer which updates the file and then applies it under
// the same lock (PATCH /v1/config, task 060) report the change as in force:
// a reload that read the file before the write would otherwise deliver the
// older bytes after the writer's own apply, and revert it until the next
// event.
//
// Watch returns once the watcher is registered and stops when ctx is
// canceled. onReload is called from the watcher goroutine.
func Watch(ctx context.Context, log *slog.Logger, dir string, mu sync.Locker, onReload func(Config)) error {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("create config watcher: %w", err)
	}
	if err := w.Add(dir); err != nil {
		_ = w.Close()
		return fmt.Errorf("watch %s: %w", dir, err)
	}
	go watchLoop(ctx, log, w, filepath.Join(dir, FileName), mu, onReload)
	return nil
}

func watchLoop(ctx context.Context, log *slog.Logger, w *fsnotify.Watcher, path string, mu sync.Locker, onReload func(Config)) {
	defer func() { _ = w.Close() }()
	var pending *time.Timer
	var fire <-chan time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-w.Events:
			if !ok {
				return
			}
			if filepath.Base(ev.Name) != FileName || ev.Op == fsnotify.Chmod {
				continue
			}
			if pending != nil {
				pending.Stop()
			}
			pending = time.NewTimer(debounce)
			fire = pending.C
		case <-fire:
			pending, fire = nil, nil
			reload(log, path, mu, onReload)
		case err, ok := <-w.Errors:
			if !ok {
				return
			}
			log.Warn("config watcher error", "error", err)
		}
	}
}

// reload is one debounced fire: read config.yaml and deliver it, with mu held
// across both (see Watch).
func reload(log *slog.Logger, path string, mu sync.Locker, onReload func(Config)) {
	mu.Lock()
	defer mu.Unlock()
	cfg, err := Load(path)
	if err != nil {
		log.Warn("config reload rejected; keeping last good config", "error", err)
		return
	}
	log.Info("config reloaded", "path", path)
	onReload(cfg)
}
