package github

import "testing"

// ParseRemote is the whole of "is this project GitHub-based" (task 035
// decision 5). It is derived at the point of use from `origin`, so its
// accept/reject boundary is the feature's boundary: everything it rejects is
// a project the issue row is simply not offered on.
func TestParseRemoteAcceptsGitHubSpellings(t *testing.T) {
	for _, tc := range []struct{ remote, want string }{
		{"https://github.com/lezli01/vincent.git", "lezli01/vincent"},
		{"https://github.com/lezli01/vincent", "lezli01/vincent"},
		{"https://github.com/lezli01/vincent/", "lezli01/vincent"},
		{"http://github.com/lezli01/vincent.git", "lezli01/vincent"},
		{"https://user@github.com/lezli01/vincent.git", "lezli01/vincent"},
		{"https://www.github.com/lezli01/vincent", "lezli01/vincent"},
		{"https://GitHub.com/lezli01/vincent", "lezli01/vincent"},
		{"git@github.com:lezli01/vincent.git", "lezli01/vincent"},
		{"git@github.com:lezli01/vincent", "lezli01/vincent"},
		{"ssh://git@github.com/lezli01/vincent.git", "lezli01/vincent"},
		{"git://github.com/lezli01/vincent.git", "lezli01/vincent"},
		{"  https://github.com/lezli01/vincent.git\n", "lezli01/vincent"},
		// A repository whose own name ends in ".git" survives: only the
		// suffix git itself adds is stripped.
		{"https://github.com/octo/dot.git.git", "octo/dot.git"},
	} {
		repo, ok := ParseRemote(tc.remote)
		if !ok {
			t.Errorf("ParseRemote(%q) rejected a github.com remote", tc.remote)
			continue
		}
		if got := repo.String(); got != tc.want {
			t.Errorf("ParseRemote(%q) = %q, want %q", tc.remote, got, tc.want)
		}
	}
}

func TestParseRemoteRejectsEverythingElse(t *testing.T) {
	for _, remote := range []string{
		"",
		"   ",
		// Another forge. The row must not appear, and no call must be made.
		"https://gitlab.com/lezli01/vincent.git",
		"git@gitlab.com:lezli01/vincent.git",
		"https://github.enterprise.example.com/octo/repo.git",
		// A host that merely ends in the right letters.
		"https://notgithub.com/octo/repo.git",
		"https://github.com.evil.example/octo/repo.git",
		// A bare local repository: `git remote get-url` returns paths too.
		"/home/dev/vincent",
		`C:\src\vincent`,
		// An SSH alias. Its real host lives in the user's ssh config, which
		// this package does not read (decision 5), so it is not GitHub-based
		// for now rather than guessed at.
		"gh:lezli01/vincent.git",
		"work:lezli01/vincent",
		// Not a repository path.
		"https://github.com/lezli01",
		"https://github.com/",
		"https://github.com/octo/repo/issues/1",
		"git@github.com:",
	} {
		if repo, ok := ParseRemote(remote); ok {
			t.Errorf("ParseRemote(%q) = %q, want rejected", remote, repo)
		}
	}
}

func TestRepoZero(t *testing.T) {
	if !(Repo{}).Zero() {
		t.Error("the zero Repo does not report Zero")
	}
	if (Repo{Owner: "octo"}).Zero() != true {
		t.Error("a half-filled Repo is not Zero, but identifies no repository")
	}
	if (Repo{Owner: "octo", Name: "repo"}).Zero() {
		t.Error("a complete Repo reports Zero")
	}
}
