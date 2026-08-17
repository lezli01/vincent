# 013 — Gating workflows that require mid-run interaction

**Status:** ✅ done (8/8) · **Opened:** 2026-08-17

An agent step may now declare that it *needs* a human mid-run:

```yaml
steps:
  - id: clarify
    type: agent
    on_input: require
    prompt: Ask me whatever you need before you start.
```

A step that says `require` will only ever run on an adapter that can stop the
run and wait for an answer. A workflow pinning an adapter that can never do so
fails to load; a task whose agent selection cannot satisfy such a step is
refused at creation; a snapshot that somehow reaches the engine anyway fails the
step with `input_unsupported`.

## The problem

§7.4 defines `on_input: wait | deny` as *the step's reaction to a mid-run input
request*, and §9.5 makes `supports_input` a per-adapter fact — true for claude
inside the verified version family, permanently false for codex and cursor,
which have no control channel at all. The two never meet: `on_input` is
"ignored by adapters without input support", so a workflow whose whole point is
a conversation runs silently non-conversationally on two of three adapters.

The author of such a workflow has no way to *say* that the conversation is the
point. The three shipped GitHub workflows document the gap in comments — "this
is ignored by codex and cursor, so on those adapters the questioning step just
proceeds" — which is the right call for those files and the wrong one for a
workflow that exists to ask you which of three designs you want. There, an
agent that cannot ask does not degrade; it guesses, and the run is worthless in
a way nothing reports.

This is the same shape as 010: a fact the author knows when writing the file,
discoverable today only by watching a run not do what it was for.

## Decisions

### 1. One capability, not a vocabulary

*2026-08-17.* `on_input: require` gates on exactly one thing: whether the
adapter can pause the run and take a human answer. No `requires:` list, no
capability tokens.

**Beat:** a general `requires: [input, cost_reporting, thinking]` mechanism.
Adapters differ in several documented ways (§9.3, §9.7 — codex reports no cost,
cursor has no effort concept), so a vocabulary is imaginable. None of those
differences can make a run *worthless*, which is the property that earns a
gate, and `supports_input` is the only capability `Availability` actually
carries. A second capability can generalize this later; designing tokens for
facts no code consumes is how a schema grows a taxonomy nobody uses.

Question-kind and permission-kind requests are one capability, not two: they
ride the same control channel, and no adapter has one without the other. So
there is nothing finer to gate on — "can it stop and ask me" is the whole
question, and the structured-question modal is what it drives.

### 2. A third value on `on_input`, per step

*2026-08-17.* `require` joins `wait` and `deny` on the existing field, settable
on a step and in `defaults:`.

**Beat (a):** a separate `requires_input: true` boolean, which keeps
`on_input`'s definition as a pure reaction. It is the more honest reading —
`require` is a precondition, and only a reaction *after* the run starts — but it
costs a second key plus a contradiction check between the two, to express what
one enum value expresses with neither.

**Beat (b):** a file-level `requires_input:` mirroring `platforms:`. 010's
restriction is whole-file *by nature* ("this file is POSIX"); "this step asks
questions" is not. Agent selection already resolves per step (step → task
override → defaults), so a per-step declaration composes with the resolution
that actually happens: a workflow can run its build on codex and its design
review on claude, and only the second one constrains anything.

### 3. The capability is static in the catalog, and detected at probe time

*2026-08-17.* `agent.Options` gains `InputSupport`: `never` (codex, cursor — no
control channel exists, ever) or `detected` (claude — the §9.5 version gate
decides). `Curated()` returns it without probing, which is what §8.2 validation
already reads.

Two values, not three. There is no adapter that supports input regardless of
version, so an `always` would be a value nothing sets.

**Beat:** a new `Adapter.Capabilities()` method. §8.2 already reads `Curated()`
and deliberately never spawns a process; putting the fact where the validator
is already looking costs no new interface surface. It does not reach the wire —
`agentResponse` copies `Models`/`Efforts`/defaults field by field, so
`GET /v1/agents` keeps answering the live question with `supports_input` alone.

### 4. Load time decides what is decidable there

*2026-08-17.* A step that both requires input and pins (or inherits) an adapter
whose `InputSupport` is `never` is a §8.2 **error**. A step resolving to an
adapter whose support is `detected` is not judged at load: the answer depends on
the installed binary, and the validation path never probes.

This is the cross-catalog rule (§8.2) applied unchanged — a value belonging to
another adapter is an error, a value nobody knows about passes with the CLI as
final authority. A workflow that can never work should fail where its author is
looking at the file.

The error points at the **agent** field, not at `on_input`: `steps[2].agent`
when the step pins it, `defaults.agent` when it inherits, collapsing duplicates
the way `findingPath` already does for catalog findings. The requirement is what
the author meant; the agent is what is wrong.

