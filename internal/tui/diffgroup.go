package tui

import "strings"

// Grouping for the diff pane (§15, task 012). A unified diff arrives as one
// flat stream of lines, which is the wrong shape for the question a reviewer
// opens the tab with: *what did this touch, and which file do I read first?*
// So the pane splits the stream into per-file sections, renders one foldable
// header per file carrying its added/removed counts, and starts with every
// file collapsed — the first screen is the file list, and a change over twenty
// files is read by opening the two that matter rather than by scrolling past
// the eighteen that do not.
//
// The parse is deliberately shallow. It splits on `diff --git`, reads the
// paths out of the markers git already writes, and counts ± lines inside
// hunks; nothing here reinterprets a hunk. What a file expands to is the
// section's own lines, so the pane still shows what git said.

// diffFileMarker opens a file section in git's output.
const diffFileMarker = "diff --git "

// diffFile is one file's section of the diff.
type diffFile struct {
	// path is what the header row names: the file, or `old → new` for a
	// rename.
	path string
	// body is what expanding the file shows — the section's lines minus the
	// four that only repeat the header row (see diffFileParse.consume).
	body []string
	// added and removed count the ± lines inside the hunks.
	added, removed int
	// binary marks a file git described rather than diffed. Its counts are
	// both zero, and `+0 -0` on the header row would read as "unchanged".
	binary bool
}

// parseDiffFiles splits a diff into its file sections. Lines ahead of the
// first marker come back separately rather than being dropped: `git diff`
// writes none today, but a pane that silently swallowed whatever preceded the
// first file would be lying about the change — and a diff with no marker at
// all (a plain patch) then still renders, as the whole lead.
func parseDiffFiles(lines []string) (lead []string, files []diffFile) {
	var cur *diffFileParse
	flush := func() {
		if cur != nil {
			files = append(files, cur.done())
			cur = nil
		}
	}
	for _, line := range lines {
		if strings.HasPrefix(line, diffFileMarker) {
			flush()
			cur = &diffFileParse{gitLine: line}
			continue
		}
		if cur == nil {
			lead = append(lead, line)
			continue
		}
		cur.consume(line)
	}
	flush()
	return lead, files
}

// diffFileParse accumulates one section. The paths are collected as they go by
// and resolved at the end, because which marker is authoritative depends on
// what the whole section turned out to be — a rename, a deletion, a plain
// edit.
type diffFileParse struct {
	file    diffFile
	gitLine string

	oldPath, newPath     string
	renameFrom, renameTo string
	// inHunk turns on at the first @@ and never off: everything after it is
	// content, where a leading + or - is a change rather than a file marker.
	inHunk bool
}

// consume reads one line of the section.
func (s *diffFileParse) consume(line string) {
	if strings.HasPrefix(line, "@@") {
		s.inHunk = true
	}
	if s.inHunk {
		// The ± here are content: the file markers are behind us, which is
		// what makes counting safe (classifyDiffLine has to solve the same
		// ambiguity by prefix alone).
		switch {
		case strings.HasPrefix(line, "+"):
			s.file.added++
		case strings.HasPrefix(line, "-"):
			s.file.removed++
		}
		s.file.body = append(s.file.body, line)
		return
	}
	// Pre-hunk metadata. The two path markers and the blob ids say only what
	// the header row now says, so they are read and dropped — three lines of
	// repetition per file is most of what a fifteen-file diff makes you
	// scroll past. Everything else here (a mode change, a rename, `Binary
	// files … differ`) is a fact about the file that would otherwise vanish,
	// so it stays in the body.
	switch {
	case strings.HasPrefix(line, "index "):
		return
	case strings.HasPrefix(line, "--- "):
		s.oldPath = diffMarkerPath(line)
		return
	case strings.HasPrefix(line, "+++ "):
		s.newPath = diffMarkerPath(line)
		return
	case strings.HasPrefix(line, "rename from "):
		s.renameFrom = strings.TrimPrefix(line, "rename from ")
	case strings.HasPrefix(line, "rename to "):
		s.renameTo = strings.TrimPrefix(line, "rename to ")
	case strings.HasPrefix(line, "Binary files "), strings.HasPrefix(line, "GIT binary patch"):
		s.file.binary = true
	}
	s.file.body = append(s.file.body, line)
}

func (s *diffFileParse) done() diffFile {
	f := s.file
	f.path = s.displayPath()
	return f
}

// displayPath names the file the way the header row should read.
//
// The `---`/`+++` markers are preferred over the `diff --git` line for one
// reason: that line is `a/X b/Y`, and the space between the halves is also a
// legal character in a path, so a name containing one cannot be split out of
// it reliably. Each marker holds exactly one path.
func (s *diffFileParse) displayPath() string {
	switch {
	case s.renameFrom != "" && s.renameTo != "":
		return s.renameFrom + " → " + s.renameTo
	case s.newPath != "":
		return s.newPath
	case s.oldPath != "":
		// A deletion: `+++ /dev/null` named no file, so the old side is the
		// only name there is.
		return s.oldPath
	default:
		return diffGitPath(s.gitLine)
	}
}

// diffMarkerPath reads the path out of a `---`/`+++` marker: the `a/`|`b/`
// prefix off the front, the tab git appends when the name contains a space off
// the back, and `/dev/null` — "this side does not exist" — reported as no path
// at all. A repository configured with different prefixes (`diff.noprefix`)
// falls through to the name as written, which is still the right label.
func diffMarkerPath(line string) string {
	p := strings.TrimRight(line[len("--- "):], "\t")
	if p == "/dev/null" {
		return ""
	}
	for _, prefix := range []string{"a/", "b/"} {
		if rest := strings.TrimPrefix(p, prefix); rest != p {
			return rest
		}
	}
	return p
}

// diffGitPath is the fallback label, for a section whose markers never
// arrived: a binary file, or a mode change with no content. `a/X b/Y` is only
// unambiguous when the two halves agree, so it is *checked* rather than split
// — and when it does not check out, the line is shown as written instead of
// guessed at.
func diffGitPath(line string) string {
	rest := strings.TrimPrefix(line, diffFileMarker)
	if a, b, ok := strings.Cut(rest, " b/"); ok {
		if p := strings.TrimPrefix(a, "a/"); p == b {
			return p
		}
	}
	return rest
}
