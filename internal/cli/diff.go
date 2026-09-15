package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/charmbracelet/colorprofile"
	"github.com/spf13/cobra"

	"github.com/lezli01/vincent/internal/apiclient"
)

// newTaskDiffCmd prints a task's diff (task 100). Reading a diff is not a §6
// human action, so task 025 decision 12 — which keeps some writes TUI-and-API
// only — does not reach it.
//
// Every form is computed from the one endpoint, GET /v1/tasks/{id}/diff: the
// stat table and its JSON are the CLI's own reading of that body, not a wire
// type, so neither the API nor §13.2 changes for them.
func newTaskDiffCmd() *cobra.Command {
	var (
		by   string
		stat bool
	)
	cmd := &cobra.Command{
		Use:   "diff <id>",
		Short: "Print a task's diff against its base",
		Long: "Print the task's worktree compared with merge-base(base, HEAD) — committed, " +
			"staged and unstaged tracked changes; untracked files are not included (§13.2).\n\n" +
			"--by lane splits it by the fan-out lane that produced each change, one `# lane` " +
			"section per lane merge and a `# remainder` section last. --stat prints a " +
			"per-file table instead of the patch. --json mirrors the wire format.\n\n" +
			"Colour is used only when stdout is a terminal and NO_COLOR is unset; piped " +
			"output is exactly the bytes the daemon served, so it can go to `git apply`.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return fmt.Errorf("task id must be a number: %q", args[0])
			}
			// Refused before any request, in the daemon's own words: `lane` is
			// the only grouping there is (task 100 decision 6).
			if by != "" && by != "lane" {
				_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "Error: by must be \"lane\"; got %q\n", by)
				return exitError{code: 1}
			}
			p := &diffPrinter{out: cmd.OutOrStdout(), json: wantJSON(cmd), stat: stat}
			return withClient(cmd, func(ctx context.Context, c *apiclient.Client) error {
				if by == "lane" {
					err = p.byLane(ctx, c, id)
				} else {
					err = p.flat(ctx, c, id)
				}
				if err != nil {
					_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "Error:", apiMessage(err))
					return exitError{code: 1}
				}
				return nil
			})
		},
	}
	cmd.Flags().StringVar(&by, "by", "", `Group the diff; "lane" splits it by fan-out lane`)
	cmd.Flags().BoolVar(&stat, "stat", false, "Print a per-file table of added and removed lines")
	jsonFlag(cmd)
	return cmd
}

// diffPrinter renders one diff request in whichever form was asked for.
type diffPrinter struct {
	out  io.Writer
	json bool
	stat bool
}

// flat prints the ungrouped diff.
//
// It streams: the endpoint's body is read to its end with no size cap, because
// a truncated patch piped into `git apply` is a corrupt patch delivered with
// exit 0 (task 100 decision 4).
func (p *diffPrinter) flat(ctx context.Context, c *apiclient.Client, id int64) error {
	body, err := c.DiffStream(ctx, id)
	if err != nil {
		return err
	}
	defer func() { _ = body.Close() }()

	switch {
	case p.stat:
		var sc diffStatScanner
		if err := eachDiffLine(body, sc.line); err != nil {
			return err
		}
		files := sc.finish()
		if p.json {
			return emitJSON(p.out, files)
		}
		rows := make([][]string, 0, len(files))
		for _, f := range files {
			rows = append(rows, append([]string{f.Path}, f.cells()...))
		}
		return table(p.out, []string{"FILE", "ADDED", "REMOVED"}, rows)
	case p.json:
		text, err := io.ReadAll(body)
		if err != nil {
			return fmt.Errorf("read diff: %w", err)
		}
		return emitJSON(p.out, struct {
			Diff string `json:"diff"`
		}{Diff: string(text)})
	}

	profile, color := diffColor(p.out)
	if !color {
		if _, err := io.Copy(p.out, body); err != nil {
			return fmt.Errorf("read diff: %w", err)
		}
		return nil
	}
	w := &colorprofile.Writer{Forward: p.out, Profile: profile}
	pt := diffPainter{w: w}
	if err := eachDiffLine(body, pt.line); err != nil {
		return err
	}
	return pt.err
}

