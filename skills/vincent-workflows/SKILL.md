---
name: vincent-workflows
description: Create, edit, review, and validate vincent workflow YAML under .vincent/workflows. Use for vincent workflow fields, step selection, templates, checks, human gates, retries, conditions, loops, parallel work, fan-out, includes, or validation errors. Do not use for GitHub Actions or other workflow systems.
license: LICENSE.txt
metadata:
  author: lezli01
---

# vincent Workflows

Design the smallest reliable vincent workflow for the requested outcome. Spend
agent tokens only where reasoning is part of the work; express everything else
with deterministic steps and vincent's native control flow.

## Gather only decisions that matter

Inspect the repository, its instructions, and existing workflows before asking
questions. Infer obvious answers and state material assumptions. Ask a compact
set of questions only when an answer changes the YAML, especially:

- What deliverable ends the run, and which commands prove it is correct?
- Which task inputs are required, optional, typed, or pattern-constrained?
- Which host platforms and shells must the commands support?
- Which vincent version and environment will execute the workflow, and is the
  locally available binary representative of that target?
- Does any step publish, deploy, push, delete, spend money, mutate an external
  service, or otherwise become difficult to reverse?
- Which choices must be known when the task is created and declared as typed
  task fields?
- Where does a person need a binary approve/reject gate for quality or an
  external effect?
- Must a person answer an agent during the same reasoning session, or is a
  between-step approval gate sufficient?
- Which credentials must already be available through the daemon environment,
  an OS credential store, or an authenticated CLI without entering workflow
  data or transcripts?
- Can independent work safely share one worktree, or does it require isolated
  child branches? Is the extra concurrency worth its compute and merge cost?
- Which failures are observations, which should retry, and which should block?

For an external or irreversible effect, ask a concrete gate question such as:
"The workflow can publish the release. Should it pause for approval immediately
before publishing, run that step unattended, or omit publishing?" Do not add a
ritual gate when the user has already made the policy clear.

## Establish compatibility and read references

If this work is inside a vincent source checkout, read its current
`docs/reference/workflow-schema.md`; it is newer and more authoritative than a
bundled snapshot. Otherwise use
[references/workflow-schema.md](references/workflow-schema.md) as the design
reference.

Run `vincent version` when the binary is available and retain the exact output
for the final summary. The installed binary's
`vincent workflow validate <file>` result is the compatibility verdict. If it
rejects a feature described by the reference, explain the version mismatch and
ask whether to upgrade vincent or target the installed feature set. Do not
silently claim compatibility or rewrite the requested design. If execution
targets another host or version, identify local validation as provisional until
the target binary validates it.

Read
[references/control-flow-and-cost.md](references/control-flow-and-cost.md) when
the workflow involves branching, repetition, concurrency, composition,
side effects, human interaction, or any agent step. Use its patterns,
agent-session envelope, and cost review before finalizing the design.

## Choose the cheapest correct primitive

Apply this order for every unit of work:

1. Use `command` for a known CLI invocation, build, test, formatter, file
   operation, structured query, API call, or other deterministic action.
2. Use `if`, `condition`, `loop`/`break`, `parallel`, `fan_out`, or `include`
   for orchestration. Never ask an agent to simulate control flow.
3. Use `manual` for binary human judgment or authorization between steps.
4. Use `agent` only for work whose correct execution requires interpreting
   ambiguous context, synthesizing a plan or prose, modifying code based on
   intent, or reviewing unstructured material.

Before keeping an `agent` step, complete this test: "This needs an agent because
___ cannot be decided reliably by a command or control-flow step." Replace the
step if the blank cannot be filled concretely. Git operations, compilation,
tests, linting, formatting, copying, releases, and deterministic checks are not
agent work.

Prefer a cheap command probe and guard before an expensive agent. Use a bounded
loop for probe → break → repair, not retries as iteration. Keep model and effort
unset unless the task requires a specific capability. Do not create separate
agent sessions for trivial phases; every agent step and retry is a fresh
session. Split only when an intermediate result is independently useful,
separately checkable, or must feed a later prompt.

