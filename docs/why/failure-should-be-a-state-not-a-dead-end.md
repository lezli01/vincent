# Failure should be a state, not a dead end

In a one-shot agent session, failure often destroys the most valuable thing the
session produced: context. The command exits, the terminal stops moving, and the
next attempt begins by rediscovering the branch, the error, and what changed
before everything went wrong.

Vincent treats failure as durable state instead.

Every attempt records its result, transcript, timing, and failure reason. A
workflow may retry automatically, and an agent retry receives the previous
failure as structured context. When retries are exhausted, the task becomes
`blocked`. It does not silently skip the step, discard the worktree, or declare
the entire task worthless. It stops at the exact decision that now needs a
person.

From there, the recovery action can match the problem:

- Retry when the step was correct and the failure was transient.
- Edit and retry when this task needs a better prompt or command.
- Run a repair agent when the files in the worktree need attention first.
- Skip when a person accepts the unmet condition.
- Cancel when continuing would be wrong.

A repair agent is deliberately limited. It changes files in the existing
worktree, records its own transcript and cost, and returns the task to the same
blocked step. It does not decide that its repair was sufficient. A person still
reviews the diff and chooses whether to retry.

Not every interruption is even a failure. A usage limit can return a task to the
queue until the reported reset time without consuming a retry or scheduler slot.
A configured retry backoff can do the same for transient errors. A daemon crash
records an interrupted attempt and recovers it without pretending the step
failed on its own merits.

The same philosophy continues after success. A completed task keeps its branch
and worktree until it is archived. If review asks for one more commit or the base
branch moves, a follow-up agent, command, or workflow can run in the existing
context without rewriting the original verdict.

Failure becomes manageable when the system remembers enough to make the next
decision well. That is the difference between restarting an agent and recovering
a workload.

---

[Back to Why vincent is awesome](README.md) · [Explore task recovery](../reference/task-lifecycle.md#states)
