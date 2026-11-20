package gitx

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Snapshot is a worktree's state captured without touching it (task 167.1).
// Commit is a commit whose tree is the worktree as `git add -A` would stage
// it — tracked edits, deletions and untracked files, ignored files excluded —
// and whose single parent is Head, the worktree's HEAD at capture time.
//
// Skipped lists the worktree paths (slash-separated, relative to its root)
// git could not read and so left out of Commit — an untracked file without
// read permission, say, which a container step can leave behind on Linux.
// A capture that skips them still succeeds (task 167, review F1): one
// unreadable file must not stop every later turn of a chat. A tracked file
// that could not be read keeps its last staged content instead.
type Snapshot struct {
	Commit  string
	Head    string
	Skipped []string
}

// snapshotIdentity is the author and committer of a snapshot commit. It is
// set explicitly so a snapshot never depends on — or fails for want of — the
// user's `user.name`/`user.email`: a checkpoint is vincent's record, not the
// user's commit, and CI runners carry no identity at all.
var snapshotIdentity = []string{
	"GIT_AUTHOR_NAME=vincent", "GIT_AUTHOR_EMAIL=vincent@localhost",
	"GIT_COMMITTER_NAME=vincent", "GIT_COMMITTER_EMAIL=vincent@localhost",
}

// Snapshot captures dir's worktree as a commit object (task 167 decision
// "snapshot mechanism"). It stages into a temporary index — `GIT_INDEX_FILE`
// pointing at a file under scratch, a directory the caller owns (the data
// directory, never the worktree) — so the real index, HEAD and every file in
// dir are left exactly as they were: `read-tree HEAD`, `add -A
// --ignore-errors`, `write-tree`, then `commit-tree` with HEAD as the parent.
// The files `add` could not read are reported in Snapshot.Skipped rather than
// failing the capture.
//
// The pre-capture split between staged and unstaged changes is not kept;
// a restore writes the tree back as unstaged changes (task 167 decision,
// beating a second tree for the index).
//
// The commit is unreferenced until the caller writes a ref to it. git
// deduplicates objects, so a capture of an unchanged worktree costs one
// commit object and reuses the previous capture's tree.
func (g *Git) Snapshot(ctx context.Context, dir, scratch, message string) (Snapshot, error) {
	head, err := g.Run(ctx, dir, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return Snapshot{}, fmt.Errorf("snapshot: resolve HEAD: %w", err)
	}
	if err := os.MkdirAll(scratch, 0o700); err != nil {
		return Snapshot{}, fmt.Errorf("snapshot: %w", err)
	}
	tmp, err := os.MkdirTemp(scratch, "snapshot-")
	if err != nil {
		return Snapshot{}, fmt.Errorf("snapshot: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	// An absolute path: git resolves a relative GIT_INDEX_FILE against its
	// own working directory, which is dir, not ours.
	index, err := filepath.Abs(filepath.Join(tmp, "index"))
	if err != nil {
		return Snapshot{}, fmt.Errorf("snapshot: %w", err)
	}
	env := append([]string{"GIT_INDEX_FILE=" + index}, snapshotIdentity...)
	if _, err := g.RunEnv(ctx, dir, env, "read-tree", head); err != nil {
		return Snapshot{}, fmt.Errorf("snapshot: read-tree: %w", err)
	}
	skipped, err := g.addAll(ctx, dir, env)
	if err != nil {
		return Snapshot{}, fmt.Errorf("snapshot: add: %w", err)
	}
	tree, err := g.RunEnv(ctx, dir, env, "write-tree")
	if err != nil {
		return Snapshot{}, fmt.Errorf("snapshot: write-tree: %w", err)
	}
	commit, err := g.RunEnv(ctx, dir, env, "commit-tree", tree, "-p", head, "-m", message)
	if err != nil {
		return Snapshot{}, fmt.Errorf("snapshot: commit-tree: %w", err)
	}
	return Snapshot{Commit: commit, Head: head, Skipped: skipped}, nil
}

// unableToIndex is the prefix of the line `git add --ignore-errors` writes
// for each file it could not add; the path follows in single quotes,
// unquoted otherwise.
const unableToIndex = "error: unable to index file '"

// addAll stages the whole worktree into env's index with `add -A
// --ignore-errors`, which adds every file it can and exits 1 when it skipped
// any. That exit is accepted only when stderr names at least one skipped
// file, and those names are returned; any other failure is an error.
func (g *Git) addAll(ctx context.Context, dir string, env []string) ([]string, error) {
	// The C locale keeps the line parsed below in English whatever the
	// daemon's environment translates git into.
	env = append(env[:len(env):len(env)], "LC_ALL=C", "LANGUAGE=C")
	_, err := g.RunEnv(ctx, dir, env, "add", "-A", "--ignore-errors", "--", ".")
	if err == nil {
		return nil, nil
	}
	var gerr *Error
	if !errors.As(err, &gerr) || gerr.ExitCode != 1 {
		return nil, err
	}
	var skipped []string
	for _, line := range strings.Split(gerr.Stderr, "\n") {
		line = strings.TrimRight(line, "\r")
		if rest, ok := strings.CutPrefix(line, unableToIndex); ok && strings.HasSuffix(rest, "'") {
			skipped = append(skipped, strings.TrimSuffix(rest, "'"))
		}
	}
	if len(skipped) == 0 {
		return nil, err
	}
	return skipped, nil
}

// Restore puts dir back to a Snapshot's state: HEAD (and so the checked-out
// branch) is reset to the snapshot's parent, the index matches that commit,
// and the worktree holds the snapshot's tree byte for byte, its differences
// from HEAD left as unstaged changes. Untracked files the snapshot did not
// have are removed; ignored files are never touched, because the snapshot
// never captured them (task 167.1).
//
// It is destructive by design. Saving the state it replaces is the caller's
// job (task 167 decision 2: a `rewound-<k>` ref written first).
func (g *Git) Restore(ctx context.Context, dir, commit string) error {
	parent, err := g.Run(ctx, dir, "rev-parse", "--verify", commit+"^1")
	if err != nil {
		return fmt.Errorf("restore: resolve parent of %s: %w", commit, err)
	}
	if _, err := g.Run(ctx, dir, "reset", "--hard", "-q", parent); err != nil {
		return fmt.Errorf("restore: reset: %w", err)
	}
	// -d for nested untracked directories; no -x, so ignored files stay.
	if _, err := g.Run(ctx, dir, "clean", "-f", "-d", "-q"); err != nil {
		return fmt.Errorf("restore: clean: %w", err)
	}
	// read-tree -u --reset moves index and worktree to the snapshot's tree,
	// removing tracked files it lacks; the mixed reset then puts the index
	// back on HEAD, leaving the difference unstaged.
	if _, err := g.Run(ctx, dir, "read-tree", "-u", "--reset", commit+"^{tree}"); err != nil {
		return fmt.Errorf("restore: read-tree: %w", err)
	}
	if _, err := g.Run(ctx, dir, "reset", "-q", parent); err != nil {
		return fmt.Errorf("restore: reset index: %w", err)
	}
	return nil
}

// UpdateRef points ref at sha, creating it if absent.
func (g *Git) UpdateRef(ctx context.Context, dir, ref, sha string) error {
	_, err := g.Run(ctx, dir, "update-ref", ref, sha)
	return err
}

// DeleteRefs deletes every ref under prefix (for example
// `refs/vincent/chats/7/`), returning how many it removed. A prefix with no
// refs is not an error.
func (g *Git) DeleteRefs(ctx context.Context, dir, prefix string) (int, error) {
	if !strings.HasPrefix(prefix, "refs/") {
		return 0, errors.New("delete refs: prefix must start with refs/")
	}
	out, err := g.Run(ctx, dir, "for-each-ref", "--format=%(refname)", prefix)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, ref := range strings.Fields(out) {
		if _, err := g.Run(ctx, dir, "update-ref", "-d", ref); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}
