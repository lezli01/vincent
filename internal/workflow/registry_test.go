package workflow

import (
	"os"
	"path/filepath"
	"strings"
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

	// Files load in name order: a-first wins the name and stays runnable.
	winner, ok := reg.Lookup(0, "dup")
	if !ok || !winner.Valid() || winner.Workflow.Description != "first" {
		t.Fatalf("Lookup(dup) = %+v, %v; want the first file's valid workflow", winner, ok)
	}

	// b-second is still listed under the same name, as an invalid entry
	// carrying a duplicate error — visible without shadowing the winner.
	var listedValid, listedDup bool
	for _, e := range reg.List(0) {
		if e.Name != "dup" {
			continue
		}
		if e.Valid() {
			listedValid = true
			continue
		}
		listedDup = true
		if len(e.Errors) == 0 || !strings.Contains(e.Errors[0].Message, "duplicate workflow name") {
			t.Errorf("loser errors = %v, want a duplicate-name message", e.Errors)
		}
		if filepath.Base(e.File) != "b-second.yaml" {
			t.Errorf("loser file = %q, want b-second.yaml", e.File)
		}
	}
	if !listedValid || !listedDup {
		t.Errorf("List = %+v, want both the valid winner and the duplicate loser", reg.List(0))
	}
}

// TestRegistryDuplicateNameUnparsableFile covers a broken file whose fallback
// name collides with a valid sibling: the valid workflow must stay lookupable
// and runnable, with the broken file surfaced as its own error entry (§5.2).
func TestRegistryDuplicateNameUnparsableFile(t *testing.T) {
	reg, globalDir := newTestRegistry(t)
	writeWorkflow(t, globalDir, "deploy", manualWorkflow("deploy", "good"))
	broken := filepath.Join(globalDir, "deploy.yml")
	if err := os.WriteFile(broken, []byte("\tthis: is: not: yaml\n  - [\n"), 0o600); err != nil {
		t.Fatalf("write %s: %v", broken, err)
	}
	reg.ReloadGlobal()

	got, ok := reg.Lookup(0, "deploy")
	if !ok || !got.Valid() || got.Workflow.Description != "good" {
		t.Fatalf("Lookup(deploy) = %+v, %v; want the valid workflow despite the broken sibling", got, ok)
	}

	var found bool
	for _, e := range reg.List(0) {
		if e.File != broken {
			continue
		}
		found = true
		if e.Valid() || len(e.Errors) == 0 {
			t.Errorf("broken entry = %+v, want invalid with errors", e)
		}
	}
	if !found {
		t.Fatal("broken duplicate file is not listed; it must be visible with its errors")
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
