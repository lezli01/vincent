package workflow

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
)

// waitFor polls cond until it holds or the deadline passes. Reloads are
// filesystem-event driven, so tests wait for the effect rather than sleeping
// a fixed interval.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestWatchGlobalScope(t *testing.T) {
	reg, globalDir := newTestRegistry(t)
	if err := os.MkdirAll(globalDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	reg.ReloadGlobal()

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	if err := reg.Watch(ctx); err != nil {
		t.Fatalf("Watch: %v", err)
	}

	path := writeWorkflow(t, globalDir, "later", manualWorkflow("later", "v1"))
	waitFor(t, "new global workflow", func() bool {
		e, ok := find(reg.List(0), "later")
		return ok && e.Valid()
	})

	// Editing the file is picked up.
	if err := os.WriteFile(path, []byte(manualWorkflow("later", "v2")), 0o600); err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	waitFor(t, "edited global workflow", func() bool {
		e, ok := find(reg.List(0), "later")
		return ok && e.Valid() && e.Workflow.Description == "v2"
	})

	// Breaking the file leaves it listed with errors, not silently stale.
	if err := os.WriteFile(path, []byte("name: later\nsteps:\n  - {id: a, type: nope}\n"), 0o600); err != nil {
		t.Fatalf("break file: %v", err)
	}
	waitFor(t, "broken global workflow", func() bool {
		e, ok := find(reg.List(0), "later")
		return ok && !e.Valid()
	})

	// Deleting it removes the entry.
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove: %v", err)
	}
	waitFor(t, "deleted global workflow", func() bool {
		_, ok := find(reg.List(0), "later")
		return !ok
	})
}

// TestWatchProjectDirCreatedLater covers the common case: a registered repo
// has no .vincent/workflows yet, and one appears while the daemon runs.
func TestWatchProjectDirCreatedLater(t *testing.T) {
	reg, _ := newTestRegistry(t)
	repo := t.TempDir()
	reg.SetProjects(map[int64]string{3: repo})

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	if err := reg.Watch(ctx); err != nil {
		t.Fatalf("Watch: %v", err)
	}

	writeWorkflow(t, filepath.Join(repo, ProjectDirName), "p", manualWorkflow("p", "project"))
	waitFor(t, "workflow in a directory created after startup", func() bool {
		e, ok := find(reg.List(3), "p")
		return ok && e.Valid() && e.Scope == ScopeProject
	})
}

// TestWatchFollowsNewProject covers a project registered after the watcher
// started (POST /v1/projects → SetProjects → rewatch).
func TestWatchFollowsNewProject(t *testing.T) {
	reg, _ := newTestRegistry(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	if err := reg.Watch(ctx); err != nil {
		t.Fatalf("Watch: %v", err)
	}

	repo := t.TempDir()
	dir := filepath.Join(repo, ProjectDirName)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	reg.SetProjects(map[int64]string{9: repo})

	writeWorkflow(t, dir, "late", manualWorkflow("late", "project"))
	waitFor(t, "workflow of a project registered after startup", func() bool {
		e, ok := find(reg.List(9), "late")
		return ok && e.Valid()
	})
}

// TestWatchIgnoresNonYAML proves editor scratch files do not churn the
// registry: a .swp write must not produce a reload.
func TestWatchIgnoresNonYAML(t *testing.T) {
	reg, globalDir := newTestRegistry(t)
	if err := os.MkdirAll(globalDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	writeWorkflow(t, globalDir, "w", manualWorkflow("w", "x"))
	reg.ReloadGlobal()

	reloads := make(chan struct{}, 16)
	reg.OnChange(func() { reloads <- struct{}{} })

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	if err := reg.Watch(ctx); err != nil {
		t.Fatalf("Watch: %v", err)
	}

	if err := os.WriteFile(filepath.Join(globalDir, ".w.yaml.swp"), []byte("junk"), 0o600); err != nil {
		t.Fatalf("write swap file: %v", err)
	}
	select {
	case <-reloads:
		t.Error("a non-YAML file triggered a registry reload")
	case <-time.After(300 * time.Millisecond):
	}
}

// TestWatchAncestorIgnoresUnrelatedChurn proves repo-root churn does not
// reload a scope watched at an ancestor: only the path leading to the
// workflows directory matters.
func TestWatchAncestorIgnoresUnrelatedChurn(t *testing.T) {
	reg, _ := newTestRegistry(t)
	repo := t.TempDir()
	reg.SetProjects(map[int64]string{4: repo})

	reloads := make(chan struct{}, 16)
	reg.OnChange(func() { reloads <- struct{}{} })

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	if err := reg.Watch(ctx); err != nil {
		t.Fatalf("Watch: %v", err)
	}

	if err := os.WriteFile(filepath.Join(repo, "notes.txt"), []byte("junk"), 0o600); err != nil {
		t.Fatalf("write unrelated file: %v", err)
	}
	select {
	case <-reloads:
		t.Error("unrelated churn in a watched ancestor triggered a registry reload")
	case <-time.After(300 * time.Millisecond):
	}
}

// TestWatchClassifySelfEvent covers the watched directory itself being
// removed or renamed (`mv workflows workflows.old`): the event's Name is the
// watch path, not a child of it, and must still resync + reload the scope.
// Exercised directly because dir self-event delivery differs per OS.
func TestWatchClassifySelfEvent(t *testing.T) {
	reg, globalDir := newTestRegistry(t)
	wt := &watcher{reg: reg, watched: map[string]scopeRef{globalDir: {scope: ScopeGlobal}}}

	ref, ok := wt.classify(fsnotify.Event{Name: globalDir, Op: fsnotify.Rename})
	if !ok || ref.scope != ScopeGlobal {
		t.Errorf("classify(rename of watched dir) = %+v, %v; want the global scope, true", ref, ok)
	}
	if _, ok := wt.classify(fsnotify.Event{Name: globalDir, Op: fsnotify.Chmod}); ok {
		t.Error("classify(chmod of watched dir) = interesting, want chmod noise ignored")
	}
}

// TestWatchClassifyAncestorEvents pins which ancestor-watch events matter:
// only path components on the way to the workflows directory.
func TestWatchClassifyAncestorEvents(t *testing.T) {
	reg, _ := newTestRegistry(t)
	repo := t.TempDir()
	reg.SetProjects(map[int64]string{3: repo})
	ref := scopeRef{scope: ScopeProject, projectID: 3}
	wt := &watcher{reg: reg, watched: map[string]scopeRef{repo: ref}}

	tests := []struct {
		name        string
		ev          string
		interesting bool
	}{
		{"component toward the workflows dir", filepath.Join(repo, ".vincent"), true},
		{"unrelated file", filepath.Join(repo, "README.md"), false},
		{"unrelated directory", filepath.Join(repo, "build"), false},
		{"unrelated yaml outside the workflows dir", filepath.Join(repo, "config.yaml"), false},
	}
	for _, tt := range tests {
		got, ok := wt.classify(fsnotify.Event{Name: tt.ev, Op: fsnotify.Create})
		if ok != tt.interesting {
			t.Errorf("%s: classify(%s) = %v, want %v", tt.name, tt.ev, ok, tt.interesting)
		}
		if ok && got != ref {
			t.Errorf("%s: classify(%s) ref = %+v, want %+v", tt.name, tt.ev, got, ref)
		}
	}
}
