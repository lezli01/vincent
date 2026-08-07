package workflow

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"
)

// debounce coalesces the event bursts an editor produces for one save
// (write+write+chmod, or rename-based atomic saves) into a single reload —
// the same interval the config watcher uses.
const debounce = 100 * time.Millisecond

// scopeRef identifies which scope a watched directory belongs to.
type scopeRef struct {
	scope     Scope
	projectID int64
}

// Watch keeps the registry in sync with the filesystem until ctx is
// canceled. Each scope is watched at the deepest directory that currently
// exists — the workflows directory itself when present, otherwise its
// nearest existing ancestor — so a `.vincent/workflows` created later is
// picked up without a restart. Watching directories (not files) is what
// makes rename-on-save editors and deletions work.
//
// Watch returns once the watcher is registered; reloads happen on its own
// goroutine.
func (r *Registry) Watch(ctx context.Context) error {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("create workflow watcher: %w", err)
	}
	wt := &watcher{reg: r, w: w, watched: map[string]scopeRef{}, wake: make(chan struct{}, 1)}
	wt.sync()
	r.mu.Lock()
	r.rewatch = wt.wake
	r.mu.Unlock()
	go wt.loop(ctx)
	return nil
}

type watcher struct {
	reg     *Registry
	w       *fsnotify.Watcher
	watched map[string]scopeRef
	wake    chan struct{}
}

// targets computes the directory to watch per scope: the workflows dir if it
// exists, else its nearest existing ancestor.
func (r *Registry) targets() map[string]scopeRef {
	out := map[string]scopeRef{}
	if r.globalDir != "" {
		if dir := deepestExisting(r.globalDir, filepath.Dir(r.globalDir)); dir != "" {
			out[dir] = scopeRef{scope: ScopeGlobal}
		}
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	for id, ps := range r.projects {
		if dir := deepestExisting(ps.dir, filepath.Dir(ps.dir), ps.root); dir != "" {
			out[dir] = scopeRef{scope: ScopeProject, projectID: id}
		}
	}
	return out
}

// deepestExisting returns the first candidate that is an existing directory.
func deepestExisting(candidates ...string) string {
	for _, c := range candidates {
		if fi, err := os.Stat(c); err == nil && fi.IsDir() {
			return c
		}
	}
	return ""
}

// sync reconciles the fsnotify watch set with the current scopes and
// returns the scopes whose watch was just added. Those must be reloaded:
// anything that happened before the watch existed produced no event.
func (wt *watcher) sync() []scopeRef {
	want := wt.reg.targets()
	for dir := range wt.watched {
		if _, ok := want[dir]; !ok {
			_ = wt.w.Remove(dir)
			delete(wt.watched, dir)
		}
	}
	var added []scopeRef
	for dir, ref := range want {
		if _, ok := wt.watched[dir]; ok {
			continue
		}
		if err := wt.w.Add(dir); err != nil {
			wt.reg.log.Warn("workflow watch failed", "dir", dir, "error", err)
			continue
		}
		wt.watched[dir] = ref
		added = append(added, ref)
	}
	return added
}

func (wt *watcher) loop(ctx context.Context) {
	defer func() { _ = wt.w.Close() }()
	dirty := map[scopeRef]bool{}
	var pending *time.Timer
	var fire <-chan time.Time
	arm := func() {
		if pending != nil {
			pending.Stop()
		}
		pending = time.NewTimer(debounce)
		fire = pending.C
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-wt.wake:
			for _, ref := range wt.sync() {
				dirty[ref] = true
			}
			if len(dirty) > 0 {
				arm()
			}
		case ev, ok := <-wt.w.Events:
			if !ok {
				return
			}
			ref, interesting := wt.classify(ev)
			if !interesting {
				continue
			}
			dirty[ref] = true
			arm()
		case <-fire:
			pending, fire = nil, nil
			// A workflows directory may have just been created, so refresh
			// the watch set before reloading what changed.
			for _, ref := range wt.sync() {
				dirty[ref] = true
			}
			for ref := range dirty {
				delete(dirty, ref)
				if ref.scope == ScopeGlobal {
					wt.reg.ReloadGlobal()
				} else {
					wt.reg.ReloadProject(ref.projectID)
				}
			}
		case err, ok := <-wt.w.Errors:
			if !ok {
				return
			}
			wt.reg.log.Warn("workflow watcher error", "error", err)
		}
	}
}

// classify maps an event to its scope, ignoring chmod noise and files that
// cannot affect the registry.
func (wt *watcher) classify(ev fsnotify.Event) (scopeRef, bool) {
	if ev.Op == fsnotify.Chmod {
		return scopeRef{}, false
	}
	dir := filepath.Dir(ev.Name)
	ref, ok := wt.watched[dir]
	if !ok {
		return scopeRef{}, false
	}
	// Events inside the workflows directory itself: only YAML matters.
	// Events on an ancestor matter only when they concern the path leading
	// to that directory (its creation).
	if wt.isWorkflowsDir(dir, ref) {
		switch strings.ToLower(filepath.Ext(ev.Name)) {
		case ".yaml", ".yml":
			return ref, true
		default:
			return scopeRef{}, false
		}
	}
	return ref, true
}

// isWorkflowsDir reports whether dir is the scope's workflows directory
// rather than a watched ancestor of it.
func (wt *watcher) isWorkflowsDir(dir string, ref scopeRef) bool {
	if ref.scope == ScopeGlobal {
		return dir == wt.reg.globalDir
	}
	wt.reg.mu.RLock()
	defer wt.reg.mu.RUnlock()
	ps, ok := wt.reg.projects[ref.projectID]
	return ok && dir == ps.dir
}