### 5. Creation refuses a *known* incapable selection, never an unknown one

*2026-08-17.* `POST /v1/tasks` consults the binary-identity cache
(`CatalogCache.Entry`) and rejects only on a positive "this adapter does not
support input" verdict. An absent binary, a failed probe or an unparseable
version allows the task through; the run-time gate is the backstop.

**Beat:** strict rejection on anything short of a positive yes. §9.6's
degrade-never-block rule exists because one cold-logon probe timeout made a
healthy CLI look dead for a whole daemon lifetime (T4.22); paying for that with
refused task creation is worse than a step that blocks at run time naming its
reason. A missing agent binary is already not a creation-time error.

### 6. Only steps that would actually use the picked agent constrain the picker

*2026-08-17.* The task-level agent choice is restricted only when at least one
requiring step leaves its `agent:` unpinned — i.e. when the choice being made is
the one that step will resolve to. A workflow whose requiring steps all pin a
capable adapter imposes no restriction at all.

The gate matches real §8.6 resolution rather than a conservative approximation
of it. Restricting the picker because of a step that ignores the picker refuses
a task that would have run perfectly, which is the one outcome a gate must never
produce. The same rule feeds the creation-time 400 and the `requires_input` flag
on the workflow entry.

### 7. `deny` still overrides an inherited `require`

*2026-08-17.* `defaults.on_input: require` with a step's `on_input: deny` is
legal, and the step wins.

Every field in `defaults:` works this way; a special case here would make this
the only field in the schema a step cannot override. The combination is also
meaningful on its own — a long unattended cleanup step inside an otherwise
interactive workflow.

`require` and `deny` cannot contradict each other *within* a step, because
decision 2 made them values of one field rather than two fields.

### 8. Run time: a pre-flight in the engine, failing the step

*2026-08-17.* The runner gains the `*agent.CatalogCache` in its `Deps`, checks
before `Start`, and returns `stepFailed` with the new reason
`input_unsupported`. `require` reaches the adapter as `InputWait`;
`agent.InputPolicy` stays two-valued and the `Adapter` contract is untouched.

`require` is a *scheduling* precondition. Once a step is running, `require` and
`wait` are the same behaviour, so the adapter has no reason to know which one it
was given.

**Beat (a):** an `agent.ErrRestrictedUnsupported`-shaped sentinel returned from
`Start`. That pattern earns its place for `restricted` because only the adapter
knows its own platform story; here the daemon already holds the answer in a
cache it shares with the API and with task creation.

**Beat (b):** blocking the task outright without spending the §7.2 budget, on
the grounds that a retry cannot change a capability. It ends in the same blocked
state with the same reason, one attempt later, and the codebase has already
refused this trade once: the `agent_unauthenticated` note rejects
short-circuiting §7.2 "to save one process spawn" — and here the check precedes
`Start`, so there is not even a spawn to save.

Reaching this at all means the task and its daemon parted company: claude
upgraded past the version ceiling, a data directory moved between machines, or a
workflow edited after the task was queued. Distinct from `agent_unavailable` on
the same grounds `restricted_unsupported` is: the CLI is installed and healthy.

### 9. `require` does not change how long a human has

*2026-08-17.* `input_timeout` keeps its §7.4 meaning and its three levels (step,
defaults, daemon). Requiring input does not imply a longer or unbounded wait.

A workflow that wants to wait all day says so in the field named for it.
Coupling the bound to the capability gate would give `require` two unrelated
meanings and make the timeout unpredictable from the field that sets it.

### 10. The shipped workflows stay on `wait`

*2026-08-17.* `github-issue.yaml`, `github-bug.yaml` and
`github-enhancement.yaml` keep `on_input: wait`; only their comments are updated
to mention that `require` now exists.

Those files degrade deliberately — the comments document it as intended — and
flipping them would make three shipped workflows unusable on two of three
adapters as a side effect of adding a feature. Adopting `require` there is a
per-file decision, not part of this work.

### 11. The daemon publishes the verdict; no client re-derives it

*2026-08-17, during implementation.* `GET /v1/agents` gained
`input_verdict`: `supported` | `unsupported` | `unknown`.

Decision 3 established that the static capability need not reach the wire, and
that remains true — what reaches it is the *verdict*, which is a different
thing. It had to, because the refusal rule is asymmetric (decision 5) and
`supports_input` alone cannot express it: `false` there means "no" for an
installed binary and "nobody can say" for an absent one, and only the first
refuses anything. A client re-deriving that from `available` + `supports_input`
would get the codex-not-installed case wrong and grey out nothing where the
daemon greys out an agent. Publishing the verdict keeps 010.3's rule intact —
the process that would run the thing is the one that says whether it can.

### 12. `edit + retry` cannot change an agent, so the gate lives on `retry`