Calculate a conservative upper bound for automatic agent sessions before a
human manually retries anything. Count `1 + max_retries` per agent, multiply
loop bodies by their maximum iterations, add parallel members and fan-out lanes,
expand includes, and include an agent merge resolver. If referenced workflows
or dynamic structure cannot be inspected, report the envelope as unknown rather
than guessing. See the cost reference for the full rules.

## Choose the human mechanism deliberately

- Use `manual` only for a binary approve/reject decision between steps. It
  returns no arbitrary value or credential, puts the task in `awaiting_gate`,
  and releases its concurrency slot.
- Use declared task `fields` for typed choices known when the task is created.
  Vincent has no generic between-step form that returns a new value.
- Use `on_input: require` only when an agent must ask a question and continue
  the same reasoning session. It requires an adapter that supports mid-run
  input and cannot be nested in `parallel` or `loop` or used by a merge
  resolver. Codex and Cursor do not support it.
- `on_input: wait` keeps the agent process and task slot alive while waiting;
  it is not a substitute for a deliberate approval gate.

Place a manual gate immediately before the effect it authorizes. Make the
instructions name the artifact or change to inspect and the next action that
approval permits.

## Make long steps report on themselves

A step that runs for many minutes is opaque to whoever is watching the board:
the row says `running` and nothing else. Any `agent` or `command` step can fix
that by running `vincent status "<one short line>"` from inside itself. The
message is shown live and the last value set stays on the finished attempt, so
it also answers "why did that fail" in words a failure reason cannot reach.

Vincent never asks an agent to do this. Add the instruction yourself, and only
where it pays for itself:

- A `command` step calls it directly between phases of its script.
- An `agent` step needs it in the prompt. Ask for a status before each
  significant phase, under ten words, and specifically for a status naming what
  is actually wrong when something fails — "3 tests red in internal/store", not
  "working on it".

Add it to steps that take minutes or that a human is likely to be waiting on,
not to every step. The message is flattened to one line and truncated to 256
bytes, two messages within a second coalesce, and it is never a failure reason
and never readable by an `if:` guard or `.Steps` — it is for humans watching.

Assume task fields, rendered prompts, instructions, command output, and agent
transcripts are persisted or inspectable. Never put a secret in them. Use
preconfigured environment credentials, credential stores, or authenticated
CLIs, and avoid commands that echo sensitive values. For an external effect,
prefer preflight or dry-run → optional manual approval → one effect attempt → a
separate read-only postcondition check. Set `max_retries: 0` when replay is not
provably safe. After an ambiguous failure, inspect remote state before any
human-triggered retry.

## Author and validate

1. Write the workflow to `.vincent/workflows/<descriptive-name>.yaml` unless
   the user specifies another registry location.
2. Give every step a meaningful, unique `id`. Keep templates defensive and
   quote YAML scalars that contain `:`.
3. Put a `check` on agent or command steps that change verifiable state. A
   prompt's claim of success is not verification.
4. Make loops bounded, concurrent writes disjoint, fan-out lanes independent,
   and platform assumptions explicit.
5. Run `vincent version`, then `vincent workflow validate <file>`. Use `--json`
   when structured output helps. Resolve warnings as design findings, not just
   syntax noise.
6. If the binary is unavailable, say validation was not run; do not substitute
   a generic YAML parser or claim the workflow is valid.
7. Calculate and review the maximum automatic agent-session envelope.

Return a compact design summary with:

- the created or edited path;
- vincent version, schema source, and validator result;
- every retained agent step's necessity, check, and maximum automatic sessions;
- the workflow's total session envelope and any unknown dynamic contribution;
- task fields, binary manual gates, and live interaction separately;
- external effects, credential source category, retry policy, and postcondition;
- material assumptions and unresolved warnings.

When reviewing an existing workflow, lead with concrete correctness, safety,
and cost findings before offering a revised file.
