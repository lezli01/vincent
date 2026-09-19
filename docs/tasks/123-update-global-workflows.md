# 123 — Let `update-workflows` update global workflows through an approved proposal

**Status:** ✅ done (6/6)
**Opened:** 2026-09-19
**Issue:** #493
**Spec:** §5.2 (the `update-workflows` entry), §12.1 (`vincent workflow ls
--global`, `vincent workflow apply`), §12.2 (`workflow-proposals/`), §13.2
(`DELETE /v1/tasks/{id}`)

## Problem

The `update-workflows` built-in (task 037) maintains only the workflows a
project versions under `.vincent/workflows`. Decision 2 of that task put the
global registry, `{config_dir}/workflows`, out of scope on purpose: no
repository versions it, so an edit to it would be an effect outside the
reviewed diff, shared by every project. The global workflows therefore fell
behind the feature set with no built-in to bring them up to date.

`update-triggers` (task 098) already solved the same shape for triggers, whose
files also live in the config directory: the agent stages a proposal, a person
approves it at a manual gate, and a daemon-free `apply` installs it after
checks a prompt cannot talk its way past. This task gives `update-workflows`
that path for the global scope, and reopens 037 decision 2 (and narrows
decision 6) to do so. Both are amended in place.

## Decisions

1. **2026-09-19 — One scope per run.** A run updates either the project's
   workflows or the global ones, never both. The two are reviewed differently
   — a branch diff or a gated proposal — and one task does not mix them.
   Duplication and shadowing across the two scopes stay out of scope.
2. **2026-09-19 — Shared steps, then one trailing condition.** A step list is
   static and an `include` cannot be picked by a field, so a project run's
   "unchanged" means: every step it had renders byte for byte as before, and
   its only new row is one trailing `condition`, `global-only`, recorded
   `stopped`. `inventory`, the `modernize` prompt and `relist` branch on
   `{{ if eq (index .Task.Fields "global") "true" }}`; `has-workflows`,
   `validate` and `changes` are unchanged, so a global run with no global
   workflows ends `done` without an agent, and its `changes` row is the empty
   diff that shows the worktree was left alone. After `global-only` come
   `approve` (a `manual` step immediately before the effect), `apply`
   (`max_retries: 0`) and `result`, a final listing. The gate stands for an
   empty proposal too, as `update-triggers`' does. The global prompt keeps the
   skill, "The bar" and "What you may not change", and replaces what a global
   file changes: the deliverable is a staged proposal; never write into
   `{config_dir}/workflows`; clear your own staging directory first, so the
   one retry stays safe; the only evidence is the files, their comments and
   the validator, because they have no git history and the host project's
   repository says nothing about how they are used; checklist item 2's "two of
   this project's workflows" means two global ones. `TestUpdateWorkflowsProjectRunRendersAsBefore`
   holds the project branch to goldens frozen from the pre-123 built-in.
   *Beaten:* `if:` guards on the three new steps, which leave three `skipped`
   rows on every project run; and two fully guarded paths, which double the
   step list and show the other path as skipped.
3. **2026-09-19 — `vincent workflow ls --global`, daemon-free.** It reads
   `{config_dir}/workflows/*.y*ml` directly, as `vincent trigger ls` does (098
   decision 4), under §5.2's regular-file and 1 MiB bounds, prints one
   absolute path per line and exits 1 when there are none — the probe shape
   `has-workflows` reads. `--json` gives `file`, `name`, `version`
   (`workflow.Version`), `valid` and `errors`, and a file that does not parse
   is still listed so the pass can repair it. Without `--global`, `ls` stays
   daemon-backed; `--global` with `--project` is a usage error. *Beaten:* a
   daemon-backed `--global`, which could not run offline or in CI.
4. **2026-09-19 — Staging in `{data_dir}/workflow-proposals/<task_id>/`**,
   after 098 decision 5: `0700`/`0600`, outside every repository and outside
   the watched registry directory. Whole files named by their live base name,
   plus `manifest.json` mapping each base name to a version or `"absent"`.
   Apply removes it after installing; a rejected run's directory stays until
   the task is deleted, and `DELETE /v1/tasks/{id}` removes it next to
   `trigger-proposals/{id}`, under the same containment check.
   `internal/trigger` now takes its manifest and absent names from
   `internal/workflow`, which it already imported.
5. **2026-09-19 — `vincent workflow apply --proposal <task_id> [--check]`**,
   daemon-free like `vincent trigger apply`. Every check runs before any
   write; one refusal writes nothing and leaves staging untouched. It refuses
   a file that fails validation; files and manifest entries that do not pair
   one for one, or anything else in the directory; a stale version, a file
   recorded `absent` that exists, or a recorded file that is gone; a name that
   is not a bare base name, or a new file not named `FileName(name:)`; **a
   rename** (the author's call: it breaks every task, `include` and trigger
   that names the workflow, and the prompt already forbids it — skipped when
   the live file has no readable name); and **a duplicate name**, against
   another global file the proposal does not replace or another staged file
   (§5.2). Files are written with `workflow.WriteFile`, atomic per file, an
   existing file keeping its mode and a new one `0644`; a failure part-way is
   reported, not rolled back. `--check` writes and removes nothing and prints
   the staged paths — the global run's `relist`, so a bad proposal blocks
   before anyone reaches the gate. *Beaten:* a check-free `ls --proposal`,
   which defers every refusal until after approval.
6. **2026-09-19 — No new checklist line and no skill edit.** `ls --global` and
   `apply` are CLI verbs, not §8.2 schema features, so "The bar" does not
   change. The global mode's corrections live in the built-in's own prompt,
   per CLAUDE.md's rule, so the embedded skill text does not change.

## Work

- [x] 123.1 `internal/workflow/builtin.go`: the `global` field, the template
  branches, and `global-only`, `approve`, `apply`, `result`.
- [x] 123.2 `internal/workflow/proposal.go`: `ListGlobal` and
  `ApplyProposal` with their refusals.
- [x] 123.3 `internal/cli`: `vincent workflow ls --global` and
  `vincent workflow apply`.
- [x] 123.4 `internal/taskrun/delete.go`: a task delete removes
  `workflow-proposals/{id}`.
- [x] 123.5 `cmd/fakeagent`'s `stage-workflows` scenario and
  `scripts/123-gate.sh`, wired into `ci.yml`'s gates job on all three
  platforms.
- [x] 123.6 Spec §5.2, §12.1, §12.2 and §13.2, 037's decisions 2 and 6, the
  CLI reference, the workflows guide, the FAQ, CLAUDE.md and the changelog.

## Verification

- `TestBuiltinUpdateWorkflowsIsValid` asserts the field, the ten step ids,
  `approve` as a manual step before `apply`, `apply`'s `max_retries: 0` and
  the condition's guard.
- `TestUpdateWorkflowsProjectRunRendersAsBefore` compares the project run's
  `inventory`, `modernize` and `relist` renders, with `global` unset, `false`
  and an empty field map, to goldens frozen before the change.
  `TestUpdateWorkflowsGlobalRunRenders` covers the global branch and the
  gate's instructions.
- `TestApplyProposalRefusals` has one case per refusal, each asserting every
  global file and the staging directory are byte-identical afterwards, under
  both `--check` and a real apply. Further tests cover installing, modes (a
  unix test file), an empty manifest, repairing an invalid live file and
  `ListGlobal`; `internal/cli` covers the commands' output and exit codes.
- `scripts/123-gate.sh` runs the real built-in against the fake agent:
  approve, reject, an empty global directory, and a project run ending `done`
  with `global-only` stopped.
