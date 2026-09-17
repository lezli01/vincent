package tui

import "testing"

// TestTaskRestoredRefreshesLikeTaskDeleted: an import (task 117) announces
// itself as task.restored, and every board that refetches on task.deleted must
// refetch on it too, or an open archived board would not show the row.
func TestTaskRestoredRefreshesLikeTaskDeleted(t *testing.T) {
	for _, typ := range []string{"task.deleted", "task.restored"} {
		if !isTaskEvent(typ) {
			t.Errorf("isTaskEvent(%q) = false; the boards would not refresh on it", typ)
		}
	}
}
