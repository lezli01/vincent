# 102 — `vincent github pr link`, `unlink`, `show` and `checks`

**Status:** ✅ done (3/3)
**Issue:** [#391](https://github.com/lezli01/vincent/issues/391)
**Spec:** amends §12.1

## Problem

The daemon side of a task's pull request was finished and the CLI had none of
it. `GET`/`POST`/`DELETE /v1/tasks/{id}/github/pull` and
`GET /v1/tasks/{id}/github/pull/checks` existed, `internal/apiclient` had a
typed method for each, the MCP server exposed all four as tools, and the TUI
called all four — but `vincent github` had only `issues`, `prs`, `status` and
`pr create`, and `docs/reference/cli.md` sent a reader to "the API" to make or
remove a link. A person or a script without the TUI could not correct vincent's
head-branch matching or read CI on a task's pull request.

No recorded decision stood in the way. Task 052 decision 6 and task 068 place
link and unlink in TUI layouts and say nothing against a CLI. Task 052
decision 5 (link and unlink make no GitHub call) still holds, because these
commands only call the existing routes. Decision record rows 11 and 27 are
untouched: link and unlink write vincent's own `github_pull_json` column, never
GitHub, so `pr create` stays the one command under `vincent github` that writes
there. Task 048's "What is still API-only" list never named these routes.

## What shipped

Four subcommands under the existing `vincent github pr` group, beside `create`:

```
vincent github pr link NUMBER --task ID [--json]
vincent github pr unlink --task ID [--json]
vincent github pr show --task ID [--json]
vincent github pr checks --task ID [--json]
```

Cobra wiring, output rendering and documentation only: no daemon, store, API,
apiclient or MCP change, and no migration.

## Decisions

1. **2026-09-15 — The task is `--task ID`, a required flag, on all four.** It
   matches `pr create` in the same group. Only `link` takes a positional
   argument, the pull request number (`cobra.ExactArgs(1)`), so the command
   reads "link PR 412 to task 61". Positional `<task> <number>` (the issue's
   wording) was rejected: it would make `pr create` the odd one out beside its
   siblings. A number that is not a positive integer is refused locally with
   exit 1; the daemon's 400 `validation_failed` remains the authority.

2. **2026-09-15 — `link` does not check that the pull request exists** (task
   052 decision 5): the route makes no GitHub call and the CLI adds none. A
   wrong number shows up as `not_found` on the next `pr show`. A project whose
   origin is not a github.com repository gets the route's 409 `not_github`,
   printed through `apiMessage` with exit 1 like the other GitHub commands.

3. **2026-09-15 — `unlink` refuses when there is no live link, before sending
   anything.** The CLI reads the task; if `github_pull` is nil, number 0 or
   already suppressed, it exits 1 saying there is nothing to unlink and never
   sends `DELETE`. `DELETE` on a never-linked task writes
   `{suppressed: true, source: human, number: 0}` (`store.SuppressPull`), which
   the reconciler then skips for good — the task would never auto-link even
   once its branch had a pull request. The TUI never offers unlink in that
   state and the CLI must not be the first surface that does. This is a fast
   client-side failure, not the authority, on task 048 decision 4's precedent.
   A daemon-side 409 was rejected: it changes an existing route's contract. On
   success the output names what was unlinked and that the refusal is sticky.

4. **2026-09-15 — `pr show` ships although the issue did not ask for it.** It
   is the CLI's read of `GET /v1/tasks/{id}/github/pull`, which the TUI calls
   on every workspace open. Without it a CLI user could link or unlink and
   confirm the result only through `task show --json`, which returns the stored
   pointer and not the live pull request. It prints `repo#number` and title,
   the live status (`GitHubPullRequest.Status()`), head → base, the link's
   `source` and the URL.

5. **2026-09-15 — `show` and `checks` exit 0 when read, 1 when unreadable, 2
   with no daemon.** Both routes always answer 200 and carry `linked: false`
   or a named `reason` in the body. `checks` exits 0 when the rollup was read
   whatever CI concluded; scripts read the verdict from `--json`'s `.state`.
   It exits 1 with no live link or when a reason stopped the read. `show`
   follows the same rule and, for a task with no live link, prints the route's
   `compare_url` as a hint when one is present. An exit code that follows CI
   (like `gh pr checks`) was rejected: it needs a code for "pending" that the
   documented 0/1/2 contract (task 048 decision 2) has no room for. "Always
   exit 0" was rejected: a script could not tell "no checks to read" from
   "checks read".

6. **2026-09-15 — One error wording.** A non-2xx answer prints
   `Error: <apiMessage>` on stderr and exits 1, like `issues`, `prs` and
   `pr create`. A 200 carrying a `reason` prints `github.Message(reason)` on
   stderr — the TUI Pull Request tab's wording — so `internal/cli` imports
   `internal/github` as `internal/tui` does. With `--json`, stdout gets the body
   unchanged and the exit code follows the same rule, so a script has both the
   `reason` field and the status.

7. **2026-09-15 — `checks` without `--json` matches the TUI tab.** A head line
   with the rollup state on the short ref (`octo/repo#412: failure on
   d3adb33fd3ad`), then a `CHECK`, `STATE`, `RUN`, `URL` table where `RUN` is
   the Actions run id or `-` for a row no Actions run backs (task 068 decision
   3). A rollup read with no runs prints one line and exits 0. Checks are
   fetched live every call and never cached (task 068 decision 6; the route
   enforces it).

8. **2026-09-15 — The decisions live here, in a new document.** Task 048
   recorded its CLI-parity work the same way. Appending sub-tasks to 052 and
   068 was rejected: it would reopen a finished record (052) and split one
   pull request across two documents.

9. **2026-09-15 — `link` and `unlink` print and emit the task read back, not
   the write's answer.** `apiclient.Task`, which `LinkGitHubPull` and
   `UnlinkGitHubPull` return, has no `github_pull` field — only `TaskDetail`
   does — so the POST's own answer cannot name `repo#number`, and emitting it
   under `--json` would silently drop the very field the command changed. Each
   command reads the task back with `GetTask` after the write, which is
   vincent's own database and so still makes no GitHub call. Adding the field
   to `apiclient.Task` was not done: it is out of this work's scope (no
   apiclient change), and the read-back is the post-action view task 048's
   commands already print.

## Work

- [x] **102.1 — The four subcommands** in `internal/cli/github.go`, registered
  in `newGitHubPRCmd`, with `newGitHubCmd`'s text saying link and unlink write
  vincent's link and not GitHub. ✓ 2026-09-15
- [x] **102.2 — Tests**: `internal/cli/githubpr_e2e_test.go` through the real
  binary against a real detached daemon with `cmd/fakegh` on PATH — link with
  no `gh` call, show and checks read live (checks exits 0 on a failing rollup),
  unlink is sticky and then show and checks exit 1, a second unlink and an
  unlink of a never-linked task change nothing, `not_github`, the `disabled`
  reason, and local argument refusals. ✓ 2026-09-15
- [x] **102.3 — Documentation**: §12.1 amended, `docs/reference/cli.md`,
  `docs/features.md`, `CHANGELOG.md`, and the dated note on task 048's
  "What is still API-only". ✓ 2026-09-15