// byLane prints the diff attributed to its fan-out lanes, in the daemon's
// section order: every lane merge, then the remainder.
func (p *diffPrinter) byLane(ctx context.Context, c *apiclient.Client, id int64) error {
	sections, err := c.DiffByLane(ctx, id)
	if err != nil {
		return err
	}
	if sections == nil {
		sections = []apiclient.DiffSection{}
	}

	if p.stat {
		rows := []laneFileStat{}
		for _, s := range sections {
			var sc diffStatScanner
			if err := eachDiffLine(strings.NewReader(s.Diff), sc.line); err != nil {
				return err
			}
			for _, f := range sc.finish() {
				rows = append(rows, laneFileStat{
					LaneID: s.LaneID, ChildTaskID: s.ChildTaskID, Remainder: s.Remainder, fileStat: f,
				})
			}
		}
		if p.json {
			return emitJSON(p.out, rows)
		}
		cells := make([][]string, 0, len(rows))
		for _, r := range rows {
			lane := r.LaneID
			if r.Remainder {
				lane = "-"
			}
			cells = append(cells, append([]string{dash(lane), r.Path}, r.cells()...))
		}
		return table(p.out, []string{"LANE", "FILE", "ADDED", "REMOVED"}, cells)
	}
	if p.json {
		return emitJSON(p.out, sections)
	}

	profile, color := diffColor(p.out)
	w := p.out
	if color {
		w = &colorprofile.Writer{Forward: p.out, Profile: profile}
	}
	for _, s := range sections {
		// A section with no change still gets its header: a lane that changed
		// nothing is worth knowing about (task 100 decision 5).
		header := sectionHeader(s)
		if color {
			header = sgrDim + header + sgrReset
		}
		if _, err := io.WriteString(w, header+"\n"); err != nil {
			return err
		}
		if !color {
			if _, err := io.WriteString(w, s.Diff); err != nil {
				return err
			}
			continue
		}
		pt := diffPainter{w: w}
		if err := eachDiffLine(strings.NewReader(s.Diff), pt.line); err != nil {
			return err
		}
		if pt.err != nil {
			return pt.err
		}
	}
	return nil
}

// sectionHeader names a `--by lane` section in ASCII (task 047 decision 7:
// piped output uses ASCII markers). `git apply` skips lines outside a patch,
// so the grouped output still applies.
func sectionHeader(s apiclient.DiffSection) string {
	if s.Remainder {
		return "# remainder (the task's own commits and uncommitted work)"
	}
	sha := s.MergeCommit
	if len(sha) > 12 {
		sha = sha[:12]
	}
	return fmt.Sprintf("# lane %s (task %d, merge %s)", s.LaneID, s.ChildTaskID, sha)
}

// diffColor decides whether to colour, and at which profile.
//
// The TTY test comes first and is not delegated: colorprofile would also honour
// CLICOLOR_FORCE on a pipe, and piped output must stay exactly the bytes the
// daemon served (task 100 decision 1). colorprofile then owns NO_COLOR,
// TERM=dumb and the Windows console, so this needs no _windows.go twin.
func diffColor(w io.Writer) (colorprofile.Profile, bool) {
	if !isTTY(w) {
		return colorprofile.NoTTY, false
	}
	p := colorprofile.Detect(w, os.Environ())
	return p, p >= colorprofile.ANSI
}

// eachDiffLine calls fn for every line of r, without its trailing newline.
//
// bufio.Reader rather than bufio.Scanner: a Scanner's 64 KiB token limit fails
// on the one-line minified or generated file a diff is exactly where you meet.
func eachDiffLine(r io.Reader, fn func(string)) error {
	br := bufio.NewReader(r)
	for {
		line, err := br.ReadString('\n')
		if line != "" {
			fn(strings.TrimSuffix(line, "\n"))
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read diff: %w", err)
		}
	}
}

