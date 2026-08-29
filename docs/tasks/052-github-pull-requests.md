# 052 — List a GitHub project's open pull requests and link them to board tasks

**Status:** ✅ done (7/7)
**Spec:** amends §3 (decision record row 27, row 26 narrowed), §12.3, §13.2,
§13.3, §14, §20
**Issue:** [#231](https://github.com/lezli01/vincent/issues/231),
[#242](https://github.com/lezli01/vincent/issues/242) (052.6, 052.7)

## Problem

vincent can create a task *from* a GitHub issue (task 035) but knows nothing
about pull requests, so the delivery half of the loop is invisible: once a
workflow's delivery step has pushed a branch and opened a PR, finding out
whether that PR exists, is still open, or belongs to the task on the board means
leaving vincent. §20 named pull requests as "the intended next piece" and task
035 decision 10 left the room on purpose.

## Decisions

**1. The link is its own column, migration 0018.** *(2026-08-29)*

`ALTER TABLE tasks ADD COLUMN github_pull_json TEXT`, NULL meaning never
matched, carrying `{repo, number, source, suppressed, linked_at}`.

The issue hoped 0014's `github_issue_json` could absorb this without a second
migration, on the strength of that migration's own comment. It cannot, honestly:
0014 is documented and implemented as "NULL = no linked issue" holding a bare
normalized `Issue`, and a task with a PR and no issue would have to make that
column non-NULL with something that is not an issue. Widening it into an
envelope would leave every existing row in the old shape and force a
shape-sniffing read path forever. One append-only migration is the cheaper
honesty; 0014's comment is amended in place to say the growth room was taken by
a sibling column.

`source` is `auto` or `human` and `suppressed` is the sticky record of a human
unlink. Both exist because "a human unlink is not re-applied by the next
listing" needs three states, not two: never matched, linked, and
matched-but-refused. Absence of a link cannot carry the third.

**035 decision 5 is not reversed.** "GitHub-based" stays a derived fact — the
`origin` remote parsed at the point of use — and no `github_repo` column is
added to `projects`. Decision 5 named PR checking as the *expected trigger* to
revisit storing repo identity; the revisit happened and landed on the task,
where the number already had to go. Projects whose GitHub remote is an SSH alias
or is not named `origin` remain out of scope, exactly as for issues today.

**2. Linking is a daemon-side reconciler, not a side effect of a GET.**
*(2026-08-29)*

The listing endpoint stays pure: it fetches, normalizes, sorts and returns, and
persists nothing. Reconciliation is a background subsystem wired in
`internal/daemon/daemon.go:Run` beside the scheduler and the notifier, modelled
on `internal/notify`'s posture — reads `currentConfig()` per tick so a hot
reload reaches the next one, bounded work, its own logging.

This is the one place the built thing diverges from the issue as drafted. The
issue proposed "fetch on view open plus a manual refresh key"; that leaves the
link existing only for projects a human happened to open, and section 1's
"nothing is persisted" contradicted section 2's "the daemon writes the link"
outright. A GET that mutates rows was the other way to reconcile them, and was
rejected: every other write in this API is a POST, and a link that only appears
when someone looks is not a durable link.

Per tick, per project, the reconciler runs the §13.2 gate first and stops at the
first "no"; lists open PRs and matches `HeadBranch` against `tasks.branch_name`
within that project; writes a link only where none exists and `suppressed` is
false, marking it `auto`; and publishes `task.github_pull_changed` so a running
TUI re-renders. It never overwrites a `human` link, never clears one, and never
un-suppresses.

The interval is `github.poll_interval`, defaulted to 5m and disableable with
`0`. Standing background network traffic is new for this daemon and the user
must be able to turn it off without turning the whole integration off.

**3. A merged PR is fetched one at a time.** *(2026-08-29)*

`internal/github` grows `GetPull(repo, number)` beside `Get`, on both legs
(`gh pr view <n> --json …`, `GET /repos/{owner}/{name}/pulls/{n}`), behind
`GET /v1/tasks/{id}/github/pull`.

The listing is open-only, so it cannot answer for a PR that has merged — and a
merged PR is precisely the case the durable link exists to serve. Rendering the
stored number alone was rejected for the reason the issue rejects snapshotting:
a merged, closed or renamed PR would read exactly like an open one. Listing
every state and filtering client-side was rejected because it pulls a
repository's whole PR history to answer a question about one row.

Nothing about the PR beyond the link is ever stored. `gh` spells a third state
`MERGED` and the REST API does not spell it at all, so both legs fold it onto
`state: closed` plus `merged: true` — one shape, whichever leg answered.

**4. The compare URL is built, never fetched; row 11 stands.** *(2026-08-29)*

"Create a PR" is offered for a task with a branch and no linked PR, as
`compare_url` on `GET /v1/tasks/{id}/github/pull`: the task's title and
description plus `Closes #N` when the task carries an issue snapshot **from the
same repository**, escaped into
`https://github.com/{owner}/{name}/compare/{base}...{head}?expand=1`.

**Decision record row 11 stands unamended.** vincent still pushes nothing, opens
nothing and merges nothing; pushing the branch remains the workflow's job, and
what row 11 forbids is hardcoded delivery *behavior*, not a link a human clicks.
The prefill is the same kind of guess 035 decision 2 already sanctions for task
creation — visible before it is used, and fully editable. The URL is produced by
string construction alone: **no request is made to GitHub when it is built**,
which is asserted rather than left to inspection, in
`TestCompareURLMakesNoRequest` and `TestTaskPullOffersACompareURL`.

`internal/github`'s read-only posture is likewise unamended: nothing here adds a
write method, a `POST`, or a mutating `gh` subcommand.

**5. The human link and unlink make no GitHub call.** *(2026-08-29)*

`POST /v1/tasks/{id}/github/pull` writes vincent's own column and does not check
the number exists. Validating it would put a network failure in the way of a
human correcting vincent, and the number is rendered live on every read anyway:
a wrong one shows as `not_found` the moment it is displayed. `DELETE` marks the
link suppressed rather than clearing it, keeping repo and number — the
reconciler has to be able to read the refusal.

**6. The offer to create is in the workspace; link and unlink are in the
takeover.** *(2026-08-29, #242)*

The takeover lists what GitHub says is open. A task with a branch and no pull
request is not a row in that list, so hanging "create" off it would mean either
mixing vincent rows into a GitHub listing or hiding the offer behind a key with
no visible row; the offer belongs beside the branch it is about, which is the
workspace. Link and unlink go the other way for the mirror-image reason: they
are the two actions that write vincent's own column, and they belong on the one
screen that can see a pull request no task claims — the case they exist for.
Spec §15's view 2 is narrowed in the same change: the pull-request *section* may
open a URL, which is not a write and leaves "read-only inspector" true in the
sense it was written.

`enter` on an unclaimed takeover row is deliberately inert rather than
overloaded into "link this one". The link key is its own, and a key that means
two unrelated things depending on the row is worse than one that sometimes does
nothing. The link picker is scoped to the row's own project: `POST` takes a bare
number and the repo comes from the task's project daemon-side, so a task from
elsewhere would silently link a different repository's number.

**7. The prefill is edited in the TUI and the URL re-encoded client-side.**
*(2026-08-29, #242)*

Decision 4 requires the prefill be visible and editable before it is used, and
`compare_url` arrives with title and body already encoded as query parameters.
So the TUI decodes them into a popup's editable rows, in the shape the repair
and follow-up popups already take, and rebuilds the URL with `net/url` on the
way out — preserving `expand=1` and the daemon's compare path. Handing GitHub's
own page the unedited URL was considered and rejected: it satisfies the letter
of "editable before submit" only by relying on a surface vincent does not own.
`TestCreatePRFormMakesNoRequest` asserts the whole path contacts nothing,
mirroring `TestCompareURLMakesNoRequest`; row 11 still stands.

**8. Availability is derived per connect, and the nav row is withheld until it
says yes.** *(2026-08-29, #242)*

There is no stored notion of a GitHub project (035 decision 5), so the TUI
issues one §13.2 probe per registered project as the connection comes up,
concurrently, and again on reconnect. While every answer is unavailable —
including while they are all still in flight — the pull-requests nav row and the
workspace's two pull-request keys are withheld from the palette, the `?` overlay
and the footer alike. This is task 054 decision 5's `fold` precedent applied to
a nav row. The filter goes at the root rather than in `shell.liveBindings`,
because that seam is shell-scoped and the nav rows are global.

**9. The gate ships as a script and a record; `ci.yml` is the maintainer's.**
*(2026-08-29, #242)*

`scripts/052-gate.sh` and `docs/gates/052-github-pull-requests.md` are committed
and runnable locally from day one. `.github/workflows/ci.yml` enumerates its
gate steps by hand and an agent session's token has no `workflow` scope, so it
cannot write that directory by push or API (#120, #122, #125) — the step to add
is given in the pull request instead. Task-numbered rather than `m10` because
this is not a §19 milestone, following 017 and 032.

## Sub-tasks

| ID | Task | Status |
|---|---|---|
| 052.1 | `internal/github`: `PullRequest`, `PullLink`, `ListPulls`/`GetPull` on both legs, `CompareURL`, `PullURL`, fixtures | [x] |
| 052.2 | `internal/store`: migration 0018, `Task.GitHubPull`, `LinkCandidates`, `SetTaskGitHubPull`, `task.github_pull_changed` | [x] |
| 052.3 | `internal/config`: `github.poll_interval`; `internal/daemon`: the reconciler, wired in `Run` | [x] |
| 052.4 | `internal/api` + `internal/apiclient`: the four routes, `github_pull` on the task DTO | [x] |
| 052.5 | `internal/cli`: `vincent github prs`; `cmd/fakegh`: `pr list` and `pr view` | [x] |
| 052.6 | `internal/tui`: `viewPullRequests` takeover, the PR block in the task workspace, the create-PR editor | [x] |
| 052.7 | The browser opener (`_unix.go`/`_darwin.go`/`_windows.go`), failing visibly; a gate script for the listing | [x] |

## What was split off, and why

052.6 and 052.7 are the issue's sections 3 and 4 — the TUI takeover and the
browser opener. The brief named section 4 as the seam if the work needed
splitting, on the grounds that sections 1, 2, 3 and 5 are a coherent shippable
piece on their own; in the event the TUI went with it, because a takeover that
cannot open a row in a browser is half a screen and the two are better built
together.

What shipped is complete and usable without them: the listing, the link, the
merged case and the compare URL are all reachable over the API and from
`vincent github prs`, and the reconciler runs whether or not anything is
watching. The `compare_url` field is served today and is what 052.6's create-PR
editor will hand to 052.7's opener, so neither is a rewrite of what is here.
