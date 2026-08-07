package workflow

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// Scope is where a workflow comes from (spec §5.2, plus the built-in scope —
// a phase 2 spec addition covering the ad-hoc workflow).
type Scope string

// Workflow scopes, in precedence order: a project workflow shadows a global
// one of the same name, and both shadow a built-in.
const (
	ScopeBuiltin Scope = "builtin"
	ScopeGlobal  Scope = "global"
	ScopeProject Scope = "project"
)

// ProjectDirName is the per-repo workflow directory, relative to the repo
// root (spec §5.2).
var ProjectDirName = filepath.Join(".vincent", "workflows")

// GlobalDirName is the global workflow directory, relative to the config
// directory (spec §5.2).
const GlobalDirName = "workflows"

// Entry is one workflow known to the registry. A file that fails to parse or
// validate is kept as an entry carrying Errors and a nil Workflow, so a
// broken file is visible without hiding the valid ones (§5.2).
type Entry struct {
	Name      string
	Scope     Scope
	ProjectID int64
	// File is the source path; empty for the built-in scope.
	File string
	// Source is the raw YAML, captured as the task's workflow_snapshot.
	Source string
	// Workflow is the parsed definition; nil when Errors is non-empty.
	Workflow *Workflow
	Errors   Errors
}

// Valid reports whether the entry parsed and validated.
func (e Entry) Valid() bool { return e.Workflow != nil }

// Registry holds the parsed workflows of every scope and reloads them when
// their files change (§5.2).
type Registry struct {
	globalDir string
	opts      Options
	log       *slog.Logger

	mu       sync.RWMutex
	global   map[string]Entry
	projects map[int64]*projectScope

	// onChange is called after any scope reloads, for the
	// workflow.registry_changed event (§13.3; wired in T2.7).
	onChange func()
	// rewatch nudges the watcher to reconcile its watch set after the
	// project scopes change; nil until Watch runs.
	rewatch chan struct{}
}

type projectScope struct {
	root    string // repo root
	dir     string // {repo}/.vincent/workflows
	entries map[string]Entry
}

// NewRegistry returns a registry over globalDir ({config_dir}/workflows).
// Nothing is read until Reload or SetProjects is called.
func NewRegistry(globalDir string, opts Options, log *slog.Logger) *Registry {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Registry{
		globalDir: globalDir,
		opts:      opts,
		log:       log,
		global:    map[string]Entry{},
		projects:  map[int64]*projectScope{},
	}
}

// Options returns the validation options the registry parses with, so
// POST /v1/workflows/validate judges a candidate exactly as a loaded file.
func (r *Registry) Options() Options { return r.opts }

// OnChange registers a callback fired after any scope reloads.
func (r *Registry) OnChange(fn func()) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.onChange = fn
}

// SetProjects replaces the set of project scopes: roots maps project id to
// repo root. Scopes that disappeared are dropped, new ones are loaded.
func (r *Registry) SetProjects(roots map[int64]string) {
	r.mu.Lock()
	for id, ps := range r.projects {
		if root, ok := roots[id]; !ok || root != ps.root {
			delete(r.projects, id)
		}
	}
	var added []int64
	for id, root := range roots {
		if _, ok := r.projects[id]; ok {
			continue
		}
		r.projects[id] = &projectScope{
			root:    root,
			dir:     filepath.Join(root, ProjectDirName),
			entries: map[string]Entry{},
		}
		added = append(added, id)
	}
	r.mu.Unlock()

	for _, id := range added {
		r.ReloadProject(id)
	}
	r.wakeWatcher()
	r.notify()
}

// wakeWatcher asks the watcher to reconcile its watch set; safe when no
// watcher is running and never blocks.
func (r *Registry) wakeWatcher() {
	r.mu.RLock()
	ch := r.rewatch
	r.mu.RUnlock()
	if ch == nil {
		return
	}
	select {
	case ch <- struct{}{}:
	default:
	}
}

// Reload re-reads every scope.
func (r *Registry) Reload() {
	r.ReloadGlobal()
	r.mu.RLock()
	ids := make([]int64, 0, len(r.projects))
	for id := range r.projects {
		ids = append(ids, id)
	}
	r.mu.RUnlock()
	for _, id := range ids {
		r.ReloadProject(id)
	}
}

// ReloadGlobal re-reads {config_dir}/workflows.
func (r *Registry) ReloadGlobal() {
	entries := r.loadDir(r.globalDir, ScopeGlobal, 0)
	r.mu.Lock()
	r.global = entries
	r.mu.Unlock()
	r.notify()
}

