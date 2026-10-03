# Task 130 gate — issues end to end

**Acceptance (task 130.17, #676):** a real daemon, over curl alone, runs
vincent's issues end to end — local CRUD and its events, a task created from
an issue, the GitHub import, state write-back through the durable outbox, and
the MCP guard on a forge write — on Linux, macOS and Windows. Task 130.16
(#675) added the discussion thread as scenario 12.

The scripted half is [`scripts/130-gate.sh`](../../scripts/130-gate.sh). It
is task-numbered rather than `mN` because this is not a §19 milestone, like
the 123 and 125 gates.

```sh
./scripts/130-gate.sh                            # all twelve scenarios
VINCENT_GATE_SCENARIO=8 ./scripts/130-gate.sh    # one, for debugging
```

It needs bash, go, git, curl and jq. Each scenario runs its own daemon over
its own config and data dirs, which is what lets one run alone. No agent CLI
is involved. The GitHub half is `cmd/fakegh` built **as `gh`** onto the
daemon's PATH, with three files under the scenario's temp dir:

- `FAKEGH_ISSUES_FILE`, the issue corpus. It is re-read on every call, so the
  gate can edit GitHub between two polls.
- `FAKEGH_SCENARIO_FILE`, which flips the fake between `success`,
  `unreachable` and `read-only` without a restart.
- `FAKEGH_ARGV_FILE`, every call the daemon made. "No write was sent" is
  therefore observed behaviour, not a reading of the source.

The seeded corpus is open issues #1–#4, closed #5 and an open pull request
#6, in `gate/issues`.

## What the script asserts

The gate asserts the vocabulary the code ships, read from
`internal/api/issues.go`: every 409 is `invalid_state` with the reason in
`details`. An imported issue's `sync` block is `{state, reason}`, and a
transferred or deleted remote shows in `source.status`.

1. **Local CRUD.** A create with an `Idempotency-Key` is 201. The same key
   and body replay the same issue with no second row, and the same key with
   another body is a 409 `idempotency_key_reused`. A `PATCH` at a stale
   `version` is a 409 `issue_changed` carrying the current version. The three
   close reasons (`completed`, `not_planned`, and `duplicate` with
   `duplicate_of`) are stored. A second close is the FSM's 409, with
   `details.state: closed`. Reopen clears the reason, and `DELETE` makes the
   issue 404. The `project_id`, `state` and `label` filters each return
   exactly the expected ids. `GET /v1/events` carries `issue.created`,
   `issue.updated`, `issue.state_changed` and `issue.deleted`, and resuming
   from a mid-run `Last-Event-ID` replays exactly the later `issue.*` events.
2. **A task from a local issue.** `POST /v1/tasks {issue_id}` prefills the
   title and description from the issue, and an explicit `title` wins over
   the prefill. The task's `issue` names the issue, `?issue_id=` returns
   exactly that task, and the issue's rollup counts one task.
3. **The import.** The first tick imports #1–#4 as open, `github`, `synced`
   issues, with closed #5 and pull request #6 absent. Once a listing carries
   `If-None-Match`, the next one moves no issue's `updated_at` or `version`:
   the idle tick is the 304.
4. **Closed on GitHub.** The corpus closes #1 `not_planned`, the vincent
   issue follows, and no `PATCH` is sent.
5. **Closed through the API.** Closing #2 reaches the corpus as
   `closed/completed`. The issue says `synced`, and the argv log has exactly
   one `PATCH` for it.
6. **An offline close drains across a forced stop.** With the fake
   `unreachable`, closing #3 is `pending` and GitHub is untouched. Then
   `vincent daemon stop --force`, the fake back to success, and a restart:
   after the outbox's 30 s backoff the write lands and the issue says
   `synced`. The write is a row, not a goroutine (crash-first).
7. **`github.enabled: false`.** A local issue is created, patched, closed
   and reopened, with a GitHub project registered beside it. Several poll
   intervals pass and the argv log stays empty: not one `gh` call.
8. **A read-only token.** #2 is closed (synced), then reopened under the
   fake's `read-only`. The write ends `failed/no_write_scope`, and the local
   state must stay open while GitHub stays closed.
9. **A conflict.** With the fake `unreachable`, #4 is closed `completed`
   locally (`pending`, base open). Meanwhile the corpus closes it
   `not_planned`. When GitHub comes back the write ends `conflict`, the
   issue carries GitHub's `not_planned`, and no `PATCH` is sent.
10. **Moved and gone.** The sweep that finds a transferred or deleted issue
    runs once a day. Its first run is on the tick that completes the initial
    import, which is the tick that imported the issues. So this scenario
    seeds 520 open issues, more than one import pass of 500. The first pass,
    a "sync now", imports the 500 oldest, #7 and #8 among them, and stops
    short. The gate then marks #7 `_fake: {transferred_to}` and #8
    `_fake: {deleted: true}`, and a second "sync now" completes the import
    and sweeps. #7 is `source.status: moved` and `sync: failed/moved`. #8 is
    `missing` and `failed/gone`. Both are still listed, and nothing was
    deleted. The poll interval is an hour, so both passes are requests and
    no timer tick races them.
11. **The MCP guard.** `tools/call issue_close` over `/mcp` on an imported
    open issue is a tool error carrying the 409 `forge_write_needs_human`.
    The issue stays open and no `PATCH` is sent. The same call on a local
    issue closes it.
12. **The discussion thread.** A comment added to #1 in the corpus is
    mirrored onto the imported issue on a tick, as `hubot`, `remote: true`
    with its GitHub id as `remote_key`, and `GET /v1/events` carries exactly
    one `issue.comment_added` for it, `by: sync`, naming the comment and
    carrying no text. A comment posted to a local issue is `201` and carries
    the author the daemon recorded on that issue. A comment posted to the
    imported #1 is a 409 `issue_mirrored`, its thread stays one long, and the
    argv log holds no `POST`: nothing is ever posted to GitHub.

The `github_issue: N` shorthand scenario is not here. #676 made it
conditional on task 130.11 (#670), and 130.11 removed `github_issue` from
task create rather than keeping it as a shorthand (task 130 decision 22.1),
so there is nothing for it to assert.

## Found by this gate: scenario 8

Scenario 8 failed at the head this gate was written against: **GATE FAIL: a
refused write undid the local reopen: closed**. The spec's write-back
section says `no_write_scope` ends the write `failed` "and keep[s] the local
state". What happened instead:

1. The reopen is written as a human transition (`issue.state_changed`
   closed→open, `by: human`).
2. The outbox reads #2 (closed, the base), sends the `PATCH`, gets the 403,
   and settles the row `failed/no_write_scope` (`issue.updated` changed
   `sync`).
3. About a second later the importer's next tick lists #2 again: the
   incremental listing asks from two minutes behind the watermark, and the
   earlier close had just stamped #2's `updated_at`. It refreshed the issue
   back to closed (`issue.updated` changed `close_reason`, `state`,
   open→closed, `by: sync`).

`refreshRemoteIssueTx` (`internal/store/issues_remote.go`) held the local
state only while a write was *pending*. It now holds it too while the
issue's newest write has ended `failed` and GitHub still shows that write's
base; once GitHub moves off the base, someone else changed it and GitHub is
the authority again. The fix landed in the same pull request as the gate,
with `TestRefreshKeepsTheStateOfAFailedWrite` in `internal/store`, and the
gate's assertion is unchanged.

## What the script does not assert

The TUI: that is task 130.18's screenshots and `internal/tui`'s own tests.
Trigger and command-step use of issues (tasks 130.15, 130.14) is out of
scope too, as #676 says.

## CI

The gate runs as the `130 gate (issues)` step of `ci.yml`'s `gates` job, on
Linux, macOS and Windows. The pull request that added it is its first run on
Linux and Windows. Those runs are recorded below from their CI results, never
in advance.

## Runs

| Date | Platform | Result | By |
|---|---|---|---|
| 2026-10-03 | macOS (darwin/arm64) | Scenarios 1–7 and 9–11 pass, each run alone with `VINCENT_GATE_SCENARIO`. Scenario 8 GATE FAIL (the product bug above) | task 130.17 |
| 2026-10-03 | macOS (darwin/arm64) | GATE PASS, all eleven scenarios end to end, with the scenario 8 fix | task 130.17 |
