package issuesource

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/lezli01/vincent/internal/issuestate"
	"github.com/lezli01/vincent/internal/jira"
	"github.com/lezli01/vincent/internal/jira/jiramap"
)

// TestChooserReasonsAreJiramaps pins the seam's three transition reasons to
// the chooser's codes (task 142.13), the way TestReasonsAreGitHubs pins the
// rest to GitHub's.
func TestChooserReasonsAreJiramaps(t *testing.T) {
	for ours, theirs := range map[string]string{
		ReasonNoTransition:          jiramap.CodeNoTransition,
		ReasonAmbiguousTransition:   jiramap.CodeAmbiguousTransition,
		ReasonTransitionNeedsFields: jiramap.CodeTransitionNeedsFields,
		ReasonNoWriteScope:          jira.ReasonNoWriteScope,
	} {
		if ours != theirs {
			t.Errorf("reason %q != %q", ours, theirs)
		}
	}
}

// TestJiraClassifiesChooserFailures: a *jiramap.ChooseError, wrapped or not,
// classifies as its Code; a write's 403 as no_write_scope.
func TestJiraClassifiesChooserFailures(t *testing.T) {
	src := NewJira(nil)
	for _, tc := range []struct {
		ts   []jiramap.Transition
		want string
	}{
		{nil, ReasonNoTransition},
		{[]jiramap.Transition{
			{ID: "1", Name: "Done", To: jiramap.Status{Name: "Done", StatusCategory: jiramap.StatusCategory{Key: jiramap.CategoryDone}}},
			{ID: "2", Name: "Finish", To: jiramap.Status{Name: "Finished", StatusCategory: jiramap.StatusCategory{Key: jiramap.CategoryDone}}},
		}, ReasonAmbiguousTransition},
		{[]jiramap.Transition{{
			ID: "1", Name: "Done", To: jiramap.Status{Name: "Done", StatusCategory: jiramap.StatusCategory{Key: jiramap.CategoryDone}},
			Fields: map[string]jiramap.FieldMeta{"customfield_1": {Required: true}},
		}}, ReasonTransitionNeedsFields},
	} {
		_, err := jiramap.ChooseTransition(issuestate.Close, issuestate.Completed, tc.ts, jiramap.Config{})
		var ce *jiramap.ChooseError
		if !errors.As(err, &ce) {
			t.Fatalf("chooser over %v answered %v", tc.ts, err)
		}
		if got := src.Classify(fmt.Errorf("wrapped: %w", err)).Reason; got != tc.want {
			t.Errorf("Classify(%v) = %q, want %q", err, got, tc.want)
		}
	}
	err := &jira.Error{Op: "transition", Status: http.StatusForbidden, Reason: jira.ReasonNoWriteScope}
	if got := src.Classify(err).Reason; got != ReasonNoWriteScope {
		t.Errorf("a write 403 classifies as %q", got)
	}
}
