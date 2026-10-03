package apiclient_test

import (
	"testing"

	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/issuestate"
	"github.com/lezli01/vincent/internal/store"
)

// TestMarkedClientIsAnAgent is task 130.10 decisions 1-2 over the wire: a
// client built in a step's or a chat agent's environment carries the marker
// header, and the real handlers refuse its close of an imported issue with
// forge_write_needs_human; one built without either closes it, and the
// sync block reports the write pending. Not parallel: it sets the
// environment the client reads.
func TestMarkedClientIsAnAgent(t *testing.T) {
	for _, env := range []string{"VINCENT_TASK_ID", apiclient.EnvChatID} {
		t.Run(env, func(t *testing.T) {
			t.Setenv("VINCENT_TASK_ID", "")
			t.Setenv(apiclient.EnvChatID, "")
			human, st, pid := newIssuesClient(t)
			iss, _, err := st.UpsertRemoteIssue(t.Context(), store.RemoteIssue{
				ProjectID: pid, Provider: "github", RemoteKey: "I_1", Repo: "o/r", Number: 1,
				Title: "imported", State: issuestate.Open,
			}, issuestate.Sync)
			if err != nil {
				t.Fatal(err)
			}
			t.Setenv(env, "7")
			marked := apiclient.New(human.BaseURL(), testToken)
			_, err = marked.CloseIssue(t.Context(), iss.ID, apiclient.CloseIssueRequest{})
			if reason, _, ok := apiclient.IssueConflict(err); !ok || reason != apiclient.IssueReasonForgeWrite {
				t.Fatalf("marked close = %v, want %s", err, apiclient.IssueReasonForgeWrite)
			}
			got, err := human.CloseIssue(t.Context(), iss.ID, apiclient.CloseIssueRequest{})
			if err != nil {
				t.Fatalf("unmarked close: %v", err)
			}
			if got.State != "closed" || got.Sync == nil || got.Sync.State != "pending" {
				t.Errorf("unmarked close = %s / %+v, want closed with a pending write", got.State, got.Sync)
			}
			status, err := human.IssueSyncStatus(t.Context(), pid)
			if err != nil || status.WritesPending != 1 {
				t.Errorf("sync status = %+v, %v; want one pending write", status, err)
			}
		})
	}
}
