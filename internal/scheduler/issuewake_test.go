package scheduler

import (
	"testing"

	"github.com/lezli01/vincent/internal/store"
)

// TestIssueEventsNeverWake: nothing about admission depends on an issue
// (task 130), so no issue.* event may cost the scheduler a pass.
func TestIssueEventsNeverWake(t *testing.T) {
	for _, typ := range []string{
		store.EventIssueCreated, store.EventIssueUpdated, store.EventIssueStateChanged,
		store.EventIssueLabelsChanged, store.EventIssueCommentAdded, store.EventIssueDeleted,
	} {
		if WakeOn(&store.Event{Type: typ}) {
			t.Errorf("WakeOn(%s) = true", typ)
		}
	}
}
