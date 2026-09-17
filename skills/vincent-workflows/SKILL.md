---
name: vincent-workflows
description: Create, edit, review, and validate vincent workflow YAML under .vincent/workflows. Use for vincent workflow fields, step selection, templates, checks, human gates, retries, conditions, loops, parallel work, fan-out, includes, or validation errors. Do not use for vincent event triggers under {config_dir}/triggers (use vincent-triggers), GitHub Actions, or other workflow systems.
license: LICENSE.txt
metadata:
  author: lezli01
  version: 1.1.1
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
loop bodies by their maximum iterations, add parallel members and fan-out lanes
— a derived lane list counts as its `max_lanes`, so it is bounded rather than
unknown — expand includes, and include an agent merge resolver. If referenced workflows
or dynamic structure cannot be inspected, report the envelope as unknown rather
than guessing. See the cost reference for the full rules.

## Choose the human mechanism deliberately

- Use `manual` only for a binary approve/reject decision between steps. It
  returns no arbitrary value or credential, puts the task in `awaiting_gate`,
  and releases its concurrency slot.
- Use declared task `fields` for typed choices known when the task is created.
  Vincent has no generic between-step form that returns a new value. A field
  whose legal values are a fixed set is `type: enum` with `values:`, not a
  `string` with a pattern spelling the same alternation and not a set restated
  in the description — only a published list becomes a picker in New task. Add
  `multiple: true` when more than one may be chosen, and `default:` (valid on
  any type) when there is an obvious value, so a scripted caller need not
  restate it.
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

A `command` step's **stdout** is the half of it later steps read:
`.Steps.<id>.Result` is that step's stdout alone, never its stderr. Print on
stdout whatever a `for_each:` must split into items or a later prompt must
quote, and leave headers, counts and progress notes on stderr, where the
transcript and the step's summary still carry them for a person. Most tools —
`git`, `go`, `curl` — write their diagnostics to stderr, so a step whose failure
text a repair prompt is meant to read has to send them to stdout deliberately.

That stdout tail is the last **200 lines or 256 KiB**, whichever binds first,
and it cuts by dropping whole leading lines — so an over-long `for_each:` list
arrives shorter rather than with a half-written item at its edge, unless a
single item is itself over 256 KiB. Size a list
against both: a hundred JSON objects of 3 KiB each is under the line count and
over the byte one. Have the producer filter or page rather than lean on the
tail.

Assume task fields, rendered prompts, instructions, command output, and agent
transcripts are persisted or inspectable. Never put a secret in them. Use
preconfigured environment credentials, credential stores, or authenticated
CLIs, and avoid commands that echo sensitive values. For an external effect,
prefer preflight or dry-run → optional manual approval → one effect attempt → a
separate read-only postcondition check. Set `max_retries: 0` when replay is not
provably safe. After an ambiguous failure, inspect remote state before any
human-triggered retry.

## Know every key before you write one

This index names every key a workflow file may carry, with when it earns its
place; the validator rejects any other. Types, defaults, and edge rules live
in the schema reference. Leave a key out when its default already says what
you mean — every key written is one a reviewer must check.

**Top level.** `name` (the registry key) and `steps` are required;
`description` is the picker text. `platforms:` (`linux`, `darwin`, `windows`,
`posix`) restricts the whole workflow to the hosts its `run:` bodies are
written for — declare it rather than let a POSIX-only body fail on Windows,
and use an `if:` on `.Host.OS` for a single step instead. `fields:` declares
task inputs; `defaults:` sets step fallbacks.

**A declared field.** `name` and `type` are required, then `required`,
`pattern`, `values`, `multiple`, and `default` as described above.
`description` is the help text; `label` is the text a form shows instead of
the name, worth setting when the name is a slug nobody would read easily.

**Every step.** A unique `id` and a `type`; `name` when the id does not explain
itself; `if` to guard it.

- Failure policy. `max_retries` counts attempts after the first (default 1),
  and each agent retry is another session in the envelope.
  `allow_failure: true` (agent and command only) when a red result is data a
  later guard reads. `retry_backoff` only when the failure is transient — a network call,
  a lock held elsewhere — since an immediate retry of a deterministic failure
  fails the same way. Both retry fields bind to an attempt: `manual`,
  `parallel`, `condition`, `break`, `loop`, and `include` refuse them, so put
  a group's retries on its sub-steps.
- Time bounds. `timeout` ends an attempt (daemon defaults: 60m agent, 15m
  command); on a `parallel` or `loop` it bounds the whole group. Tighten it
  where a hang is likelier than slow work. `check_timeout` bounds the `check`
  on its own and defaults to the daemon's command timeout, never the step's
  `timeout`. `input_timeout` bounds each wait in `awaiting_input` (default
  24h); shorten it on a step whose question nobody may be there to answer,
  because the slot is held for the whole wait.

**Agent step.** `prompt`; `agent`, `model`, and `effort` only for a specific
capability (Cursor has no effort); `on_input`, `check`, `check_timeout`.
`permission_mode: restricted` for a step that needs no writes or approval-gated
actions — a review, a plan, a triage. Denied actions become `permission` input
requests under `on_input`, and Cursor cannot restrict on Windows. Never widen a
step to `full-auto` for convenience.