// diffLineKind is what one line of a unified diff is.
type diffLineKind int

const (
	// diffLineFile is a `diff --git` line, which starts a file.
	diffLineFile diffLineKind = iota
	// diffLineMeta is the file's own header: index, mode, rename, the
	// `---`/`+++` markers, `Binary files … differ`.
	diffLineMeta
	// diffLineHunk is an `@@` hunk header.
	diffLineHunk
	diffLineAdd
	diffLineRemove
	// diffLineContext is a context line, or `\ No newline at end of file`.
	diffLineContext
)

// diffLines classifies the lines of a unified diff, in order.
//
// It tracks whether it is inside a hunk rather than reading prefixes alone,
// because the prefixes are ambiguous: a removed line whose content starts with
// `--` begins with `---`, which is also the old-file marker. Inside a hunk every
// line is a context, `+` or `-` line — or the next `@@` — until the next
// `diff --git`, since no content line can begin with either. The colour
// renderer and the stat table both read the diff through this one type, so they
// cannot disagree about which line is which.
type diffLines struct {
	inHunk bool
}

func (d *diffLines) kind(line string) diffLineKind {
	switch {
	case strings.HasPrefix(line, "diff --git "):
		d.inHunk = false
		return diffLineFile
	case strings.HasPrefix(line, "@@"):
		d.inHunk = true
		return diffLineHunk
	case !d.inHunk:
		return diffLineMeta
	case strings.HasPrefix(line, "+"):
		return diffLineAdd
	case strings.HasPrefix(line, "-"):
		return diffLineRemove
	default:
		return diffLineContext
	}
}

// The SGR sequences the renderer writes: the basic sixteen-colour set, which
// every profile from ANSI up passes through unchanged.
const (
	sgrReset = "\x1b[0m"
	sgrBold  = "\x1b[1m"
	sgrDim   = "\x1b[2m"
	sgrRed   = "\x1b[31m"
	sgrGreen = "\x1b[32m"
	sgrCyan  = "\x1b[36m"
)

// diffPainter colours a diff line by line: file headers bold, hunk headers
// cyan, additions green, removals red. It is the CLI's own renderer, not the
// TUI's lipgloss styles (task 047 decision 7). The first write error is kept
// and every later line is dropped.
type diffPainter struct {
	w     io.Writer
	lines diffLines
	err   error
}

func (p *diffPainter) line(line string) {
	if p.err != nil {
		return
	}
	var style string
	switch p.lines.kind(line) {
	case diffLineFile, diffLineMeta:
		style = sgrBold
	case diffLineHunk:
		style = sgrCyan
	case diffLineAdd:
		style = sgrGreen
	case diffLineRemove:
		style = sgrRed
	case diffLineContext:
	}
	if style == "" {
		_, p.err = io.WriteString(p.w, line+"\n")
		return
	}
	_, p.err = io.WriteString(p.w, style+line+sgrReset+"\n")
}

// fileStat is one row of `--stat`. It is the CLI's shape, not a wire type.
type fileStat struct {
	Path    string `json:"path"`
	Added   int    `json:"added"`
	Removed int    `json:"removed"`
	Binary  bool   `json:"binary"`
}

// cells renders the ADDED and REMOVED columns. A binary file has no line
// counts to report, and 0/0 would claim it did not change.
func (f fileStat) cells() []string {
	if f.Binary {
		return []string{"binary", "binary"}
	}
	return []string{"+" + strconv.Itoa(f.Added), "-" + strconv.Itoa(f.Removed)}
}

// laneFileStat is a `--stat --by lane` row: the file row, credited to the
// section it came from.
type laneFileStat struct {
	LaneID      string `json:"lane_id"`
	ChildTaskID int64  `json:"child_task_id"`
	Remainder   bool   `json:"remainder"`
	fileStat
}

