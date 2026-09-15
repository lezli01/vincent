# Task 069 gate — open a pull request from vincent

**Acceptance (task 069.8):** a real daemon pushes a task's branch to a real
remote and opens its pull request, through the API and through
`vincent github pr create`, as a ready pull request and as a draft — and every
way that can go wrong ends where the task's decisions say it does: a second
attempt on a linked task is refused before anything is pushed, GitHub's own
refusal after an unlink is a fallback rather than an error, a create that fails
after the push hands back a compare page for a branch that is really there,
and a rejected push creates nothing and forces nothing.

The scripted half is [`scripts/069-gate.sh`](../../scripts/069-gate.sh). It is
task-numbered rather than `mN` because this is not a §19 milestone; 017, 032
and 052 set that precedent.

```sh
./scripts/069-gate.sh                            # all five scenarios
VINCENT_GATE_SCENARIO=3 ./scripts/069-gate.sh    # one, for debugging
```

It needs bash, go, git, curl and jq, and nothing else. `cmd/fakegh` is built
**as `gh`** onto the daemon's PATH, which is how the daemon finds the real CLI
too (there is no `gh_path` config key). `GITHUB_TOKEN` and `GH_TOKEN` are unset,
so a `gh` that failed to resolve fails the gate as `no_credential` rather than
reaching api.github.com.

Each scenario is its own installation — its own config and data directories,
daemon, argv log and created-pull-request file for the fake. Its project is a
real repository whose `remote.origin.url` is `https://github.com/octo/repo.git`
(which is what makes it a GitHub project, §13.2) and whose
`remote.origin.pushurl` is a local bare repository: the identity comes from the
fetch URL, the push goes somewhere the gate can inspect. That is the split
`internal/api/githubpullcreate_route_test.go` uses. No agent CLI is involved:
the one workflow is a command step whose whole body is
`git commit --allow-empty -m gate`, portable to the daemon's pwsh on Windows,
which leaves the task's branch one real commit ahead of `main`.

Task titles are chosen so no branch matches one of the fake's corpus heads;
otherwise the reconciler's startup tick could auto-link a corpus row and a
create would be refused `pull_already_linked` for the wrong reason.

## What the script asserts

1. **A ready pull request through the API.**
   `POST /v1/tasks/{id}/github/pull/create` with `draft: false` answers `200`
   with `created` and `pushed` true, `remote: origin`, the task's
   `branch_name`, pull request #999 not a draft, and the task carrying a
   `human` link to `octo/repo`. The bare remote's branch equals the local one,
   which is the step's commit rather than `main`, and
   `branch.{branch}.remote` is `origin` — the push set its upstream.
   `GET /v1/tasks/{id}` and `GET /v1/tasks/{id}/github/pull` agree. The fake
   recorded exactly one `pr create`, naming `--head {branch}` and
   `--base main`, with no `--draft`.
2. **A draft pull request through the CLI.**
   `vincent github pr create --task N --title T --body B --draft --json` exits
   0 with `created: true` and `pull.draft: true`, the one recorded `pr create`
   carries `--draft`, the branch is on the remote, and the task route shows the
   `human` link. This is the surface the command exists to give a gate, and
   the draft/ready toggle end to end.
3. **Double submission, then GitHub's backstop.** After one create, a second
   `POST` is refused `409` with `reason: pull_already_linked`; the fake still
   recorded one `pr create` and the remote's branch did not move. After
   `DELETE /v1/tasks/{id}/github/pull` the task is `linked: false`,
   `suppressed: true`. With the daemon restarted under
   `FAKEGH_SCENARIO=pr-exists`, the same `POST` answers `200` with
   `created: false`, `pushed: true`, `reason: pull_exists` and a `compare_url`
   on `https://github.com/octo/repo/compare/main...`; the fake recorded a
   second `pr create`, and the task is still unlinked.
