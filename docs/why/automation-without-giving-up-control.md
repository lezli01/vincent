# Automation without giving up control

Automation is often presented as a choice between two extremes. Either a person
drives every step manually, or the system runs unattended and people accept
whatever happens. Agentic delivery needs a better model than that.

Most of a well-understood process should run without interruption. Preparing a
worktree, running checks, formatting files, and collecting pull-request details
do not benefit from a ceremonial click after every step. But some boundaries
change the world outside the task: opening or merging a pull request, publishing
a package, deploying a service, or closing a ticket. At those boundaries,
review and authorization are part of correctness.

Vincent makes that distinction explicit with `manual` steps. A workflow runs
until it reaches a gate, persists its place, and releases its scheduler slot
while it waits. The branch, worktree, transcripts, results, and diff remain
available for inspection. A person can approve and continue, reject and block
the task, or skip when the workflow allows that decision.

The placement of the gate matters. “Review eventually” is not a control. A gate
immediately before an external effect says precisely what has already been
verified and exactly what approval permits next. Everything before it can stay
efficient; everything after it has an explicit authorization trail.

Human input during an agent session serves a different purpose. A supported
agent may ask a structured question because it cannot proceed without an answer.
That keeps the same reasoning session alive. A manual gate sits between steps
and asks for a binary decision about quality or permission. One resolves
ambiguity inside the work; the other controls whether the workflow crosses a
boundary.

This also means “full-auto” does not have to mean “out of control.” An agent may
need broad permission inside an isolated task worktree to build and test a
change. That does not automatically grant permission to publish the result.
Execution permission and delivery authorization are separate choices, and a
workflow can represent both.

The best automation does not remove people indiscriminately. It removes people
from repetition and places them where judgment has the highest value. Vincent is
awesome because the pause is not an informal habit or a reminder in a prompt;
it is part of the executable process.

---

[Back to Why vincent is awesome](README.md) · [See lifecycle actions](../reference/task-lifecycle.md#human-actions)
