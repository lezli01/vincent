# Task 134 gate — an issue's main worktree and merge-back

**Acceptance (task 134.17, #764):** a real daemon, over curl alone, proves
the single-occupant rule of an issue's main worktree and both merge-back
modes end to end — one main task at a time in the issue's directory, the
hand-over of that directory and its branch to the next main task, a side
task merged back cleanly, on a conflict by hand, and on a conflict by the
agent resolver — and is green on the Linux, macOS and Windows CI legs.

The scripted half is [`scripts/134-gate.sh`](../../scripts/134-gate.sh). It
is task-numbered rather than `mN` because this is not a §19 milestone, like
the 123, 125 and 130 gates.

```sh
./scripts/134-gate.sh                            # all nine scenarios
VINCENT_GATE_SCENARIO=5 ./scripts/134-gate.sh    # one, for debugging
```

It needs bash, go, git, curl and jq. Each scenario runs its own daemon over
its own config dir, data dir and repository, which is what lets one run
alone; scenario 9 runs as part of scenario 4, so `VINCENT_GATE_SCENARIO=4`
and `=9` run the same thing. Every scenario creates a local issue with
`POST /v1/issues`, then main tasks with `POST /v1/tasks {issue_id}` and side
tasks with `merge_back: {on_conflict}` beside it.

Every step is a command step, so its timing and its conflicts are
deterministic, except scenario 6's resolver: there `cmd/fakeagent` is
configured as `claude`, the side task names `claude` as its agent (the
resolver's agent is the side task's override), and `FAKEAGENT_WRITE_FILE` /
`FAKEAGENT_WRITE_CONTENT` on that scenario's daemon tell it what to write
over the conflicted file. The `run:` bodies are `exit N`, `sleep N` and
`git …` only — the files are written with `git config -f` — because they
run under `/bin/sh` on POSIX and `pwsh` on Windows.

## What the script asserts

Everything is read from the API — the task's `state`, `block_reason`,
`worktree_path`, `branch_name`, `base_sha`, `issue_worktree`, `merge_back`,
`started_at` and `finished_at`, the 201's
`main_worktree_occupant_task_id`, the issue's `lane` and
`main_worktree.occupant_task_id`, and `GET /v1/tasks/{id}/commits` — plus
`git log` and `git show` of the issue's branch. A merge-back carries no
source id on the DTO; the gate finds it by `workflow == "__merge_back"` and
its title, `Merge task {side} into issue #{issue}`, which is also its merge
commit's message.

A predecessor's `worktree_path` is cleared when it hands the directory on,
so the gate reads each occupant's directory while it holds it, and compares
paths exactly as the API returns them.

1. **Single occupant.** Occupant A sleeps, commits `a-work`, fails a step,
   waits at a manual gate and sleeps again. Main task B, created while A
   runs, carries `main_worktree_occupant_task_id == A` on its 201 and
   `issue_worktree: main`. B stays `queued` while A is `running`, `blocked`
   (then `skip`) and `awaiting_gate` (then `approve`), with a quiet spell at
   each stop, and the issue's `main_worktree.occupant_task_id` is A
   throughout. Once A is `done`, B runs in A's directory on A's branch, its
   `base_sha` is A's last commit — the tip A settled on — and that is B's
   commit's parent. A's `/commits` is `a-work` alone even after B committed
   on the shared branch: it ends at the tip A handed over (`end_sha`, which
   is not on the DTO — task 134.17 decision 2). B's `/commits` is `b-work`
   alone.
2. **One walk.** Two main tasks are created back to back. No poll sees both
   `running`, and the later `started_at` is no earlier than the earlier
   `finished_at` — a comparison of timestamps, so a short overlap a poll
   could miss would still fail it. Both end `done`, in one directory.
3. **A crash under the occupant.** The daemon is killed (`taskkill` on
   Windows, SIGKILL elsewhere) while occupant A sleeps, with main task B
   queued. After the restart A runs again and ends `done`, B is `queued`
   until then, and B is then admitted into A's directory. A's `/commits` is
   its one commit: the interrupted step re-ran, the one after it ran once.
4. **Manual merge-back, clean.** While occupant A sleeps, side task S is
   created with `merge_back: {on_conflict: block}`: `issue_worktree: side`,
   its `base_sha` is the main branch's tip, and it runs in a directory other
   than A's. S writes a file of its own and ends `done`. Its merge-back M is
   `queued`, `issue_worktree: main`, and stays queued until A is done. Then
   M runs in A's directory and ends `done`; the issue branch's first-parent
   log holds `Merge task S into issue #N`, and S's file is on it.
5. **Manual merge-back, conflict.** A and S both create `shared.txt` with
   different values — S from the tip before A's commit. M ends `blocked`
   `merge_conflict` in the main worktree with an unmerged path. The gate
   writes a resolution there, stages it and calls `POST /retry`. M ends
   `done`; the resolution is the branch's `shared.txt`, and the branch has
   exactly one `Merge task …` commit.
6. **Agentic merge-back.** The same conflict with
   `merge_back: {on_conflict: agent}`. M ends `done` with no human action,
   and no poll sees it `blocked`. The resolver's content is the branch's
   `shared.txt`, with one merge commit.
7. **FIFO.** While occupant A sleeps, side task S1 runs to `done`, and only
   then S2 does, so the finishing order is forced. M1 waits for A; both end
   `done`, M1's `finished_at` is no later than M2's `started_at`, and in the
   first-parent log S1's merge comes before S2's.
8. **A crash with a merge in progress.** Scenario 5's setup leaves M
   `blocked` `merge_conflict`, the merge in progress in the main worktree.
   Main task B, created then, names M as the occupant on its 201. The daemon
   is killed and restarted. After a quiet spell M is still `blocked`
   `merge_conflict`, B is still `queued`, and the conflict is still in the
   worktree. A hand resolution and `retry` end M `done` with exactly one
   `Merge task …` commit; B is then admitted into the same directory.
9. **Lanes**, over scenario 4's run: `GET /v1/issues/{id}` reports
   `in_progress` while A runs and while M is pending, and `hand_off` once A,
   S and M are all `done` and the issue is open.

## What the script does not assert

**A daemon killed while `git merge` itself is running** (task 134.17
decision 1). Scenario 8 kills the daemon while a conflicted merge is *left*
in the main worktree, the shape m6 scenario 6 uses for `fan_out`'s join. The
literal case is not driven from the gate: that merge takes well under a
second, nothing in the product holds it open, and a git hook that did would
leave an orphaned git process racing the restart on all three platforms. Its
abort-and-re-merge path is proven by
`TestMergeBackInterruptedMidMergeAbortsAndReMerges`
(`internal/taskrun/mergeback_test.go`).

The TUI and CLI rendering of occupants and merge-backs is task 134.15 and
134.16's.

## CI

The gate runs as the `134 gate (issue main worktree and merge-back)` step of
`ci.yml`'s `gate-group` job, group 4, on Linux, macOS and Windows. The pull
request that added it is its first run on Linux and Windows. Those runs are
recorded below from their CI results, never in advance.

## Runs

| Date | Platform | Result | By |
|---|---|---|---|
| 2026-10-07 | macOS (darwin/arm64) | GATE PASS, all nine scenarios end to end, on three consecutive runs | task 134.17 |
