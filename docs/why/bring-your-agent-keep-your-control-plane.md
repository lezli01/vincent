# Bring your agent, keep your control plane

Coding agents change quickly. Models appear, capabilities move, authentication
flows evolve, and the tool that fits one repository may not fit the next. An
orchestrator should not require the entire engineering process to move whenever
the preferred agent changes.

Vincent runs the agent CLIs already installed and authenticated on the machine.
It stores no agent API keys, provides no model access of its own, and does not
replace the vendor's login flow. An agent step launches the same Claude Code,
Codex, or Cursor binary that could have been run by hand, but places it inside a
durable workflow and an isolated task worktree.

Above those adapters, the workflow vocabulary stays consistent. A step can have
a prompt, timeout, retry policy, deterministic check, permission mode, model,
and effort selection. Resolution can come from the step, a task override, the
workflow default, or the adapter default. That makes the orchestration policy
portable even when the selected agent changes.

Portability does not mean pretending the agents are identical. Vincent documents
capability differences and refuses to fake them. Codex does not provide the same
model catalog or mid-run input surface. Cursor expresses effort through its
model identifier and cannot honor restricted mode on Windows. Cost reporting
also depends on what the underlying CLI exposes. When a capability is missing,
the workflow should know that rather than receiving a convincing imitation.

This honesty is more valuable than a perfectly uniform abstraction. It lets a
workflow require a capability when the process depends on it, while ignoring an
irrelevant setting when it does not. Newly released model names can still be
entered before a catalog learns about them.

The control plane remains local too. The daemon, database, worktrees,
transcripts, and scheduling policy stay on the machine. Agent credentials stay
with the authenticated CLI. Changing the agent does not require exporting the
task history or rebuilding the delivery process around another hosted service.

The goal is not to make every agent interchangeable. It is to keep ownership of
the workflow while choosing the best available reasoning tool for each step.

---

[Back to Why vincent is awesome](README.md) · [Compare supported agent CLIs](../guides/agents.md)
