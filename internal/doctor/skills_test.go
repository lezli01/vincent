package doctor

import (
	"context"
	"testing"

	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/skill"
)

// TestSkillsAreNeverAProblem is decision 5: the closed unhealthy set (task
// 005 decision 7) stays closed. Every skill missing, stale, ahead of this
// build and unreadable at once, and `vincent doctor` still exits 0 — the
// built-in workflows carry the skill's text in their own prompts, so nothing
// a row here can say stops a task from running.
func TestSkillsAreNeverAProblem(t *testing.T) {
	r := &Report{Skills: []Skill{
		{Name: "a", State: skill.StateAbsent},
		{Name: "b", State: skill.StateOlder, Installed: "1.0.0", Shipped: "2.0.0"},
		{Name: "c", State: skill.StateNewer, Installed: "3.0.0", Shipped: "2.0.0"},
		{Name: "d", State: skill.StateUnreadable, Message: "cannot read SKILL.md"},
		{Name: "e", State: skill.StateDiffers, Installed: "wednesday", Shipped: "2.0.0"},
	}}
	r.Evaluate()
	if len(r.Problems) != 0 {
		t.Fatalf("skills produced problems: %+v", r.Problems)
	}
	if !r.Healthy() {
		t.Error("a report whose only findings are skill rows is not healthy")
	}
}

// TestComposeFillsTheSkillsGroup proves the group is composed client-side,
// with no daemon and no database (decision 9). What the rows *say* depends on
// the machine the test runs on, so nothing here asserts a state — only that
// every published skill has one.
func TestComposeFillsTheSkillsGroup(t *testing.T) {
	dir := t.TempDir()
	rep := Compose(context.Background(), Options{
		Dirs:   config.Dirs{Config: dir, Data: dir},
		Daemon: Daemon{Status: StatusNotRunning},
	})
	if len(rep.Skills) == 0 {
		t.Fatal("no skills group; this build publishes at least one skill")
	}
	for _, s := range rep.Skills {
		if s.State == "" {
			t.Errorf("%s has no state", s.Name)
		}
		if s.Shipped == "" {
			t.Errorf("%s reports no shipped version", s.Name)
		}
		if s.StorePath == "" {
			t.Errorf("%s reports no store path", s.Name)
		}
	}
	if len(rep.Problems) != 0 {
		t.Errorf("a local compose produced problems: %+v", rep.Problems)
	}
}
