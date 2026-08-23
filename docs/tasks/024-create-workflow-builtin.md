# 024 — `create-workflow`, a built-in that writes workflows

**Status:** ✅ done (7/7)
**Opened:** 2026-08-23

Add a second built-in workflow, `create-workflow`, whose deliverable is another
workflow file. Its single agent step carries the `vincent-workflows` skill
(task 023), embedded from `skills/vincent-workflows/SKILL.md` at build time. Two
task fields shape the result: a required `workflow_name`, and a boolean `global`
selecting whether it installs into the global registry or the task's own project
registry.

Until now the built-in scope held exactly one entry, and writing a first
workflow meant knowing the schema before having a reason to learn it. The skill
from 023 closes that gap for an agent a person is already talking to; this
closes it for the daemon, which can be handed the job as a task like any other.

## Decisions

1. **2026-08-23 — Select the destination with a boolean `global` field, not a
   project name, path, or id.** A registry has exactly two writable scopes from
   inside a run, so the choice is binary and a boolean says so. It beats a
   `target_project` string, which would have needed the agent to resolve a name
   against `vincent project ls`, and would have implied vincent can install a
   workflow into a project the task is not running against — a capability
   nothing else in the product has. Its zero value is the safe one: an
   unfilled field installs into the repository, where git versions the result
   and a person can revert it.
2. **2026-08-23 — Write to the live registry directory, never to the task's
   worktree.** `{{.Project.Path}}` is the project's own checkout, and
   `{config_dir}/workflows` is outside any repository. The registry watches
   project repo roots (§5.2), so a file written into the worktree would not be
   a workflow at all until the branch merged — the task would report success
   and produce nothing runnable. The cost is that the result is not reviewed as
   part of the task's diff; the prompt says so in both branches rather than
   leaving it to be discovered. This beats staging the file in the worktree for
   a human to copy, which turns a one-step task into a two-step chore and still
   fails for the global destination.
3. **2026-08-23 — Resolve the project destination in the template and the
   global one at run time.** Because decision 1 makes the project always the
   task's own, `{{.Project.Path}}/.vincent/workflows` is exactly known before
   the step starts, and a rendered prompt that names the literal path cannot be
   misresolved. §8.4's render context has no config directory, so the global
   branch instructs the agent to read `paths.config_dir` from
   `vincent doctor --json` — the one command that prints it without a running
   daemon. This beats adding a `.Config` context for one built-in's benefit,
   which is a spec §8.4 change every workflow author would then have to be
   taught.
4. **2026-08-23 — Verify with the agent running `vincent workflow validate`,
   with no deterministic `check`.** A `check` command can only run on a path a
   template knows, and the file's name is the agent's to choose; forcing it
   into a required `workflow_name` field would constrain the design work to buy
   one assertion. The skill already makes the validator the final verdict, so
   the prompt requires running it and reporting its output. This is the
   documented weaker option, recorded so the trade is not rediscovered.
5. **2026-08-23 — Pin `max_retries: 0`.** A retry is a fresh session that would
   find the first attempt's file already written and no record of why the first
   failed. That is §8.4's own rule for a step whose replay is not provably
   safe, and it matches `adhoc`'s reasoning for the same field.
6. ~~**2026-08-23 — Restate the skill's rules in the prompt rather than
   referencing it.**~~ *Superseded 2026-08-23 by decision 7.* The first cut
   paraphrased the skill into the prompt so the built-in would work on a host
   where the skill was never installed. That property is real and decision 7
   keeps it; what the paraphrase failed at is staying true, since a skill edit
   would have left the built-in quietly describing an older design.
7. **2026-08-23 — Embed the published skill at build time, so it is the single
   copy.** `skills/embed.go` holds `//go:embed vincent-workflows/SKILL.md` and
   `CreateWorkflowSource` splices it into the prompt at init. Editing the skill
   changes the built-in at the next build with no Go change, which is the whole
   point: two copies of design guidance drift, and the copy inside a binary
   drifts invisibly. The package sits at the repository root rather than under
   `internal/` because `go:embed` reads only its own package's directory tree —
   no package elsewhere can name that path at all. It depends on nothing, so
   the one-way dependency direction is unaffected.
   The costs are accepted, not overlooked: the skill is now load-bearing
   runtime text, so a careless edit to it is a behavior change to the daemon;
   and the rendered prompt is about 10 KB, paid once per `create-workflow`
   run. Only `SKILL.md` is embedded — its `references/` would triple that, and
   the skill already prefers the repository's own
   `docs/reference/workflow-schema.md` when one is present.
