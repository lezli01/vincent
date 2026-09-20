package agent_test

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/agent"
	"github.com/lezli01/vincent/internal/agent/agenttest"
)

// The bundled half of the skill cache (§9.6, task 124.16, #512): claude marks
// its bundled skills and its built-in commands alike, and a turn's init line
// is the one machine signal that separates them.

// withBuiltins is a listing whose rows alternate ordinary and `builtin`, so a
// test can see both that the right rows are withheld and that order survives.
func withBuiltins(rows ...agent.Skill) agent.SkillList {
	return agent.SkillList{Skills: rows}
}

func skill(name string) agent.Skill { return agent.Skill{Name: name, Description: name} }
func builtin(name string) agent.Skill {
	return agent.Skill{Name: name, Description: name, Builtin: true}
}

func names(ans agent.SkillAnswer) []string {
	out := make([]string, 0, len(ans.Skills))
	for _, s := range ans.Skills {
		out = append(out, s.Name)
	}
	return out
}

// bundledListing is one project skill, one bundled skill and one built-in
// command, in that order — claude's shape in miniature.
func bundledListing() agent.SkillList {
	return withBuiltins(skill("proj-skill"), builtin("simplify"), builtin("clear"))
}

// TestBundledRowsAreWithheldUntilATurnNamesThem is the whole classification in
// one pass: before any turn nothing `builtin` is served and the answer says
// why; after a turn naming `simplify` and not `clear`, `simplify` comes back
// flagged, in the CLI's place, and `clear` is served by nothing.
func TestBundledRowsAreWithheldUntilATurnNamesThem(t *testing.T) {
	c, _ := newSkillCache()
	stub := &agenttest.StubSkills{}
	stub.Script(bundledListing(), nil)
	dir := t.TempDir()

	before := c.Lookup(t.Context(), stub, dir, false)
	if got := names(before); len(got) != 1 || got[0] != "proj-skill" {
		t.Fatalf("names before any turn = %q, want only the non-builtin row", got)
	}
	if before.Bundled != agent.BundledAfterFirstTurn {
		t.Errorf("Bundled = %q, want %q", before.Bundled, agent.BundledAfterFirstTurn)
	}

	// What a claude turn's init line reports: bundled skills, no commands.
	c.ReportBundled(stub, []string{"proj-skill", "simplify"})

	after := c.Lookup(t.Context(), stub, dir, false)
	wantCalls(t, stub, 1) // the classification is not a reason to re-probe
	got := names(after)
	if len(got) != 2 || got[0] != "proj-skill" || got[1] != "simplify" {
		t.Fatalf("names after the turn = %q, want the listing minus `clear`, in order", got)
	}
	if after.Bundled != agent.BundledListed {
		t.Errorf("Bundled = %q, want %q", after.Bundled, agent.BundledListed)
	}
	if after.Skills[0].Builtin || !after.Skills[1].Builtin {
		t.Errorf("Builtin flags = %v/%v, want false for the project skill and true for the bundled one",
			after.Skills[0].Builtin, after.Skills[1].Builtin)
	}
}

// TestBundledSetIsKeyedByTheBinaryNotTheDirectory: the first turn anywhere
// restores the rows everywhere on that CLI, so a chat created a moment ago is
// not penalised for being new — while another binary inherits nothing.
func TestBundledSetIsKeyedByTheBinaryNotTheDirectory(t *testing.T) {
	c, _ := newSkillCache()
	one := &fileSkills{StubSkills: &agenttest.StubSkills{}, path: binaryFile(t, "claude-a")}
	one.Script(bundledListing(), nil)
	first, second := t.TempDir(), t.TempDir()

	if got := c.Lookup(t.Context(), one, first, false); got.Bundled != agent.BundledAfterFirstTurn {
		t.Fatalf("Bundled = %q, want %q", got.Bundled, agent.BundledAfterFirstTurn)
	}
	c.ReportBundled(one, []string{"simplify"})

	// A directory that has never had a turn of its own, on the same binary.
	other := c.Lookup(t.Context(), one, second, false)
	if got := names(other); len(got) != 2 || got[1] != "simplify" {
		t.Errorf("a second directory = %q, want the restored bundled row", got)
	}
	if other.Bundled != agent.BundledListed {
		t.Errorf("Bundled = %q, want %q", other.Bundled, agent.BundledListed)
	}

	// A different binary identity, which is what an upgraded CLI is too.
	two := &fileSkills{StubSkills: &agenttest.StubSkills{}, path: binaryFile(t, "claude-b")}
	two.Script(bundledListing(), nil)
	if got := c.Lookup(t.Context(), two, first, false); got.Bundled != agent.BundledAfterFirstTurn {
		t.Errorf("another binary = %q, want %q: a set is never inherited",
			got.Bundled, agent.BundledAfterFirstTurn)
	} else if n := names(got); len(n) != 1 {
		t.Errorf("another binary listed %q, want the non-builtin row alone", n)
	}
}

