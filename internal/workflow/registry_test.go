package workflow

import (
	"os"
	"path/filepath"
	"testing"
)

// writeWorkflow writes src to dir/name.yaml, creating dir.
func writeWorkflow(t *testing.T, dir, name, src string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	path := filepath.Join(dir, name+".yaml")
	if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

// manualWorkflow is the smallest valid definition, parameterized by name and
// the instructions text (used to tell shadowed copies apart).
func manualWorkflow(name, marker string) string {
	return "name: " + name + "\ndescription: " + marker +
		"\nsteps:\n  - {id: gate, type: manual, instructions: " + marker + "}\n"
}

func newTestRegistry(t *testing.T) (*Registry, string) {
	t.Helper()
	globalDir := filepath.Join(t.TempDir(), "workflows")
	return NewRegistry(globalDir, Options{KnownAgents: []string{"claude"}}, nil), globalDir
}

func find(entries []Entry, name string) (Entry, bool) {
	for _, e := range entries {
		if e.Name == name {
			return e, true
		}
	}
	return Entry{}, false
}

func TestRegistryListsBuiltinWithoutAnyFiles(t *testing.T) {
	reg, _ := newTestRegistry(t)
	reg.Reload()

	entries := reg.List(0)
	if len(entries) != 1 {
		t.Fatalf("entries = %d, want only the built-in (%v)", len(entries), entries)
	}
	if entries[0].Name != AdhocName || entries[0].Scope != ScopeBuiltin {
		t.Errorf("entry = %+v, want the built-in adhoc", entries[0])
	}
	if _, ok := reg.Lookup(0, AdhocName); !ok {
		t.Error("Lookup(adhoc) failed; the built-in must always resolve")
	}
}

func TestRegistryScopeShadowing(t *testing.T) {
	reg, globalDir := newTestRegistry(t)
	repo := t.TempDir()
	projectDir := filepath.Join(repo, ProjectDirName)

	writeWorkflow(t, globalDir, "feature", manualWorkflow("feature", "global"))
	writeWorkflow(t, globalDir, "onlyglobal", manualWorkflow("onlyglobal", "global"))
	writeWorkflow(t, projectDir, "feature", manualWorkflow("feature", "project"))
	writeWorkflow(t, projectDir, "adhoc", manualWorkflow("adhoc", "project override"))

	reg.ReloadGlobal()
	reg.SetProjects(map[int64]string{7: repo})

	entries := reg.List(7)
	if len(entries) != 3 {
		t.Fatalf("entries = %d, want 3 (adhoc, feature, onlyglobal): %v", len(entries), entries)
	}
	feature, _ := find(entries, "feature")
	if feature.Scope != ScopeProject || feature.Workflow.Description != "project" {
		t.Errorf("feature = %+v, want the project copy to shadow the global one", feature)
	}
	adhoc, _ := find(entries, AdhocName)
	if adhoc.Scope != ScopeProject {
		t.Errorf("adhoc scope = %q, want a project file to shadow the built-in", adhoc.Scope)
	}
	onlyGlobal, _ := find(entries, "onlyglobal")
	if onlyGlobal.Scope != ScopeGlobal {
		t.Errorf("onlyglobal scope = %q, want global", onlyGlobal.Scope)
	}

	// Another project sees the global copy, not project 7's.
	other := t.TempDir()
	reg.SetProjects(map[int64]string{7: repo, 8: other})
	otherFeature, ok := find(reg.List(8), "feature")
	if !ok || otherFeature.Scope != ScopeGlobal {
		t.Errorf("project 8 feature = %+v, want the global copy", otherFeature)
	}

	// Lookup applies the same precedence.
	got, ok := reg.Lookup(7, "feature")
	if !ok || got.Scope != ScopeProject {
		t.Errorf("Lookup(7, feature) = %+v, %v; want the project copy", got, ok)
	}
}

func TestRegistryBrokenFileIsolation(t *testing.T) {
	reg, globalDir := newTestRegistry(t)
	writeWorkflow(t, globalDir, "good", manualWorkflow("good", "ok"))
	writeWorkflow(t, globalDir, "broken", "name: broken\nsteps:\n  - {id: a, type: nonsense}\n")
	reg.ReloadGlobal()

	entries := reg.List(0)
	good, ok := find(entries, "good")
	if !ok || !good.Valid() {
		t.Fatalf("good workflow = %+v, want it valid despite a broken sibling", good)
	}
	broken, ok := find(entries, "broken")
	if !ok {
		t.Fatal("broken workflow is not listed; it must be visible with its errors")
	}
	if broken.Valid() || len(broken.Errors) == 0 {
		t.Errorf("broken = %+v, want an invalid entry carrying errors", broken)
	}
	if broken.File == "" {
		t.Error("broken entry has no File; the error must point at a file")
	}
}

// TestRegistryUnparsableFileStillListed covers the file that cannot even be
// decoded, where the entry name falls back to the file name.
func TestRegistryUnparsableFileStillListed(t *testing.T) {
	reg, globalDir := newTestRegistry(t)
	writeWorkflow(t, globalDir, "garbage", "\tthis: is: not: yaml\n  - [\n")
	reg.ReloadGlobal()

	entry, ok := find(reg.List(0), "garbage")
	if !ok {
		t.Fatalf("unparsable file is not listed: %v", reg.List(0))
	}
	if entry.Valid() || len(entry.Errors) == 0 {
		t.Errorf("entry = %+v, want invalid with errors", entry)
	}
}

func TestRegistryDuplicateNameInScope(t *testing.T) {
	reg, globalDir := newTestRegistry(t)
	writeWorkflow(t, globalDir, "a-first", manualWorkflow("dup", "first"))
	writeWorkflow(t, globalDir, "b-second", manualWorkflow("dup", "second"))
	reg.ReloadGlobal()

	entry, ok := find(reg.List(0), "dup")
	if !ok {
		t.Fatal("duplicate name not listed at all")
	}
	// Files load in name order: a-first wins, b-second is reported.
	if entry.Valid() {
		t.Errorf("entry = %+v, want the second file to invalidate the name", entry)
	}
	if len(entry.Errors) == 0 || entry.Errors[0].Message == "" {
		t.Errorf("errors = %v, want a duplicate-name message", entry.Errors)
	}
}

func TestRegistrySetProjectsDropsRemovedScopes(t *testing.T) {
	reg, _ := newTestRegistry(t)
	repo := t.TempDir()
	writeWorkflow(t, filepath.Join(repo, ProjectDirName), "p", manualWorkflow("p", "project"))
	reg.SetProjects(map[int64]string{1: repo})
	if _, ok := find(reg.List(1), "p"); !ok {
		t.Fatal("project workflow not loaded")
	}

	reg.SetProjects(map[int64]string{})
	if _, ok := find(reg.List(1), "p"); ok {
		t.Error("project workflow still listed after the project was removed")
	}
}

// TestRegistrySetProjectsFollowsRepoint covers PATCH /v1/projects path
// re-pointing: the same id must pick up the new repo's workflows.
func TestRegistrySetProjectsFollowsRepoint(t *testing.T) {
	reg, _ := newTestRegistry(t)
	oldRepo, newRepo := t.TempDir(), t.TempDir()
	writeWorkflow(t, filepath.Join(oldRepo, ProjectDirName), "old", manualWorkflow("old", "old"))
	writeWorkflow(t, filepath.Join(newRepo, ProjectDirName), "new", manualWorkflow("new", "new"))

	reg.SetProjects(map[int64]string{1: oldRepo})
	reg.SetProjects(map[int64]string{1: newRepo})

	if _, ok := find(reg.List(1), "old"); ok {
		t.Error("old repo's workflow still listed after re-pointing")
	}
	if _, ok := find(reg.List(1), "new"); !ok {
		t.Error("new repo's workflow not listed after re-pointing")
	}
}

func TestRegistryOnChangeFires(t *testing.T) {
	reg, globalDir := newTestRegistry(t)
	writeWorkflow(t, globalDir, "w", manualWorkflow("w", "x"))
	calls := 0
	reg.OnChange(func() { calls++ })
	reg.ReloadGlobal()
	if calls != 1 {
		t.Errorf("OnChange calls = %d, want 1", calls)
	}
}
