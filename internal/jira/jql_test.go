package jira

import (
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/jira/fakejira"
)

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
	} {
		if _, err := fakejira.ParseJQL(jql); err != nil {
			t.Errorf("fakejira rejects %q: %v", jql, err)
		}
	}
}
