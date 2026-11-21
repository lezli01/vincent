package gitx

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// SnapshotTree writes dir's working tree — tracked and untracked, ignored
// excluded — as a tree object and returns its id (task 169.1). No commit, no
// ref; the real index, HEAD and every file are untouched. scratch as in
// Snapshot: a directory the caller owns, never the worktree.
//
// It is the tree half of Snapshot and shares its temporary index, so a chat's
// diff sees exactly what a checkpoint would capture — untracked files included
// (task 169 decision 1, a chat-only departure from the task diff's "untracked
// files excluded"), ignored files not (task 167 decision 5). A file git could
// not read is left out of the tree, as Snapshot leaves it out of its commit.
func (g *Git) SnapshotTree(ctx context.Context, dir, scratch string) (tree string, err error) {
	tree, _, _, err = g.worktreeTree(ctx, dir, scratch)
	if err != nil {
		return "", fmt.Errorf("snapshot tree: %w", err)
	}
	return tree, nil
}

// File statuses of a FileDiff.
const (
	StatusAdded    = "added"
	StatusDeleted  = "deleted"
	StatusModified = "modified"
	StatusRenamed  = "renamed"
)

// FileDiff is one file of a tree-to-tree diff (task 169.1).
type FileDiff struct {
	// Path and OldPath are slash-separated on every OS, exactly as git stores
	// them; OldPath is set only for a rename.
	Path, OldPath string
	// Status is one of the Status* constants.
	Status string
	// Binary is a file git would not diff as text; ModeOnly is a change of
	// mode with the content identical. Neither has hunks.
	Binary, ModeOnly bool
	// Gitlink is an entry that is, or was, a commit rather than a blob (mode
	// 160000): a submodule, or a directory holding its own .git that `add -A`
	// recorded as one. Its one "Subproject commit" hunk is git's description
	// of it, not lines of a file.
	Gitlink        bool
	Added, Removed int
	// Header is the file's lines ahead of its first hunk (diff --git, index,
	// new file mode, ---, +++ ...), byte for byte.
	Header []string
	Hunks  []Hunk
}

// Hunk is one hunk of a FileDiff.
type Hunk struct {
	// Hash identifies the hunk by content (task 169 decision 2): lowercase hex
	// SHA-256 of Path + "\x00" + strings.Join(Lines, "\n"). The @@ line numbers
	// are deliberately left out, so rejecting one hunk does not invalidate the
	// hunks below it, and a mark survives a restart but dies when the content
	// changes. When the same content occurs more than once in one file, the
	// 2nd, 3rd ... occurrence appends "\x00" + its ordinal ("2", "3") to the
	// hashed bytes, so hashes are unique within a file.
	Hash string
	// Header is the "@@ -a,b +c,d @@ ..." line.
	Header string
	// Lines are the lines after the header, each with its ' ', '+', '-' or
	// '\' prefix, no trailing "\n", any "\r" kept.
	Lines          []string
	Added, Removed int
}

// Why a file's hunks cannot be reverse-applied one at a time, as
// FileDiff.NotRejectable reports it.
const (
	NotRejectableBinary   = "binary"
	NotRejectableRenamed  = "renamed"
	NotRejectableModeOnly = "mode_only"
	// NotRejectableGitlink: `git apply -R` on a gitlink's patch only tries to
	// rmdir the directory, warns when it is not empty and exits 0 — a reject
	// that reports success and reverts nothing (review F4).
	NotRejectableGitlink = "gitlink"
	// NotRejectableEmpty: the file has no hunk at all — a zero-byte file
	// added or deleted. A reject names a hunk, so there is nothing to name
	// (review F8).
	NotRejectableEmpty = "empty"
)

// NotRejectable is why the file's hunks cannot be reverse-applied one at a
// time — one of the NotRejectable* constants — or "" when they can.
func (f FileDiff) NotRejectable() string {
	switch {
	case f.Binary:
		return NotRejectableBinary
	case f.Status == StatusRenamed:
		return NotRejectableRenamed
	case f.ModeOnly:
		return NotRejectableModeOnly
	case f.Gitlink:
		return NotRejectableGitlink
	case len(f.Hunks) == 0:
		return NotRejectableEmpty
	}
	return ""
}

// Rejectable reports whether the file's hunks can be reverse-applied one at a
// time: false for binary files, renames, mode-only changes, gitlinks and files
// with no hunk.
func (f FileDiff) Rejectable() bool { return f.NotRejectable() == "" }

