package issues

import (
	"fmt"
	"strconv"

	"github.com/lezli01/vincent/internal/store"
)

// providerJira is issue_remotes.provider for an issue imported from Jira
// (task 142, #896). Nothing imports one yet; the renderer knows its shape so
// a client never has to.
const providerJira = "jira"

// SourceRef is how an imported issue's reference reads (task 142.8): the
// daemon renders it so a client prints it and never builds one from a
// provider's parts. Key is the provider's display key — the number, as a
// string, for GitHub; `PROJ-12` for Jira. Ref is the full reference
// (`owner/repo#142`, `PROJ-12`), ShortRef the compact one a list prints when
// every imported row shares one provider and repo (`gh#142`, `PROJ-12`), and
// ProviderName the provider's display name (`GitHub`, `Jira`).
type SourceRef struct {
	Key          string
	Ref          string
	ShortRef     string
	ProviderName string
}

// RenderSource renders a reference from a provider's parts (task 142
// decision 11.2). It is the one place a reference is spelled: the issue DTO and
// both halves of a task's `issue` — the live issue and its frozen snapshot —
// call it, so a snapshot taken before keys were stored still gets a
// reference. key is the stored display key and may be empty: for GitHub it
// falls back to the number. An unknown provider keeps its id as its name and
// reads as repo#number, or its key when it has no number.
func RenderSource(provider, repo string, number int, key string) SourceRef {
	switch provider {
	case providerGitHub:
		if key == "" && number > 0 {
			key = strconv.Itoa(number)
		}
		ref := repoRef(repo, number)
		short := ref
		if number > 0 {
			short = "gh#" + strconv.Itoa(number)
		}
		return SourceRef{Key: key, Ref: ref, ShortRef: short, ProviderName: "GitHub"}
	case providerJira:
		return SourceRef{Key: key, Ref: key, ShortRef: key, ProviderName: "Jira"}
	}
	ref := key
	if number > 0 || ref == "" {
		ref = repoRef(repo, number)
	}
	return SourceRef{Key: key, Ref: ref, ShortRef: ref, ProviderName: provider}
}

// repoRef is repo#number, the bare repo when number is 0.
func repoRef(repo string, number int) string {
	if number <= 0 {
		return repo
	}
	return repo + "#" + strconv.Itoa(number)
}

// RemoteKey is the display key of a live issue's remote (task 142.8): a
// Jira issue's `key` is read from the stored payload at read time — its
// issue_remotes.remote_key is the provider's id, not what a person reads —
// and every other provider has none, which RenderSource fills from the
// number. Nil-safe.
func RemoteKey(rem *store.IssueRemote) string {
	if rem == nil || rem.Provider == providerGitHub {
		return ""
	}
	return store.RemotePayloadKey(rem.RemoteJSON)
}

// RenderRemote renders a live issue's remote; the zero SourceRef for nil.
func RenderRemote(rem *store.IssueRemote) SourceRef {
	if rem == nil {
		return SourceRef{}
	}
	return RenderSource(rem.Provider, rem.Repo, rem.Number, RemoteKey(rem))
}

// ProviderName is the display name of iss's remote provider, "" for a
// local issue. Text that names where a write goes reads it rather than
// spelling a provider.
func ProviderName(iss *store.Issue) string {
	if iss == nil || iss.Remote == nil {
		return ""
	}
	return RenderSource(iss.Remote.Provider, "", 0, "").ProviderName
}

// ForgeWriteError is ErrForgeWriteNeedsHuman for iss, its prose naming
// the issue's provider (task 142 decision 11.3): "issue 7: only a human, or
// a handoff a human granted it, may write to GitHub". errors.Is still
// matches the sentinel. A local issue keeps the sentinel's neutral wording.
func ForgeWriteError(iss *store.Issue) error {
	return &forgeWriteError{prefix: fmt.Sprintf("issue %d", iss.ID), provider: ProviderName(iss)}
}

// forgeWriteError is a refusal whose prose names a provider: prefix says
// what was refused, provider where it would have written ("" for the
// sentinel's own wording).
type forgeWriteError struct {
	prefix   string
	provider string
}

func (e *forgeWriteError) Error() string {
	if e.provider == "" {
		return e.prefix + ": " + ErrForgeWriteNeedsHuman.Error()
	}
	return e.prefix + ": only a human, or a handoff a human granted it, may write to " + e.provider
}

func (*forgeWriteError) Unwrap() error { return ErrForgeWriteNeedsHuman }
