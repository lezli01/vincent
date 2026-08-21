# Control flow, human gates, and AI cost

Use this reference to turn intent into an efficient execution graph. Optimize
for total reliable work: deterministic actions should not consume agent
sessions, while genuine reasoning should not be forced into brittle shell code.

## Step decision table

| Need | Prefer | Avoid |
|---|---|---|
| Known CLI, build, test, lint, format, copy, structured API call | `command` | An agent narrating or typing the command |
| Skip one step and continue | `if` | Prompting an agent to decide whether to act |
| End the successful run when there is no work | `condition` after a probe | Running every later step anyway |
| Observe a nonzero result and branch | `allow_failure` command, then a guard | A failing probe that blocks the task |
| Repair until a deterministic check passes | Bounded `loop`: probe → `break` → repair | High retries on a repair agent |
| Run independent read-only or disjoint work in one worktree | `parallel` | Sequential execution without a dependency |
| Isolate concurrent branches and merge later | `fan_out` | Parallel writers in one worktree |
| Reuse a stable workflow fragment | `include` | Copying steps into every workflow |
| Human review or authorization between steps | `manual` | `on_input` or a long-running idle agent |
| A question must continue the same reasoning session | Supported agent with `on_input: require` | A manual gate that loses necessary conversation state |
| Interpret ambiguity, modify code from intent, synthesize, review prose | `agent` with a concrete prompt and check | Encoding semantic judgment in fragile shell heuristics |

Control flow is orchestration, not agent work. A prompt such as "run the tests,
and if they fail fix them, otherwise publish" hides a probe, branch, repair,
gate, and side effect inside one expensive and hard-to-audit session. Represent
those decisions as visible steps.

## Ask questions at design forks

Ask only unresolved questions whose answers alter the graph. Group related
questions and offer concrete choices.

### Outcome and verification

- What must exist or change when the workflow succeeds?
- Which deterministic command proves each writable step worked?
- Is a red test expected input to a repair cycle, or a blocking failure?

If no acceptance command is known, ask for one before inventing an agent-based
review. A subjective output can legitimately require a manual review.

### Inputs and portability

- Which values should be declared fields rather than embedded in prompts?
- Which are required or constrained, and which may be omitted?
- Must the workflow run on POSIX, Windows, or both?
- Are external tools guaranteed on the target hosts?

Do not claim cross-platform support for shell syntax that was not ported and
tested. Restrict `platforms`, split commands by `.Host.OS`, or ask the user to
choose a supported target.

### Side effects and human authority

Identify push, publish, deploy, release, delete, payment, notification, issue
mutation, production access, and other external or hard-to-reverse effects.
Ask, for each distinct authority boundary:

- Should the workflow stop immediately before this effect?
- What should the reviewer inspect?
- May it run unattended when the requested fields or conditions are present?
- Is dry-run or preview output available before approval?

Default recommendation: generate and verify locally, expose the diff or preview,
then place one `manual` gate directly before a consolidated side-effect step.
Respect an explicit unattended policy; make it visible in the final explanation.

### Interaction mode

Ask whether a human must answer during the agent's live reasoning. If the person
only approves an artifact or supplies a choice used by the next fresh step, use
`manual`. If the same agent session must incorporate the answer, verify a
question-capable adapter and consider `on_input: require`.

Claude can support mid-run questions. Codex and Cursor cannot. `on_input: wait`
keeps a process and task slot alive, whereas `manual` parks the task and releases
the slot. `on_input: require` is invalid in parallel groups, loop bodies, and
merge resolvers.

### Concurrency and budget

- Are units truly independent?
- Do they write the same files?
- Does each unit justify another agent session?
- Is branch isolation worth child-task scheduling, worktrees, and merge risk?
- What machine-level `max_parallel` is safe?

Use concurrency for wall-clock benefit, not as a default. Multiple independent
command checks are cheap parallel candidates. Multiple agent reviewers often
multiply spend without adding distinct evidence; give each a non-overlapping
purpose or keep one.

## Cost-aware patterns

### Probe before invoking an agent

Run a cheap deterministic probe, treat its nonzero result as data, and stop the
workflow successfully when there is no work.

```yaml
steps:
  - id: probe
    type: command
    run: git diff --quiet HEAD~1
    allow_failure: true
    max_retries: 0

  - id: changes-exist
    type: condition
    if: '{{ ne (index .Steps "probe").ExitCode 0 }}'

  - id: review
    type: agent
    prompt: |
      Review the current diff for correctness and make necessary fixes.
      Preserve the intended behavior and report the files changed.
    check: git diff --check
```

