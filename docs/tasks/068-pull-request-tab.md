# 068 — Pull request tab on the task workspace, with checks and PR actions

**Status:** 🔄 in progress (4/5) · **Issue:**
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
manual refresh key. *Superseded in part by task 093 decision 4 (noted
2026-09-14, issue #372):* the tab has no refresh key — `r` is retry and `R`
repair on every tab of the workspace, and the poll already re-reads. Never per
render. `CheckRollup` names the **ref** it is
about, because a pull request that gains a push while a fetch is in flight has
checks belonging to the previous head, and rendering them under the new one
would show a green build for code nobody ran.

## Sub-tasks

| ID | What | Status |
|---|---|---|
| 068.1 | `CheckRun` and `CheckRollup` in `internal/github`, produced identically by both legs, with the Actions provenance decision 3 needs | ✅ done |
| 068.2 | `GET /v1/tasks/{id}/github/pull/checks`, its `internal/apiclient` type and its MCP tool | ✅ done |
| 068.3 | The Pull Request tab: conditional presence, the cycle that skips it, the check rows, open-check, refresh, and unlink's second home. *Task 093 moved open-check from `c` to `enter` and removed refresh (noted 2026-09-14, issue #372)* | ✅ done |
| 068.4 | The write leg (`gh pr merge`/`close`/`reopen`/`comment`, `gh run rerun --failed`; `PUT /pulls/{n}/merge`, `PATCH /pulls/{n}`, `POST /issues/{n}/comments`, `POST /actions/runs/{id}/rerun-failed-jobs`), its new reason cases, the write routes and the tab's confirmed actions. **Row 11 is rewritten here**, with its three reaffirmations amended in the same pull request (tracked in [#386](https://github.com/lezli01/vincent/issues/386) and [#387](https://github.com/lezli01/vincent/issues/387), 2026-09-13). *Delivered in two pull requests:* the daemon half — `internal/github` writes, the five routes, `internal/apiclient`, `vincent github pr` subcommands, `cmd/fakegh`'s write subcommands, and row 11 rewritten — landed 2026-09-15 (#386); the tab's confirmed actions landed 2026-09-16 (#387) | ✅ done |
| 068.5 | `scripts/068-gate.sh` and `docs/gates/068-*.md`, `cmd/fakegh`'s write subcommands, the re-captured `docs/assets/tui-*.png`, and the derived documentation for the write surface (tracked in [#388](https://github.com/lezli01/vincent/issues/388), 2026-09-13) | ☐ open |

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

  > **Superseded 2026-09-15 (#386).** There are no such tools: all five write
  > routes are in `mcp.Excluded`, under task 069 decision 3's wording. "The
  > keypress is the consent" only holds while a human presses it, and
  > `mcp.wire_steps` defaults to true, so a tool would put these writes on the
  > step path that decision 1 says nothing reaches. That rule wins over this
  > bullet. Nothing is lost: a step's agent can run `gh pr merge` in its own
  > worktree, which is row 11's original path. The "a rejected confirmation
  > sends nothing" test above belongs to the TUI half (#387).

## 068.4, daemon half (#386, 2026-09-15)

Settled with the author before the work, and binding:

1. **The write routes are excluded from MCP** (above). Spec §13.4 carries the
   amendment and the new count, and `internal/api/mcp_parity_test.go` names the
   five routes.
2. **CLI subcommands ship with the routes**, under `vincent github pr`:
   `merge` (`--task`, `--method`, `--head-sha`), `close`, `reopen`, `comment`
   (`--body` or `--body-file -`) and `rerun` (`--task`, `--run-id`). `merge`
   requires `--method` and `--head-sha`: the CLI has no confirmation popup, so
   the flags are where the human names exactly what is sent (decision 4).
3. **Merge refusals come from a preflight read, and the merge is pinned to a
   head SHA.** `mergeStateStatus` (`gh`) or `mergeable_state` (REST), plus the
   live rollup when blocked: `BEHIND` → `branch_behind`; `BLOCKED` with an
   unfinished check → `checks_running`; `BLOCKED` otherwise, `DIRTY`, `DRAFT`,
   closed or merged → `not_mergeable`; `CLEAN`, `UNSTABLE`, `HAS_HOOKS` and
   `UNKNOWN` → send. A head that is not the confirmed `head_sha` is
   `head_changed`, and the send carries the pin (`--match-head-commit`, REST
   `sha`) so a push between preflight and merge is refused rather than merged
   (decision 6). GitHub's own refusal text is a fallback that reaches only the
   daemon log. The merge state is **not** a `PullRequest` field: the REST
   listing cannot fill it, which is 068.1's reasoning about a workflow name.
4. **A 403 on any write is `no_write_scope`, `CreatePull` included.**
   `forbidden` stays the read side's reason. This changes task 069's fallback
   reason, so `scripts/069-gate.sh` scenario 4, its gate record and the 069
   record carry dated notes. A 404 is never reinterpreted as missing scope.

Decided without asking, as conventional or already forced by a record: every
route refuses 409 `pull_not_linked` without a live link and runs the §13.2
gate first; merge, close and reopen answer the pull request re-read after the
write, comment its URL, re-run its run id, and no event is published. A re-run
is validated against the live rollup — only a failed, Actions-backed row's run
— so it cannot re-run an arbitrary run in the repository. There is no
task-state guard and no idempotency key (069 decisions 4 and 7). Never
`--delete-branch`, `--auto` or `--admin`.

## 068.4, TUI half (#387, 2026-09-16)

Settled with the author before the work, and binding:

1. **Keys: `m` merge · `X` close/reopen · `i` comment · `ctrl+r` re-run the
   failed jobs.** All four are `ctxTaskPull` rows, `github: true`, with no
   vocabulary term — they are surface-local. The §6 letters `p a x r E R s c A
   F` do not move (task 093 decision 1), which rules out `c`, `x` and `r`. `X`
   is one key for close and reopen, whichever the state allows, on `p`'s
   pause/resume precedent. `ctrl+r` carries a modifier because `r` is retry,
   and that also makes it the hardest of the four to hit by accident. `X`
   (triggers poll), `i` (workflows form, daemon status line) and `ctrl+r`
   (chat detail) carry no term on the surfaces that already use them, and none
   of those can be open beside a task workspace, so the three vocabulary tests
   pass unchanged with no new exception.
2. **The merge popup opens with no method selected.** merge / squash / rebase
   are listed with none chosen, and `y` does nothing until `←`/`→` picks one;
   `n` and `esc` close it and send nothing. This is decision 4 applied to the
   popup: the wire has no default and the CLI's `--method` is required.

Taken without asking, because each is conventional or forced by a record:

- **Every write is absent, not present-and-refusing,** where it cannot apply —
  decision 3 extended from re-run to all four. `m` needs an open, non-draft
  pull request with a known head; `X` is `close` on an open one (drafts
  included), `reopen` on a closed unmerged one, absent on a merged one; `i`
  needs any fetched pull request; `ctrl+r` needs the selected row to be both
  Actions-backed and failed. All four are absent when the pull row carries a
  reason instead of a pull request. The hint line, the footer, the palette and
  the key handler read the same predicates (`taskView.liveBindings`). `m` is
  not hidden on a blocked, behind or failing pull request: the merge state is
  not on the row, and the daemon's preflight answers with a named reason.
- **The merge pin follows the check rollup.** The popup names `repo#number`,
  the title, head → base, the short head commit and the rollup's state; the
  head it shows and sends is the rollup's `Ref` (decision 6), the pull row's
  `HeadSHA` only while no rollup has loaded. When both are known and differ
  the popup says the head moved and `y` stays inert until a refetch makes them
  agree. `enter` never confirms.
- **Close, reopen and re-run confirm inline** with a y/n that names the
  consequence; the re-run prompt lists every failed row of the run it acts on.
  Any key but `y` declines.
- **The comment popup is its own confirmation** (task 069 decision 2's
  reasoning): typed inline or in `$EDITOR`, posted by `ctrl+s`, discarded by
  `esc`; a blank body is refused in the client and paste lands in the body.
- **In-flight writes cannot be sent twice** (task 069 decision 7): each write's
  key, and the comment's `ctrl+s`, are refused until the answer lands.
- **Popups and prompts own the keyboard and the footer** (`ctxPullMerge`,
  `ctxPullComment`, `ctxPullConfirm`).
- **After a reply the tab refetches** what it changed — the daemon publishes no
  event for these writes — and the note line says what happened, or carries the
  daemon's 409 message verbatim.
- No task-state guard, and nothing offers `--delete-branch`, `--auto`,
  `--admin`, or a merge anywhere but this tab.

The gate, its walkthrough, the re-captured screenshots and the rest of the
derived documentation are 068.5 (#388).

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
