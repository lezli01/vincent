package store

import (
	"testing"

	"github.com/lezli01/vincent/internal/jira/jiramap"
)

// TestRemotePayloadKeyReadsJiramapsRecord is review F3: task 142.8 reads a
// Jira issue's display key from remote_json's `key`, and jiramap.RemoteJSON
// is the record #908 stores. The two must agree, or every Jira issue's ref
// renders empty.
func TestRemotePayloadKeyReadsJiramapsRecord(t *testing.T) {
	raw := jiramap.RemoteJSON("ABC-123", jiramap.Fields{})
	if got := RemotePayloadKey(string(raw)); got != "ABC-123" {
		t.Errorf("RemotePayloadKey(%s) = %q, want ABC-123", raw, got)
	}
}
