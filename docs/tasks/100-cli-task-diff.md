# 100 — `vincent task diff`, with per-lane attribution

**Status:** ✅ done (4/4)
**Opened:** 2026-09-15

*Issue [#389](https://github.com/lezli01/vincent/issues/389).*

`GET /v1/tasks/{id}/diff` (§13.2) serves a task's worktree against
`merge-base(base_sha || base_branch, HEAD)` as `text/plain`, and `?by=lane`
(task 084 decision 6) serves the same change cut into one section per lane merge
on the parent's first-parent chain, remainder last. Two clients read it — the
TUI's Diff tab and MCP's `task_diff` — and the CLI did not. Task 048's "What is
still API-only" named `task diff` as its own work and pointed here.

This task adds `vincent task diff <id> [--by lane] [--stat] [--json]`, a thin API
client like its siblings. It follows task 047 decision 7 (the CLI writes its own
small renderer rather than reusing the TUI's lipgloss code) and PR U decision
(every data subcommand has `--json`). Task 025 decision 12 does not reach it:
that decision covers writes, and reading a diff is not one. The API, MCP, the
TUI and §13.2's route text do not change.

## Decisions

**1. Colour only on a terminal, ANSI, no flag.** *(2026-09-15)*

On a TTY with `NO_COLOR` unset, the text form colours file-header lines, `@@`
hunk lines, additions and removals. Piped or redirected output is exactly the
bytes the daemon served. There is no `--color` flag. Detection and writing go
through `github.com/charmbracelet/colorprofile`, already a direct dependency,
which owns `NO_COLOR`, `TERM=dumb` and the Windows console — so there is no
`_windows.go` file. The CLI's own `isTTY` is consulted first, because
colorprofile would honour `CLICOLOR_FORCE` on a pipe and the piped bytes are a
promise. This is the CLI's first colour.

Lines are classified by tracking hunk state, not by prefix alone: a removed line
whose content starts with `--` begins with `---`, which a prefix-only reader
takes for a file marker. The colour renderer and the stat table read the diff
through the same classifier, so they cannot disagree.

The alternative it beat: a `--color=auto|always|never` flag. `NO_COLOR` and the
TTY test cover every case a script meets, and `always` is the one setting that
would make piped output something other than a patch.

**2. `--stat` is a per-file table computed in the CLI.** *(2026-09-15)*

`FILE`, `ADDED`, `REMOVED` through `output.go`'s `table()`: `+N`/`-N`, or
`binary` for a binary file. `--by lane` adds a leading `LANE` column, `-` for the
remainder. The table is never coloured — `tabwriter` counts escape bytes as
width — and an empty diff still prints its header row. Files are read from
`diff --git` headers, lines are counted inside hunks only, and binary files,
renames, deletions, new files and mode-only changes are each handled. A path
appearing twice in one diff is one row with its counts summed: the remainder is
the parent's own commits joined to its uncommitted work, so the same file can be
two entries.

The alternative it beat: a `stat` shape on the API. It would change §13.2 for a
view one client needs, and the body already carries everything the table says.

**3. `--json` mirrors the wire format.** *(2026-09-15)*

- Plain `--json`: `{"diff": "<text>"}`.
- `--by lane --json`: the sections array exactly as `?by=lane` serves it — never
  `null`, always at least the remainder section.
- `--stat --json`: `[{path, added, removed, binary}]`; with `--by lane` each row
  also carries `lane_id`, `child_task_id` and `remainder`. Empty is `[]`.

The stat shapes belong to the CLI; they are not API wire types.

**4. The ungrouped diff is streamed with no size cap.** *(2026-09-15)*

`apiclient.Diff` cuts its read at 8 MiB (`maxDiffBytes`, sized for the TUI pane)
without saying so. Piping a cut diff into `git apply` is a corrupt patch
delivered with exit 0. `apiclient.DiffStream` returns the response body with
Diff's `*Error` handling, and `Diff` is now a bounded read over it, so the TUI
keeps its bound. The stream is read on the untimed stream client with the
request timeout bounding only the wait for headers: a reader paging a large diff
through `less` holds the body open longer than ten seconds. The CLI reads it with
`bufio.Reader`, not `bufio.Scanner`, whose 64 KiB token limit fails on one-line
minified or generated files. `DiffByLane` already decodes without a limit.

**5. `--by lane` text rendering.** *(2026-09-15)*

Every section in the daemon's order, each under one ASCII header line (047
decision 7): `# lane <lane_id> (task <child_task_id>, merge <sha, 12 chars>)`, or
`# remainder (the task's own commits and uncommitted work)`. A section with an
empty diff still gets its header — a lane that changed nothing is worth knowing.
`git apply` ignores lines outside a patch, so grouped output still applies. On a
TTY the header is dim.

**6. Validation and errors.** *(2026-09-15)*

`--by` accepts only `lane`, refused before any request in the daemon's own words
(`by must be "lane"; got "x"`), exit 1 — the exit-code table's "client refused
the input" row. An invalid id exits 1. A 404 or 409 prints `Error: <daemon
message>` on stderr and exits 1 through `apiMessage`; no daemon is exit 2 through
`withClient`. An empty diff prints nothing and exits 0, as `git diff` does.
Every diagnostic goes to stderr.

## Work

- [x] **100.1 — `internal/apiclient`: `DiffStream`, with `Diff` as a bounded read over it.** ✓ 2026-09-15
- [x] **100.2 — `internal/cli/diff.go`: the command, the line classifier, the colour renderer, the stat scanner and the section headers; registered in `newTaskCmd`; `tty.go`'s comment.** Depends: 100.1. ✓ 2026-09-15
- [x] **100.3 — Tests: `diff_live_test.go` on the transcript harness, `diff_test.go` for the renderer and the scanner, `diffstream_test.go` for the uncapped stream.** Depends: 100.2. ✓ 2026-09-15
- [x] **100.4 — Spec §12.1 row, the CLI reference, the scripting guide, `README.md`, 048's dated amendment, `CHANGELOG.md`.** ✓ 2026-09-15

## What the tests prove

- Piped plain output is the endpoint's body byte for byte, uncommitted changes
  included, with no escape byte; `--json` carries the same text.
- `--by lane` over real `--no-ff` lane merges prints the sections in order under
  their headers, remainder last, each section its own bytes; a task with no lanes
  prints one remainder section.
- `--stat` counts a binary file, a rename, a deletion, a new file, uncommitted
  lines and a removed `--` line correctly, and `--by lane` adds the `LANE`
  column.
- Every JSON form has the shape above, and an empty diff is `[]` or
  `{"diff": ""}`, never `null`.
- `--by x` exits 1 without a request; a task with no worktree exits 1 with the
  daemon's 409 wording.
- The renderer (forced on) and the scanner are table-tested, and a body larger
  than 8 MiB streams through `DiffStream` whole.
