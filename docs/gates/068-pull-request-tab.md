# Task 068 gate — acting on a linked pull request

**Acceptance (task 068.5):** a real daemon merges, closes, reopens, comments
on and re-runs the failed checks of a task's linked pull request, through the
API and through `vincent github pr`, sending exactly what was asked for — and
every refusal in the write vocabulary is decided where task 068.4 put it:
`validation_failed`, `pull_not_linked` and `disabled` before GitHub is asked at
all, `branch_behind`, `checks_running`, `not_mergeable` and `head_changed` by
the merge's preflight read before anything is sent, and `head_changed` and
`no_write_scope` by GitHub after the write was sent. Reading the pull request
and its checks, and the reconciler's ticks, write nothing.

The scripted half is [`scripts/068-gate.sh`](../../scripts/068-gate.sh). It is
task-numbered rather than `mN` because this is not a §19 milestone; 017, 032,
052 and 069 set that precedent.

```sh
./scripts/068-gate.sh                            # all seven scenarios
VINCENT_GATE_SCENARIO=5 ./scripts/068-gate.sh    # one, for debugging
```

It needs bash, go, git, curl and jq, and nothing else. `cmd/fakegh` is built
**as `gh`** onto the daemon's PATH, which is how the daemon finds the real CLI
too (there is no `gh_path` config key). `GITHUB_TOKEN` and `GH_TOKEN` are unset,
so a `gh` that failed to resolve fails the gate as `no_credential` rather than
reaching api.github.com.

Each scenario is its own installation: its own config and data directories,
daemon, argv log, state file and stdin file for the fake. `FAKEGH_STATE_FILE`
is what lets a `pr view` after a write read back the state the write left, and
`FAKEGH_STDIN_FILE` is the comment body exactly as `gh` read it. The project is
a repository whose `origin` is `https://github.com/octo/repo.git`, which is
what makes it a GitHub project (§13.2). Nothing is pushed. No agent CLI is
involved: the one workflow is a command step whose whole body is `exit 0`.

Every task is linked **by hand** (`POST /v1/tasks/{id}/github/pull`) rather
than by the reconciler, so no scenario waits on a poll tick for its link, and a
human link is one the reconciler never overwrites. Task titles are chosen so
no branch matches one of the fake's corpus heads. The write routes have no
task-state guard, so each task is created and left where it is. The pull
request is corpus #412: open, head `d3adb33f…`, with a failed Actions check
(`build`, run 5150), a running one (`test`, run 5150), a third-party check
(`license/cla`) and a legacy commit status (`ci/legacy-builder`).

"Sent" and "not sent" are observed process behaviour: the fake appends every
argv it is invoked with to a log, and each scenario asserts the exact count of
`pr merge`, `pr close`, `pr reopen`, `pr comment` and `run rerun` lines after
every call.

## What the script asserts

1. **Reads, and nothing written without a route.** With
   `github: {enabled: true, poll_interval: 1s}`,
   `GET /v1/tasks/{id}/github/pull` answers `linked`, `number: 412`,
   `source: human`, the pull request open and unmerged on head `d3adb33f…`.
   `GET …/github/pull/checks` answers that head as `ref` and four rows: `build`
   failed and `test` in progress, both carrying `run_id: 5150`, `license/cla`
   and `ci/legacy-builder` carrying none, and a rollup `state` of `failure`,
   because failure beats running. `vincent github pr checks --task N --json`
   answers the same `ref`, `state` and rows. A second, unlinked task keeps the
   reconciler listing; after two new `pr list` calls, the argv log holds no
   write of any verb.
2. **Refused before GitHub is asked.** `400 validation_failed` for a merge with
   no `method`, with `method: "fast-forward"`, and with no `head_sha`, for a
   blank comment, and for a re-run of `run_id: 0`. `409 pull_not_linked` on all
   five routes for a task that was never linked. With the daemon restarted
   under `github.enabled: false`, all five routes on the linked task answer
   `409` with `reason: disabled`, and the argv log stays **empty** from the
   restart on: no `gh` process runs at all, not even the credential probe.
   Back on, after `DELETE /v1/tasks/{id}/github/pull` the task reads
   `linked: false`, `suppressed: true`, and all five routes answer
   `409 pull_not_linked` — a suppressed link does not count. No write was sent
   in any of it.