// TestBundledStateIsEmptyWhenTheQuestionDoesNotArise: a listing with no
// `builtin` row — every codex and cursor one, neither CLI having the concept
// — and an answer that is not a list at all.
func TestBundledStateIsEmptyWhenTheQuestionDoesNotArise(t *testing.T) {
	c, _ := newSkillCache()
	plain := &agenttest.StubSkills{}
	plain.Script(listed("review", "ship"), nil)
	if got := c.Lookup(t.Context(), plain, t.TempDir(), false); got.Bundled != agent.BundledNotApplicable {
		t.Errorf("Bundled = %q on a listing with no builtin row, want empty", got.Bundled)
	}

	no := &namedSkills{StubSkills: &agenttest.StubSkills{}, name: "codex"}
	no.Script(agent.SkillList{}, agent.ErrSkillsUnsupported)
	if got := c.Lookup(t.Context(), no, t.TempDir(), false); got.Bundled != agent.BundledNotApplicable {
		t.Errorf("Bundled = %q on an unsupported build, want empty", got.Bundled)
	}
}

// TestReportBundledIsToleratedWhereItCannotAct: the nil receiver a runner
// built without a cache has, a nil adapter, and the empty report a build too
// old to send `skills` would make — none of which may panic, and none of
// which may be read as "this CLI bundles nothing".
func TestReportBundledIsToleratedWhereItCannotAct(t *testing.T) {
	var nilCache *agent.SkillCache
	nilCache.ReportBundled(&agenttest.StubSkills{}, []string{"simplify"})

	c, _ := newSkillCache()
	c.ReportBundled(nil, []string{"simplify"})
	stub := &agenttest.StubSkills{}
	stub.Script(bundledListing(), nil)
	c.ReportBundled(stub, nil)
	if got := c.Lookup(t.Context(), stub, t.TempDir(), false); got.Bundled != agent.BundledAfterFirstTurn {
		t.Errorf("Bundled = %q after an empty report, want %q: no names is not an answer",
			got.Bundled, agent.BundledAfterFirstTurn)
	}
}

// TestBundledRegistryIsBounded: distinct binaries cannot grow it without
// limit, and the least recently used set is the one that goes.
func TestBundledRegistryIsBounded(t *testing.T) {
	c, _ := newSkillCache()
	adapters := make([]*fileSkills, 0, agent.BundledMax+1)
	for i := range agent.BundledMax + 1 {
		a := &fileSkills{StubSkills: &agenttest.StubSkills{}, path: binaryFile(t, fmt.Sprintf("claude-%d", i))}
		a.Script(bundledListing(), nil)
		adapters = append(adapters, a)
		c.ReportBundled(a, []string{"simplify"})
	}
	if got := agent.BundledSetCount(c); got != agent.BundledMax {
		t.Fatalf("the registry holds %d sets, want at most %d", got, agent.BundledMax)
	}
	// The first reporter is the least recently used, and so the evicted one.
	if got := c.Lookup(t.Context(), adapters[0], t.TempDir(), false); got.Bundled != agent.BundledAfterFirstTurn {
		t.Errorf("the evicted binary = %q, want %q", got.Bundled, agent.BundledAfterFirstTurn)
	}
	last := adapters[len(adapters)-1]
	if got := c.Lookup(t.Context(), last, t.TempDir(), false); got.Bundled != agent.BundledListed {
		t.Errorf("the newest binary = %q, want %q", got.Bundled, agent.BundledListed)
	}
}

// TestBundledFilteringNeverMutatesTheCachedList: serve builds its own slice,
// so one caller's answer cannot shorten the next one's — the cache's rows are
// shared with everybody.
func TestBundledFilteringNeverMutatesTheCachedList(t *testing.T) {
	c, _ := newSkillCache()
	stub := &agenttest.StubSkills{}
	stub.Script(bundledListing(), nil)
	dir := t.TempDir()

	withheld := c.Lookup(t.Context(), stub, dir, false)
	c.ReportBundled(stub, []string{"simplify", "clear"})
	restored := c.Lookup(t.Context(), stub, dir, false)
	wantCalls(t, stub, 1)

	if got := len(withheld.Skills); got != 1 {
		t.Errorf("the first answer now holds %d rows, want the 1 it was served", got)
	}
	if got := names(restored); len(got) != 3 {
		t.Fatalf("names = %q, want all three rows once both builtins are claimed", got)
	}
}

// binaryFile is a real file standing in for an installed CLI, so the cache's
// binary identity — path plus mtime — is a distinct key per name.
func binaryFile(t *testing.T, name string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	// Distinct mtimes as well as distinct paths, so neither alone is what
	// the test leans on.
	stamp := time.Date(2026, 9, 19, 10, 0, 0, 0, time.UTC).Add(time.Duration(len(name)) * time.Second)
	if err := os.Chtimes(p, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	return p
}