// diffTreeArgs pins everything about `diff-tree`'s output a user's config or
// environment could otherwise change (task 169.1). diff-tree is plumbing and
// already ignores most of `diff.*`; each flag is spelled out anyway so the
// parse below — and Hunk.Hash, which is content — never depends on which
// settings a given git version happens to read. core.quotepath=false keeps
// non-ASCII paths readable in Header; the paths themselves come from the -z
// listing, never from the patch text. -U3 is the one setting left to the
// patch call's own format: -U implies -p, and would turn a --raw or --numstat
// listing into a patch.
func diffTreeArgs(format ...string) []string {
	args := []string{
		"-c", "core.quotepath=false",
		// Read by plumbing too: set, it writes a blank context line as an
		// empty line instead of a lone space, which parsePatch takes for a
		// hunk cut short — and would change the hash and the patch `apply`
		// is handed (review F3).
		"-c", "diff.suppressBlankEmpty=false",
		"diff-tree", "-r", "-M",
		"--no-color", "--no-ext-diff", "--no-textconv", "--no-relative",
		"--src-prefix=a/", "--dst-prefix=b/",
		"--inter-hunk-context=0",
		"--diff-algorithm=myers", "--indent-heuristic",
		"--ignore-submodules=none", "--submodule=short",
	}
	return append(args, format...)
}

// DiffTrees is the structured diff from `from` (any tree-ish: a commit or a
// tree) to the tree `to`, files in git's order, with rename detection on.
//
// It asks git twice. The `--raw -z` listing is the authority on each file's
// path, status and modes, because a patch's header quotes any path with a
// tab, a quote or a backslash in it; the patch supplies the text. Both run
// with the same options, so they list the same files in the same order.
//
// A type change (a file replaced by a symlink, say) is the one entry git
// renders as two patches — a deletion, then an addition — and it is returned
// the same way: two FileDiffs on one Path.
func (g *Git) DiffTrees(ctx context.Context, dir, from, to string) ([]FileDiff, error) {
	rawOut, err := g.RunRaw(ctx, dir, append(diffTreeArgs("--raw", "-z", "--no-abbrev"), from, to)...)
	if err != nil {
		return nil, fmt.Errorf("diff trees: %w", err)
	}
	entries, err := parseRawDiff(string(rawOut))
	if err != nil {
		return nil, fmt.Errorf("diff trees: %w", err)
	}
	if len(entries) == 0 {
		return nil, nil
	}
	patchOut, err := g.RunRaw(ctx, dir, append(diffTreeArgs("-p", "-U3"), from, to)...)
	if err != nil {
		return nil, fmt.Errorf("diff trees: %w", err)
	}
	sections, err := parsePatch(string(patchOut))
	if err != nil {
		return nil, fmt.Errorf("diff trees: %w", err)
	}
	files := make([]FileDiff, 0, len(sections))
	next := 0
	take := func(e rawEntry, status string) error {
		if next >= len(sections) {
			return fmt.Errorf("diff trees: no patch for %q", e.path)
		}
		f := sections[next]
		next++
		f.Path, f.Status = e.path, status
		if status == StatusRenamed {
			f.OldPath = e.oldPath
		}
		f.ModeOnly = status == StatusModified && e.oldMode != e.newMode && e.oldSHA == e.newSHA
		// A type change is taken as a deletion then an addition, and only the
		// half that is the commit is the gitlink.
		switch {
		case e.status == 'T' && status == StatusDeleted:
			f.Gitlink = e.oldMode == gitlinkMode
		case e.status == 'T':
			f.Gitlink = e.newMode == gitlinkMode
		default:
			f.Gitlink = e.oldMode == gitlinkMode || e.newMode == gitlinkMode
		}
		hashHunks(&f)
		files = append(files, f)
		return nil
	}
	for _, e := range entries {
		var err error
		switch e.status {
		case 'A':
			err = take(e, StatusAdded)
		case 'D':
			err = take(e, StatusDeleted)
		case 'R':
			err = take(e, StatusRenamed)
		case 'T':
			if err = take(e, StatusDeleted); err == nil {
				err = take(e, StatusAdded)
			}
		default:
			err = take(e, StatusModified)
		}
		if err != nil {
			return nil, err
		}
	}
	if next != len(sections) {
		return nil, fmt.Errorf("diff trees: %d patches for %d files", len(sections), next)
	}
	return files, nil
}

// DiffTreesStat is the cheap form (numstat): file count and +/- totals. A
// binary file counts as a file and contributes no lines.
func (g *Git) DiffTreesStat(ctx context.Context, dir, from, to string) (files, added, removed int, err error) {
	out, err := g.RunRaw(ctx, dir, append(diffTreeArgs("--numstat", "-z"), from, to)...)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("diff trees stat: %w", err)
	}
	// "added\tremoved\tpath\0", or for a rename "added\tremoved\t\0old\0new\0".
	// A path may itself hold a tab, so only the first two are split off.
	toks := strings.Split(string(out), "\x00")
	for i := 0; i < len(toks); i++ {
		if toks[i] == "" {
			continue
		}
		parts := strings.SplitN(toks[i], "\t", 3)
		if len(parts) != 3 {
			return 0, 0, 0, fmt.Errorf("diff trees stat: unexpected numstat record %q", toks[i])
		}
		files++
		// "-" marks a binary file: no line counts.
		if n, err := strconv.Atoi(parts[0]); err == nil {
			added += n
		}
		if n, err := strconv.Atoi(parts[1]); err == nil {
			removed += n
		}
		if parts[2] == "" {
			i += 2
		}
	}
	return files, added, removed, nil
}