3. **Each write, through the API.** Each answers `200` and adds exactly one
   line of its own verb, and no other write:
   - **Comment**, with a body that is multi-line, indented, carries quotes, a
     `$`, a backslash and one CRLF, and ends in a newline. The answer's `url`
     is `https://github.com/octo/repo/pull/412#issuecomment-1`, the argv is
     `pr comment 412 -R octo/repo --body-file -`, and the bytes `gh` read on
     stdin are byte-identical to the body sent — compared with
     `git hash-object --no-filters`, never a CR-stripped copy.
   - **Re-run** of run 5150 answers `{run_id: 5150}` with argv
     `run rerun 5150 --failed -R octo/repo`. A re-run of run 9999, which backs
     no failed Actions row on the head, is `409 bad_request`, and no second
     `run rerun` is sent.
   - **Close** answers `state: closed`, and the pull request reads closed.
   - **Reopen** answers `state: open`, and the pull request reads open.
   - **Merge** with `method: squash` and the head answers `merged: true`, and
     the pull request reads merged. The argv carries `--squash` and
     `--match-head-commit d3adb33f…`, and none of `--delete-branch`, `--auto`
     or `--admin`. A second merge is `409 not_mergeable`, still with exactly
     one `pr merge`: the preflight refuses a double merge.
4. **Each write, through the CLI**, the surface a script without a terminal
   has. `vincent github pr comment --task N --body-file - --json`, with the
   body on its stdin, exits 0 and prints the comment URL, and the bytes `gh`
   read match — two stdin hops, the CLI's and then `gh`'s.
   `vincent github pr merge --task N --method rebase --head-sha d3adb33f… --json`
   exits 0 printing a merged #412, sent `--rebase`. Two refusals exit with
   status 1 and send no `pr merge`: the same merge again, and a merge on a
   second task linked to #377, which is already merged.
5. **Merge refusals from the preflight send nothing.** One daemon restart per
   `FAKEGH_SCENARIO`, on a task linked to #412. Each merge answers `409` with
   the reason, the argv log gains no `pr merge`, and the pull request still
   reads open:

   | `FAKEGH_SCENARIO` | `head_sha` | Reason |
   |---|---|---|
   | `behind` | the head | `branch_behind` |
   | `blocked-running` | the head | `checks_running` |
   | `blocked` (the running check dropped) | the head | `not_mergeable` |
   | `dirty` | the head | `not_mergeable` |
   | `success` | not the head | `head_changed` |

6. **The pin fires at send.** Under `FAKEGH_SCENARIO=head-moved` the preflight
   passes — `CLEAN`, and the head matches — so the merge is sent, and GitHub
   refuses it: `409 head_changed` with **exactly one** `pr merge`, carrying
   `--match-head-commit d3adb33f…`, and the pull request still reads open.
   With scenario 5's last row this tells the two `head_changed` paths apart by
   what was sent.
7. **No write scope.** Under `FAKEGH_SCENARIO=read-only` the pull request and
   its checks still answer `200` with data and no `reason`. Merge (with the
   right head, so the preflight read passes), close, reopen, comment and a
   re-run of 5150 each answer `409 no_write_scope`, each after exactly one
   argv line of its verb — the attempt was made and GitHub refused it — and
   the pull request still reads open.

Between them the scenarios cover every member of 068.4's write vocabulary
(`no_write_scope`, `not_mergeable`, `checks_running`, `branch_behind`,
`head_changed`) end to end, and `pull_not_linked`, `disabled`, `bad_request`
and `validation_failed`.

## What the script does not assert

