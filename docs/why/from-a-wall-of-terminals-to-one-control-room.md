# From a wall of terminals to one control room

Running one agent in one terminal is easy to understand. Running several agents,
commands, retries, and approval gates across multiple repositories is not.

The usual solution is a wall of terminal tabs. Each tab contains a partial story:
one is still streaming, one stopped twenty minutes ago, one is waiting for an
answer, and another finished on a branch whose name is no longer visible. The
operator's attention becomes the only thing connecting them.

Vincent's TUI is designed around workloads rather than sessions.

![The task board with a selected task's timeline and live output](../assets/tui-board.png)

The board shows every task's state, current step, elapsed time, reported cost
when available, and the status message supplied by the running step. Filtering
and grouping answer operational questions such as “what is blocked?” or “which
project is consuming the queue?” without visiting every process individually.

Selecting a task keeps its attempt timeline beside its live output. A failed
attempt remains visible after a retry. The diff is another tab on the same task,
and the complete transcript can open in an editor when the summarized result is
not enough. The interface preserves the distinction between vincent's fixed
failure reason and the free-text status the step reported about itself.

The workflow view provides a different kind of visibility. Its graph shows
parallel groups, fan-out lanes, conditions, loops, checks, and includes before a
task runs. The new-task flow presents declared inputs and overrides, then gives
the complete request a final review stage.

Most importantly, the TUI does not own execution. It renders state from the
daemon and sends actions the daemon has already declared valid for the task's
current state. Closing the interface does not stop the work. Reopening it does
not reconstruct the story from terminal output; it reads the persisted story.

A control room should direct attention, not demand it continuously. Healthy work
can stay in the background. Gates, questions, blocked steps, and failed checks
become visible when they need a decision. That is what makes managing agentic
workloads feel different from babysitting a collection of terminals.

---

[Back to Why vincent is awesome](README.md) · [Use the TUI](../guides/tui.md)