// gitlinkMode is the tree mode of a commit entry.
const gitlinkMode = "160000"

// rawEntry is one record of `diff-tree --raw -z`.
type rawEntry struct {
	oldMode, newMode string
	oldSHA, newSHA   string
	status           byte
	path, oldPath    string
}

// parseRawDiff reads `diff-tree --raw -z`: ":oldmode newmode oldsha newsha
// STATUS\0path\0", a rename carrying "old\0new\0" instead of one path.
func parseRawDiff(out string) ([]rawEntry, error) {
	toks := strings.Split(out, "\x00")
	var entries []rawEntry
	for i := 0; i < len(toks); i++ {
		if toks[i] == "" {
			continue
		}
		meta := strings.Fields(strings.TrimPrefix(toks[i], ":"))
		if !strings.HasPrefix(toks[i], ":") || len(meta) != 5 || meta[4] == "" {
			return nil, fmt.Errorf("unexpected raw diff record %q", toks[i])
		}
		e := rawEntry{oldMode: meta[0], newMode: meta[1], oldSHA: meta[2], newSHA: meta[3], status: meta[4][0]}
		paths := 1
		if e.status == 'R' || e.status == 'C' {
			paths = 2
		}
		if i+paths >= len(toks) {
			return nil, fmt.Errorf("raw diff record %q has no path", toks[i])
		}
		if paths == 2 {
			e.oldPath = toks[i+1]
		}
		e.path = toks[i+paths]
		i += paths
		entries = append(entries, e)
	}
	return entries, nil
}

