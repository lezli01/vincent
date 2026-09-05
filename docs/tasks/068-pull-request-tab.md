# 068 — Pull request tab on the task workspace, with checks and PR actions

**Status:** 🔄 in progress (3/5) · **Issue:**
[#271](https://github.com/lezli01/vincent/issues/271)
· **Spec:** §2, §5.3, §8.4, §12.3, §13.2, §13.4, §15

A task can have a GitHub pull request linked to it (task 052), and the
workspace tells you almost nothing about it. `internal/tui/taskpull.go`'s
`pullSectionLines` renders eight facts inside the **Task Details** tab, sharing
one scrolling pane with every other task fact, and that is the whole surface.
Two things are missing from it.

**CI state was absent entirely.** Nothing in the tree read a pull request's
checks: `ghPullFields` did not ask for `statusCheckRollup`, `rest.go` called
only `/pulls` and `/issues`, and there was no check-run type anywhere in
`internal/github`. The most common question about a pull request a task just
pushed — did the build pass, and if not which job failed — could not be
answered without leaving vincent.

**Acting on the pull request means leaving vincent.** Every operation is a
browser trip, and unlink was not reachable from the task at all: task 052
decision 6 put link and unlink only in the pull-requests takeover, so a human
looking at a task whose link the reconciler got wrong had to navigate away to a
listing to fix it.

The answer is a sixth full-screen tab, **Pull Request**, appended after
Workflow and bound to `6`, present only when the task has a linked pull request
and `github.enabled` is on. It renders the facts the Task Details section
renders, plus a live per-check rollup for the head commit, and it carries seven
operations: open the pull request, open the selected check's run page, unlink,
merge, close/reopen, re-run the failed Actions jobs, and comment.

> **Superseded in part, 2026-09-05 — [task 088](088-step-details-tab.md).** The
> tab is no longer the sixth and no longer answers to `6`: 088 inserted **Step
> Details** ahead of it, so Pull Request is selected by `7`. Everything else
> 068.3 settled stands, and the property it was protecting is intact — the tab
> is still conditional, still **last** on the strip, and still costs nothing
> when it is absent, because the digits bind to tabs and not to positions and
> Step Details is unconditional. `6` is Step Details either way, and `7` does
> nothing on a task with no linked pull request, exactly as `6` did before.
> What was paid, deliberately, is that `6` changes meaning once for a reader
> who had learned the old number. Spec §15 view 2 records it as a supersession.

Four of those write to GitHub. They are the first writes vincent has ever made
to GitHub, and they are what makes this task large.

## Decisions

**1. Decision record row 11 is rewritten wholesale, not narrowed.**

Row 11 reads "Delivery: owned entirely by workflow steps; no hardcoded
push/PR/merge behavior", and it has been reaffirmed verbatim three times since
— task 052 decision 4 ("vincent still pushes nothing, opens nothing and merges
nothing"), task 064's opening paragraph, and twice in §20's promotion notes.
The issue did not name it; merging a pull request from the TUI contradicts it
head on, and no reading of "hardcoded behavior" makes that go away when the
merge is vincent's own HTTP call.

Row 11 is **obsolete rather than narrowed**: vincent does deliver now,
human-triggered. It is rewritten to say so rather than kept alive with a
step-path-only qualifier. The three reaffirmations take dated amendment notes
in the same pull request as the code that makes them false — a reaffirmation
left standing that the row it points at no longer says is worse than the row
itself being wrong.

This is the decision most likely to be re-litigated later, so the reasoning is
recorded and not just the outcome: **the round trip to a browser is the cost
this issue exists to remove, and a checks view that cannot merge the pull
request whose checks it just showed you is half a sentence.**

The constraint that survives row 11 unchanged, and that is most at risk from a
later "just add a merge step": **nothing on the step path reaches GitHub.**
Every write is human-triggered from the TUI, so §8.4's property that a step
render cannot fail for an external reason is untouched. A `merge` step type is
not a smaller version of this task; it is the thing this decision does not
license.

**2. The full write set ships, sequenced as sub-tasks in one task.**

One document with `068.n` sub-tasks landing over several pull requests, reads
before writes. This is how 052 ran, and it keeps the row 11 supersession in one
document rather than spread across two tasks that each half-argue it.

**3. Re-run is offered only on rows backed by GitHub Actions.**

"Re-run the failed checks" has no honest meaning for a third-party check run
(its own app owns it) or for a legacy commit status, which `statusCheckRollup`
folds in beside check runs on the `gh` leg and which the REST leg reads from
`/status`. The normalized `CheckRun` therefore carries enough provenance to
tell an Actions-backed row from the others, and the key-hint line offers re-run
only when the selected row is one. On the other rows it is **absent, not
present and failing** — a key that is offered and then refuses is the thing the
reason vocabulary exists to avoid at a lower level.

Carrying that provenance is a real cost on the `gh` leg and is part of this
work, not an afterthought: `gh pr view --json statusCheckRollup` must yield the
same answer to "is this an Actions run, and which one" as
`/repos/{o}/{r}/commits/{sha}/check-runs` does. **068.1 settled that by
deriving it from the check's own URL** rather than from either leg's metadata.
The REST leg could read `app.slug`; `gh` has no app field at all, so any rule
built on one leg's metadata would have needed a second, different rule on the
other — and two rules is exactly how the two legs come to disagree. An Actions
check's details URL is `/{owner}/{repo}/actions/runs/{run}/job/{job}` on both
legs, and the host is checked as well as the path, so a third-party service
serving `/actions/runs/1` cannot hand vincent a run id to re-run on github.com.

**4. The merge method is chosen in the confirmation, not in config.**

The confirmation popup that already has to name `repo#number` also picks merge
/ squash / rebase. No `github.merge_method` key is added: §12.3 stays as it is,
and a repository that forbids the chosen method fails in the reason vocabulary
rather than being pre-guessed wrongly by a default. This also keeps the
confirmation honest — it names exactly what will be sent, which is the whole
point of having it.

**5. `github.enabled: false` hides the tab, and the tab's absence is read off
the pull-request row rather than probed (068.3).**

The workspace already fetches `GET /v1/tasks/{id}/github/pull` on every open,
and that route answers 200 with the named reason when the integration is
unusable. Availability is therefore "a live link, and a reason that is not
`disabled`" — computed from a row the workspace has already paid for, rather
than a second probe that could disagree with it.

**6. Checks are live, never snapshotted (068.1).**

For the reason `PullRequest` is a pointer: a stored check result reads exactly
like a current one while being wrong. Fetched on tab open, on
`task.github_pull_changed`, on the tab's own poll while it is open, and on a
manual refresh key. Never per render. `CheckRollup` names the **ref** it is
about, because a pull request that gains a push while a fetch is in flight has
checks belonging to the previous head, and rendering them under the new one
would show a green build for code nobody ran.

## Sub-tasks

| ID | What | Status |
|---|---|---|
| 068.1 | `CheckRun` and `CheckRollup` in `internal/github`, produced identically by both legs, with the Actions provenance decision 3 needs | ✅ done |
| 068.2 | `GET /v1/tasks/{id}/github/pull/checks`, its `internal/apiclient` type and its MCP tool | ✅ done |
| 068.3 | The Pull Request tab: conditional presence, the cycle that skips it, the check rows, open-check, refresh, and unlink's second home | ✅ done |
| 068.4 | The write leg (`gh pr merge`/`close`/`reopen`/`comment`, `gh run rerun --failed`; `PUT /pulls/{n}/merge`, `PATCH /pulls/{n}`, `POST /issues/{n}/comments`, `POST /actions/runs/{id}/rerun-failed-jobs`), its new reason cases, the write routes and the tab's confirmed actions. **Row 11 is rewritten here**, with its three reaffirmations amended in the same pull request | ☐ open |
| 068.5 | `scripts/068-gate.sh` and `docs/gates/068-*.md`, `cmd/fakegh`'s write subcommands, the re-captured `docs/assets/tui-*.png`, and the derived documentation for the write surface | ☐ open |

## What 068.4 must hold

- **vincent stores no credential.** Writes go through the same two legs the
  reads do — `gh` preferred, `GITHUB_TOKEN`/`GH_TOKEN` from the daemon's
  inherited environment as the fallback. §2's secret-management non-goal is
  unchanged.
- **Neither leg's own error text reaches a client** (task 052 decision 1). This
  gets harder with writes, because GitHub's merge failures are the most
  informative strings in the whole integration and the temptation to pass them
  through is real. `reason.go` grows: no write scope, not mergeable, checks
  still running, branch behind. `ReasonForbidden`'s and `ReasonNotFound`'s
  messages both say "issues" today and need widening now that they answer for
  pull requests and checks too.
- **Every write asks first, and a rejected confirmation sends nothing** —
  asserted by a test that fails if a request is made, in the shape
  `TestCompareURLMakesNoRequest` and `TestCreatePRFormMakesNoRequest` already
  take for the read-only paths. This is the single most important test in the
  task: it is what makes "a mistyped key must not merge a pull request" a
  property rather than a hope.
- The MCP tools for the write routes need descriptions that **say they write to
  GitHub**.

## What 068.1–068.3 changed

- `internal/github/check.go`: `CheckRun`, `CheckRollup`, the state vocabulary,
  the rollup fold (failure beats running), the ordering (unfinished, then
  failed, then the rest) and `actionsRunID`.
- `internal/github/gh.go`: `statusCheckRollup` and `headRefOid` added to
  `ghPullFields` — one field list, so `GetPull` and `Checks` cannot drift into
  answering about different heads — plus the `__typename` discrimination
  between a check run and a `StatusContext`.
- `internal/github/rest.go`: `/commits/{sha}/check-runs` and
  `/commits/{sha}/status`, pinned to `per_page=100` because a partial rollup
  reads exactly like a complete one, plus `head.sha` on `restPull`.
- `internal/github/doc.go`: the read-only paragraph is dated rather than
  restated, because it is scheduled to stop being true in 068.4. The leaf
  property and "vincent stores no credential" are restated, because they are
  the ones that did *not* change.
- `internal/api`, `internal/apiclient`, `internal/mcp`: the read route and its
  two clients.
- `internal/tui`: `taskTabPull`, `taskpulltab.go`, `ctxTaskPull` and its rows,
  and `switchTab` walking `tabs()` instead of `taskTabCount` — the modulo was
  the first thing a conditional tab was going to get wrong.

`CheckRun` deliberately carries **no workflow name**, though `gh` reports one:
the REST leg's `check-runs` response has no equivalent, so a field only one leg
could fill would be a difference a client could see. It is display sugar, and
`RunID` is what re-run actually needs.
