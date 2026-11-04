package issuesource

import (
	"testing"

	"github.com/lezli01/vincent/internal/github"
	"github.com/lezli01/vincent/internal/store"
)

func TestResolveWith(t *testing.T) {
	gh := github.Repo{Owner: "o", Name: "r"}
	jira := store.IssueSource{Provider: store.IssueProviderJira, JiraSite: "acme.atlassian.net", JiraProjectKey: "ACME", JiraJQL: "x"}
	cases := []struct {
		name   string
		src    store.IssueSource
		origin bool
		want   Resolution
	}{
		{
			name: "derive, github origin", origin: true,
			want: Resolution{Provider: store.IssueProviderGitHub, OriginGitHub: true, Repo: gh},
		},
		{
			name: "derive, other origin",
			want: Resolution{Provider: store.IssueProviderNone},
		},
		{
			name: "explicit none, github origin", src: store.IssueSource{Provider: store.IssueProviderNone}, origin: true,
			want: Resolution{Provider: store.IssueProviderNone, Explicit: true, OriginGitHub: true, Repo: gh},
		},
		{
			name: "explicit none, other origin", src: store.IssueSource{Provider: store.IssueProviderNone},
			want: Resolution{Provider: store.IssueProviderNone, Explicit: true},
		},
		{
			name: "explicit github, github origin", src: store.IssueSource{Provider: store.IssueProviderGitHub}, origin: true,
			want: Resolution{Provider: store.IssueProviderGitHub, Explicit: true, OriginGitHub: true, Repo: gh},
		},
		{
			name: "explicit github, other origin", src: store.IssueSource{Provider: store.IssueProviderGitHub},
			want: Resolution{Provider: store.IssueProviderGitHub, Explicit: true},
		},
		{
			name: "explicit jira, github origin", src: jira, origin: true,
			want: Resolution{
				Provider: store.IssueProviderJira, Explicit: true, OriginGitHub: true, Repo: gh,
				JiraSite: "acme.atlassian.net", JiraProjectKey: "ACME",
			},
		},
		{
			name: "explicit jira, other origin", src: jira,
			want: Resolution{
				Provider: store.IssueProviderJira, Explicit: true,
				JiraSite: "acme.atlassian.net", JiraProjectKey: "ACME",
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := github.Repo{}
			if tc.origin {
				repo = gh
			}
			p := store.Project{ID: 1, IssueSource: tc.src}
			got := ResolveWith(p, repo, tc.origin)
			if got != tc.want {
				t.Errorf("ResolveWith = %+v, want %+v", got, tc.want)
			}
			if eff := tc.src.Effective(tc.origin); got.Provider != eff {
				t.Errorf("Provider %q disagrees with IssueSource.Effective %q", got.Provider, eff)
			}
		})
	}
}

// A nil git reads origin as not GitHub: derive resolves to none, and an
// explicit binding still wins.
func TestResolveNilGitIsNotGitHub(t *testing.T) {
	p := store.Project{ID: 1, Path: t.TempDir()}
	if got := Resolve(t.Context(), nil, p); got != (Resolution{Provider: store.IssueProviderNone}) {
		t.Errorf("Resolve(nil git, derive) = %+v", got)
	}
	p.IssueSource = store.IssueSource{Provider: store.IssueProviderJira, JiraSite: "s", JiraProjectKey: "K"}
	if got := Resolve(t.Context(), nil, p); got.Provider != ProviderJira || got.JiraSite != "s" || got.OriginGitHub {
		t.Errorf("Resolve(nil git, jira) = %+v", got)
	}
}

func TestProviderJira(t *testing.T) {
	if ProviderJira != "jira" {
		t.Errorf("ProviderJira = %q", ProviderJira)
	}
}