- **The TUI.** The Pull Request tab's popups, and the property that a rejected
  confirmation sends nothing, are
  `internal/tui/taskpullwrite_test.go:TestPullWriteRejectedConfirmationSendsNothing`
  and `internal/tui/taskpullwritelive_test.go` against the real API handlers.
  What a screen looks like is a judgement, and this gate stops at the API and
  the CLI.
- **The REST leg.** No fake REST server exists for scripts. `PUT
  /pulls/{n}/merge`, `PATCH /pulls/{n}`, `POST /issues/{n}/comments` and
  `POST /actions/runs/{id}/rerun-failed-jobs`, and their refusals, are
  `internal/github/write_test.go`'s, and the manual leg's token walk below.
- **The MCP exclusion.** That none of the five routes is an MCP tool is
  `internal/api/mcp_parity_test.go`'s to prove.
- **The draft refusal.** Corpus #401 is the draft, but it carries no head, so
  the preflight answers `head_changed` before it reaches the draft check.
  `internal/github/write_test.go`'s `TestMergeRefusal` covers a draft.
- **Real GitHub.** The fake stands in for `gh`. Proving the writes against
  github.com is the manual leg below.

## CI

`.github/workflows/ci.yml`'s `gates` job runs the scripted leg on Linux, macOS
and Windows as the `068 gate (pull request writes)` step. Windows matters to it
four ways: `gh` is resolved from the detached daemon's PATH to a fake built as
`gh.exe`, `FAKEGH_SCENARIO` has to reach that child across each daemon
stop/start, the comment body travels to it on stdin via `--body-file -` and has
to arrive byte-identical, CRLF included, and the argv log it writes is read
back with the platform's own line endings.

## Scripted runs

| Date | Platform | Result | By |
|---|---|---|---|
| 2026-09-16 | macOS (darwin/arm64) | GATE PASS, all seven scenarios | task 068.5 |

A Linux or Windows row is added from a CI run of the step, never predicted.

## Manual leg — real GitHub

This needs a throwaway GitHub repository you can write to, a real `gh`
authenticated against it, and a vincent project whose `origin` is that
repository. It writes to GitHub: it merges, closes, reopens and comments on a
real pull request and re-runs a real Actions job.

1. **Set up the repository.** Add a GitHub Actions workflow with one job that
   always fails and one that takes a few minutes. Protect the base branch with
   a required status check on the slow job and "Require branches to be up to
   date before merging".
2. Push a branch, open a pull request from it, link it to a task
   (`vincent github pr link P --task N`, or the pull-requests takeover), and
   open the task's **Pull Request** tab.
3. Press `m` while the required check is still running. The merge is refused
   `checks_running`, and nothing is merged on github.com.
4. Push a commit to the base branch, and press `m` once the check has passed.
   The merge is refused `branch_behind`.
5. Update the branch. On the failed Actions row, press `ctrl+r` and confirm.
   The failed job re-runs on github.com. The key is not offered on a
   third-party check row or a legacy status row, if the repository has one.
6. Press `i`, type a comment of several lines, and post it with `ctrl+s`. It
   appears on the pull request, line breaks intact.
7. Press `X` and confirm: the pull request is closed on github.com. Press `X`
   again and confirm: it is open again.
8. Open the merge popup with `m`, and before confirming push a commit to the
   pull request's branch from elsewhere, and wait for the tab's next refetch.
   The popup says the head moved, and `y` does nothing.
9. On a green head, press `m`, choose squash with `←`/`→` and confirm with
   `y`. The pull request is
   merged on github.com, and its branch is **not** deleted.
10. **The token leg.** Take `gh` off the daemon's PATH, set `GITHUB_TOKEN` to a
    token with write access to the repository, restart the daemon, and repeat
    steps 5, 6, 7 and 9 on a fresh pull request. The results are the same,
    over REST.
11. **No write scope.** Restart the daemon with a fine-grained token that can
    only read the repository. The tab and its checks still load, and each of
    merge, close, comment and re-run is refused `no_write_scope`.

| Date | Platform | gh version | Walked by | Result |
|---|---|---|---|---|
| — | — | — | — | not yet walked |

Add a row per walk.