// parsePatch splits `diff-tree -p` output into one FileDiff per "diff --git"
// section, filling Header, Hunks, Binary and the line counts. It is
// byte-faithful: the text is split on "\n" only, so a CRLF file's "\r" stays
// on its lines, and a hunk's extent comes from the counts in its @@ line
// rather than from what the next line looks like — a removed line reading
// "-- a/x" or an added "diff --git" cannot be mistaken for structure. A
// "\ No newline at end of file" line belongs to the hunk it follows.
func parsePatch(out string) ([]FileDiff, error) {
	lines := strings.Split(out, "\n")
	if n := len(lines); n > 0 && lines[n-1] == "" {
		lines = lines[:n-1]
	}
	var files []FileDiff
	i := 0
	for i < len(lines) {
		if !strings.HasPrefix(lines[i], "diff --git ") {
			return nil, fmt.Errorf("unexpected patch line %q", lines[i])
		}
		f := FileDiff{Header: []string{lines[i]}}
		i++
		for i < len(lines) && !strings.HasPrefix(lines[i], "@@ ") && !strings.HasPrefix(lines[i], "diff --git ") {
			if strings.HasPrefix(lines[i], "Binary files ") || lines[i] == "GIT binary patch" {
				f.Binary = true
			}
			f.Header = append(f.Header, lines[i])
			i++
		}
		for i < len(lines) && strings.HasPrefix(lines[i], "@@ ") {
			oldN, newN, err := hunkCounts(lines[i])
			if err != nil {
				return nil, err
			}
			h := Hunk{Header: lines[i]}
			i++
			for oldN > 0 || newN > 0 {
				if i >= len(lines) || lines[i] == "" {
					return nil, fmt.Errorf("hunk %q is cut short", h.Header)
				}
				switch lines[i][0] {
				case ' ':
					oldN--
					newN--
				case '-':
					oldN--
					h.Removed++
				case '+':
					newN--
					h.Added++
				case '\\':
				default:
					return nil, fmt.Errorf("unexpected line %q in hunk %q", lines[i], h.Header)
				}
				h.Lines = append(h.Lines, lines[i])
				i++
			}
			// The marker for the hunk's last line comes after the counts are spent.
			for i < len(lines) && strings.HasPrefix(lines[i], `\`) {
				h.Lines = append(h.Lines, lines[i])
				i++
			}
			f.Added += h.Added
			f.Removed += h.Removed
			f.Hunks = append(f.Hunks, h)
		}
		files = append(files, f)
	}
	return files, nil
}

// hunkCounts reads the old and new line counts from "@@ -a[,b] +c[,d] @@";
// an omitted count is 1.
func hunkCounts(header string) (oldN, newN int, err error) {
	fields := strings.Fields(header)
	if len(fields) < 4 || fields[0] != "@@" || fields[3] != "@@" ||
		!strings.HasPrefix(fields[1], "-") || !strings.HasPrefix(fields[2], "+") {
		return 0, 0, fmt.Errorf("unexpected hunk header %q", header)
	}
	count := func(r string) (int, error) {
		_, n, ok := strings.Cut(r[1:], ",")
		if !ok {
			return 1, nil
		}
		return strconv.Atoi(n)
	}
	if oldN, err = count(fields[1]); err == nil {
		newN, err = count(fields[2])
	}
	if err != nil || oldN < 0 || newN < 0 {
		return 0, 0, fmt.Errorf("unexpected hunk header %q", header)
	}
	return oldN, newN, nil
}

// hashHunks sets every hunk's Hash, as Hunk.Hash documents.
func hashHunks(f *FileDiff) {
	seen := make(map[string]int, len(f.Hunks))
	for i := range f.Hunks {
		content := f.Path + "\x00" + strings.Join(f.Hunks[i].Lines, "\n")
		seen[content]++
		if n := seen[content]; n > 1 {
			content += "\x00" + strconv.Itoa(n)
		}
		sum := sha256.Sum256([]byte(content))
		f.Hunks[i].Hash = hex.EncodeToString(sum[:])
	}
}

// Errors of ReverseHunk.
var (
	// ErrHunkNotFound: no hunk of the file carries that hash — the content
	// changed since the diff the hash came from.
	ErrHunkNotFound = errors.New("hunk not found")
	// ErrHunkConflict: git would not apply the reversal. git's message is
	// wrapped; the working tree is as it was.
	ErrHunkConflict = errors.New("hunk does not apply")
	// ErrHunkNotRejectable: FileDiff.NotRejectable says why.
	ErrHunkNotRejectable = errors.New("hunk cannot be rejected")
)

// ReverseHunk builds a one-hunk patch from f.Header and the hunk whose Hash
// is hash and runs `git apply -R` on it in dir's working tree. Nothing else
// in the file changes; the index and HEAD are untouched (task 169 decision 6:
// a rejection is an uncommitted edit, never history). Rejecting the only hunk
// of an added file deletes the file; of a deleted file, restores it.
//
// git, not vincent, decides whether the reversal applies, and it is atomic: a
// hunk whose lines were since edited is ErrHunkConflict and writes nothing.
// git finds the hunk by its context, so one stays rejectable after a hunk
// above it was rejected and shifted its line numbers.
//
// Line endings are git's to convert, as on any checkout: `apply` reads the
// file through the worktree's own core.autocrlf and attributes before
// matching and writes it back through them, and keeps a patch's CRLF lines
// as they are — so a CRLF file round-trips whether its blob holds CRLF or LF.
func (g *Git) ReverseHunk(ctx context.Context, dir string, f FileDiff, hash string) error {
	if !f.Rejectable() {
		return fmt.Errorf("%w: %s", ErrHunkNotRejectable, f.Path)
	}
	var hunk *Hunk
	for i := range f.Hunks {
		if f.Hunks[i].Hash == hash {
			hunk = &f.Hunks[i]
			break
		}
	}
	if hunk == nil {
		return fmt.Errorf("%w: %s", ErrHunkNotFound, f.Path)
	}
	var patch strings.Builder
	for _, line := range f.Header {
		// A modified file's patch keeps only what names the file. Its "old
		// mode"/"new mode" lines would have the reversal put the mode back
		// too, and that is not this hunk's change to undo.
		if f.Status == StatusModified && !strings.HasPrefix(line, "diff --git ") &&
			!strings.HasPrefix(line, "--- ") && !strings.HasPrefix(line, "+++ ") {
			continue
		}
		patch.WriteString(line)
		patch.WriteByte('\n')
	}
	patch.WriteString(hunk.Header)
	patch.WriteByte('\n')
	for _, line := range hunk.Lines {
		patch.WriteString(line)
		patch.WriteByte('\n')
	}
	// No --index and no --cached: the working tree only. --whitespace=nowarn
	// and apply.ignoreWhitespace=no override a user's apply.* config, which
	// could otherwise "fix" the CR of a CRLF line on its way back, refuse it
	// as a whitespace error, or match a hunk whose lines no longer agree.
	_, err := g.runInput(ctx, dir, nil, []byte(patch.String()),
		"-c", "apply.ignoreWhitespace=no", "apply", "-R", "--whitespace=nowarn")
	var gerr *Error
	if errors.As(err, &gerr) && gerr.ExitCode == 1 {
		return fmt.Errorf("%w: %s", ErrHunkConflict, gerr.Stderr)
	}
	return err
}
