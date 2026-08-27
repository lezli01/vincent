# The terminal should not own the work

A terminal is a good place to start a process. It is a poor place to store the
truth about that process.

When an agentic task belongs to one terminal tab, the tab becomes an accidental
control plane. Closing it can stop the work. Losing scrollback can erase the
useful explanation. Reopening the project means reconstructing what ran, which
branch it changed, and whether it was waiting, finished, or quietly stuck. The
longer the task runs, the more fragile that arrangement feels.

Vincent moves ownership into a background daemon. The daemon owns task state,
workflow execution, agent processes, scheduling, the database, and git
worktrees. The TUI, CLI, and API are clients of that state. Closing any client
changes nothing about the work behind it.

That architectural choice has consequences that are easy to feel. I can start a
task, close the TUI, use the terminal for something else, and return later to the
same state and history. Several clients can inspect the same daemon without
becoming competing writers. The scheduler can admit tasks by priority and
concurrency limits even when nobody has a dashboard open.

Durability also changes how interruption is handled. Vincent persists a state
transition before acting on it. If the daemon stops during a step, restart
recovery records the interrupted attempt, verifies any orphaned process before
stopping it, and runs the step again without consuming a failure retry. The
system does not pretend that a process survives a machine restart; it makes the
interruption explicit and recovers from known state.

Installing the daemon as a user service extends the same model across logins.
The operating system starts the control plane, and the terminal returns to being
what it should be: one optional window into the work.

This matters because agentic coding is increasingly a workload rather than a
conversation. Tasks wait on quotas, gates, retries, child branches, and external
checks. Their lifetime should not be coupled to the lifetime of the interface
that launched them.

The terminal can show the work. It should not own the work.

---

[Back to Why vincent is awesome](README.md) · [Understand the daemon](../getting-started/concepts.md#the-daemon)
