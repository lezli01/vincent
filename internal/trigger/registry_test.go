package trigger

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func registryDoc(id, title string) string {
	return fmt.Sprintf(`id: %s
enabled: true
source:
  type: command
  project: 1
  poll_interval: 1m
  command: [poll]
action:
  type: create_task
  title: %q
`, id, title)
}

// changeLog records what a registry's OnChange hooks were handed.
type changeLog [][]string

func (c *changeLog) record(removed []string) { *c = append(*c, slices.Clone(removed)) }

func registryIDs(r *Registry) []string {
	var out []string
	for _, e := range r.List() {
		out = append(out, e.ID)
	}
	return out
}

// TestRegistryReload: add, edit and remove each reach OnChange, only a
// removal names ids, and an invalid file stays as an invalid entry.
func TestRegistryReload(t *testing.T) {
	dir := t.TempDir()
	reg := NewRegistry(dir, nil)
	var changes changeLog
	reg.OnChange(changes.record)

	reg.Reload()
	if len(reg.List()) != 0 || len(changes) != 0 {
		t.Fatalf("empty directory: %v entries, %d changes", registryIDs(reg), len(changes))
	}

	writeFile(t, dir, "a.yaml", registryDoc("a", "first"))
	writeFile(t, dir, "b.yaml", registryDoc("b", "b"))
	writeFile(t, dir, "notes.txt", "not a trigger")
	if err := os.Mkdir(filepath.Join(dir, "sub.yaml"), 0o700); err != nil {
		t.Fatal(err)
	}
	reg.Reload()
	if got := registryIDs(reg); !slices.Equal(got, []string{"a", "b"}) {
		t.Fatalf("ids = %v", got)
	}
	if len(changes) != 1 || len(changes[0]) != 0 {
		t.Fatalf("add: changes = %v, want one with no removals", changes)
	}
	a1, _ := reg.Get("a")
	if !a1.Valid() || a1.Def.Action.Title != "first" || a1.File != filepath.Join(dir, "a.yaml") {
		t.Fatalf("a = %+v", a1)
	}

	reg.Reload()
	if len(changes) != 1 {
		t.Errorf("a reload with nothing changed ran OnChange: %v", changes)
	}

	writeFile(t, dir, "a.yaml", registryDoc("a", "second"))
	reg.Reload()
	a2, _ := reg.Get("a")
	if a2.Version == a1.Version || a2.Def.Action.Title != "second" {
		t.Errorf("edit: version %s -> %s, title %q", a1.Version, a2.Version, a2.Def.Action.Title)
	}
	if len(changes) != 2 || len(changes[1]) != 0 {
		t.Errorf("edit: changes = %v", changes)
	}

	if err := os.Remove(filepath.Join(dir, "b.yaml")); err != nil {
		t.Fatal(err)
	}
	reg.Reload()
	if got := registryIDs(reg); !slices.Equal(got, []string{"a"}) {
		t.Errorf("after removal ids = %v", got)
	}
	if len(changes) != 3 || !slices.Equal(changes[2], []string{"b"}) {
		t.Errorf("remove: changes = %v, want b removed", changes)
	}

	writeFile(t, dir, "a.yaml", "id: a\nenabled: true\nsource: [half-saved\n")
	reg.Reload()
	a3, ok := reg.Get("a")
	if !ok || a3.Valid() || a3.Def != nil || len(a3.Errors) == 0 {
		t.Errorf("invalid a = %+v, present %v; want an invalid entry", a3, ok)
	}
	if len(changes) != 4 || len(changes[3]) != 0 {
		t.Errorf("invalid: changes = %v, want a change that removes nothing", changes)
	}
}

// TestRegistryMissingDirectory: no directory is no triggers, and a directory
// that goes away removes every trigger it held.
func TestRegistryMissingDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "triggers")
	reg := NewRegistry(dir, nil)
	var changes changeLog
	reg.OnChange(changes.record)

	reg.Reload()
	if len(reg.List()) != 0 || len(changes) != 0 {
		t.Fatalf("missing directory: %v, %v", registryIDs(reg), changes)
	}
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, dir, "a.yaml", registryDoc("a", "a"))
	reg.Reload()
	if got := registryIDs(reg); !slices.Equal(got, []string{"a"}) {
		t.Fatalf("ids = %v", got)
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	reg.Reload()
	if len(reg.List()) != 0 || len(changes) != 2 || !slices.Equal(changes[1], []string{"a"}) {
		t.Errorf("directory gone: %v, changes %v", registryIDs(reg), changes)
	}
}

// TestRegistryWatch: a file written after Watch started is loaded, and its
// removal unloads it — in a directory that existed, and in one created later.
func TestRegistryWatch(t *testing.T) {
	for _, tc := range []struct {
		name   string
		exists bool
	}{{"existing directory", true}, {"directory created later", false}} {
		t.Run(tc.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "triggers")
			if tc.exists {
				if err := os.Mkdir(dir, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			reg := NewRegistry(dir, nil)
			reg.Reload()
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			if err := reg.Watch(ctx); err != nil {
				t.Fatal(err)
			}
			if !tc.exists {
				if err := os.Mkdir(dir, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			writeFile(t, dir, "late.yaml", registryDoc("late", "late"))
			waitFor(t, "the watcher to load late.yaml", func() bool {
				e, ok := reg.Get("late")
				return ok && e.Valid()
			})
			if err := os.Remove(filepath.Join(dir, "late.yaml")); err != nil {
				t.Fatal(err)
			}
			waitFor(t, "the watcher to drop late.yaml", func() bool {
				_, ok := reg.Get("late")
				return !ok
			})
		})
	}
}
