package workflow

import (
	"fmt"
	"io"
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
	// Warnings are non-fatal §8.2 catalog findings; the entry stays valid.
	Warnings Errors
}

// Valid reports whether the entry parsed and validated.
func (e Entry) Valid() bool { return e.Workflow != nil }

// RunsHere reports whether the entry's `platforms:` restriction (§8.1.1) admits
// this host. An entry that failed to parse answers true: its errors are
// already the reason it cannot back a task, and a second one would only make
// the message wrong.
func (e Entry) RunsHere() bool { return e.Workflow == nil || e.Workflow.SupportsHost() }

// NeedsInputAgent reports whether picking an agent for a task built on this
// entry is constrained by `on_input: require` (task 013). A broken entry
// answers false for the same reason RunsHere answers true: its errors are
// already why it cannot back a task.
func (e Entry) NeedsInputAgent() bool { return e.Workflow != nil && e.Workflow.RequiresInput() }

// Includes lists the workflows this entry includes directly (§7.9, task 019),
// so a client can show what a workflow depends on. Derived rather than stored,
// for the reason RunsHere and NeedsInputAgent are: one source of truth, and
// nothing to keep in sync when a file reloads.
func (e Entry) Includes() []string { return IncludeNames(e.Workflow) }

// Registry holds the parsed workflows of every scope and reloads them when
// their files change (§5.2).
type Registry struct {
	globalDir string
	opts      Options
	log       *slog.Logger

	mu       sync.RWMutex
	global   scopeEntries
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
	entries scopeEntries
}

// scopeEntries is the loaded contents of one workflow directory. byName
// resolves lookups; on a duplicate name the first file (in name order) keeps
// the entry, and each later file lands in dupes as an invalid entry so it
// stays visible in List without knocking out the valid one (§5.2).
type scopeEntries struct {
	byName map[string]Entry
	dupes  []Entry
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
		global:    scopeEntries{byName: map[string]Entry{}},
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
			entries: scopeEntries{byName: map[string]Entry{}},
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
	r.warnCrossFileRefs(0)
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
	r.warnCrossFileRefs(id)
	r.notify()
}

