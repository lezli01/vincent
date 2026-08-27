# One task, one branch, no checkout traffic jam

Agentic work becomes awkward the moment two tasks want the same checkout.

One agent edits a file while another rebases. A developer switches branches and
invalidates a process still running in the old tree. Uncommitted work forces
everyone to wait. The repository may support parallel development, but a single
working directory turns it back into a queue.

Vincent gives every task its own git worktree and branch before the workflow
runs. The task receives an isolated directory containing the same repository,
while the developer's active checkout stays untouched. Starting another task
creates another worktree instead of competing for the first one.

This makes concurrency understandable. The scheduler controls how many tasks may
run globally and per project, so parallelism is intentional rather than an
accident measured in terminal tabs. A task waiting at a human gate, a retry
backoff, or a fan-out join releases its slot so idle work does not block useful
work.

Isolation also gives every result a clear home. The task's commits belong to its
branch. Its diff is calculated from its own worktree. A blocked task keeps both
for repair and review. A finished task can receive follow-up work in the same
place, then archive the worktree when the branch is no longer needed.

There are still two kinds of parallelism worth distinguishing. Separate tasks
have separate worktrees and can safely change the same original files on their
own branches. A `parallel` group inside one workflow shares a single task
worktree, so its sub-steps must not write the same files. When work truly needs
isolated child branches, `fan_out` creates child tasks and joins their results
explicitly.

Worktrees do not make merge conflicts disappear. They make ownership visible
and keep conflicts from corrupting unrelated in-progress work. A merge problem
becomes a recorded condition on the affected task instead of a surprise in the
developer's checkout.

The result is a simple mental model: one task, one branch, one worktree. That
small invariant is what lets several agents and a human work on the same
repository without turning branch switching into traffic control.

---

[Back to Why vincent is awesome](README.md) · [Learn about task worktrees](../getting-started/concepts.md#a-worktree)