This spends no agent session when the diff is empty.

### Converge with a bounded loop

Retries repeat a failed attempt. Iterations represent a successful observation
followed by another cycle. Use the latter for repair:

```yaml
steps:
  - id: converge
    type: loop
    count: 4
    max_iterations: 4
    steps:
      - id: suite
        type: command
        run: go test ./...
        allow_failure: true
        max_retries: 0

      - id: green
        type: break
        if: '{{ eq (index .Steps "suite").ExitCode 0 }}'

      - id: repair
        type: agent
        prompt: |
          The test suite failed:

          {{ (index .Steps "suite").Result }}

          Fix the underlying defect. Do not weaken, skip, or delete tests.

  - id: verify
    type: command
    run: go test ./...
    max_retries: 0
```

The command decides whether repair is needed. The final command prevents the
loop ceiling from turning an unverified last repair into success.

### Gate immediately before an external effect

```yaml
steps:
  - id: build
    type: command
    run: ./scripts/build-release.sh
    check: ./scripts/verify-release.sh

  - id: approve-release
    type: manual
    instructions: |
      Inspect the release artifacts and verification output for task
      #{{.Task.ID}}. Approval permits the next step to publish them.

  - id: publish
    type: command
    run: ./scripts/publish-release.sh
    max_retries: 0
```

Prepare and verify before occupying a person's attention. Keep authorization
adjacent to the effect so later work cannot invalidate what was reviewed.

### Parallel checks in one worktree

```yaml
- id: checks
  type: parallel
  max_parallel: 3
  steps:
    - { id: test, type: command, run: go test ./... }
    - { id: lint, type: command, run: go run mage.go lint }
    - { id: vet, type: command, run: go vet ./... }
```

Use this for independent reads or disjoint outputs. Do not place two agents
that may edit the same source files in one parallel group. Siblings cannot use
one another's `.Steps` output.

### Fan out only for real branch isolation

Use `fan_out` when lanes are independently schedulable work products that need
their own branches, not merely because they can be named separately. Good
candidates are modules with non-overlapping ownership or a large task whose
parallel speedup outweighs merge cost. Prefer `parallel` for commands sharing
one worktree and sequential steps when outputs depend on each other.

Default `merge.on_conflict: block`. An agent conflict resolver adds another
session at the most safety-sensitive point; use one only when conflicts are
expected, mechanically reviewable, and protected by a strong check.

### Include stable mechanics

Put repeated build, test, or policy steps in a named workflow and `include` it.
Do not extract a fragment just to reduce a few YAML lines: included ids share
the caller's namespace, conditions can end the caller, and registry resolution
occurs only when a task is created.

## Agent-step design

Keep an agent step only with a concrete justification. Suitable reasons include:

- translating a goal across a codebase whose required edits are not known;
- synthesizing a design or prose from unstructured evidence;
- semantic review where rules alone do not define correctness;
- repairing a failure whose cause requires code reasoning.

Unsuitable reasons include running git, invoking a build, waiting for CI,
formatting, copying, uploading, publishing, checking an exit code, selecting a
branch based on known data, or retrying until a command passes.

For each retained agent:

1. Give it only the context and responsibility it needs.
2. Define done and prohibited shortcuts in the prompt.
3. Add a deterministic `check` when the result changes verifiable state.
4. Leave `model` and `effort` at inherited defaults unless a requirement
   justifies the override.
5. Avoid redundant reviewers. If two are needed, make their scopes distinct.
6. Remember that each retry and each loop iteration starts a fresh session.

## Final cost and safety review

Before returning a workflow, verify:

- Every deterministic operation is a `command`, not an agent prompt.
- Branching and repetition use native control flow.
- Cheap probes guard expensive steps and use `max_retries: 0`.
- Loops are bounded and end with deterministic verification when needed.
- Parallel substeps have no write collision; `max_parallel` fits the machine.
- Fan-out has enough isolated work to justify child tasks and merge overhead.
- Each agent has a one-sentence necessity and an objective completion signal.
- No model or high-effort override is present without a capability reason.
- Every external, destructive, or irreversible effect has the user-selected
  gate policy and `max_retries: 0` when replay is unsafe.
- `manual` is used for between-step authority; mid-run input is used only for
  essential same-session dialogue on a supported adapter.
- Templates read optional values defensively and do not assume parallel sibling
  output.
- `vincent workflow validate` passes without unresolved warnings.
