# 047 — `vincent daemon logs` and `vincent task transcript`

**Status:** ✅ done (5/5)
**Issue:** [#92](https://github.com/lezli01/vincent/issues/92)
**Spec:** amends §12.1

## Problem

The two artifacts a failure is diagnosed from — the daemon log and a step's
transcript — were reachable only from the TUI, or by knowing where the files
live and reaching for `tail` yourself.

- **The daemon log had no CLI surface.** `internal/tui/daemon.go` tails it off
  disk on a 2 s timer (500 lines); `vincent daemon start` prints a 20-line tail
  only when startup fails; `vincent doctor` carries a fixed 20-line tail inside a
  larger report. There was no way to ask for the log itself, at a length you
  choose, or to follow it.
- **The transcript gap was the CLI's alone.** `GET /v1/tasks/{id}/steps/{run_id}/transcript`
  already served `format=raw|normalized`, `offset`/`tail` and a record-boundary
  `X-Next-Offset`, and `apiclient.Client.Transcript` already consumed it. Only
  the TUI called it; `vincent task show` printed the file paths and stopped.

Both bite hardest in the case they exist for: a task blocked overnight on a box
you are on over SSH, or a daemon that will not start — where launching a
full-screen Bubble Tea client is either impossible or the wrong tool.

`vincent doctor` is not the missing command, and this does not relitigate §17's
"Entry point" note. That note records doctor replacing five surfaces for the
question "why is nothing running?", one of which was the TUI's daemon view.
Doctor answers that question in one pass with a fixed 20-line tail; it is not a
pager and cannot follow. `daemon logs` serves the other question — "what is the
daemon doing right now" — and doctor's row is unchanged.

## Decisions

**Decision 1 (2026-08-28) — `--json` keeps its meaning; `--raw` is the new
flag.** `vincent task transcript --json` emits the *normalized* records as
NDJSON, in vincent's own vocabulary including its `vincent.*` annotations.
`--raw` passes `format=raw` and streams the agent's untouched dialect.

Beat: the issue's spelling, where `--json` meant `format=raw`. `--json` means
"vincent's typed JSON" on every other subcommand, and one command redefining it
is a trap for exactly the reader who learned the convention elsewhere.

**Decision 2 (2026-08-28) — follow polls the transcript endpoint.** `-f` opens
on `apiclient.DefaultTailBytes` and re-fetches from `offset=NextOffset` every
2 s.

Beat: subscribing to §13.3's live output stream. The deciding reason is an
ownership invariant rather than simplicity: live output chunks are *dropped*
for slow subscribers because the transcript file is the durable copy, and a CLI
writing into a slow pipe is exactly that subscriber — so the SSE route would
silently lose output in the case the command exists for. Polling also gives one
code path for a live attempt and an already-finished one.

**Decision 3 (2026-08-28) — with `--step` omitted, the running attempt if there
is one, else the newest by `step_run` id.** `t.Steps` arrives ordered
`step_index, iteration, attempt, id`, which stops being chronological the moment
a task has parallel steps or fan-out lanes; `id` is creation order and does not.
A step run's state is `running` until it settles, so "the running attempt" is a
single unambiguous row in practice, and ties fall through to the newest id.

Beat: "the last row of the list", which is the same thing only for a linear
workflow, and silently wrong for the shapes this project has had since §7.5.

**Decision 4 (2026-08-28) — `vincent daemon logs` takes no `--data-dir`.** It
resolves directories through `config.ResolveDirs` — `VINCENT_DATA_DIR`, else the
§12.2 platform default — exactly as `daemon start`, `stop`, `status`, `doctor`
and every other subcommand do.

Beat: mirroring the flag on `vincent daemon` itself. That flag exists for the
Windows Scheduled Task, whose `Exec` action has no environment (§12.1); it is
not a CLI-wide convention, and adding it to one subcommand of five would be a
new unevenness for no case the environment variable does not already serve.

**Decision 5 (2026-08-28) — everything a reader reads goes to stdout, including
a command step's `stream: stderr` output.** A transcript is one interleaved
record stream; splitting it across two file descriptors scrambles the ordering
that makes it readable. Stderr records are tagged `[stderr]` in the rendered
text instead, and `--raw`/`--json` are byte-faithful either way. The CLI's own
diagnostics — a step with no transcript, a follow that ended — go to stderr, so
stdout stays pipeable per `docs/reference/cli.md`'s global-behavior rules.

**Decision 6 (2026-08-28) — "no transcript" is decided client-side, before the
request.** The endpoint returns 404 with `CodeNotFound` for two different facts:
"step run has no transcript" (a manual gate) and "transcript file is gone
(pruned or removed)". The CLI already holds the step rows from `GetTask` — it
needs them for decision 3 — and `TranscriptPath` is empty for the first case, so
it reports "step run N has no transcript" on stderr and exits **0** without
calling the endpoint. A 404 that still comes back therefore means the file was
pruned or removed, which is exit **1** naming `transcript_retention_days`.

Beat: matching on the endpoint's message strings, which couples the CLI to
wording the API is free to change.

**Decision 7 (2026-08-28) — the CLI's renderer is new code, not a reuse of the
TUI's.** `detail.renderRecord` returns `paneLine`s of lipgloss segments and
branches on the pane's detail level; it is a Bubble Tea layout primitive, not a
text formatter. Extracting a shared one would mean lifting `paneLine`, `segment`
and the style table into a package `internal/cli` can import — a larger and
worse change than one small renderer over the same public `apiclient` records.
What holds the two together is the record vocabulary, which is where the
contract already lives. The markers differ deliberately: the CLI's are ASCII,
because its output is piped and a Windows console under an OEM code page renders
`✓` as noise.

**Decision 8 (2026-08-28) — `GET /v1/daemon/logs` is left unbuilt.** The one
client that exists would then read the log through the daemon that may be what
is broken. It becomes right when a client that is not on the daemon's machine is
real — a web UI — at which point the CLI can prefer it and keep the disk read as
the fallback. Recorded in §12.1 rather than left to be rediscovered.

**Decision 9 (2026-08-28) — giving a read-only view a command line breaks no
rule.** Task 027's decision that `retry`, `repair`, `skip` and `approve` stay
TUI-and-API only is about §6 *human actions* — writes. Reading a transcript is
neither, so that decision is untouched and stays binding.

## Tasks

- [x] **047.1** `internal/apiclient`: `TranscriptOptions.query` takes the
  format, `Client.TranscriptRaw` returns the endpoint's bytes and
  `X-Next-Offset` unparsed — never through `decodeTranscript`, which tolerantly
  drops lines a renderer is right to skip and a byte-faithful flag is not.
  ✓ 2026-08-28
- [x] **047.2** `vincent daemon logs [-n N] [-f]` in `internal/cli/daemon.go`,
  reading disk through `daemon.LogPath` + `daemon.TailFile`, with a follow that
  opens/reads/closes per poll so log rotation is not broken by watching it.
  ✓ 2026-08-28
- [x] **047.3** `vincent task transcript <id> [--step RUN] [-f] [--json|--raw]`
  in a new `internal/cli/transcript.go`, with the plain-text renderer beside it.
  ✓ 2026-08-28
- [x] **047.4** Tests: `internal/cli/logs_test.go` for the log command including
  the rotation case, and `internal/cli/transcript_live_test.go` against the
  **real** API handlers over `httptest` for selection, rendering, `--json`,
  `--raw`, the two 404s and the follow seam. ✓ 2026-08-28
- [x] **047.5** Spec §12.1 amendment, `docs/reference/cli.md`,
  `docs/reference/files.md`, `docs/guides/troubleshooting.md`,
  `docs/guides/scripting.md`, the `CHANGELOG.md` entry, and a drift check
  holding every command in the tree to a mention on the CLI page. ✓ 2026-08-28

## Out of scope

`GET /v1/daemon/logs` (decision 8) — a follow-up issue when a remote client is
actually on the table, not before.

No gate script. Neither command changes daemon behavior, and both are covered by
in-process tests against the real handlers; the acceptance gates exist for
end-to-end daemon behavior over curl.