// diffStatScanner counts added and removed lines per file.
//
// Files begin at `diff --git`; counts come from inside hunks only, so the
// `---`/`+++` markers are never counted. The path follows the TUI's rules for
// the same question (diffgroup.go): the markers are preferred over the
// `diff --git` line, whose `a/X b/Y` cannot be split reliably when a name holds
// a space; a rename is named by its destination; a deletion by its old side.
//
// A path that appears more than once is one row, its counts summed. The
// `--by lane` remainder is several diffs joined — the parent's own commits,
// then its uncommitted work — so a file touched in both is two `diff --git`
// entries, and a per-file table listing it twice would be a table of entries.
type diffStatScanner struct {
	lines diffLines
	files []fileStat
	index map[string]int
	cur   *diffStatFile
}

type diffStatFile struct {
	stat     fileStat
	gitLine  string
	oldPath  string
	newPath  string
	renameTo string
}

func (s *diffStatScanner) line(line string) {
	kind := s.lines.kind(line)
	if kind == diffLineFile {
		s.flush()
		s.cur = &diffStatFile{gitLine: line}
		return
	}
	if s.cur == nil {
		return
	}
	switch kind {
	case diffLineAdd:
		s.cur.stat.Added++
	case diffLineRemove:
		s.cur.stat.Removed++
	case diffLineMeta:
		switch {
		case strings.HasPrefix(line, "--- "):
			s.cur.oldPath = diffMarkerPath(line)
		case strings.HasPrefix(line, "+++ "):
			s.cur.newPath = diffMarkerPath(line)
		case strings.HasPrefix(line, "rename to "):
			s.cur.renameTo = strings.TrimPrefix(line, "rename to ")
		case strings.HasPrefix(line, "Binary files "), strings.HasPrefix(line, "GIT binary patch"):
			s.cur.stat.Binary = true
		}
	case diffLineFile, diffLineHunk, diffLineContext:
	}
}

func (s *diffStatScanner) flush() {
	if s.cur == nil {
		return
	}
	f := s.cur.stat
	switch {
	case s.cur.renameTo != "":
		f.Path = s.cur.renameTo
	case s.cur.newPath != "":
		f.Path = s.cur.newPath
	case s.cur.oldPath != "":
		f.Path = s.cur.oldPath
	default:
		f.Path = diffGitPath(s.cur.gitLine)
	}
	s.cur = nil
	if i, ok := s.index[f.Path]; ok {
		s.files[i].Added += f.Added
		s.files[i].Removed += f.Removed
		s.files[i].Binary = s.files[i].Binary || f.Binary
		return
	}
	if s.index == nil {
		s.index = map[string]int{}
	}
	s.index[f.Path] = len(s.files)
	s.files = append(s.files, f)
}

// finish returns every file read, never nil: an empty diff is `[]`.
func (s *diffStatScanner) finish() []fileStat {
	s.flush()
	if s.files == nil {
		return []fileStat{}
	}
	return s.files
}

// diffMarkerPath reads the path out of a `---`/`+++` marker: the `a/`|`b/`
// prefix off the front, the tab git appends to a name containing a space off
// the back, and `/dev/null` reported as no path at all.
func diffMarkerPath(line string) string {
	p := strings.TrimRight(line[len("--- "):], "\t")
	if p == "/dev/null" {
		return ""
	}
	for _, prefix := range []string{"a/", "b/"} {
		if rest, ok := strings.CutPrefix(p, prefix); ok {
			return rest
		}
	}
	return p
}

// diffGitPath is the fallback path, for a file whose markers never arrived —
// a binary file, or a mode change with no content. `a/X b/Y` is only
// unambiguous when the halves agree, so it is checked rather than split.
func diffGitPath(line string) string {
	rest := strings.TrimPrefix(line, "diff --git ")
	if a, b, ok := strings.Cut(rest, " b/"); ok {
		if p := strings.TrimPrefix(a, "a/"); p == b {
			return p
		}
	}
	return rest
}
