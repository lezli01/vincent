# 056 — Fetch the base branch before creating a task worktree

**Status:** ✅ done (1/1)
**Spec:** amends §10 (creation fetches; `--no-track`; the empty-branch check's
revision), §5.3 (`base_sha`), §5.2 (`default_branch` is detected once), §12.3
(`fetch_base_branch`), §14 (the column)
**Issue:** [#229](https://github.com/lezli01/vincent/issues/229)

## Problem

Every task started from a stale base branch, and nothing in vincent ever
refreshed it. There was no `git fetch` anywhere in the tree: `Manager.create`
branched from a purely local ref, so a task built on whatever the local base
happened to be the last time the human ran `git pull` in their own checkout. On
a daemon that runs for days over projects that keep receiving merged pull
requests, that is arbitrarily stale — the agent writes against code that has
already moved, and the divergence surfaces later as merge conflicts or as work
redoing something already merged.

Nothing in the product surface hinted at it either: `default_branch` was
documented as "auto-detected from `origin/HEAD`", which reads as if the remote
is consulted at run time. It is consulted once, at project registration.

## Decisions

**1. The fetch goes in `Manager.create`, at first admission.** *(2026-08-29)*

Immediately before `git worktree add`, inside the per-repository lock `create`
already holds. The issue called this "task creation, not the step path" and
cited §26; the location is right and the reasoning is right, but the label was
not. Worktrees are built by `Runner.ensureWorktree` when the scheduler **first
admits** the task, not in the `POST /v1/tasks` handler — and §10 already calls
that moment "creation". §26's rule is untouched either way: admission is outside
the step path, so no step can fail for a network reason. Admission is also the
better of the two moments, since a task can sit `queued` behind the §11 caps for
hours and fetching then is strictly fresher.

`POST /v1/tasks`'s base-branch validation is **not** touched. It still 400s on a
`base_branch` with no local branch, §10's fail-fast rule stands, and task
creation stays entirely offline. A base that exists only on the remote is not a
case this serves; the user fetches it once in their own checkout.

**2. The remote comes from the base branch's own config, never from `origin`.**
*(2026-08-29)*

`branch.<base>.remote` plus `branch.<base>.merge`, resolved through
`Manager.branchUpstream` — the resolver [008](008-archive-branch-cleanup.md)
wrote for exactly this question, whose comment already refuses to guess a remote
name from a local one. That buys the issue's three fallback cases for free
rather than as special cases: a repository with no remote, a branch that exists
only locally, and a `fan_out` child whose base is its parent's branch (§7.6) all
resolve to "no upstream", which means no fetch, a log line, and today's
behaviour exactly. It is also correct where `<remote>/<base>` is not — a local
`master` tracking `refs/heads/main` upstream fetches the right ref.

The start point is then `git rev-parse FETCH_HEAD`, taken under the same lock,
and the worktree is created from that **SHA**. Branching from a resolved commit
does not depend on the remote's refspec config, does not require a
remote-tracking ref to exist, cannot be re-pointed by a later fetch, and yields
the base SHA decision 4 needs without a second call. The one exposure is a
user's own concurrent `git fetch` in the same repository rewriting `FETCH_HEAD`
between the two calls; the window is microseconds and the failure mode is a task
based on a different upstream commit, not corruption.

**3. `--no-track` is mandatory.** *(2026-08-29)*

Verified on git 2.50.1: `git worktree add <t> -b vincent/1-x origin/master` sets
`branch.vincent/1-x.remote=origin` and `branch.vincent/1-x.merge=refs/heads/master`
under the default `branch.autoSetupMerge`. Two live code paths read those keys
off the task branch:

- `DeleteEmptyBranch`'s remote leg runs `git push --delete <remote> <merge-ref>`.
  On an attended archive of a task that wrote nothing, with
  `delete_remote_branch_on_archive` on, that is
  `git push --delete origin refs/heads/master` — **deleting the project's default
  branch on the forge.** Unrecoverable and outward-facing, which is the exact
  hazard [008](008-archive-branch-cleanup.md) put guard rails around.
- A `fan_out` child inheriting a parent branch that carried an upstream would have
  that upstream fetched and would be based off `origin/master` instead of its
  parent's branch, silently unmaking §7.6's inheritance.

Branching from a raw SHA already avoids both; `--no-track` is passed anyway, and
unconditionally — including when the start point is a local branch name, which
`autoSetupMerge = always` was always able to give an upstream. A test asserting
the task branch carries no upstream config after creation is the one thing
standing between this change and a deleted `master`.

**4. The resolved base SHA is recorded on the task.** *(2026-08-29)*

Once the task branch starts from the remote tip, `base_branch` is a name that no
longer resolves to where the task started, and two consumers read it as if it
did:

- `GET /v1/tasks/{id}/diff` runs `merge-base(base_branch, HEAD)`. With the local
  base an ancestor of the remote tip, the merge-base is the *local* base, so the
  diff a reviewer reads contains every upstream commit the fetch brought in,
  presented as the task's work.
- [008](008-archive-branch-cleanup.md)'s `branchIsEmpty` runs
  `rev-list -n 1 refs/heads/<base>..refs/heads/<branch>`. A task that wrote
  nothing is ahead of the stale local base, so the answer flips to `has_commits`
  and `delete_empty_branch_on_archive` silently stops firing.

A new nullable `base_sha` column (migration `0019_base_sha.sql`), written where
`worktree_path` is written — inside `ensureWorktree`'s claim callback, through an
extended `SetTaskProgress`. Both consumers use it when present and fall back to
the branch name when it is NULL, which is every task that predates the migration
and every task created with `fetch_base_branch: false`. It is the only option
that stays correct as the remote ref keeps moving under later fetches, and unlike
a remote-tracking ref it exists for a `fan_out` lane.

The column is deliberately **not** added to the task DTO in `internal/api`:
nothing in the TUI or CLI has a question it answers, and §13.2 stays unchanged.

**5. `git branch -D`, in one narrow case only.** *(2026-08-29)*

Not anticipated by the plan, and found by decision 4's own test. Fixing
`branchIsEmpty` is necessary but not sufficient: `git branch -d`'s own check is
"merged into HEAD, or into this branch's upstream", and HEAD in the project
repository *is* the human's local base branch — behind the remote in precisely
the situation this task exists for. A task branch cut from a fetched tip that
wrote nothing is still ahead of local HEAD, so `-d` refuses it and
`delete_empty_branch_on_archive` stops firing anyway, one step further along.

So the delete is `-D` **when, and only when, a recorded `base_sha` let the
`rev-list` prove emptiness against the right commit.** That is not the weakening
it looks like: `-d`'s merged check was described in 008 as "a second belt behind
the rev-list", and here it is approximating the question against the wrong
commit while the rev-list asks it against the right one. The guard that actually
matters is untouched — git refuses to delete a branch checked out in any worktree
under either flag, which a test pins. With no recorded SHA, nothing has been
proved against the right commit and `-d` stays, exactly as 008 wrote it.

**6. One global key; the per-project override is deferred.** *(2026-08-29)*

`fetch_base_branch`, top level beside `branch_template` and the `delete_*` pair,
defaulting to **true**, read per worktree creation so a hot reload reaches the
next admission. Default-on outbound traffic needs no separate argument:
`github.enabled` already defaults true and §26 settled that posture; a fetch
reads.

The issue asked for "the same shape `branch_template` uses" — a migration, a
nullable `projects` column, `POST`/`PATCH` fields, `apiclient`, the TUI project
form and renderer, a CLI flag and a docs page: roughly half the pull request, for
an escape hatch the global key already provides. It becomes its own task if a
real repository needs per-project granularity. This is a deliberate narrowing of
the issue's stated scope, recorded here rather than done quietly.

## What the tests prove

Hermetic unit tests throughout — `internal/testrepo` already builds a repository
with a real bare remote, so none of this needs a network or a fake remote server.
`internal/worktree/fetch_test.go` covers the task branch landing on the remote
tip; the local base being byte-identical afterwards, dirty working tree included;
the task branch carrying no upstream config under `autoSetupMerge = always`, with
and without a fetch; a repository with no remote and a base with no upstream; an
unreachable remote falling back inside `RemoteTimeout`; `fetch_base_branch: false`
reproducing the old behaviour with no `base_sha`; and both halves of decision 4's
archive regression, including the NULL fallback. `internal/api` pins the diff
regression, again with the NULL control. `internal/taskrun` wires it through the
engine: a `fan_out` parent records the fetched tip, and its lane fetches nothing
and forks from the parent's branch.

No new gate script. The behaviour is observable from unit tests, and the gates'
`run:` bodies are held to the sh∩pwsh intersection, which makes a network-remote
scenario expensive to express for no additional proof.
