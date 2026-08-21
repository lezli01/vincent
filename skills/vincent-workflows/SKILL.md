---
name: vincent-workflows
description: Create, edit, review, and validate Vincent workflow YAML under .vincent/workflows. Use for Vincent workflow fields, step selection, templates, checks, human gates, retries, conditions, loops, parallel work, fan-out, includes, or validation errors. Do not use for GitHub Actions or other workflow systems.
license: LICENSE.txt
metadata:
  author: lezli01
---

# Vincent Workflows

Design the smallest reliable Vincent workflow for the requested outcome. Spend
agent tokens only where reasoning is part of the work; express everything else
with deterministic steps and Vincent's native control flow.

## Gather only decisions that matter

Inspect the repository, its instructions, and existing workflows before asking
questions. Infer obvious answers and state material assumptions. Ask a compact
set of questions only when an answer changes the YAML, especially:

- What deliverable ends the run, and which commands prove it is correct?
- Which task inputs are required, optional, typed, or pattern-constrained?
- Which host platforms and shells must the commands support?
- Does any step publish, deploy, push, delete, spend money, mutate an external
  service, or otherwise become difficult to reverse?
- Where does a person need to judge quality, authorize an effect, supply a
  secret outside the workflow, or choose among alternatives?
- Must a person answer an agent during the same reasoning session, or is a
  between-step approval gate sufficient?
- Can independent work safely share one worktree, or does it require isolated
  child branches? Is the extra concurrency worth its compute and merge cost?
- Which failures are observations, which should retry, and which should block?

For an external or irreversible effect, ask a concrete gate question such as:
"The workflow can publish the release. Should it pause for approval immediately
before publishing, run that step unattended, or omit publishing?" Do not add a
ritual gate when the user has already made the policy clear.

## Read the relevant references

Read [references/workflow-schema.md](references/workflow-schema.md) before
writing or reviewing YAML. It contains the supported fields, nesting rules,
template context, and validation contract.

Also read
[references/control-flow-and-cost.md](references/control-flow-and-cost.md) when
the workflow involves branching, repetition, concurrency, composition,
side-effects, human interaction, or more than one agent step. Use its patterns
and cost review before finalizing the design.

## Choose the cheapest correct primitive

Apply this order for every unit of work:

1. Use `command` for a known CLI invocation, build, test, formatter, file
   operation, structured query, API call, or other deterministic action.
2. Use `if`, `condition`, `loop`/`break`, `parallel`, `fan_out`, or `include`
   for orchestration. Never ask an agent to simulate control flow.
3. Use `manual` for human judgment or authorization between steps.
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

## Choose the human mechanism deliberately

- Use `manual` for review, approval, credentials, or choices between steps. It
  puts the task in `awaiting_gate` and releases its concurrency slot.
- Use `on_input: require` only when an agent must ask a question and continue
  the same reasoning session. It requires an adapter that supports mid-run
  input and cannot be nested in `parallel` or `loop` or used by a merge
  resolver. Codex and Cursor do not support it.
- `on_input: wait` keeps the agent process and task slot alive while waiting;
  it is not a substitute for a deliberate approval gate.

Place a manual gate immediately before the effect it authorizes. Make the
instructions name the artifact or change to inspect and the next action that
approval permits. Set `max_retries: 0` on non-idempotent side effects such as
publish, push, release, comment, or delete.

## Author and validate

1. Write the workflow to `.vincent/workflows/<descriptive-name>.yaml` unless
   the user specifies another registry location.
2. Give every step a meaningful, unique `id`. Keep templates defensive and
   quote YAML scalars that contain `:`.
3. Put a `check` on agent or command steps that change verifiable state. A
   prompt's claim of success is not verification.
4. Make loops bounded, concurrent writes disjoint, fan-out lanes independent,
   and platform assumptions explicit.
5. Run `vincent workflow validate <file>`. Use `--json` when structured output
   helps. Resolve warnings as design findings, not just syntax noise.
6. If the binary is unavailable, say validation was not run; do not substitute
   a generic YAML parser or claim the workflow is valid.

Return the created or edited path, the validator result, material assumptions,
and a short explanation of every retained agent step and human gate. When
reviewing an existing workflow, lead with concrete correctness, safety, and
cost findings before offering a revised file.