8. **2026-08-23 — Escape and re-indent the embedded text, and correct it from
   the header rather than editing the skill.** The prompt is a `text/template`
   body inside a YAML block scalar, so the splice escapes `{{` (only that
   sequence opens an action; a lone `}}` is literal) and prefixes every
   non-empty line to the block's column. The skill is written for an agent with
   a person to talk to and a `references/` directory on disk, neither of which
   holds here, so the header states three standing corrections — what asking
   costs (decision 9), the destination is the one above, the references are
   not there — and says they win on disagreement. This beats forking the
   skill's wording for the daemon, which would recreate the two copies
   decision 7 exists to remove.
9. **2026-08-23 — Let the step ask, sparingly, and say so in the YAML.** The
   first cut told the agent it was unattended and must never ask. That was
   inherited from `adhoc`'s framing rather than argued, and it contradicted
   two things at once: `resolveInputPolicy` falls through to `wait` when
   `on_input` is unset, so the step already permitted mid-run input on
   `claude`, the adapter `defaults.agent` pins; and the skill's "Gather only
   decisions that matter" is a questioning protocol, which is the most
   valuable section of the text decision 7 exists to embed. Suppressing it
   made the built-in argue with itself.
   What asking actually costs is now stated in the prompt instead of used as a
   silent reason to forbid it: `awaiting_input` **holds a concurrency slot**
   with the agent process alive on its stdin (§6), and an unanswered question
   expires into `input_timeout` — a *failed* step, not a fallback to the
   agent's own judgement (§7.4). So the correction bounds asking rather than
   banning it: ask only where an answer changes the YAML and cannot be settled
   from the repository, batch the questions, decide the rest and report the
   assumptions. `on_input: wait` is written on the step even though it is also
   the fallback, so the YAML and the prompt cannot drift apart.
   The alternatives were weighed and lost: `deny` is the honest spelling of
   the first cut but throws away the skill's best section; `require` adds a
   creation-time refusal when the agent is overridden to codex or cursor,
   which is a real failure mode for a built-in that should degrade rather than
   refuse; and a short `input_timeout` only converts a long stall into a fast
   failure, which is not obviously better for work a person may well come back
   to.
10. **2026-08-23 — Declare the new workflow's name as a required field, held
    to the slug vocabulary.** Letting the agent invent the name made the
    deliverable's identity unpredictable — a person who queues the task cannot
    say afterwards which workflow to run without reading the summary. The
    field is `workflow_name`, required, `pattern: '^[a-z0-9][a-z0-9._-]*$'`.
    That is deliberately stricter than the schema's own rule for a workflow
    `name:` (§8.2 rejects only whitespace and path separators), because this
    value is also the file name: `../escape` and `a/b` clear the schema's rule
    and would write outside the registry. It is the same vocabulary `isSlug`
    already accepts for a field name, so it adds no third spelling of "slug"
    to the product.
    It also makes the *project* destination fully known before the step starts
    — `{{.Project.Path}}/.vincent/workflows/{{ index .Task.Fields
    "workflow_name" }}.yaml` — which is the precondition decision 4 said it
    lacked. Decision 4 is **not** reopened here: the global destination is
    still not template-computable, so a `check` would cover one branch and not
    the other. Recorded so the option is found rather than rediscovered.

## Work

- [x] **024.1 — Generalize the built-in scope to more than one entry.** ✓ 2026-08-23
- [x] **024.2 — Add the `create-workflow` source, field, and prompt.** ✓ 2026-08-23
- [x] **024.3 — Cover it with tests, including both destination branches.** ✓ 2026-08-23
- [x] **024.4 — Amend the spec and the workflow documentation.** ✓ 2026-08-23
- [x] **024.5 — Embed the skill instead of paraphrasing it.** ✓ 2026-08-23
- [x] **024.6 — Let the step ask questions, bounded by what asking costs.** ✓ 2026-08-23
- [x] **024.7 — Declare the new workflow's name as a required, pattern-checked field.** ✓ 2026-08-23

## Verification

- `go test ./...` passes, including the new
  `TestBuiltinCreateWorkflowIsValid`;
  `TestBuiltinCreateWorkflowDestinationBranches`, which renders the prompt with
  `global` unset, `false`, and `true` and asserts each branch names its own
  destination and not the other's, carries the skill's body and not its front
  matter; and `TestCreateWorkflowSkillSplicingIsTemplateSafe`, which feeds a
  synthetic skill containing `{{.Task.Title}}` through the splice and requires
  it to render back as literal text.
- `golangci-lint run ./...` is clean for `GOOS=windows` and `GOOS=darwin`.
  The `GOOS=linux` run crashes inside staticcheck's `buildir` while analyzing
  the standard library's `internal/poll` package; it does so on an unmodified
  checkout too, so it is a toolchain/analyzer incompatibility, not a finding
  against this change.
