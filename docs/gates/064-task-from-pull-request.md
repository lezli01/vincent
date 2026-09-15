# Task 064 gate — a task from a pull request

**Acceptance (task 064.9):** a real daemon, over curl alone, lists pull
requests by state, turns one into a task that runs on the pull request's own
head branch — the link written at creation, the head fetched at admission, a
workflow's push reaching the pull request's branch — runs a fork's pull
request without an upstream, refuses `branch_override` on such a task, and
archives a merged pull request's task without touching its head branch.

The scripted half is [`scripts/064-gate.sh`](../../scripts/064-gate.sh). It is
task-numbered rather than `mN` because this is not a §19 milestone, the way
052's is. It runs in `ci.yml`'s `gates` job on Linux, macOS and Windows.

```sh
./scripts/064-gate.sh                            # all four scenarios
VINCENT_GATE_SCENARIO=2 ./scripts/064-gate.sh    # one, for debugging
```

It needs bash, go, git, curl and jq, and nothing else. GitHub is stood in for
twice over:

- **Its API** is `cmd/fakegh`, built **as `gh`** onto the daemon's PATH, which
  is how the daemon finds the real CLI too. `FAKEGH_PR_BRANCH` moves #412's
  head to `feature/add-a-thing`, off the fake's default
  `vincent/1-add-a-thing` — that is exactly the name the default branch
  template gives task 1 titled "Add a thing", so "the task is on the pull
  request's head branch" would otherwise pass for the wrong reason.
- **Its git side** is a bare repository in the scenario's own temporary
  directory. A project is a GitHub project only because
  `git remote get-url origin` parses as github.com, and `get-url` applies
  `url.*.insteadOf`, so rewriting `origin` would stop the project being one.
  Each project repository therefore keeps two remotes: `origin`, a github.com
  URL that is identity only and is never fetched, and `local`, the bare
  repository, which `main` tracks (`branch.main.remote`). Admission fetches a
  pull request's head from the base branch's upstream, and task 056's base
  fetch reads the same upstream, so nothing in the gate touches the network.

The bare repository is seeded with `main`, `refs/heads/feature/add-a-thing`
one commit past it (#412), `refs/pull/355/head` one commit past it and on no
branch (the fork, #355), and `refs/heads/vincent/3-ship-the-thing` at `main`'s
own commit (the merged #377). Every head is pushed from a bare commit object,
so no local branch of any head name exists when a task is admitted. Each
project repository sets its own `user.name`, `user.email`,
`commit.gpgsign false` and `push.default simple`, because the daemon inherits
the invoking user's global git config.

No agent CLI is involved. Every workflow is `command` steps whose whole body is
`exit 0`, `git commit --allow-empty -m gate-commit` or `git push` — the
intersection of `/bin/sh` and the daemon's pwsh on Windows (§8.3).

## What the script asserts

Every task is created with `POST /v1/tasks` naming `github_pull` and no title
or description, so the prefill is what fills them in.

1. **The listing's state filter** (decision 9). `GET
   /v1/projects/{id}/github/pulls` returns `412 401 355`, newest first — the
   fork row is open, so it is in the default listing. `?state=closed` returns
   exactly `377`, with `merged: true`, and `?state=all` returns
   `412 401 377 355`.
2. **A same-repository pull request becomes a task on its head branch**
   (decisions 1, 2, 6, 7, 8, 10). The create response has
   `branch_name: feature/add-a-thing`, a title starting `#412`, and a
   `github_pull` of number 412, `source: human`, `branch: true` and no fork.
   `GET /v1/tasks/{id}/github/pull` says `linked: true, source: human`
   straight away — the default `poll_interval` is five minutes and a
   reconciler's link would say `auto`, so this is the link written at
   creation — and the project listing's #412 row carries the task's id. The
   workflow commits and then runs a bare `git push`. Once the task is `done`:
   `base_sha` is the head the gate pushed, the worktree is on
   `feature/add-a-thing`, the branch's upstream remote is `local`, the
   worktree's `HEAD` is one commit past the head, and the bare repository's
   `refs/heads/feature/add-a-thing` is that commit — the task's commit reached
   the pull request's branch through the upstream admission configured.
   `POST /v1/tasks/{id}/retry {"branch_override": "elsewhere"}` then answers
   **409** `invalid_state` with `details.branch: feature/add-a-thing`, and
   `branch_name` is unchanged. `details.branch` rather than `details.state` is
   what proves it is the pull-request refusal and not the generic "needs a
   blocked task" one.
3. **A fork pull request runs, with no upstream and no remote added**
   (decision 5). The task from #355 is on `typo-fix` with `fork: true` and
   `branch: true`. Once `done`, `base_sha` is the commit at
   `refs/pull/355/head`, the worktree is on `typo-fix`,
   `git config --get branch.typo-fix.remote` exits 1, and `git remote` still
   lists exactly `local` and `origin`. No push is attempted: with no upstream,
   what git does depends on the user's global `push.autoSetupRemote` and falls
   through to `origin`, a github.com URL — so "cannot push back" is asserted as
   the absence of upstream configuration, not as a failed push.
4. **Archive never touches a branch vincent did not cut** (decision 3). With
   `delete_empty_branch_on_archive` and `delete_remote_branch_on_archive` both
   on, the task from the merged #377 — whose head has no commits past its base,
   exactly the case both legs fire on for a branch vincent cut — is archived
   once `done`. The response's `branch.result` is `not_ours` with no remote leg
   reporting `deleted`, `vincent/3-ship-the-thing` still resolves in the
   project repository and in the bare repository, and the task's worktree
   directory is gone.

## What the script does not assert

- The three admission blocks — `pull_fetch_failed`, `pull_branch_diverged`,
  `pull_branch_checked_out` — and the fast-forward of a stale local head
  branch. They are covered against real repositories by
  `internal/worktree/pull_test.go`.
- The in-transaction "one active task per pull request" `400`, covered by the
  API tests.
- The TUI hand-off: `c` and the `s` state cycle on the pull-requests takeover,
  covered by `internal/tui`'s tests.
- `vincent task add --github-pull`. The gate stops at the API, as 052's does.
- Real GitHub. That is the manual walk below.

## The manual walk against real GitHub

Only a human can walk this: it needs a real repository on github.com, a
logged-in `gh`, a fork owned by a second account, and a person to judge the
TUI. Use a throwaway repository.

1. **A same-repository pull request reaches the pull request.** Open a pull
   request from a branch of the repository itself. In the TUI's pull-requests
   takeover, select it, press `c`, and confirm the new-task form's title
   starts with its `#N` and its description is the pull request's body. Run it
   under a workflow whose last step is `git push`. On github.com, the task's
   commit appears on the pull request.
2. **A fork pull request runs and cannot push back.** Open a pull request from
   the second account's fork. Create a task from it the same way. The task
   runs on the fork's head branch; the project repository gains no remote and
   the branch no upstream; a `git push` step fails rather than pushing
   anywhere.
3. **A merged pull request's head survives archive.** With both
   `delete_empty_branch_on_archive` and `delete_remote_branch_on_archive` on,
   create a task from a merged pull request whose head branch still exists on
   github.com, let it finish, and archive it. The archive reports `not_ours`,
   and the head branch still exists both locally and on github.com.
4. **The state cycle.** On the takeover, `s` cycles open → closed → all, and
   the merged pull request from step 3 is listed under closed and all only.

## Runs

| Date | Platform | Result | By |
|---|---|---|---|
| 2026-09-15 | macOS (darwin/arm64) | GATE PASS, all four scenarios | task 064.9 (#382) |

The manual walk against real GitHub has not been walked.
