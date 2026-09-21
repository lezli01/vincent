package worktree

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/lezli01/vincent/internal/gitx"
)

// ReasonWorkspacePathMissing is the directory a listing was asked for not
// existing (task 126).
//
// It is deliberately not ReasonProjectPathMissing. The directory a workspace
// listing is asked about is usually a task or chat worktree and only sometimes
// the project checkout itself, and a reason that names the wrong one is worse
// than one more entry in a vocabulary that already has eleven: a client shown
// "project path missing" would go looking at the project row for a directory
// the daemon created and removed itself.
//
// Unlike its neighbours this one is not a task block_reason — nothing blocks on
// a listing — so it exists only to be mapped to an HTTP status by the route
// that serves the list.
const ReasonWorkspacePathMissing = "workspace_path_missing"

// ListFiles enumerates the files of a workspace — a task or chat worktree, or a
// project checkout — as workspace-relative, forward-slash paths in git's own
// order, alongside the number of rows dropped as unservable.
//
// It is ListBranches' sibling: one bounded git query in a directory, returning
// what a picker offers a human. `--cached --others --exclude-standard` is what
// "the files of the project" means to someone typing `@`: a file created a
// minute ago and never `git add`ed is exactly the file they want to point an
// agent at, and `.gitignore` is honoured so `node_modules/` and `.env` stay
// out. No `--full-name` (a `git worktree add` target and a project path are
// both toplevels, so cwd-relative already equals repo-relative) and no
// `--recurse-submodules` (a submodule is one gitlink row; its files belong to
// another repository).
//
// `-z` is not optional. Without it git C-quotes any path it considers unusual,
// and `core.quotePath=false` is not enough — it unquotes a high-bit path and
// still quotes the ones carrying control characters. It is also the only form
// in which a newline inside a filename cannot corrupt the split.
//
// Paths are returned exactly as git printed them: forward slashes on every
// platform, which is the form the agent CLIs take. No filepath.FromSlash, which
// would produce a backslash path the listing never observed, and no case
// folding, which would serve a path that differs from the file's own name.
//
// The dropped count is returned rather than logged: this package's methods take
// no logger and its siblings do not log. What reaches the wire, and where the
// daemon-log line goes, belongs to the route that serves this.
func (m *Manager) ListFiles(ctx context.Context, dir string) (paths []string, dropped int, err error) {
	// The missing directory is caught here rather than read off git's exit
	// code, because git never runs: gitx sets cmd.Dir, and Go's own fork/exec
	// fails the chdir first. The error that arrives is a *gitx.Error with
	// ExitCode -1, empty stderr and "chdir …: no such file or directory" —
	// there is no `fatal: cannot change to` and no exit 128 to match on, which
	// is what `git -C <missing>` would have produced instead.
	if err := m.requireWorkspacePath(dir); err != nil {
		return nil, 0, err
	}
	listCtx, cancel := context.WithTimeout(ctx, gitx.QueryTimeout)
	defer cancel()
	out, err := m.git.RunRaw(listCtx, dir, "ls-files", "-z", "--cached", "--others", "--exclude-standard")
	if err != nil {
		return nil, 0, &Error{Reason: ReasonGitError, Message: "git ls-files failed", Err: err}
	}
	seen := make(map[string]struct{})
	for _, row := range bytes.Split(out, []byte{0}) {
		// The terminator leaves an empty tail; an empty path is not a file
		// either way.
		if len(row) == 0 {
			continue
		}
		path := string(row)
		if _, dup := seen[path]; dup {
			// A worktree mid-merge prints a conflicted path once per index
			// stage — three times for a three-way conflict. Deduping in Go
			// rather than with `--deduplicate` is deliberate: that flag
			// landed in git 2.31, which is exactly vincent's floor, and the
			// floor is a startup warning rather than a refusal. git rejects
			// an unknown long option outright — exit 129, usage on stderr,
			// no listing at all — so sending it would turn a degraded result
			// on git 2.30 into a dead picker. The multi-stage rows are the
			// identical path repeated and this enumerator emits paths and
			// never stages, so the Go pass is not a belt over the flag's
			// braces; it is the whole mechanism, and dropping the flag
			// removes the version coupling rather than converting it into a
			// hard failure.
			continue
		}
		seen[path] = struct{}{}
		if pathHostile(path) {
			dropped++
			continue
		}
		paths = append(paths, path)
	}
	return paths, dropped, nil
}

// pathHostile reports a row that cannot cross a JSON wire honestly: a path that
// is not valid UTF-8, or one carrying a C0 or C1 control — a newline among them,
// and an ANSI escape's introducer too.
//
// It is the server-side twin of the TUI's chatSkillHostile
// (internal/tui/chatinlinelist.go), duplicated rather than shared because
// worktree cannot import internal/tui and the client guard stays as defence
// in depth.
//
// The drop happens here rather than downstream because encoding/json replaces
// invalid UTF-8 with U+FFFD and returns *no error*: "caf\xe9.txt" marshals to a
// path that does not round-trip, with err nil. A row a client could not use is
// better counted than offered.
//
// Whitespace is not hostile, and that is the point of the raw runner above: a
// leading space, a trailing space and an interior space are all part of the
// file's name and all survive intact.
func pathHostile(p string) bool {
	if !utf8.ValidString(p) {
		return true
	}
	return strings.IndexFunc(p, func(r rune) bool {
		return r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f)
	}) >= 0
}

// requireWorkspacePath is requireProjectPath for a workspace directory: the
// same os.Stat pre-check, carrying the reason that names what was missing.
func (m *Manager) requireWorkspacePath(dir string) error {
	if _, err := os.Stat(dir); err != nil {
		return &Error{
			Reason:  ReasonWorkspacePathMissing,
			Message: fmt.Sprintf("workspace path %s does not exist", dir), Err: err,
		}
	}
	return nil
}
