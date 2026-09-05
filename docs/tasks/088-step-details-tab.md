# 088 — A Step Details tab that shows what each attempt was actually given

**Status:** ✅ done (3/3)
**Issue:** #323
**Amends:** §5.4 (the StepRun field list gains the recorded input and the
resolution behind it), §13.2 (`GET /v1/tasks/{id}/steps`' DTO), §14
(`step_runs`, migration 0027), §15 view 2 (the new tab, as a **supersession** of
068.3's placement) and §15's Layout paragraph (the strip is seven tabs long, or
six)
**Supersedes:** [068](068-pull-request-tab.md).3's placement of the Pull Request
tab — it is no longer the sixth tab and no longer answers to `6`
**Keeps, without relitigating:** [015](015-conditional-steps.md) decision
10 (guards are re-evaluated every time and never sticky), [053](053-workflow-step-detail.md) decision 2's
beat against resolving values for the graph's step modal,
[049](049-full-screen-task-workspace.md) decision 3 (Task Details is complete and
read-only) and decision 4 (one attempt cursor for the whole workspace)

When a step misbehaved, the workspace could say what an attempt *did* and it
could show the workflow's template, but nothing anywhere recorded the
substitution in between. `step_runs` had no column for it —
`prompt_override`/`run_override` (migration 0003) hold only text a human typed at
edit+retry — and the transcript had none either: `step_started` writes the
agent/model/effort triple, the `debug: true` note writes argv, and the claude
adapter passes the prompt on **stdin**, so argv does not carry it.
`command_started` recording the rendered script was the one input that survived.
`vincent workflow render --task` is explicitly a preview: it binds
run-discovered values to visible sentinels and renders against *now*, so it can
never be the bytes an attempt got.

The same gap hid the rest of the run-time resolution. A `skipped` row said `by
condition` without saying what the `if:` rendered to; the triple was shown
without which level supplied each part; and the permission mode, timeout and
shell existed only inside a `debug` transcript note, which is to say on almost no
runs at all.

## Decisions

1. **Step Details is the sixth tab, bound to `6`; Pull Request moves to `7`.**
   *2026-09-04.* The issue's own placement sentence was self-contradictory —
   it asked for the tab to be appended after Workflow so that `1`–`6` keep their
   meaning and `7` selects it, but Pull Request already *is* the tab after
   Workflow and already answers to `6` (068.3). Inserting ahead of it was chosen
   over appending after it, and that **supersedes 068.3's placement**.

   What 068.3 was protecting survives whole. Digits bind to tabs and not to
   positions (`taskview.go:updateKey`), and Step Details is unconditional, so no
   tab's number moves when the pull-request tab is absent: `6` is Step Details
   either way, and `7` does nothing when nothing is linked, exactly as `6` did
   before. 068.3's other half survives too — the conditional tab stays last on
   the strip, so `tabs()` and the cycle keep the shape they have. What is
   genuinely paid is that `6` changes meaning **once**, for a reader with a
   linked pull request who had learned the old number. That cost was accepted
   deliberately, which is why §15 records this as a supersession rather than as a
   restatement.

2. **The recorded input is capped at 64 KiB per field, with an explicit
   truncation marker.** *2026-09-04.* Sized here rather than left to the
   implementer, because the sizing argument is worse than the issue assumed. On a
   retry the bytes the adapter receives are the §8.4 render *plus* the daemon's
   appended `<previous-attempt-failure>` block, whose output tail is itself
   bounded at 200 lines **or 256 KiB** (`outputTailBytes`). Uncapped, that is a
   quarter-megabyte per retried attempt of bytes the transcript and
   `result_summary` already hold. Nothing prunes `step_runs` —
   `internal/taskrun/prune.go` deletes transcripts of tasks archived past the
   window and idempotency keys, and nothing else — and the database ships whole
   in `vincent daemon backup` (task 030).

   64 KiB is roughly six times the largest prompt vincent renders today (the
   `create-workflow` built-in's, about 10 KB, task 024 decision) while cutting
   the pathological case. The cut is on a rune boundary, the row carries the
   marker, and the tab says so on screen: eliding silently is the thing this
   design refused. `result_summary`'s 4096 is deliberately **not** reused — that
   bounds a summary a board renders, and this is a record whose value is being
   exact.

3. **"The rendered input" means exactly what the adapter got.** *2026-09-04.*
   The §8.4 render *after* `AppendFailureBlock`, not before. That half is the one
   a re-render can never reproduce, because it draws on the previous attempt's
   row — and it is what a human is trying to explain when they ask what the agent
   was told. The tab marks the appended part as daemon-authored so a reader can
   tell it from what the workflow wrote, but the recorded bytes are the bytes
   handed over. That is the question the issue opens with, answered literally.

4. **The resolution facts are persisted on the same row, not derived on read.**
   *2026-09-04.* The issue was internally inconsistent here: its first point
   argues that re-rendering at read time drifts silently, and its second
   describes the resolution facts as ones the daemon "already computes and then
   discards", which reads as derivation. Persistence wins, for the first point's
   own reason applied honestly. `config.yaml` hot-reloads (§12.3), so a timeout
   or a shell default can move under a row that already ran; a task's
   `agent`/`model`/`effort` overrides are patchable after an attempt, so
   re-running `agent.ResolveWithSources` later can name a different level than
   the one that actually supplied the value. A derived answer would disagree with
   what the attempt got, with nothing on screen saying so.

   The corollary that makes this work: `resolveSelection` now calls
   `agent.ResolveWithSources`, which is already what `POST /v1/resolve`
   implements. An engine that answered the provenance question its own way would
   be free to drift from the endpoint a user consults to predict it.

5. **The record is display-only, and 015 decision 10 is not relitigated.**
   *2026-09-04.* Decision 10 refuses a *persisted verdict consulted later*. This
   column is never read by the engine: `retry` and §12.4 recovery both re-enter
   `evaluateGuard` and re-render the guard against current facts. It extends
   decision 10's own closing clause — "the mitigation is visibility (the skipped
   row records why), not stickiness" — because today `recordGuardOutcome` records
   the **raw template** in `result_summary`, and this records what it rendered to
   as well. A test holds the property, not just a comment.

6. **The record is written before the process, by a narrow additive writer.**
   *2026-09-04.* `RecordStepRunInput` rather than a widening of
   `UpdateStepRun`, and the reason is load-bearing: the input is known before
   anything is spawned and must already be on the row while the attempt is
   `running` and after §12.4 recovery finalizes it `interrupted` — which is
   precisely the attempt someone opens this tab for. An `UPDATE` carrying an
   actor's stale struct would erase it. Additive per call matters for the same
   practical reason: an agent step records its prompt at one moment, a command
   step its script at another and its check command later still, and all three
   are one row. Values known *at insert* — a decision row's rendered guard, a
   loop body's resolved `for_each` list — go through the INSERT instead, which is
   already `loop_item`/`loop_total`'s rule (migration 0026): both are what *that
   admission* planned.

7. **The tab shares the workspace's attempt cursor.** *2026-09-04.* 049 decision
   4's rule, not a second copy of the selection: arriving from Output or Diff
   lands on the attempt already being read, `←`/`→` keep working, and a move made
   here is still made when the reader goes back. It is also why the pane is a
   sibling of `detailsPane` rather than a second instance of it — `detailsPane`
   owns its selection, and this one must not.

8. **Task Details' workflow snapshot stays exactly as it is.** *2026-09-04.* 049
   decision 3 said Task Details is complete and read-only, and its snapshot
   section already renders each step's *un-rendered* `prompt`/`run`/`instructions`
   and `resolved_from` — the issue's "show the snapshot template only"
   alternative, already shipped. Step Details is the rendered counterpart. The two
   are the template and the substitution, and seeing both is the point. For the
   same reason `resolved_from` is **read** here and nothing is added for it: it
   already exists end to end, and the graph's step modal is untouched — 053
   decision 2's beat against resolving values there was about a definition with
   no run behind it, and does not reach a row that records what its run did.

9. **The tab is about the open task's own attempts.** *2026-09-04.* No
   cross-task fetch is added for a fan-out. A lane's inputs are read by opening
   the lane task, which `l` from the board and `U` back to the parent already
   make a short trip (task 084, #316). The parent's `fan_out` join attempt shows
   its own input like any other row.

## Sub-tasks

| ID | Work | Status |
|---|---|---|
| 088.1 | Migration 0027, the `StepRun` fields, the 64 KiB cut, `RecordStepRunInput`, and the API/apiclient DTOs | ✅ done |
| 088.2 | The engine capture: the prompt after `AppendFailureBlock`, the run script, the check command, the shell, the guard (via `workflow.EvaluateRendered`), the resolved `for_each` list, and the §8.6 provenance through `agent.ResolveWithSources` | ✅ done |
| 088.3 | The Step Details tab, `6`, the strip and the bindings context | ✅ done |

## Not done here

No gate script. What is judged is whether a panel is legible, which is the same
reason M3's and task 017's surfaces have none; the daemon half is asserted in Go
tests, and the tab's screenshot tape and `docs/gates/` run record are the
follow-up.
