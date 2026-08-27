# 037 — `update-workflows`, a built-in that maintains the ones you have

**Status:** ✅ done (5/5)
**Opened:** 2026-08-27

Add a third built-in workflow, `update-workflows`, whose deliverable is a
reviewed rewrite of the workflows a project already versions under
`.vincent/workflows`. Its agent step carries the same `vincent-workflows` skill
(task 023) `create-workflow` embeds, plus a checklist of the features a
workflow can be behind on, and its output is verified by a `for_each` loop that
runs `vincent workflow validate` over every file.

Task 024 closed the "I have no workflows" gap. This closes the other end of the
same problem: a workflow written against vincent 0.3 is still valid against
0.7 — unknown keys are errors, but *unused* features are not — so a registry
silently ages. Fan-out, conditions, loops, includes, fields, `.Issue`,
`retry_backoff` and `vincent status` all shipped after the first workflows
people wrote, and nothing in the product ever suggests using them. Running one
task now says "bring these up to date", and the answer arrives as a diff.

The same argument applies to the built-ins themselves, which is why this task
also audits them (decision 5).

## Decisions

1. **2026-08-27 — Deliver into the task's worktree and branch, not the live
   registry.** This is the one place `update-workflows` deliberately parts from
   `create-workflow`'s decision 2, and for the reason that decision turned on:
   what makes a file a workflow. `create-workflow` writes a file that does not
   exist yet, so writing it anywhere but the live registry produces nothing
   runnable until a branch merges. Here the files already exist and are already
   live; the question is not how the change becomes real but how it gets
   reviewed. Rewriting a versioned file in the project's own checkout would
   mutate the user's working tree behind their back, collide with whatever they
   have uncommitted, and land a rewrite of every workflow with no diff, no
   branch, and no revert that is not `git checkout`. The worktree gives the
   change vincent's normal review path, and merging is exactly the right moment
   for a rewritten workflow to go live.
   The accepted cost is that a workflow file the project has never committed is
   not in the worktree at all and is therefore out of scope. The prompt says so
   rather than leaving it to be discovered.
2. **2026-08-27 — Project scope only; the global registry is never touched.**
   A task belongs to a project, and only the project's own `.vincent/workflows`
   is versioned by anything. Editing `{config_dir}/workflows` from a project
   task would be an effect outside the diff, shared with every other project,
   and invisible to review — the exact property decision 1 exists to keep. It
   also falls out of decision 1 for free: what is not in the repository is not
   in the worktree.
3. **2026-08-27 — Verify with a `for_each` loop, not a `check:`.** A prompt's
   claim that it validated is not verification, but `vincent workflow validate`
   takes exactly one file and the step-body shell is the §8.3 intersection of
   `/bin/sh` and `pwsh`, where iterating a list is not expressible. The
   template context cannot close the gap either: rendering uses stock
   `text/template` with no `split`, so a step's newline-separated output cannot
   become N commands. `for_each` over a step's output *is* the product's answer
   to per-item work (§7.8), so the loop is the natural spelling rather than a
   workaround. A failing file blocks the task, which is right: an invalid
   workflow must not reach a reviewer as a merge candidate.
   The list is produced by a second `git ls-files` **after** the agent runs,
   with `--others --exclude-standard`, so a file the pass added — an `include`
   extracted out of two workflows — is validated too. Reusing the first
   inventory would have validated only what existed before the change.
4. **2026-08-27 — `on_input: deny`, where `create-workflow` chose `wait`.**
   Task 024 decision 9 argued that suppressing the skill's questioning protocol
   made that built-in argue with itself, and that is still true *there*: it is
   designing something that does not exist, and a question may be the only way
   to get it right. This one has the answers in front of it — the file, its
   comments, its git history, the repository's agent instructions — and its
   output is reviewed before it can affect anything, so an ambiguity is
   resolved by leaving the current behavior alone and reporting it. Parking a
   maintenance pass in `awaiting_input` would hold a concurrency slot with a
   live process (§6) for a question the repository already answers. The prompt
   states the policy and the fallback, so the agent is not merely denied but
   told what to do instead. `deny` degrades quietly on codex and cursor, which
   a `require` would not.
5. **2026-08-27 — The built-ins are held to the same bar, and the checklist is
   version-coupled on purpose.** A workflow whose job is auditing other
   people's workflows cannot itself be behind. The audit this task performed
   found one applicable gap in the two existing built-ins: neither asked its
   agent for a `vincent status` line (§5.6, task 036), so an `adhoc` run that
   takes forty minutes shows a `running` row and nothing else. The instruction
   now lives in one `StatusInstruction` const spliced into all three prompts,
   and `TestBuiltinAgentStepsAskForStatus` fails the day a fourth built-in
   forgets it.
   The rest of the checklist under "The bar" is this file's own text rather
   than anything derived, which means it goes stale the same way a registry
   does. That is accepted and stated in the source comment: adding the line is
   part of shipping a workflow feature, alongside the schema reference and the
   guide.
6. **2026-08-27 — No task fields.** A `only: <names>` field was considered for
   bounding cost on a large registry and rejected: decision 3 validates every
   file regardless, so a field that narrows what the agent may fix while the
   loop still judges everything sets the task up to block on a file it was told
   not to touch. One session reading N files is also cheaper than the field
   would save, and the workflows in one project routinely `include` each other
   — subsetting them hides the duplication item 2 of the checklist exists to
   find.
7. **2026-08-27 — `max_retries: 1` on the agent step, against both other
   built-ins' `0`.** §8.4's rule is that replay must be provably safe. Here it
   is: the edits are in a private worktree with no external effect, a second
   session sees the first's partial work as ordinary uncommitted changes and
   `git diff` tells it exactly what happened, and every item of the checklist
   asks a file to *conform* rather than to change again — so the pass is
   convergent, unlike `create-workflow`'s write of a file that would already be
   there. The retry also carries §8.4's failure block, which for a timeout or a
   transcript-cap kill is the difference between finishing and blocking a
   human.

## Work

- [x] **037.1 — Add the `update-workflows` source: probe, condition, agent, relist, validation loop, diff.** ✓ 2026-08-27
- [x] **037.2 — Splice the skill and the review checklist into its prompt.** ✓ 2026-08-27
- [x] **037.3 — Audit the existing built-ins and give all three the `vincent status` instruction.** ✓ 2026-08-27
- [x] **037.4 — Cover it with tests: shape, prompt render, and the status instruction across every built-in.** ✓ 2026-08-27
- [x] **037.5 — Amend the spec and the workflow documentation.** ✓ 2026-08-27

## Verification

- `go test ./...` passes, including `TestBuiltinUpdateWorkflowsIsValid` (step
  order, the allow-failure probe, `on_input: deny`, the `for_each` loop driven
  by the relist step), `TestBuiltinUpdateWorkflowsPromptRenders` (the inventory
  listing reaches the prompt, the skill is spliced without its front matter,
  the checklist's quoted `{{with index …}}` example survives the render as
  text, and the three verification steps' `run:` bodies render) and
  `TestBuiltinAgentStepsAskForStatus`, which holds every built-in agent step to
  decision 5.
- `vincent workflow validate` on the generated source: `ok — update-workflows,
  6 step(s), 0 warning(s)`.
- `golangci-lint run ./...` clean for `GOOS=windows`, `GOOS=darwin` and
  `GOOS=linux`.

## Assumptions

- The `vincent` binary is on the daemon's `PATH`. The validation loop and the
  `vincent status` calls need it, as does every documented example of a step
  that reports its own status (§5.6). A daemon started from a build directory
  without installing is the case where this workflow's last three steps fail.
