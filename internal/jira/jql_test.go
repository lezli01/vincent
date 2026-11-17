package jira

import (
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/jira/fakejira"
)

// twoPlain is two projects, neither narrowed.
var twoPlain = []Scope{{Project: "10000"}, {Project: "10001"}}

// Several projects are one query (task 146): a plain `project in` when none
// narrows, else an OR in which each project's JQL is held to that project.
func TestJQLBuildersForSeveralProjects(t *testing.T) {
	since := time.Date(2026, 11, 4, 8, 0, 0, 0, time.UTC)
	mixed := []Scope{{Project: "10000", JQL: "labels = a OR labels = b"}, {Project: "10001"}, {Project: "10002"}}
	for _, tc := range []struct{ got, want string }{
		{InitialJQLFor(twoPlain, ""), "project in (10000, 10001) AND statusCategory != Done ORDER BY id ASC"},
		{IncrementalJQLFor(twoPlain, since, "10042"), "project in (10000, 10001) AND updated >= 1793779020000 AND id > 10042 ORDER BY id ASC"},
		{
			InitialJQLFor(mixed, ""),
			"((project = 10000 AND (labels = a OR labels = b)) OR project in (10001, 10002)) AND statusCategory != Done ORDER BY id ASC",
		},
		{
			IncrementalJQLFor([]Scope{{Project: "10000", JQL: "type = Bug"}, {Project: `"OPS"`, JQL: "labels = x"}}, time.Time{}, ""),
			`((project = 10000 AND (type = Bug)) OR (project = "OPS" AND (labels = x))) ORDER BY id ASC`,
		},
		{
			IncrementalJQLFor([]Scope{{Project: "10000", JQL: "type = Bug"}, {Project: "10001"}}, time.Time{}, ""),
			"((project = 10000 AND (type = Bug)) OR project = 10001) ORDER BY id ASC",
		},
	} {
		if tc.got != tc.want {
			t.Errorf("got  %s\nwant %s", tc.got, tc.want)
		}
	}
}

func TestJQLBuilders(t *testing.T) {
	since := time.Date(2026, 11, 4, 8, 0, 0, 0, time.UTC)
	ms := since.Add(-Overlap).UnixMilli()
	for _, tc := range []struct{ got, want string }{
		{InitialJQL("10000", "", ""), "project = 10000 AND statusCategory != Done ORDER BY id ASC"},
		{InitialJQL("10000", " labels = vincent ", "10042"), "project = 10000 AND statusCategory != Done AND (labels = vincent) AND id > 10042 ORDER BY id ASC"},
		{IncrementalJQL("10000", "", since, ""), "project = 10000 AND updated >= 1793779020000 ORDER BY id ASC"},
		{IncrementalJQL("10000", "type = Bug", since, ""), "project = 10000 AND (type = Bug) AND updated >= 1793779020000 ORDER BY id ASC"},
		{IncrementalJQL("10000", "", since, "10042"), "project = 10000 AND updated >= 1793779020000 AND id > 10042 ORDER BY id ASC"},
		{IncrementalJQL("10000", "", time.Time{}, ""), "project = 10000 ORDER BY id ASC"},
	} {
		if tc.got != tc.want {
			t.Errorf("got  %s\nwant %s", tc.got, tc.want)
		}
	}
	if ms != 1793779020000 {
		t.Fatalf("epoch ms %d", ms)
	}
	// The fake answers only its JQL subset; every query without extra JQL
	// must be inside it.
	for _, jql := range []string{
		InitialJQL("10000", "", ""), InitialJQL("10000", "", "7"),
		IncrementalJQL("10000", "", since, ""), IncrementalJQL("10000", "", since, "7"),
		IncrementalJQL("10000", "", time.Time{}, ""),
		InitialJQLFor(twoPlain, "7"), IncrementalJQLFor(twoPlain, since, "7"),
	} {
		if _, err := fakejira.ParseJQL(jql); err != nil {
			t.Errorf("fakejira rejects %q: %v", jql, err)
		}
	}
}
