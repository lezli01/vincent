# Spend inference only where it belongs

One reason vincent is cost-effective is that it does not treat every part of an
agentic task as an inference problem.

Without a workflow, I might give an agent a prompt like this:

> Deliver the fix on a new branch and open a pull request when it is done.

That sounds efficient because it is short, but it hands the entire delivery
process to the agent. The agent must spend time and tokens dealing with branch
setup, rebasing, validation, pull-request creation, and CI status—even though
most of those operations are deterministic. They have known commands, clear
inputs, and machine-readable exit codes. They do not become better simply
because an agent reasons its way through them again on every run.

vincent lets the same request become a small workflow in which each job is given
to the cheapest correct primitive. The portable
[vincent Workflows skill](../../skills/vincent-workflows/SKILL.md) follows this
principle when it helps create workflows: it prefers commands and native control
flow, then uses an agent only when the work actually requires interpretation or
judgment.

The workflow could look like this in practice:

1. vincent creates the task's isolated worktree and branch. A command step can
   then rebase it onto the required base branch or perform any other predictable
   repository preparation.
2. An agent step reads the issue, understands the code, implements the fix, and
   writes the tests. While it still has the necessary context, it can also leave
   structured files containing the proposed pull-request title, description,
   and testing notes.
3. Command steps run the formatter, tests, linter, or other repository checks.
   Their exit codes decide whether the workflow can continue.
4. If the delivery policy requires it, a human gate pauses for review before
   anything is published.
5. A command or repository script reads the prepared files and opens the pull
   request with the expected CLI arguments and metadata.
6. Another command watches the GitHub Actions checks and reports their result.

Only the middle of that sequence truly needs inference. Understanding an
ambiguous report, navigating an unfamiliar codebase, choosing the right change,
and explaining the result are reasoning tasks. Creating a branch, running a
rebase, invoking a test command, opening a pull request from known inputs, and
waiting for checks are not.

This separation reduces more than token usage. Deterministic steps are easier to
inspect, reproduce, retry, and debug. A failed command provides an exit code and
captured output instead of requiring another model call to work out what
happened. The agent receives a narrower assignment with less surrounding noise,
so its context is spent on the part where it can add value. And the workflow
makes the delivery policy visible instead of hiding it inside a broad prompt.

The goal is not to avoid agents. It is to stop paying for inference where a
command is more precise. vincent makes agentic work cost-effective by combining
both: agents for judgment, deterministic tools for mechanics, and human gates
for decisions that should remain human.

---

[Back to Why vincent is awesome](README.md) · [Write a command-first workflow](../guides/workflows.md)