// ReloadProject re-reads one project's {repo}/.vincent/workflows. Unknown
// ids are ignored (the project was removed concurrently).
func (r *Registry) ReloadProject(id int64) {
	r.mu.RLock()
	ps, ok := r.projects[id]
	dir := ""
	if ok {
		dir = ps.dir
	}
	r.mu.RUnlock()
	if !ok {
		return
	}
	entries := r.loadDir(dir, ScopeProject, id)
	r.mu.Lock()
	if ps, ok := r.projects[id]; ok {
		ps.entries = entries
	}
	r.mu.Unlock()
	r.notify()
}

func (r *Registry) notify() {
	r.mu.RLock()
	fn := r.onChange
	r.mu.RUnlock()
	if fn != nil {
		fn()
	}
}

// loadDir parses every *.yaml/*.yml in dir. A missing directory is normal
// (most repos have no .vincent/workflows) and yields no entries. Files are
// processed in name order so a duplicate `name:` deterministically keeps the
// first file and reports the second.
func (r *Registry) loadDir(dir string, scope Scope, projectID int64) map[string]Entry {
	entries := map[string]Entry{}
	if dir == "" {
		return entries
	}
	names, err := yamlFiles(dir)
	if err != nil {
		if !os.IsNotExist(err) {
			r.log.Warn("workflow directory unreadable", "dir", dir, "scope", scope, "error", err)
		}
		return entries
	}
	for _, path := range names {
		src, err := os.ReadFile(path) //nolint:gosec // registry paths are daemon-owned
		if err != nil {
			r.log.Warn("workflow file unreadable", "file", path, "error", err)
			continue
		}
		entry := Entry{Scope: scope, ProjectID: projectID, File: path, Source: string(src)}
		wf, perr := Parse(src, r.opts)
		switch {
		case perr != nil:
			var errs Errors
			if !asErrors(perr, &errs) {
				errs = Errors{{Message: perr.Error()}}
			}
			entry.Errors = errs
			entry.Name = fallbackName(src, path)
			r.log.Warn("workflow invalid", "file", path, "errors", errs.Error())
		default:
			entry.Workflow = wf
			entry.Name = wf.Name
		}
		if prev, dup := entries[entry.Name]; dup {
			entry.Workflow = nil
			entry.Errors = Errors{{
				Path:    "name",
				Message: fmt.Sprintf("duplicate workflow name %q in this scope (already defined by %s)", entry.Name, prev.File),
			}}
			r.log.Warn("duplicate workflow name", "name", entry.Name, "file", path, "first", prev.File)
		}
		entries[entry.Name] = entry
	}
	return entries
}

// List returns the merged view for a project: built-in, overlaid by global,
// overlaid by that project's workflows (§5.2 shadowing), sorted by name.
// A projectID of 0 lists built-in + global only.
func (r *Registry) List(projectID int64) []Entry {
	merged := map[string]Entry{}
	for name, e := range builtins() {
		merged[name] = e
	}
	r.mu.RLock()
	for name, e := range r.global {
		merged[name] = e
	}
	if ps, ok := r.projects[projectID]; ok {
		for name, e := range ps.entries {
			merged[name] = e
		}
	}
	r.mu.RUnlock()

	out := make([]Entry, 0, len(merged))
	for _, e := range merged {
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Lookup resolves one workflow name for a project, applying the same
// shadowing as List. The returned entry may be invalid (Errors set), which
// callers report rather than treating as missing — a broken file shadowing a
// valid global one must not silently run the global.
func (r *Registry) Lookup(projectID int64, name string) (Entry, bool) {
	r.mu.RLock()
	if ps, ok := r.projects[projectID]; ok {
		if e, ok := ps.entries[name]; ok {
			r.mu.RUnlock()
			return e, true
		}
	}
	if e, ok := r.global[name]; ok {
		r.mu.RUnlock()
		return e, true
	}
	r.mu.RUnlock()
	if e, ok := builtins()[name]; ok {
		return e, true
	}
	return Entry{}, false
}

// yamlFiles lists the YAML files directly inside dir, sorted by name.
func yamlFiles(dir string) ([]string, error) {
	des, err := os.ReadDir(dir)
	if err != nil {
		return nil, err //nolint:wrapcheck // caller inspects os.IsNotExist
	}
	var out []string
	for _, de := range des {
		if de.IsDir() {
			continue
		}
		switch strings.ToLower(filepath.Ext(de.Name())) {
		case ".yaml", ".yml":
			out = append(out, filepath.Join(dir, de.Name()))
		}
	}
	sort.Strings(out)
	return out, nil
}

// fallbackName names a broken file so it can still be listed: its declared
// `name:` if that much decodes, else the file's base name.
func fallbackName(src []byte, path string) string {
	var probe struct {
		Name string `yaml:"name"`
	}
	if err := yamlUnmarshalLenient(src, &probe); err == nil && probe.Name != "" {
		return probe.Name
	}
	base := filepath.Base(path)
	return strings.TrimSuffix(base, filepath.Ext(base))
}
