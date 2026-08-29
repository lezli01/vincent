# Task 052 gate — GitHub pull requests

**Acceptance (task 052.7):** a real daemon, over curl alone, lists a GitHub
project's open pull requests, links one to the task whose branch heads it,
lets a human override that link and remove it, and does not undo the removal
on the next tick — with the compare URL produced without a single call to
GitHub.

The scripted half is [`scripts/052-gate.sh`](../../scripts/052-gate.sh). It is
task-numbered rather than `m10` because this is not a §19 milestone; 017 and
032 set that precedent.

```sh
./scripts/052-gate.sh                            # all five scenarios
VINCENT_GATE_SCENARIO=4 ./scripts/052-gate.sh    # one, for debugging
```

It needs bash, go, git, curl and jq, and nothing else: `cmd/fakegh` is built
**as `gh`** onto the daemon's PATH, which is how the daemon finds the real CLI
too (there is no `gh_path` config key — `GHPath` empty means "resolve `gh`
from PATH"). No agent CLI is involved: the gate's one workflow is a single
`command` step whose whole body is `exit 0`, so it is portable to the daemon's
pwsh on Windows and settles in seconds. What the gate wants from it is the
branch the worktree was made on, which is what a pull request's head is
matched against.

## What the script asserts

1. **The capability probe and the open-only listing.** `GET
   /v1/projects/{id}/github` answers `available: true` for a repository whose
   `origin` is `github.com` — which is the whole of what makes a project a
   GitHub project, since §13.2 deliberately stores no flag. `GET
   …/github/pulls` then returns exactly the corpus's two open rows, newest
   first, with the draft among them and the **merged** row absent: an
   open-only listing can never answer for a merged pull request, which is what
   the durable link exists to serve through the task route instead.
2. **The reconciler's auto-link.** With `github.poll_interval: 1s` and the
   fake's first pull request pointed at a real task's branch
   (`FAKEGH_PR_BRANCH`), the link appears on `GET /v1/tasks/{id}/github/pull`
   with `source: auto` and the live pull request beside it — and the project
   listing agrees about which task claims the row.
3. **The human link.** `POST /v1/tasks/{id}/github/pull` with a different
   number takes, and comes back as `source: human`.
4. **The sticky unlink.** `DELETE` clears the link, and several reconciler
   ticks later it is still gone and still `suppressed: true`. This is the one
   assertion a UI cannot make on its own behalf, and the reason the
   confirmation in the TUI says the refusal sticks.
5. **The compare URL is built, never fetched.** A task with a branch and no
   link carries a `compare_url` on a github.com compare page, and the fake
   `gh`'s argv log — every call the daemon actually made — contains nothing
   about it and no `pr create`. Decision record row 11 stands: vincent pushes
   nothing, opens nothing and merges nothing.

## What the script does not assert

The TUI. The takeover, the workspace section and the compare-URL editor are
covered by `internal/tui`'s own tests, including a `*live_test.go` against the
real API handlers and the same `cmd/fakegh`; what a screen *looks like* is a
judgement, the way M3's and 017's are, and this gate deliberately stops at the
API. The browser opener is likewise not exercised here — a gate that launched
a browser on CI would be a gate nobody could run.

## Not wired into CI

`.github/workflows/ci.yml` enumerates its gate steps by hand, and the session
that wrote this gate could not edit `.github/workflows/` — an agent session's
token has no `workflow` scope, by push or by API (#120, #122, #125). The step
to add to the `gates` job is:

```yaml
      - name: 052 gate (GitHub pull requests)
        run: ./scripts/052-gate.sh
        shell: bash
```

Until that lands, **this gate is not known to pass on Windows**: it has only
been run on macOS. That is the exact failure mode CLAUDE.md records for m6, m7
and m8, where wiring them in turned up two Windows-only faults in a script
that had been green on Linux for weeks.

## Runs

| Date | Platform | Result | By |
|---|---|---|---|
| 2026-08-29 | macOS (darwin/arm64) | GATE PASS, all five scenarios | task 052.7 |