**Command step.** `run`; `check`, `check_timeout`. `shell` (`sh`, `pwsh`, or
`cmd`) only when a body truly needs one shell: a pinned shell that is missing
fails rather than falls back, so pair it with `platforms:` or a `.Host.OS`
guard. `env` adds non-secret configuration a command reads, such as
`CI: "true"`; it is never for a secret, since the file and the rendered step
are inspectable. Credentials come from the daemon's environment.

**Structure.** `manual` takes `instructions`; `condition` and `break` take
`if`; `include` takes `workflow`. `parallel` takes `steps` and `max_parallel`,
which caps processes started at once inside this one task (default 4), is not
governed by the daemon's concurrency caps, and never shrinks the envelope:
every member still runs. `loop` takes `steps`, one of `count` or `for_each`,
and `max_iterations` (default 10) — the multiplier the envelope counts for its
body, so set it to the real bound; exceeding it blocks.

**Fan-out.** `lanes`, or a `lane` template with `for_each` and `max_lanes`;
`schedule`; `merge`. A lane takes `id`, one of `workflow` or `steps`, `if`,
`needs`, and `fields` (a map handed to the child task). `agent`, `model`,
`effort`, and `priority` on a lane override the inherited selection and
scheduler priority for its whole subtree — only for a lane that needs another
capability or must be admitted ahead of its siblings.
`merge.on_conflict: block` (the default) stops for a person at no agent cost; `on_conflict: agent`
resolves with `merge.agent`, a complete agent step that counts as one more
session in the envelope and cannot use `on_input: require`. Choose it only
when conflicts are expected, mechanically reviewable, and checked.

**Defaults.** `defaults:` takes `agent`, `model`, `effort`, `permission_mode`,
`on_input`, `input_timeout`, `max_retries`, `retry_backoff`, and `timeout`,
which a step's own value overrides — set one when most steps share the value,
not to restate the daemon's. It also takes `container`, which runs the
workflow's agent steps, command steps and checks in a container and merges per
key over the daemon's `container:` config: `image` is the switch (`""` forces
the host), `runtime` names the CLI (`docker`, `podman`), `mount_agent_config`
mounts the agent CLI's credentials, `network` gives the container a network,
and `extra_mounts` adds host paths. Add a container only when the user asks for
one — which image a project runs in is a deployment decision. With an image,
`run:` bodies use the image's `/bin/sh` (`shell: pwsh` or `cmd` is refused),
the image must carry the agent CLI, and `platforms:` still gates on the
daemon's host. `network: false` on a workflow with an agent step is refused at
task creation while the daemon wires MCP into steps. Inside a container
`vincent status` does not work, so ask an agent for the `step_status` tool
instead.

`derived_from` and `resolved_from` appear only in a task's workflow snapshot,
written by the daemon. Never author them.

## Author and validate

1. Write the workflow to `.vincent/workflows/<descriptive-name>.yaml` unless
   the user specifies another registry location.
2. Give every step a meaningful, unique `id`. Keep templates defensive and
   quote YAML scalars that contain `:`.
3. Put a `check` on agent or command steps that change verifiable state. A
   prompt's claim of success is not verification.
4. Make loops bounded, concurrent writes disjoint, fan-out lanes independent —
   or ordered with `needs` when one truly depends on another's merged work —
   and platform assumptions explicit. A `needs` DAG runs in barrier rounds by
   default. Add `schedule: eager` only when the lanes are genuinely independent
   of each other's files and the wall-clock saving is worth the price: under
   `eager` a lane starts when its own dependencies merge, so what else happened
   to be on the branch by then is a stopwatch question and a re-run can produce
   a different result. `barrier` is the reproducible default; a flat lane list
   is a barrier either way.
5. Run `vincent version`, then `vincent workflow validate <file>`, then
   `vincent workflow render <file>`. Use `--json` when structured output helps.
   Resolve warnings as design findings, not just syntax noise.
6. `validate` parses templates; `render` executes them, which is the only way a
   typo'd `{{.Task.Titel}}`, an unsupplied `{{.Task.Fields.ticket}}` or a
   `{{.Steps.plan.Reslt}}` is caught before a task is created. Both run without
   a daemon. Read the rendered prompts: placeholders such as `<worktree>` and
   `<steps.plan.result>` are the preview, not what the agent receives.
7. If the binary is unavailable, say validation was not run; do not substitute
   a generic YAML parser or claim the workflow is valid.
8. Calculate and review the maximum automatic agent-session envelope.

Return a compact design summary with:

- the created or edited path;
- vincent version, schema source, and the validate and render results;
- every retained agent step's necessity, check, and maximum automatic sessions;
- the workflow's total session envelope and any unknown dynamic contribution;
- task fields, binary manual gates, and live interaction separately;
- external effects, credential source category, retry policy, and postcondition;
- material assumptions and unresolved warnings.

When reviewing an existing workflow, lead with concrete correctness, safety,
and cost findings before offering a revised file.
