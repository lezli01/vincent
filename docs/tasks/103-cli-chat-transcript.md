# 103 — `vincent chat transcript`

**Status:** ✅ done (4/4)
**Issue:** [#392](https://github.com/lezli01/vincent/issues/392)
**Spec:** amends §12.1

## Problem

`vincent chat` had `start`, `send`, `answer`, `cancel`, `list`, `show`,
`archive`, `handoff` and `delete`. `show` prints each turn's prompt and
`result_text`, never what the agent did along the way, so the only client that
read a chat turn's transcript was the TUI's chat workspace. A person in a
terminal or a script had curl.

The daemon already served everything needed.
`GET /v1/chats/{id}/turns/{seq}/transcript` (task 067 decision 2) takes
`format=raw|normalized` and `offset`/`tail`, returns `X-Next-Offset`, and
shares `transcriptRange` and `serveTranscriptFile` with the step route. Nothing
recorded the gap as intended: tasks 063 and 067 never decided against a CLI
read, and task 047 decision 9 — a read-only view is not a §6 human action —
covers it.

## What shipped

```
vincent chat transcript CHAT_ID [--turn N] [-f|--follow] [--json|--raw]
```

A thin API client over the existing route: no daemon, store, API or migration
change. `internal/apiclient` gains `ChatTurnTranscriptRaw`, and
`internal/chatrun` exports the predicate decision 4 needs.

## Decisions

1. **2026-09-16 — Output flags mirror task 047 decision 1: `--json | --raw`,
   not `--format raw|normalized`.** Default output is the normalized records
   rendered as text by the existing renderer. `--json` gives those records as
   NDJSON, each record's own line, `vincent.*` annotations kept. `--raw` gives
   the agent's dialect byte for byte. The two flags are mutually exclusive. The
   issue's `--format` spelling was dropped because decision 1 is binding and
   every `vincent chat` verb already carries `--json` with its CLI-wide
   meaning. The author confirmed this.

2. **2026-09-16 — With no `--turn`, print the running turn if there is one,
   else the newest by seq.** This mirrors task 047 decision 3, in one turn's
   form. A chat's turns are strictly sequential, so seq order is chronological
   and no id tiebreak is needed. `--turn N` takes the 1-based seq `chat show`
   prints as `--- turn N ---`. An unknown seq exits 1 with
   `turn N not found on chat M`; a chat with no turns exits 1 with
   `chat M has no turns yet`. Printing every turn in order was rejected: a long
   chat's full transcript is unbounded output, and `chat show` already gives
   the whole conversation at prompt/answer level.

3. **2026-09-16 — `-f`/`--follow` follows one turn, then exits.** This mirrors
   task 047 decision 2. The follow opens on `apiclient.DefaultTailBytes` and
   polls the endpoint from `X-Next-Offset` every `transcriptPollInterval`
   (2 s). It does **not** subscribe to `GET /v1/chats/{id}/events`, whose live
   chunks are dropped for a slow subscriber. Before each fetch it reads the
   turn's state through `GetChat`; the fetch after the turn settles is the
   last one, then `turn N is <state>` goes to stderr and the command exits 0. A
   turn parked in `awaiting_input` is still `running`, so the follow keeps
   going. It does not wait for a later `send`. Following the whole chat was
   rejected: an idle chat would make the command never exit.

4. **2026-09-16 — "No transcript" is decided client-side from the turn row's
   `fail_reason`, with no wire change.** This mirrors task 047 decision 6.
   `internal/chatrun`'s `runTurn` opens `{seq}.jsonl` after the adapter lookup,
   so exactly two failures leave a turn with no file: `agent_unavailable` and
   `transcript_io_error`. A turn failed for either prints
   `turn N (<reason>) has no transcript` on stderr, makes **no request**, and
   exits **0**. Any other 404 means the file was pruned or removed: exit **1**
   through the existing `transcriptError`, naming `transcript_retention_days`.
   A typed `details.reason` on the endpoint's 404 was rejected as an API change
   for a fact the client already holds, and a uniform exit 1 because it drops
   decision 6's distinction. The reason strings are not retyped in
   `internal/cli`: the predicate is `chatrun.TurnWritesNoTranscript`, beside
   `runTurn`, whose ordering it describes, and a test pins it to exactly those
   two reasons and to what `runTurn` leaves on disk. `cli → chatrun` keeps the
   dependency direction; `cli` already reaches it through `daemon`.
   One known, accepted edge: a daemon crash in the instant between inserting
   the turn row and opening the file leaves an `interrupted` turn with no file.
   That 404s into the exit-1 "pruned or removed" message. The window is a few
   statements wide, and recovery records no reason that could tell it apart.

5. **2026-09-16 — One printer, two sources.** `transcriptPrinter` is
   generalized over a `transcriptSource` seam rather than copied. The seam is
   three calls: a normalized fetch, a raw fetch, and a state probe returning
   whether the subject is still running plus the line a follow ends on. The
   task command builds it from `Transcript`/`TranscriptRaw`/`GetTask`
   (`stepRunSource`), the chat command from
   `ChatTurnTranscript`/`ChatTurnTranscriptRaw`/`GetChat` (`chatTurnSource`).
   The renderer and the `sawOutput` state are unchanged, and `print`/`follow`
   keep their task-shaped signatures as thin wrappers, so
   `vincent task transcript` output is byte-identical and its existing live
   tests pass unmodified.

## Work

- [x] **103.1 — Client and predicate**: `apiclient.ChatTurnTranscriptRaw`
  through `transcriptAt`, never `decodeTranscript`, sharing its unparsed read
  with `TranscriptRaw`; `chatrun.TurnWritesNoTranscript` with a table test and
  a test against `runTurn` itself. ✓ 2026-09-16
- [x] **103.2 — The command**: the source seam in `internal/cli/transcript.go`,
  and `newChatTranscriptCmd` with `selectChatTurn` in
  `internal/cli/chattranscript.go`, registered in `newChatCmd`. ✓ 2026-09-16
- [x] **103.3 — Tests**: `internal/cli/chattranscript_live_test.go` against the
  real handlers (`liveHarness` now sets `api.Deps.Dirs`) — a captured claude
  run renders identically as a chat turn and as a task attempt, `--raw` is the
  file verbatim, `--json` is NDJSON with annotation fields kept, `--json --raw`
  is refused, selection, unknown turn and empty chat, no-transcript with zero
  requests versus a pruned file, and a follow with no gap or duplicate that
  ends with the turn; `TestChatTurnTranscriptRaw` in `internal/apiclient`.
  No gate script: like task 047, this is a thin read-only client covered
  in-process against the real handlers. ✓ 2026-09-16
- [x] **103.4 — Documentation**: §12.1 amended, `docs/reference/cli.md`,
  `docs/guides/scripting.md`, `docs/guides/troubleshooting.md`,
  `docs/features.md`, `CHANGELOG.md`. ✓ 2026-09-16
