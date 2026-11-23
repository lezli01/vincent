package trigger

import (
	"strings"
	"testing"
)

// #1123: every source a create offers starts from a file that loads, is
// disabled, and carries no on_fire line.
func TestEveryStarterValidates(t *testing.T) {
	for _, typ := range append([]string{""}, StarterSourceTypes()...) {
		src := Starter(StarterSpec{ID: "t1", Project: 3, SourceType: typ})
		d, errs := Parse(src, "t1")
		if len(errs) > 0 {
			t.Errorf("the %q starter does not load: %v\n%s", typ, errs, src)
			continue
		}
		if d.Enabled || strings.Contains(string(src), "on_fire") {
			t.Errorf("the %q starter is armed or sets on_fire:\n%s", typ, src)
		}
		if want := starterType(typ); d.Source.Type != want {
			t.Errorf("the %q starter has source.type %q", typ, d.Source.Type)
		}
	}
	for _, typ := range StarterSourceTypes() {
		if typ == SourceGitHubIssues {
			t.Error("the deprecated github_issues is offered as a starter")
		}
	}
}