// warnCrossFileRefs logs the §8.2 warnings that are properties of *resolved*
// names for one project's view: fan-out cycles (task 014 decision 5) and
// whatever is wrong with a workflow's includes (task 019 decision 8).
//
// It runs after the load rather than inside it, because which files a lane's
// `workflow:` or an `include:` reaches is decided by builtin < global <
// project shadowing, which loadDir cannot see while it is still building a
// scope.
//
// Warnings, not errors. Each is real only once a task picks a root, and a
// project workflow may shadow the very name that closed the loop, went missing
// or restricted the platform. Task creation is where each becomes a 400.
func (r *Registry) warnCrossFileRefs(projectID int64) {
	lookup := func(name string) (*Workflow, bool) {
		e, ok := r.Lookup(projectID, name)
		if !ok || !e.Valid() {
			return nil, false
		}
		return e.Workflow, true
	}
	for _, e := range r.List(projectID) {
		if !e.Valid() {
			continue
		}
		if HasFanOut(e.Workflow) {
			for _, w := range LaneCycleWarnings(e.Workflow, lookup) {
				r.log.Warn("workflow fan-out cycle", "file", e.File, "name", e.Name, "warning", w)
			}
		}
		for _, w := range IncludeWarnings(e.Workflow, lookup) {
			r.log.Warn("workflow include unresolvable", "file", e.File, "name", e.Name, "warning", w)
		}
	}
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
// first file and reports each later one.
//
// Only regular files under maxSourceBytes are sourced (§5.2): the directory is
// daemon-owned, but its entries are whatever a registered repository contains.
// A file that fails either bound is catalogued as an invalid entry.
func (r *Registry) loadDir(dir string, scope Scope, projectID int64) scopeEntries {
	entries := scopeEntries{byName: map[string]Entry{}}
	if dir == "" {
		return entries
	}
	files, err := yamlFiles(dir)
	if err != nil {
		if !os.IsNotExist(err) {
			r.log.Warn("workflow directory unreadable", "dir", dir, "scope", scope, "error", err)
		}
		return entries
	}
	for _, f := range files {
		path := f.path
		src, reject, err := readSource(f)
		if err != nil {
			r.log.Warn("workflow file unreadable", "file", path, "error", err)
			continue
		}
		entry := Entry{Scope: scope, ProjectID: projectID, File: path}
		var (
			wf    *Workflow
			warns Errors
			perr  error
		)
		if reject == "" {
			entry.Source = string(src)
			wf, warns, perr = Parse(src, r.opts)
			entry.Warnings = warns
		}
		switch {
		case reject != "":
			// A file the scope must not source (§5.2, issue #136) becomes an
			// invalid entry rather than a silent skip, so the reason is
			// visible in the TUI and the API and its siblings stay valid.
			// The name can only come from the path: nothing was read.
			entry.Errors = Errors{{Message: reject}}
			entry.Name = fallbackName(nil, path)
			r.log.Warn("workflow file rejected", "file", path, "error", reject)
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
			if len(warns) > 0 {
				r.log.Warn("workflow has catalog warnings", "file", path, "warnings", warns.Error())
			}
		}
		// First file wins a duplicate name (§5.2 isolation): the later file
		// becomes an invalid dupe entry instead of overwriting the winner.
		if prev, dup := entries.byName[entry.Name]; dup {
			entry.Workflow = nil
			entry.Errors = append(entry.Errors, Error{
				Path:    "name",
				Message: fmt.Sprintf("duplicate workflow name %q in this scope (already defined by %s)", entry.Name, prev.File),
			})
			r.log.Warn("duplicate workflow name", "name", entry.Name, "file", path, "first", prev.File)
			entries.dupes = append(entries.dupes, entry)
			continue
		}
		entries.byName[entry.Name] = entry
	}
	return entries
}

// List returns the merged view for a project: built-in, overlaid by global,
// overlaid by that project's workflows (§5.2 shadowing), sorted by name and
// then file. A projectID of 0 lists built-in + global only. Duplicate-name
// losers appear as extra invalid entries after the file that kept the name,
// so a colliding file is visible without hiding the valid one.
func (r *Registry) List(projectID int64) []Entry {
	merged := map[string]Entry{}
	for name, e := range builtins() {
		merged[name] = e
	}
	r.mu.RLock()
	for name, e := range r.global.byName {
		merged[name] = e
	}
	dupes := append([]Entry(nil), r.global.dupes...)
	if ps, ok := r.projects[projectID]; ok {
		for name, e := range ps.entries.byName {
			merged[name] = e
		}
		dupes = append(dupes, ps.entries.dupes...)
	}
	r.mu.RUnlock()

	out := make([]Entry, 0, len(merged)+len(dupes))
	for _, e := range merged {
		out = append(out, e)
	}
	out = append(out, dupes...)
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].File < out[j].File
	})
	return out
}

// Lookup resolves one workflow name for a project, applying the same
// shadowing as List. The returned entry may be invalid (Errors set), which
// callers report rather than treating as missing — a broken file shadowing a
// valid global one must not silently run the global.
func (r *Registry) Lookup(projectID int64, name string) (Entry, bool) {
	r.mu.RLock()
	if ps, ok := r.projects[projectID]; ok {
		if e, ok := ps.entries.byName[name]; ok {
			r.mu.RUnlock()
			return e, true
		}
	}
	if e, ok := r.global.byName[name]; ok {
		r.mu.RUnlock()
		return e, true
	}
	r.mu.RUnlock()
	if e, ok := builtins()[name]; ok {
		return e, true
	}
	return Entry{}, false
}

// maxSourceBytes bounds one workflow file (§5.2). A workflow is a
// human-written definition and a megabyte is far past any real one; the bound
// exists because a project scope is whatever a registered repository happens
// to contain, and loading it must not be able to exhaust the daemon.
const maxSourceBytes = 1 << 20

