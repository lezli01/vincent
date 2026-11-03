package issues

import (
	"errors"
	"testing"

	"github.com/lezli01/vincent/internal/store"
)

func TestRenderSource(t *testing.T) {
	for _, tc := range []struct {
		name                string
		provider, repo, key string
		number              int
		want                SourceRef
	}{
		{"github", "github", "octo/repo", "", 142, SourceRef{Key: "142", Ref: "octo/repo#142", ShortRef: "gh#142", ProviderName: "GitHub"}},
		{"github without a number", "github", "octo/repo", "", 0, SourceRef{Ref: "octo/repo", ShortRef: "octo/repo", ProviderName: "GitHub"}},
		{"github with a stored key", "github", "octo/repo", "142", 142, SourceRef{Key: "142", Ref: "octo/repo#142", ShortRef: "gh#142", ProviderName: "GitHub"}},
		{"jira", "jira", "", "PROJ-12", 0, SourceRef{Key: "PROJ-12", Ref: "PROJ-12", ShortRef: "PROJ-12", ProviderName: "Jira"}},
		{"unknown with a number", "gitea", "o/r", "", 7, SourceRef{Ref: "o/r#7", ShortRef: "o/r#7", ProviderName: "gitea"}},
		{"unknown with a key", "linear", "", "ENG-3", 0, SourceRef{Key: "ENG-3", Ref: "ENG-3", ShortRef: "ENG-3", ProviderName: "linear"}},
	} {
		if got := RenderSource(tc.provider, tc.repo, tc.number, tc.key); got != tc.want {
			t.Errorf("%s: RenderSource = %+v, want %+v", tc.name, got, tc.want)
		}
	}
}

func TestRemoteKeyReadsThePayload(t *testing.T) {
	jira := &store.IssueRemote{Provider: "jira", RemoteJSON: `{"key":"PROJ-12"}`}
	if got := RenderRemote(jira); got.Ref != "PROJ-12" || got.ProviderName != "Jira" {
		t.Errorf("jira remote = %+v", got)
	}
	// GitHub's payload is never read for a key: its key is its number.
	gh := &store.IssueRemote{Provider: "github", Repo: "o/r", Number: 3, RemoteJSON: `{"key":"nope"}`}
	if got := RenderRemote(gh); got.Key != "3" || got.Ref != "o/r#3" {
		t.Errorf("github remote = %+v", got)
	}
	if got := RenderRemote(nil); got != (SourceRef{}) {
		t.Errorf("nil remote = %+v", got)
	}
}

func TestForgeWriteErrorNamesTheProvider(t *testing.T) {
	iss := &store.Issue{ID: 7, Remote: &store.IssueRemote{Provider: "github"}}
	err := ForgeWriteError(iss)
	if !errors.Is(err, ErrForgeWriteNeedsHuman) {
		t.Errorf("%v does not match the sentinel", err)
	}
	if want := "issue 7: only a human, or a handoff a human granted it, may write to GitHub"; err.Error() != want {
		t.Errorf("err = %q, want %q", err, want)
	}
	if got := ForgeWriteError(&store.Issue{ID: 8}).Error(); got != "issue 8: "+ErrForgeWriteNeedsHuman.Error() {
		t.Errorf("local issue err = %q", got)
	}
}
