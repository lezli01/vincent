# Task 137 gate — handoffs end to end

**Acceptance (task 137.13, #846):** a real daemon, driven over curl and the
CLI, runs handoffs end to end — the claim, the grant over a handoff task's
forge writes, the item verbs, and readiness — on Linux, macOS and Windows.

The scripted half is [`scripts/137-gate.sh`](../../scripts/137-gate.sh). It
is task-numbered rather than `mN` because this is not a §19 milestone, like
the 130 gate.

```sh
./scripts/137-gate.sh                            # all seven scenarios
VINCENT_GATE_SCENARIO=2 ./scripts/137-gate.sh    # one, for debugging
```

It needs bash, go, git, curl and jq. Each scenario runs its own daemon over
its own config and data dirs, which is what lets one run alone. The gate
plays the human, so it unsets every `VINCENT_*` variable but its own
`VINCENT_GATE_*` knobs first: run from inside a vincent step, the step's
`VINCENT_TASK_ID` would otherwise mark every `vincent` call the gate makes as
an agent's.

The fakes:

- `cmd/fakeagent`, configured as the `claude` agent. Only scenario 4's
  follow-up prompt reaches it.
- `cmd/fakegh` built **as `gh`** onto the daemon's PATH, as in the 130 and
  068 gates, with three files under the scenario's temp dir:
  `FAKEGH_ISSUES_FILE`, the issue corpus (open issues in `gate/handoffs`,
  re-read on every call, so the gate can close one on GitHub);
  `FAKEGH_STATE_FILE`, where `pr merge` records pull request #412 merged;
  and `FAKEGH_ARGV_FILE`, every call the daemon made, so "nothing was sent"
  is observed behaviour.

An issue reaches the `hand_off` lane the way a user's does: a task created
from the imported issue runs `work`, one `exit 0` step under the default
`hand_off: auto`, to `done`. The handoff workflows declare `role: handoff`.
The steps that act from inside a handoff's task run `vincent ...` — the
gate's build, on the daemon's PATH, under `pwsh` on Windows — so the caller
the forge-write guard judges is a real step carrying its own task marker.
A handoff task that must stay live waits on a `manual` step (`hold`).

## What the script asserts

1. **Claim exclusivity.** Two `POST /v1/handoffs` for one `hand_off` issue
   are sent concurrently. Exactly one is 201 and the other 409
   `issue_not_eligible`, naming the winner's `handoff_id`. One live handoff
   claims the issue, its item unresolved, and the project gained one task.
2. **The grant.** The delivering task has fakegh's #412 linked with
   `vincent github pr link`. An ungranted handoff whose step runs
   `vincent issue close` on its item, and another whose step runs
   `vincent github pr merge --task` on the item's delivering task, each
   block with the step `failed` and are cancelled. The issue stays open, and
   after a few sync ticks the argv log has no `PATCH` of #1 and no
   `pr merge`. Then `vincent handoff start --grant issue.close --grant
   pull.merge` runs both steps to `done`: the issue is closed, written back
   to the corpus with exactly one `PATCH`, `gh pr merge 412` was called once,
   and the issue's one `issue.state_changed` event says `by: handoff` with
   the handoff's id.
3. **`wait` resolved by a sync close.** A step runs `vincent handoff wait`;
   the handoff task finishes `done`, the item is `waiting` (by `handoff`),
   the handoff is `waiting`, and a new handoff over the issue is refused
   `issue_not_eligible`. The gate closes #1 in the corpus; the next import
   records the item `delivered` by `sync`, the handoff `settled`, the issue
   closed.
4. **`return` queues a follow-up.** `vincent handoff return --follow-up
   PROMPT --paused` on a live handoff: the item is `returned`, the handoff
   still `live`, the issue in `in_progress` with its delivery no longer
   ready, and the issue's main task `paused` on the new round. `--paused`
   is there so the lane is read before the round runs: the fake agent
   finishes fast enough to deliver again and put the issue back in
   `hand_off`. Resumed, the task finishes `done` with a step whose
   `rendered_prompt` carries the prompt — the round ran on the issue, which a
   claimed issue would refuse.
5. **An aborted handoff releases its items.** A handoff over two issues waits
   on its manual step and is cancelled. Both items are `released` by
   `settle`, both issues are still in `hand_off`, and a fresh handoff claims
   both.
6. **`hand_off: explicit`.** Two issues whose tasks ran `explicit` to
   `done` stay `in_progress`, and a handoff over one is refused
   `issue_not_eligible`. `vincent task deliver --task ID --ready` moves the
   first to `hand_off`, `vincent issue ready` the second, and a handoff then
   claims both. The CLI's `idle` lane label is task 137.10's and is not
   asserted.
7. **A task on a claimed issue.** While a live handoff claims the issue,
   `POST /v1/tasks` with its `issue_id` is 409 `issue_in_handoff`; after
   `vincent handoff return` (no follow-up) the same request is 201.

## What the script does not assert

Triggers' `start_handoff` (137.9), the derived `handoff` marker and the
CLI's lane column (137.10), and the TUI (137.11) have their own items and
tests. The MCP surface of a granted handoff's step is not driven here: every
write goes through the CLI a step runs.

## CI

The gate runs as the `137 gate (handoffs)` step of `ci.yml`'s `gate-group`
job, group 2, on Linux, macOS and Windows. The pull request that added it is
its first run on Linux and Windows. Those runs are recorded below from their
CI results, never in advance.

## Runs

| Date | Platform | Result | By |
|---|---|---|---|
| 2026-10-28 | macOS (darwin/arm64) | GATE PASS, all seven scenarios end to end, at 01ac1515 | task 137.13 |
