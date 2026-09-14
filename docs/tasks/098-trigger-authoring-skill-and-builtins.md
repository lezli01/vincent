# 098 — The `vincent-triggers` skill and the `create-trigger` and `update-triggers` built-ins

**Status:** ✅ done (6/6)
**Opened:** 2026-09-14

*Issue [#363](https://github.com/lezli01/vincent/issues/363).*

Task [096](096-event-triggers.md) shipped event triggers, and with them a file
format nobody has been taught to write: `{config_dir}/triggers/*.yaml`, four
source types, four actions, a trusted-event rule, a ledger and three dangerous
switches. Tasks [023](023-vincent-workflow-authoring-skill.md),
[024](024-create-workflow-builtin.md) and
[037](037-update-workflows-builtin.md) closed the same gap for workflows with a
published skill, a built-in that writes a new file and a built-in that
maintains the files you have. This task does the same for triggers: a
published `vincent-triggers` skill, a `create-trigger` built-in, and an
`update-triggers` built-in.

The difference from the workflow pair is what a trigger file can do. A
workflow does nothing until a person starts a task on it. A trigger starts
agents with nobody at the keyboard, so an agent that writes one is an agent
that could arm one. The rule this task settles, and enforces in code rather
than only in prompts, is that **a built-in may author a disarmed trigger file,
and arming stays human-only** (decisions 2 and 3).

The issue was written against 096's *plan*. Several of its details are wrong
against what shipped, and the implementation follows what shipped wherever the
two differ: there is no trigger-level `container:` (096 decision 23);
`allowed_actors` is refused, not degraded, on an untrusted GitHub event with no
list (31F, 32); `source.project` is the numeric project id; GitHub sources
refuse `poll_interval` (31D); a `cancel` must write `on_fire: create` (31C), so
no built-in can produce a loadable one; the ledger has a seventh outcome,
`seeded` (31B); and `vincent trigger test` is a dry run against a loaded
trigger, not a validator (decision 29).

## Decisions

These were settled with the author on 2026-09-14 and bind the implementation.

1. **2026-09-14 — Scope: all of 096 as shipped, in one pull request.** The
   skill and both built-ins cover `type: command`, `github_issues` and
   `github_prs` (trusted events and `allowed_actors`), `type: http` (the
   signature scheme and `secret_env`), `create_task`, and the `follow_up`,
   `retry` and `cancel` reactions. `update-triggers`' checklist starts with one
   line per shipped feature. The issue planned "096.2 first, then one pull
   request per later sub-task". That plan is dropped because every one of
   those sub-tasks has landed. The coupling rule survives for future trigger
   features: each one extends the skill, re-reads both trigger built-ins and
   adds its line to the checklist.
2. **2026-09-14 — 096 decision 22 is narrowed, not reversed.** Decision 22 kept
   the trigger write routes off MCP because "an agent must not author or arm a
   trigger that starts agents". It now reads: a built-in that a **human
   started** may **author a disarmed trigger file**. **Arming stays
   human-only**, and arming is any of:
   - `enabled: true`
   - the global `triggers.enabled`
   - `on_fire: create`
   - `permission: workflow`

   The MCP exclusion list does not change. An agent a person is talking to over
   MCP still cannot write a trigger through the API; a built-in writes through
   `vincent trigger apply` (decision 3), which is a command step in a task a
   human created. `create-trigger` has no manual gate, like `create-workflow`,
   because what it writes cannot fire until a human arms it. Decision 22 is
   amended in place in 096 with a note citing this document, and 096 decision
   16 already named "#363's built-ins" as a route by which trigger files come
   and go.
3. **2026-09-14 — The never-arm rule is enforced in code, by three CLI verbs.**
   This narrows 096 decision 31H ("the CLI gains exactly `vincent trigger
   test`"), recorded there as a dated amendment.
   - `vincent trigger validate <file>` needs no daemon. It runs `trigger.Parse`
     with the file's stem, so its verdict is `POST /v1/triggers/validate`'s plus
     the check that `id:` equals the file name. Exit `0` valid, `1` invalid or
     unreadable, mirroring `vincent workflow validate`.
   - `vincent trigger ls --project <id>` also needs no daemon (decision 4).
   - `vincent trigger apply --proposal <task_id> --project <id>` writes staged
     files into `{config_dir}/triggers/` through `internal/trigger`'s writer.
     It refuses, writes nothing and names every offending file and key, when:
     - any staged file fails `Parse`;
     - a staged file's `source.project` is not `--project`, so a run in project
       A can never write project B's triggers;
     - a staged file has no manifest entry, or a manifest entry has no staged
       file;
     - an existing file no longer carries the version token the proposal
       recorded (stale), a file recorded `absent` now exists, or a recorded
       file is gone, so apply never overwrites silently;
     - any change **arms** relative to the file on disk, where a new file
       compares against absent: `enabled` from false or absent to `true`,
       `on_fire` from absent or `propose` to `create`, or `permission` from
       absent or `restricted` to `workflow`.

     A value that is already armed may be kept, and disarming is always
     allowed. The verb has no override flag. Humans arm in the TUI, which asks
     first (096 decision 19), or in `$EDITOR`. Files are written `0600` (096
     decision 20) by a new version-guarded whole-file `Replace` on the writer,
     beside `Create`, `Patch` and `Delete`. The staging directory is removed
     once every file is written. Global `triggers.enabled` lives in
     `config.yaml`, and apply cannot touch it.
   *Beaten:* the rule in the prompts alone. A prompt is text an agent weighs,
   and the whole point of the rule is that no weighing reaches arming.
4. **2026-09-14 — The inventory uses `.Project.ID` and `vincent trigger ls`.**
   §8.4's `.Project` gains `ID`, the project's numeric id, filled wherever the
   render context is built, including the `vincent workflow render` preview.
   `vincent trigger ls --project <id>` prints one trigger file path per line for
   the files whose `source.project` equals `<id>`, and exits `1` when there are
   none: the same probe shape as `update-workflows`' `git ls-files
   --error-unmatch`. With `--json` it prints each file's id, version token,
   validity and its `enabled`, `on_fire` and `permission` values, which is what
   a proposal records. For a file that does not parse, `ls` reads only
   `source.project` leniently, so an invalid file that belongs to the project is
   still listed and can still be repaired. A file whose project cannot be read
   at all is reported on stderr and left out.
5. **2026-09-14 — Proposals wait in `{data_dir}/trigger-proposals/<task_id>/`.**
   The directory is `0700` and its files `0600`. It is outside every
   repository and outside the registry directory. A trigger file can carry a
   token in a poll argv, so a proposal never goes in a worktree and never
   exists only as text parsed out of a message. It holds the full proposed
   files plus `manifest.json`, a JSON object mapping each trigger id to the
   version token `ls --json` reported, or the string `"absent"` for a new file.
   Apply removes the directory after a successful apply. Task delete (task
   [092](092-archived-boards-and-delete.md)) removes it too, next to
   `{data_dir}/transcripts/{id}`, under the same data-root containment. A
   rejected run's directory stays until the task is deleted.
   *Beaten:* staging in the task's worktree, which is a repository; and staging
   in `{config_dir}/triggers/` under another extension, which is the directory
   the registry watches.
6. **2026-09-14 — `create-trigger` has two steps and no gate.** `author` is an
   agent step with `on_input: wait` and `max_retries: 0`, for task 024
   decisions 9 and 5's reasons. It stages the file and its manifest, runs
   `vincent trigger validate` on it, and may ask. `install` is a command step
   running `vincent trigger apply --proposal {{.Task.ID}} --project
   {{.Project.ID}}`. The issue asked for one step. The second is deterministic
   enforcement rather than review, so decision 2's no-gate call stands.
   Its one field, `trigger_id`, is required and uses `workflow_name`'s pattern
   `^[a-z0-9][a-z0-9._-]*$` (task 024 decision 10); `trigger.ValidID`
   additionally refuses `..` at apply. The value becomes both `id:` and the
   file name. The prompt also says:
   - `source.project` is `{{.Project.ID}}`.
   - Check `vincent trigger ls --project` for an existing id, and ask rather
     than overwrite. If the user says to replace it, the manifest records that
     file's version token.
   - Check `vincent workflow ls` for `action.workflow`. When it is missing, ask,
     or name an existing workflow and point to `create-workflow`. Never write a
     workflow.
   - A `type: command` trigger's poll script goes under
     `{config_dir}/trigger-scripts/` (decision 8), and `command:` names it by
     absolute path.
   - Refuse a request for a `cancel` trigger, or for anything that arms, and
     explain why. A `cancel` must write `on_fire: create`, so no built-in can
     produce one that loads.
   - The final message names what a human must do to arm the trigger.
7. **2026-09-14 — `update-triggers` works on this project only, behind a
   manual gate.** Steps:
   1. `inventory`, a command step (`allow_failure`, `max_retries: 0`) running
      `vincent trigger ls --project {{.Project.ID}}`.
   2. `has-triggers`, a `condition` that ends the run `done` when the inventory
      found nothing.
   3. `propose`, an agent step with `on_input: deny` and `max_retries: 1`. The
      retry is safe for task 037 decision 7's reason, because the agent clears
      its own staging directory before writing. It writes nothing under
      `{config_dir}`. It stages full proposed files and the manifest, and runs
      `vincent trigger validate` on each. Its final message gives, per trigger,
      a before/after diff and the checklist items that applied.
   4. `approve`, a `manual` step whose `instructions` render the proposal and
      name the staging path. Rejecting it ends the task with every trigger
      untouched.
   5. `apply`, a command step with `max_retries: 0` running `vincent trigger
      apply --proposal {{.Task.ID}} --project {{.Project.ID}}`.
   6. `result`, a command step running `vincent trigger ls --project
      {{.Project.ID}}` for the record.

   It may not change a trigger's `id`, its file name or `source.project`; it
   deletes no file; and `enabled`, `on_fire` and `permission` keep their current
   values, which apply enforces and the prompt says. **A `dedupe_key` must never
   change what it renders for an event already delivered**: a changed key does
   not match the ledger's existing rows, so every delivered event would fire
   again. That hazard is not in the issue. A trigger that is already right is
   left byte for byte, and saying so is a correct outcome.
   The gate is where this parts from `create-trigger`. A rewrite of an armed
   trigger is live the moment it is written, so a proposal is reviewed before
   apply, where `create-trigger`'s file cannot fire at all.
   The review checklist is version-coupled to trigger features, as task 037
   decision 5 couples `update-workflows`' checklist to workflow features. Its
   first lines cover `match:` as a prefilter in front of `if:`; an explicit
   `dedupe_key`; `limits.max_per_hour`; `limits.max_task_cost_usd` on
   `create_task`; `github_issue` and `github_pull` prefill instead of numbers
   parsed out of titles; no `poll_interval` on GitHub sources;
   `allowed_actors` on untrusted GitHub events; reactions carrying none of the
   keys they refuse; `http` signatures with `secret_env`; no secret in an argv
   that could come from the inherited environment; poll scripts that follow
   096 appendix A (a string `id`, a trailing `cursor` line, a non-zero exit on
   failure); `printf "%.0f"` on keys built from JSON numbers; and nothing that
   relies on `.Event` in a step template (096 decision 11).
8. **2026-09-14 — Poll scripts live in `{config_dir}/trigger-scripts/`.** A
   documented convention, not a path the daemon enforces. It sits beside the
   registry directory, not inside it, so a script never shares a directory with
   the files the registry watches. On POSIX the directory and each script are
   owner-only (`0700`). The argv runs with no shell, so on Windows a script is
   named as `command: [pwsh, -NoProfile, -File, <absolute path>.ps1]`.
   Credentials come from the daemon's inherited environment (§2 and §12.3's
   `environment` policy) and never go in the script or the trigger file. A poll
   script never lives in a repository, which is 096 decision 8's supply-chain
   hole by another route.
9. **2026-09-14 — The two skills' descriptions split cleanly.**
   `vincent-triggers` covers `{config_dir}/triggers` YAML, poll scripts,
   arming, dry runs and the ledger. `vincent-workflows` gains a "not for vincent
   triggers" clause and a `metadata.version` bump. That clause changes neither
   workflow built-in's prompt, because the splice strips front matter, but it
   does change the skill's hash (§9.8). For the workflow a trigger's
   `action.workflow` names, `vincent-triggers` defers to `vincent-workflows`.

### `update-workflows`' checklist gains no line

CLAUDE.md's standing rule, from task 037 decision 5, says a workflow feature
that lands re-reads the three workflow built-ins and adds its line to
`update-workflows`' checklist. `.Project.ID` is a workflow feature, and the
built-ins were re-read. It gets no line, and this is the reasoned exception,
not an omission, as task 044 decision 9 was for `render`.

The checklist lists patterns a workflow can be **behind on**: a prompt that
says "if X then Y" where a guard belongs, a value buried in a description
where a field belongs. Each line replaces something an older workflow wrote.
`.Project.ID` replaces nothing. Before it, no workflow could name its project's
id at all, so there is no older spelling in anybody's file for a maintenance
pass to find and rewrite. It adds a value, and a workflow that needs it will
be written with it. The two trigger built-ins are the ones that use it.

## Work

- [x] **098.1 — `internal/trigger`: the offline lister, the arming check, the proposal manifest and the version-guarded `Replace`, plus the `validate`, `ls` and `apply` CLI verbs.** ✓ 2026-09-14
- [x] **098.2 — `.Project.ID` in §8.4's render context, at run time and in the `vincent workflow render` preview.** ✓ 2026-09-14
- [x] **098.3 — The published `vincent-triggers` skill, and the `vincent-workflows` description clause.** ✓ 2026-09-14
- [x] **098.4 — The `create-trigger` and `update-triggers` built-ins, splicing the skill.** Depends: 098.1, 098.2, 098.3. ✓ 2026-09-14
- [x] **098.5 — Task delete removes `{data_dir}/trigger-proposals/{id}`.** ✓ 2026-09-14
- [x] **098.6 — `m16` gate scenario 10, the spec amendments and the documentation.** Scenario 10 stages a proposal, sees apply refuse an arming change, then applies a clean one and sees the registry list it disarmed. ✓ 2026-09-14
