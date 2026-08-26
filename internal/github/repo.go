package github

import (
	"net/url"
	"strings"
)

// Host is the only host this package recognizes. GitHub Enterprise reaches
// vincent only through whatever `gh` itself resolves for a github.com-shaped
// remote; a bespoke host list is task 035 decision 10's explicit non-goal.
const Host = "github.com"

// Repo is a GitHub repository identity, derived from a git remote URL at the
// point of use and never stored (task 035 decision 5). Storing it would be a
// `Project` column and a migration for a fact the remote already carries; the
// alternative is held in reserve for PR checking, which needs a durable
// identity.
type Repo struct {
	Owner string
	Name  string
}

// String is the `owner/name` spelling `gh --repo` and the REST path both take.
func (r Repo) String() string { return r.Owner + "/" + r.Name }

// Zero reports that no repository was identified.
func (r Repo) Zero() bool { return r.Owner == "" || r.Name == "" }

// ParseRemote derives a Repo from a git remote URL, reporting false for
// anything that is not a github.com repository.
//
// It accepts the four spellings `git remote get-url` actually returns for a
// GitHub remote — https, ssh://, scp-style `git@github.com:owner/name`, and
// git:// — with or without the `.git` suffix and a trailing slash. It rejects
// every other host, a bare local path, and an SSH alias (`gh:owner/name`),
// because an alias's real host lives in the user's ssh config and this
// package does not read it. A project behind an alias is simply not
// GitHub-based for now (decision 5).
func ParseRemote(remote string) (Repo, bool) {
	remote = strings.TrimSpace(remote)
	if remote == "" {
		return Repo{}, false
	}
	// scp-style has no scheme and cannot be parsed as a URL: `git@host:path`.
	// The `//` guard keeps `ssh://git@github.com/o/n` on the URL path below.
	if !strings.Contains(remote, "//") {
		before, after, ok := strings.Cut(remote, ":")
		if !ok {
			return Repo{}, false
		}
		if !hostMatches(userHost(before)) {
			return Repo{}, false
		}
		return splitPath(after)
	}
	u, err := url.Parse(remote)
	if err != nil {
		return Repo{}, false
	}
	switch u.Scheme {
	case "https", "http", "ssh", "git":
	default:
		return Repo{}, false
	}
	if !hostMatches(u.Hostname()) {
		return Repo{}, false
	}
	return splitPath(u.Path)
}

// userHost strips an scp-style `user@` prefix.
func userHost(s string) string {
	if _, host, ok := strings.Cut(s, "@"); ok {
		return host
	}
	return s
}

// hostMatches accepts github.com and its www alias, case-insensitively —
// remotes are typed by hand and git does not normalize the host's case.
func hostMatches(host string) bool {
	host = strings.ToLower(host)
	return host == Host || host == "www."+Host
}

// splitPath turns `/owner/name.git/` into a Repo. Exactly two non-empty
// segments are required: a deeper path is a gist, a wiki or an enterprise
// route this package does not claim to understand.
func splitPath(path string) (Repo, bool) {
	path = strings.Trim(path, "/")
	path = strings.TrimSuffix(path, ".git")
	owner, name, ok := strings.Cut(path, "/")
	if !ok || owner == "" || name == "" || strings.Contains(name, "/") {
		return Repo{}, false
	}
	return Repo{Owner: owner, Name: name}, true
}
