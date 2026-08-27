# 023 — vincent workflow authoring skill

**Status:** ✅ done (10/10)
**Opened:** 2026-08-21

Create a portable Agent Skill that helps coding agents design, write, review, and
validate vincent workflow YAML. The skill must select vincent's control-flow
primitives deliberately, reserve prompt-based agent steps for work that requires
reasoning, and surface meaningful human-gate and interaction choices before the
workflow is committed.

## Decisions

1. **2026-08-21 — Publish the canonical skill at
   `skills/vincent-workflows/` in the open Agent Skills format.** This keeps the
   source installable from GitHub with `npx skills` across supporting clients.
   It beats adding another entry to `.claude/skills/`, which is a vendored,
   Claude-specific collection, and beats maintaining an npm package whose only
   purpose would be copying Markdown files.
2. **2026-08-21 — Choose the cheapest correct workflow primitive.** Prefer
   deterministic command steps, express orchestration with native control flow,
   and use manual steps for human judgment or authorization. Every agent step
   must have a stated reason that deterministic commands or structure cannot
   satisfy. This beats treating an agent prompt as the default implementation
   mechanism.
3. **2026-08-21 — Ask targeted questions when answers materially change the
   workflow.** In particular, resolve required inputs, acceptance checks,
   platforms, failure policy, side effects, concurrency, and whether a human
   gate or live agent interaction is required. This beats either a fixed
   questionnaire or silently assuming that all workflows should be unattended.
4. **2026-08-21 — Use progressive references and vincent's own validator.** Keep
   the entrypoint focused, put schema and design detail in references, and make
   `vincent workflow validate` the final deterministic check. This beats
   duplicating the entire documentation set or adding a helper script around an
   existing command.
5. **2026-08-21 — Bundle the repository's license with the skill.** Standalone
   installers must retain the same terms as the source repository. This beats
   relying on consumers to discover a parent-directory license after the skill
   has been copied elsewhere.
6. **2026-08-21 — Treat `manual` as a binary gate, not a data-input step.** Use
   task fields for choices known at creation and supported mid-run agent input
   only when the same reasoning session needs an answer. This beats implying
   that approval can return an arbitrary choice or credential.
7. **2026-08-21 — Make schema-version drift visible.** Prefer repository-local
   workflow documentation, record the installed vincent version, and rely on
   that binary's validator for the final compatibility verdict. This beats
   silently treating the bundled reference as timeless.
8. **2026-08-21 — Report a conservative agent-session cost envelope.** Count
   retry, loop, parallel, and fan-out multipliers before presenting a design.
   This beats calling a workflow cost-effective without quantifying its maximum
   planned AI use.
9. **2026-08-21 — Handle secrets and external effects explicitly.** Keep
   credentials out of persisted task data and transcripts, prefer preflight or
   dry-run support, disable unsafe retries, and verify remote state separately.
   This beats relying on a manual gate as the whole safety model.
10. **2026-08-21 — Return a compact design summary with every workflow.** State
    agent justifications, the session envelope, gates, effects, compatibility,
    and validation result. This beats leaving those decisions implicit in YAML.

## Work

- [x] **023.1 — Define the portable skill entrypoint and interaction/step-selection process.** ✓ 2026-08-21
- [x] **023.2 — Add compact schema and control-flow/cost references.** ✓ 2026-08-21
- [x] **023.3 — Document GitHub and `npx skills` installation/discovery.** ✓ 2026-08-21
- [x] **023.4 — Validate skill structure, discovery, and representative workflow behavior.** ✓ 2026-08-21
- [x] **023.5 — Review the final diff and repository verification.** ✓ 2026-08-21
- [x] **023.6 — Correct manual-gate, choice, and credential guidance.** ✓ 2026-08-21
- [x] **023.7 — Add vincent version and schema-drift handling.** ✓ 2026-08-21
- [x] **023.8 — Add a conservative agent-session cost envelope.** ✓ 2026-08-21
- [x] **023.9 — Strengthen secret and external-effect safety guidance.** ✓ 2026-08-21
- [x] **023.10 — Add the final workflow design summary and re-verify the skill.** ✓ 2026-08-21

## Verification

- The skill-creator structural validator passes.
- `npx skills` discovers the skill, installs it into a clean test directory,
  and copies its entrypoint, references, metadata, and license unchanged.
- Vincent v0.4.2 accepts representative probe/gate, bounded-loop, parallel, and
  fan-out workflows without warnings. It also rejects
  `on_input: require` inside a loop at the documented validation boundary.
- CI run 364 passes all seven Linux, macOS, Windows, gate, and packaging jobs.
- The updated source and a clean `npx skills` copy both pass the skill-creator
  validator and compare byte-for-byte.
- Vincent v0.4.2 accepts the four-session convergence and external-effect
  reconciliation examples without warnings. It rejects a made-up manual
  `value` field and the newer workflow `fields` key, exercising both corrected
  gate semantics and explicit version-mismatch handling.
- CI run 367 passes all seven Linux, macOS, Windows, gate, and packaging jobs.
