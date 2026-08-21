# 023 — Vincent workflow authoring skill

**Status:** 🚧 in progress (0/5)  
**Opened:** 2026-08-21

Create a portable Agent Skill that helps coding agents design, write, review, and
validate Vincent workflow YAML. The skill must select Vincent's control-flow
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
4. **2026-08-21 — Use progressive references and Vincent's own validator.** Keep
   the entrypoint focused, put schema and design detail in references, and make
   `vincent workflow validate` the final deterministic check. This beats
   duplicating the entire documentation set or adding a helper script around an
   existing command.
5. **2026-08-21 — Bundle the repository's license with the skill.** Standalone
   installers must retain the same terms as the source repository. This beats
   relying on consumers to discover a parent-directory license after the skill
   has been copied elsewhere.

## Work

- [~] **023.1 — Define the portable skill entrypoint and interaction/step-selection process.**
- [ ] **023.2 — Add compact schema and control-flow/cost references.**
- [ ] **023.3 — Document GitHub and `npx skills` installation/discovery.**
- [ ] **023.4 — Validate skill structure, discovery, and representative workflow behavior.**
- [ ] **023.5 — Review the final diff and repository verification.**