// yamlFile is one candidate found in a scope directory. reject is empty for a
// regular file, and otherwise says why the file cannot be a workflow source.
type yamlFile struct {
	path   string
	reject string
}

// yamlFiles lists the YAML files directly inside dir, sorted by name. An entry
// that is not a regular file — a directory, a symlink, a FIFO, a socket or a
// device — is returned carrying a reject reason rather than dropped: DirEntry
// type bits are Lstat-shaped, so this is where the difference can be seen
// before anything opens the file, and §5.2 wants an unusable file surfaced
// rather than silently skipped.
func yamlFiles(dir string) ([]yamlFile, error) {
	des, err := os.ReadDir(dir)
	if err != nil {
		return nil, err //nolint:wrapcheck // caller inspects os.IsNotExist
	}
	var out []yamlFile
	for _, de := range des {
		switch strings.ToLower(filepath.Ext(de.Name())) {
		case ".yaml", ".yml":
		default:
			continue
		}
		f := yamlFile{path: filepath.Join(dir, de.Name())}
		if mode := de.Type(); !mode.IsRegular() {
			// The directory read may carry no type at all; Info re-asks the
			// OS, still without following a symlink.
			if info, ierr := de.Info(); ierr != nil || !info.Mode().IsRegular() {
				f.reject = rejectType(f.path, mode)
			}
		}
		out = append(out, f)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].path < out[j].path })
	return out, nil
}

// readSource reads one discovered workflow file. A non-empty reject is a file
// the registry must not source — the wrong type, or more than maxSourceBytes —
// leaving err for a genuine I/O failure.
//
// The type is checked twice: once on the directory entry, and once on the
// *opened handle*, so a file swapped for a symlink or a FIFO between the two
// cannot smuggle itself in. The open itself refuses to follow a final symlink
// and never blocks (sourceOpenFlags), which is what keeps a FIFO in a scope
// from parking the loader forever.
func readSource(f yamlFile) ([]byte, string, error) {
	if f.reject != "" {
		return nil, f.reject, nil
	}
	file, err := os.OpenFile(f.path, sourceOpenFlags, 0)
	if err != nil {
		// It was a regular file a moment ago, so a failure here may mean it
		// has been replaced since: a no-follow open fails on a symlink.
		if info, lerr := os.Lstat(f.path); lerr == nil && !info.Mode().IsRegular() {
			return nil, rejectType(f.path, info.Mode()), nil
		}
		return nil, "", err
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return nil, "", err
	}
	if !info.Mode().IsRegular() {
		return nil, rejectType(f.path, info.Mode()), nil
	}
	// One byte past the limit: enough to know the file is over it, without
	// allocating it, and a file of exactly the limit still parses.
	src, err := io.ReadAll(io.LimitReader(file, maxSourceBytes+1))
	if err != nil {
		return nil, "", err
	}
	if len(src) > maxSourceBytes {
		return nil, fmt.Sprintf("%s: a workflow file must be at most %d bytes", f.path, maxSourceBytes), nil
	}
	return src, "", nil
}

// rejectType is the catalog message for a file of the wrong type. It names the
// path and the type and nothing else: for a symlink, what is behind it is
// somebody else's file and must not be read, let alone reported.
func rejectType(path string, mode os.FileMode) string {
	return fmt.Sprintf("%s: a workflow file must be a regular file, not %s", path, typeName(mode))
}

func typeName(mode os.FileMode) string {
	switch {
	case mode&os.ModeSymlink != 0:
		return "a symbolic link"
	case mode.IsDir():
		return "a directory"
	case mode&os.ModeNamedPipe != 0:
		return "a named pipe"
	case mode&os.ModeSocket != 0:
		return "a socket"
	case mode&os.ModeDevice != 0:
		return "a device"
	default:
		return "a special file"
	}
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
