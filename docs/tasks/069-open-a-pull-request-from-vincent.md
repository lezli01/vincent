# 069 — Open a pull request from vincent itself

**Status:** 🔄 in progress (7/8) · **Issue:**
[#273](https://github.com/lezli01/vincent/issues/273)
· **Spec:** §10, §12.3, §13.2, §13.4, §15, §18, §20, decision record rows 11 and 27

vincent already knew about a task's pull request and already offered to start
one: `P` on the Task Details tab built GitHub's prefilled compare page and
handed it to a browser (task 052.6). It stopped there, and the stop cost three
things — finishing the delivery half meant leaving the TUI, the compare URL was
offered for branches nobody had pushed (which is a dead page, and the fix was a
manual `git push` in the worktree), and the cross-project Pull Requests
takeover had no way to *start* a pull request at all.

This is one human-initiated action, reachable from both surfaces, that pushes
the branch and creates the pull request without leaving vincent. It keeps the
editable prefill, adds a draft/ready toggle, writes the link immediately as
`human`, and falls back to today's compare-URL hand-off whenever it cannot
write.

**It is the first write vincent has ever made to GitHub, and the second thing
it has ever pushed.** That is the whole cost of the feature, and both decision
record rows say so in place.

## Decisions

1. **Rows 11 and 27 are amended, deliberately.** Row 11 ("delivery owned
   entirely by workflow steps; no hardcoded push/PR/merge behavior") gains a
   human-initiated exception; workflow-owned delivery is unchanged for workflow
   runs. Row 27 ("read-only, daemon-side… no write method, no `POST`, no
   mutating `gh` subcommand") gains **exactly one** write path: pull-request
   creation. Nothing else in `internal/github` writes. The row's other halves
   stand unchanged — the link is still a pointer and never a snapshot, the
   listing is still pure, the reconciler still never overwrites a human link,
   and `TestCompareURLMakesNoRequest` still holds, because the compare URL is
   still string construction and is now the *fallback* rather than the only
   path. **Merging stays out of scope**, and row 11's prohibition on hardcoded
   merge behaviour is untouched.

   Two rejected alternatives, recorded so they are not rediscovered.
   *Push only, no create* — vincent pushes and the existing compare URL then
   works — fixes the dead-page complaint but not the acceptance criterion the
   issue exists for, which is finishing the delivery half without leaving the
   TUI. *Keep read-only and reject* — a workflow step already runs `git push`
   and `gh pr create` in its own worktree — was declined for the same reason:
   the manual path is precisely the one with no workflow behind it.

2. **`github.enabled` is the only gate.** No new config key. The consent is the
   human keypress and the editable popup in front of it, not a second line in
   `config.yaml` that nobody would turn on. The cost is a documentation debt
   paid in the same change: `internal/config`'s `GitHub` doc comment,
   `bootstrap.go`'s `github:` block and `internal/github/doc.go` all described a
   read-only integration and all three became false, so all three are rewritten
   to say the integration has one write path, what it is, and that turning
   `enabled` off turns it off with everything else. This answer is only safe
   because of decision 3; the two were decided together.

3. **The new route is excluded from MCP.** Row 28 makes the tool surface *be*
   the route table minus `mcp.Excluded`, enforced by
   `internal/api/mcp_parity_test.go`, so a new route is an agent-callable tool
   by default. This one is listed in `Excluded` under the same wording the
   config and workflow-write exclusions carry: a write to a forge is a human's
   act, and "the keypress is the consent" is only true while a human is the one
   pressing it. Nothing is lost — a step's agent already has a full-auto shell
   in its own worktree and can push and run `gh pr create` there, which is row
   11's original path and stays open.

4. **No eligibility guardrails beyond branch-and-no-live-link.** Everything
   else is reported by the push or the create failing, with a named reason.
   Three consequences, stated rather than discovered:

   - Uncommitted work in the worktree is not in the pull request. A push sends
     commits; the popup says so before the human confirms.
   - A running task's branch may move under the pull request. That is allowed.
   - A task 064 task whose link a human *suppressed* becomes eligible again,
     and its branch is somebody else's head. A fork branch has no upstream by
     design (064 decision 5), so `git push -u origin` either fails or lands a
     copy of a contributor's branch in the user's own repository. It is never a
     force-push and destroys nothing, and it surfaces as `push_rejected` or an
     ordinary new branch. This is the one place "no guardrails" has teeth, and
     it is recorded here so the next person reads it as a choice.

5. **The push never forces.** `git push --set-upstream origin {branch}`,
   bounded by `gitx.RemoteTimeout`, with `GIT_TERMINAL_PROMPT=0` so a
   credential helper that wants a terminal fails instead of hanging a request
   handler. A diverged, protected or rejected push creates no pull request,
   changes nothing on the remote, and reports a named snake_case reason in
   `internal/worktree`'s existing vocabulary. Force-pushing is refused for the
   reason 064 decision 4 gives for `pull_branch_diverged`: the local branch may
   hold commits nobody pushed and discarding them silently is dishonest. The
   argv is asserted by a test rather than left to review.

6. **The work is synchronous in the handler.** `handleTaskDiff` already runs
   git inside a request and `internal/worktree`'s fetch already bounds a
   network git call at `gitx.RemoteTimeout`; the create leg is bounded at
   `github.RemoteTimeout`. No background job, no new event beyond the existing
   `task.github_pull_changed` the link write already publishes.

7. **Double submission is refused at the source, not with an idempotency key.**
   `Idempotency-Key` is opt-in per route (task 040) and not wired here. The
   link is written the moment the pull request exists, so the second call sees
   a live link and is refused `409`; the TUI disables its submit after the
   first send; GitHub itself refuses a second pull request for the same head
   and base, which is the backstop.

## Shape

`POST /v1/tasks/{id}/github/pull/create` — body `{title, body, draft}`, all
three from the editable popup. The handler runs the §13.2 gate first and stops
at the first "no", refuses a task that already has a live link, pushes,
creates, writes the link as `source: human` (so 052 decision 2's reconciler
will not overwrite it), and returns the created pull request.

The fallback is a **200 carrying `compare_url` and a `reason`**, not an error:
when there is no write credential or the create call fails, the client opens
the browser exactly as it does today. A *push* failure is a `409` with its
named reason and nothing is attempted at GitHub. When the push succeeds and
only the create fails, the fallback compare URL now works — which is the
issue's second complaint fixed even on the unhappy path.

## Subtasks

- [x] 069.1 `internal/gitx` `RunEnv`; `internal/worktree` non-forcing
      `PushBranch` with `push_rejected` / `push_no_credential` / `push_failed`.
- [x] 069.2 `internal/github` `CreatePull` on both legs, `pull_exists` and
      `bad_request` reasons, `ReasonForbidden`'s message no longer says
      "issues", `doc.go` rewritten.
- [x] 069.3 `internal/api` route, handler and fallback envelope;
      `internal/mcp` exclusion.
- [x] 069.4 `internal/apiclient` wire types and method; `vincent github pr
      create`.
- [x] 069.5 TUI: the draft toggle and the daemon call in `createprform.go`, the
      section text, the bindings, the takeover's task picker.
- [x] 069.6 `internal/config` and `bootstrap.go` stop describing a read-only
      integration; `cmd/fakegh pr create`.
- [x] 069.7 Spec amendments (rows 11 and 27, §10, §12.3, §13.2, §13.4, §15,
      §18, §20), this record, and the derived pages under `docs/`.
- [ ] 069.8 `scripts/069-gate.sh` and `docs/gates/069-open-a-pull-request.md`,
      driving a real push to a local bare remote and `cmd/fakegh`'s `pr
      create`, plus the `ci.yml` step. **Not landed in this change** — see
      "Open" below.

## Open

The gate can prove the push leg against a local bare remote and the create leg
against `fakegh`; it cannot prove the two together against real GitHub. That is
the same wall task 064.9 hit: it is a manual walkthrough recorded in
`docs/gates/`, not a reason to hold the work.

069.8 is left open deliberately rather than half-landed. The gate script is a
`ci.yml` change, and a cloud session's token has no `workflow` scope and so
cannot write `.github/workflows/` by any route — push or API (#120, #122,
#125). A gate committed without its CI step is a gate that has never run on any
platform, and CLAUDE.md is explicit that such a gate is not known to pass: two
Windows-only faults were found exactly that way when `m6`/`m7`/`m8` were
finally wired in. The route is covered end to end in the meantime by
`internal/api/githubpullcreate_route_test.go`, which pushes to a real bare
repository and drives `cmd/fakegh`, and by `internal/tui`'s live tests against
the real handlers.
