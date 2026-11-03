package jiratest

import "testing"

func TestBuildFakeJiraCompilesOnce(t *testing.T) {
	first := BuildFakeJira(t)
	if second := BuildFakeJira(t); second != first {
		t.Fatalf("two builds: %s and %s", first, second)
	}
}
