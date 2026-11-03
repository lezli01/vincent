package issues

import (
	"testing"

	"github.com/lezli01/vincent/internal/jira/jiramap"
)

// TestJiramapBoundsMatch: internal/jira/jiramap repeats these bounds because
// it cannot import this package (it imports the store); they must agree, or
// an imported Jira kind or label could pass the mapping and fail here.
func TestJiramapBoundsMatch(t *testing.T) {
	if jiramap.MaxKindBytes != MaxKindBytes {
		t.Errorf("jiramap.MaxKindBytes = %d, issues.MaxKindBytes = %d", jiramap.MaxKindBytes, MaxKindBytes)
	}
	if jiramap.MaxLabelBytes != MaxLabelBytes {
		t.Errorf("jiramap.MaxLabelBytes = %d, issues.MaxLabelBytes = %d", jiramap.MaxLabelBytes, MaxLabelBytes)
	}
	for _, k := range []string{"bug", "sub-task", "new-feature", "a_b"} {
		if !kindPattern.MatchString(k) || jiramap.KindSeed(k) != k {
			t.Errorf("kind %q: issues accepts %v, jiramap seeds %q", k, kindPattern.MatchString(k), jiramap.KindSeed(k))
		}
	}
}
