package trigger

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/lezli01/vincent/internal/workflow"
)

// The registry is the set of files under {config_dir}/triggers/, kept in step
// with the disk the way internal/workflow's registry is (task 096 decision 4)
// — minus the scopes and the shadowing, which decision 8 removed.
//
// It is also where "the cursor follows the file" (decision 16) is decided,
// because only the registry can tell a file that left from a file it could
// not read. Three cases, deliberately different:
//
//   - a file that is gone — deleted over the API, `rm`, renamed away — is a
//     removal, reported to OnChange so the manager drops its cursor;
//   - a file that is present but does not validate stays in the registry as an
//     invalid entry and keeps its cursor: an editor's half-written save must
//     not re-seed a trigger that is about to come back unchanged;
//   - a directory that cannot be read is not "every file gone": the previous
//     set is kept and the failure logged, or one permissions blip would re-arm
//     every trigger with a fresh seed.

// Entry is one trigger file as the registry last read it.
type Entry struct {
	ID      string
	File    string
	Version string
	Source  []byte
	// Def is the parsed definition, nil when Errors is not empty.
	Def    *Definition
	Errors workflow.Errors
}

// Valid reports whether the file parsed and validated.
func (e *Entry) Valid() bool { return e.Def != nil }

// Registry holds the trigger files of one directory.
type Registry struct {
	dir string
	log *slog.Logger

	mu       sync.RWMutex
	entries  map[string]*Entry
	onChange []func(removed []string)
}

// NewRegistry returns an empty registry for dir, normally
// {config_dir}/triggers. Nothing is read until Reload.
func NewRegistry(dir string, log *slog.Logger) *Registry {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Registry{dir: dir, log: log, entries: map[string]*Entry{}}
}

// Dir is the triggers directory.
func (r *Registry) Dir() string { return r.dir }

// OnChange registers fn to run after a reload that changed the set, with the
// ids whose files left it. It runs on the reloading goroutine, outside the
// registry's lock.
func (r *Registry) OnChange(fn func(removed []string)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.onChange = append(r.onChange, fn)
}

// List returns every entry, sorted by id.
func (r *Registry) List() []Entry {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Entry, 0, len(r.entries))
	for _, e := range r.entries {
		out = append(out, *e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Get returns one entry.
func (r *Registry) Get(id string) (Entry, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	e, ok := r.entries[id]
	if !ok {
		return Entry{}, false
	}
	return *e, true
}

// Reload re-reads the directory. It is safe to call from any goroutine: the
// API calls it after a write so the answer reflects the file, and the watcher
// calls it on a change.
func (r *Registry) Reload() {
	next, ok := r.read()
	if !ok {
		return
	}
	r.mu.Lock()
	var removed []string
	changed := len(next) != len(r.entries)
	for id, prev := range r.entries {
		cur, still := next[id]
		if !still {
			removed = append(removed, id)
			changed = true
			continue
		}
		if cur.Version != prev.Version {
			changed = true
		}
	}
	r.entries = next
	hooks := append([]func([]string){}, r.onChange...)
	r.mu.Unlock()
	if !changed {
		return
	}
	sort.Strings(removed)
	for _, fn := range hooks {
		fn(removed)
	}
}

// read builds the entry set from disk. false means the directory could not be
// read and the previous set must stand.
func (r *Registry) read() (map[string]*Entry, bool) {
	out := map[string]*Entry{}
	files, err := os.ReadDir(r.dir)
	if errors.Is(err, os.ErrNotExist) {
		// No directory is no files: `mv triggers triggers.old` removes every
		// trigger, which is a removal by a route decision 16 names.
		return out, true
	}
	if err != nil {
		r.log.Warn("triggers directory unreadable; keeping the triggers already loaded",
			"dir", r.dir, "error", err)
		return nil, false
	}
	r.mu.RLock()
	prev := r.entries
	r.mu.RUnlock()
	for _, f := range files {
		if f.IsDir() || !strings.EqualFold(filepath.Ext(f.Name()), ".yaml") {
			continue
		}
		path := filepath.Join(r.dir, f.Name())
		id := Stem(path)
		e, err := readEntry(path, id)
		if err != nil {
			// One unreadable file is not that file gone either.
			if old, ok := prev[id]; ok {
				out[id] = old
			}
			r.log.Warn("trigger file unreadable; keeping what was loaded", "file", path, "error", err)
			continue
		}
		out[id] = e
	}
	return out, true
}

func readEntry(path, id string) (*Entry, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("stat trigger: %w", err)
	}
	//nolint:gosec // G304: a file the registry listed in the daemon's own triggers dir
	src, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read trigger: %w", err)
	}
	e := &Entry{
		ID: id, File: path, Source: src,
		Version: workflow.VersionOf(fi.ModTime().UTC().UnixNano(), src),
	}
	e.Def, e.Errors = Parse(src, id)
	return e, nil
}

// watchDebounce coalesces an editor's burst of events for one save, as the
// workflow and config watchers do.
const watchDebounce = 100 * time.Millisecond

// Watch keeps the registry in step with the directory until ctx is done. It
// watches the triggers directory when it exists and its parent otherwise, so a
// directory created later — by the API's first create, or by hand — is picked
// up without a restart. It returns once the watch is registered.
func (r *Registry) Watch(ctx context.Context) error {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("create trigger watcher: %w", err)
	}
	watched := ""
	rewatch := func() bool {
		want := r.dir
		if fi, err := os.Stat(r.dir); err != nil || !fi.IsDir() {
			want = filepath.Dir(r.dir)
		}
		if want == watched {
			return false
		}
		if watched != "" {
			_ = w.Remove(watched)
		}
		if err := w.Add(want); err != nil {
			r.log.Warn("trigger watch failed", "dir", want, "error", err)
			watched = ""
			return false
		}
		watched = want
		return true
	}
	rewatch()
	go func() {
		defer func() { _ = w.Close() }()
		var fire <-chan time.Time
		var timer *time.Timer
		arm := func() {
			if timer != nil {
				timer.Stop()
			}
			timer = time.NewTimer(watchDebounce)
			fire = timer.C
		}
		for {
			select {
			case <-ctx.Done():
				return
			case ev, ok := <-w.Events:
				if !ok {
					return
				}
				if ev.Op == fsnotify.Chmod {
					continue
				}
				if ev.Name == r.dir || filepath.Dir(ev.Name) == r.dir {
					arm()
				}
			case <-fire:
				fire = nil
				rewatch()
				r.Reload()
			case err, ok := <-w.Errors:
				if !ok {
					return
				}
				r.log.Warn("trigger watcher error", "error", err)
			}
		}
	}()
	return nil
}
