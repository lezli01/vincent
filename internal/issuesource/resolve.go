package issuesource

import (
	"context"

	"github.com/lezli01/vincent/internal/github"
	"github.com/lezli01/vincent/internal/gitx"
	"github.com/lezli01/vincent/internal/store"
)

// ProviderJira is the provider column's value for every Jira row, named
// here beside ProviderGitHub so the daemon's per-provider loops (task 142.7)
// name both without a string literal.
const ProviderJira = store.IssueProviderJira

// Resolution is a project's effective issue source (task 142.7): an
// explicit binding wins; otherwise it is derived from origin — github when
// origin is a github.com remote, none otherwise.
//
// It is the one definition the daemon's tick, "sync now" and the API all
// read, so that what a project syncs with, what a "sync now" wakes and what
// the API reports cannot disagree. It carries no config: whether a
// provider's loop runs at all (config's github and jira switches) is each
// caller's gate, which keeps this package free of internal/config.
type Resolution struct {
	// Provider is store.IssueProviderNone, IssueProviderGitHub or
	// IssueProviderJira — never IssueProviderDerive.
	Provider string
	// Explicit is whether the project's stored binding chose Provider
	// (false: derived from origin).
	Explicit bool
	// OriginGitHub is whether origin is a github.com remote, and Repo that
	// repository when it is — whatever Provider is (a jira-bound project on
	// github.com still links pull requests).
	OriginGitHub bool
	Repo         github.Repo
	// JiraSite and JiraProjectKey are the binding's, set when Provider is jira.
	JiraSite, JiraProjectKey string
}

// ResolveWith resolves project against an origin the caller has already
// read (repo, originGitHub as RepoOf returns them). Pure; no I/O. The
// provider is store.IssueSource.Effective's answer, so the store's rule and
// this one are the same rule.
func ResolveWith(project store.Project, repo github.Repo, originGitHub bool) Resolution {
	src := project.IssueSource
	r := Resolution{
		Provider:     src.Effective(originGitHub),
		Explicit:     src.Provider != store.IssueProviderDerive,
		OriginGitHub: originGitHub,
	}
	if originGitHub {
		r.Repo = repo
	}
	if r.Provider == store.IssueProviderJira {
		r.JiraSite, r.JiraProjectKey = src.JiraSite, src.JiraProjectKey
	}
	return r
}

// Resolve reads origin with git (via RepoOf; a nil git reads as "origin is
// not GitHub") and resolves.
func Resolve(ctx context.Context, git *gitx.Git, project store.Project) Resolution {
	repo, ok := RepoOf(ctx, git, project)
	return ResolveWith(project, repo, ok)
}