4. **The fallback when the create fails after the push.** Under
   `FAKEGH_SCENARIO=forbidden` the `POST` answers `200` — the fallback is not an
   error — with `created: false`, `pushed: true`, `reason: no_write_scope` (*it was
   `forbidden` until 2026-09-15, when task 068.4 gave a 403 on every write one
   spelling; the script asserts the new one*) and a
   `compare_url` naming `main...` and the path-escaped branch. The branch is on
   the remote, so that page is live, and the task is unlinked.
5. **A rejected push creates nothing.** A diverging commit is seeded under the
   task's branch name on the remote only. The `POST` is refused `409` with
   `reason: push_rejected`, the fake recorded no `pr create`, the remote's
   branch is still the seeded commit — the push never forces — and the task is
   unlinked.

### `pull_already_linked` is not `pull_exists`

The issue asked for `pull_exists` "on a second attempt". A second attempt on a
task that is **still linked** never reaches GitHub: it is refused `409
pull_already_linked` before any push or `gh` call (decision 7). `pull_exists`
is GitHub's backstop, and it is only reachable once the task is no longer
linked — a human unlink leaves the link `suppressed`, and suppressed does not
count as linked (decision 4) — and then it is a `200` fallback, not an error.
Scenario 3 asserts both, in that order.

## What the script does not assert

- **The TUI.** The pull-request form, its draft toggle and the takeover's task
  picker are covered by `internal/tui`'s tests, including the live tests
  against the real API handlers; what a screen looks like is a judgement, and
  this gate stops at the API and the CLI.
- **The MCP exclusion.** That the route is not an MCP tool is
  `internal/api/mcp_parity_test.go`'s to prove.
- **The push argv.** That no element of it is a force flag is asserted by
  `internal/worktree/push_test.go`. Scenario 5 shows the consequence — the
  remote did not move — rather than the argv.
- **Real GitHub.** The fake stands in for the create leg and a bare repository
  for the remote. Proving the two together is the manual leg below.

## CI

`.github/workflows/ci.yml`'s `gates` job runs the scripted leg on Linux, macOS
and Windows as the `069 gate (open a pull request)` step. Windows matters to it
four ways: the push goes to a drive-letter `pushurl`, `gh` is resolved from
PATH to `gh.exe` (as it is for the 052 gate), the pull request body
reaches `gh` on stdin via `--body-file -` because argv would hit Windows'
command-line limit, and the argv log is read back with CRLF line endings.

## Scripted runs

| Date | Platform | Result | By |
|---|---|---|---|
| 2026-09-15 | macOS (darwin/arm64) | GATE PASS, all five scenarios | task 069.8 |

A Linux or Windows row is added from a CI run of the step, never predicted.

## Manual leg — real GitHub

This needs a throwaway GitHub repository you can write to, a real `gh`
authenticated against it, and a vincent project whose `origin` is that
repository. It writes to GitHub.

1. Create a task on the project with a workflow that commits to its branch,
   and let it reach `done`.
2. Run `vincent github pr create --task N --title "gate walk" --draft`. On
   github.com, confirm the branch is pushed and the pull request exists **as a
   draft**.
3. Open the task in the TUI and confirm the pull request is shown as linked by
   a **human**.
4. Run the same command again. It is refused with `pull_already_linked`, and
   nothing new appears on GitHub.
5. Unlink the pull request (TUI, or `DELETE /v1/tasks/{id}/github/pull`) and
   run the command again. It falls back with `pull_exists`, and the printed
   compare URL opens a live compare page on github.com.
6. On a second task, push a diverging commit to its branch name on GitHub (or
   protect that branch name), then run the command. It reports
   `push_rejected`, creates no pull request, and the branch on GitHub is
   unchanged.
7. The token leg: take `gh` off the daemon's PATH, set `GITHUB_TOKEN` to a
   token with write access to the repository, restart the daemon, and repeat
   steps 2 and 4 on a fresh task. The results are the same, over REST.

| Date | Platform | gh version | Walked by | Result |
|---|---|---|---|---|
| — | — | — | — | not yet walked |

Add a row per walk.
