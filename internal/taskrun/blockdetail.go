package taskrun

import (
	"errors"
	"regexp"

	"github.com/lezli01/vincent/internal/worktree"
)

// block_detail (§5.3, issue #594) is a daemon-authored sentence, not an error
// dump. An underlying error may be appended only when it comes from the local
// filesystem or from git, and then only with URL userinfo stripped: a remote
// configured as https://user:token@host/… puts the token into every git
// message that names the remote. Errors from GitHub sources are never
// embedded, the rule internal/github/reason.go already follows for its
// client-facing messages.

// userinfoPattern matches the `user:password@` or `user@` part of a URL that
// has a scheme. An scp-style `git@host:path` carries no secret and has no
// scheme, so it is left alone.
var userinfoPattern = regexp.MustCompile(`([A-Za-z][A-Za-z0-9+.-]*://)[^/@\s]+@`)

// scrubUserinfo removes URL userinfo from s.
func scrubUserinfo(s string) string {
	return userinfoPattern.ReplaceAllString(s, "$1")
}

// withLocalError appends err to sentence. Only for errors from a local or git
// source; the caller vouches for that, and fail scrubs the userinfo.
func withLocalError(sentence string, err error) string {
	if err == nil {
		return sentence
	}
	return sentence + ": " + err.Error()
}

// worktreeDetail is the block detail of a worktree-creation failure: the
// worktree.Error's own message, which names the path, branch or remote at
// fault, followed by git's error when there is one. Anything else is a git
// failure without a message of its own.
func worktreeDetail(err error) string {
	var we *worktree.Error
	if !errors.As(err, &we) {
		return withLocalError("the task's worktree could not be created", err)
	}
	return withLocalError(we.Message, we.Err)
}