*2026-08-17, correcting the premise of 013.4.* The plan assumed the repair for
an `input_unsupported` block was to edit the step's `agent:` in the task's
snapshot. It is not: `POST /v1/tasks/{id}/retry` overrides `prompt` and `run`
only (`store.Override`), and no endpoint changes `agent_override` after
creation. There was no agent edit to re-validate.

The real repair is environmental — install the agent, or bring claude back
inside the verified family — so the check moved onto the retry action itself,
against the task's own snapshot: a retry that would only reproduce the block is
refused with the reason, and once the environment is fixed the same retry
passes. That makes the check *more* useful than the planned one, not less; it
is also the only place the asymmetry pays off twice, since an agent uninstalled
between the block and the retry reads as unknown and is let through.

## Tasks

- [x] **013.1 — Schema and static capability.** ✓ 2026-08-17 `on_input: require` accepted on
  agent steps and in `defaults:`; `agent.Options.InputSupport` (`never` |
  `detected`) set by all three adapters' curated catalogs; §8.2 errors on a
  requiring step resolving to a `never` adapter, attributed per decision 4.
- [x] **013.2 — Registry and API.** ✓ 2026-08-17 A computed `requires_input` on the
  `GET /v1/workflows` entry, derived by the decision-6 rule (daemon decides,
  clients report — 010.2's precedent, including the `*bool`-style tolerance for
  an older daemon).
- [x] **013.3 — `POST /v1/tasks` refuses a known-incapable selection.** ✓ 2026-08-17
  Depends: 013.1, 013.2. 400 naming the step, the agent, and why; unknown and
  absent probes pass through (decision 5).
- [x] **013.4 — `retry` re-validates.** ✓ 2026-08-17 Depends: 013.3. The snapshot edit
  re-runs the creation-time check, so the repair path cannot land a task back
  into the same block.
- [x] **013.5 — Engine pre-flight.** ✓ 2026-08-17 Depends: 013.1. `*agent.CatalogCache` in
  `taskrun.Deps`, the check before `Start`, `ReasonInputUnsupported =
  "input_unsupported"` in the §18 vocabulary.
- [x] **013.6 — Clients.** ✓ 2026-08-17 Depends: 013.2, 013.3. TUI: the agent picker
  annotates an incapable adapter with the reason and refuses it on select
  (010.5's `opt.note` + row-error pattern); the new-task summary says why. CLI
  relays the API's 400 with no client-side rule of its own.
- [x] **013.7 — Docs.** ✓ 2026-08-17 Depends: all. Spec §7.4 (the third value), §8.2 (the
  new rule), §9.5/§9.6 (the static capability), §13.2 (the entry field) and §18
  (the reason), amended in place with dated notes; user-facing
  `reference/workflow-schema.md`, `guides/workflows.md`, `guides/agents.md`,
  `reference/api.md` and the block-reason table; comments in the three shipped
  workflows (decision 10).
- [x] **013.8 — Gate leg.** ✓ 2026-08-17 Depends: 013.3. One `m2-gate.sh` scenario asserting
  the creation-time 400 against a fake agent presenting as a `never` adapter.
  The run-time block stays in `engine_test.go`, where 010 put its equivalent:
  reaching it requires manufacturing a snapshot/daemon mismatch, which a curl
  script should not be in the business of staging.

## Verification

*2026-08-17, all run:*

- `go run mage.go test` and `go run mage.go testrace` green, including the new
  cases: `require` accepted on agent steps and in `defaults:` and rejected on
  command steps, the unknown-value error naming all three, the §8.2 capability
  error and its attribution to both `steps[N].agent` and a collapsed
  `defaults.agent`, claude deliberately *not* judged at load, the decision-6
  derivation across pinned/unpinned steps, `InputMismatch` resolving through
  §8.6 overrides, the seven-case `InputVerdict` table (including both
  not-installed cases, which must answer unknown), the three adapters' curated
  `InputSupport`, the creation-time 400 plus the uninstalled-agent case that
  must *not* 400, the retry refusal, the engine's `input_unsupported` block
  with no pid recorded, the capable-agent run that still succeeds, and the two
  TUI paths (picker note and disablement, agent-row error on submit).
- `go tool golangci-lint run ./...` clean for `GOOS=linux`, `darwin` and
  `windows` from a host-built linter, per CLAUDE.md.
- `./scripts/m2-gate.sh` green, scenario 8 included: the impossible workflow
  listed with its error pointing at `steps[0].agent`, `requires_input` true on
  the runnable one and false on `adhoc`, `input_verdict` published per adapter,
  the 400 on a codex task, and the same workflow running to `done` on claude.

The gate script's new scenario is committed executable, and its dispatcher
accepts `VINCENT_GATE_SCENARIO=8`.
