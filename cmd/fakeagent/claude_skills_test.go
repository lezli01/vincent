package main_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lezli01/vincent/internal/agent"
	"github.com/lezli01/vincent/internal/agent/agenttest"
	"github.com/lezli01/vincent/internal/agent/claude"
)

// seedClaudeSkill writes one `.claude/skills/<dir>/SKILL.md` under root.
func seedClaudeSkill(t *testing.T, root, dir, doc string) {
	t.Helper()
	at := filepath.Join(root, ".claude", "skills", dir)
	if err := os.MkdirAll(at, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(at, "SKILL.md"), []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}
}

// listNames is every listed name, in the order the CLI reported them.
func listNames(list agent.SkillList) []string {
	names := make([]string, 0, len(list.Skills))
	for _, s := range list.Skills {
		names = append(names, s.Name)
	}
	return names
}

// TestClaudeListsTheWorkingDirectorysSkills: the fake claude derives
// `initialize` rows from the directory it was started in, the way its
// app-server dialect already derives codex's from `.agents/skills` (task 124
// decision 44). It is driven through the real claude lister, because what the
// m14 leg asserts is what that lister makes of the reply.
//
// It is not parallel: the version probe reads the process environment, and
// the listing floor is above the fake's default version.
func TestClaudeListsTheWorkingDirectorysSkills(t *testing.T) {
	bin := agenttest.BuildFakeAgent(t)
	t.Setenv("FAKEAGENT_VERSION", "2.1.277")
	a := claude.New(func() string { return bin })

	dir := t.TempDir()
	seedClaudeSkill(t, dir, "gate-skill",
		"---\nname: gate-skill\ndescription: The gate's own skill.\nargument-hint: \"[target]\"\n---\n\nbody\n")
	seedClaudeSkill(t, dir, "second", "---\nname: second\ndescription: Another one.\n---\n\nbody\n")
	// No front matter, so no name: skipped rather than listed blank.
	seedClaudeSkill(t, dir, "nameless", "just a body\n")

	list, err := a.ListSkills(t.Context(), agent.SkillQuery{WorkDir: dir})
	if err != nil {
		t.Fatalf("ListSkills: %v", err)
	}
	// The default rows first, minus the built-in the lister drops, then the
	// directory's in directory order — which is what "appended" has to mean
	// for the existing callers to keep the rows they pin.
	want := []string{"fake-skill", "fake:tool", "gate-skill", "second"}
	got := listNames(list)
	if len(got) != len(want) {
		t.Fatalf("names = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("names = %v, want %v", got, want)
		}
	}
	seeded := list.Skills[2]
	if seeded.Description != "The gate's own skill." || seeded.ArgumentHint != "[target]" {
		t.Errorf("gate-skill = %+v, want its description and its [target] hint", seeded)
	}

	// A directory with no `.claude/skills` at all changes nothing, which is
	// every test and gate written before this one.
	bare, err := a.ListSkills(t.Context(), agent.SkillQuery{WorkDir: t.TempDir()})
	if err != nil {
		t.Fatalf("ListSkills in a bare directory: %v", err)
	}
	if names := listNames(bare); len(names) != 2 || names[0] != "fake-skill" || names[1] != "fake:tool" {
		t.Errorf("a bare directory listed %v, want only the default rows", names)
	}
}
