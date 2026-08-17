# Vendored agent skills

This directory holds [Matt Pocock's agent skills](https://github.com/mattpocock/skills),
vendored into the repo so every clone — including cloud/CI agent sessions, where the
interactive `/plugin` installer is unavailable — picks them up with no per-developer
setup step.

Licence: MIT, see [`LICENSE.upstream`](LICENSE.upstream). These files are **not**
covered by vincent's own licence.

## How they got here

```sh
npx skills@latest add mattpocock/skills --agent claude-code
```

That command wrote `.claude/skills/<name>/` for each skill and recorded the source and
a content hash per skill in `skills-lock.json` at the repo root. To pull upstream
changes, re-run the command (or `npx skills@latest update`) and commit the resulting
diff, including the lockfile.

The upstream repo also publishes a Claude Code plugin (`mattpocock-skills@mattpocock`).
Vendoring and installing the plugin are alternatives, not complements — doing both
registers every skill twice. If this repo later switches to the plugin, delete this
directory and the lockfile in the same change.

## What is here

35 skills. The ones that carry their weight in a Go codebase:

| Skill | Use |
|---|---|
| `ask-matt` | Router — asks which of these skills fits the situation |
| `grill-me`, `grill-with-docs`, `grilling` | Interview a plan or design before any code is written |
| `to-spec`, `to-tickets`, `wayfinder` | Turn a discussion into a spec, tickets, or a multi-session map |
| `implement`, `tdd`, `prototype` | Build from a spec, test-first, or throwaway-first |
| `diagnosing-bugs`, `resolving-merge-conflicts` | Debug loop; in-progress merge/rebase conflicts |
| `code-review`, `codebase-design`, `improve-codebase-architecture` | Review against repo standards; deep-module design vocabulary |
| `domain-modeling`, `research`, `writing-for-agents` | CONTEXT.md/ADRs, sourced research notes, editing CLAUDE.md and skills |
| `triage`, `handoff`, `claude-handoff`, `teach`, `wait-what`, `wizard` | Issue triage, session handoff, explanation, human-in-the-loop bash wizards |

Some skills in the set target Matt Pocock's own TypeScript and course-authoring work
and will not apply here: `migrate-to-shoehorn`, `setup-ts-deep-modules`,
`setup-pre-commit` (Husky/lint-staged), `scaffold-exercises`, and the
`writing-beats`/`writing-fragments`/`writing-shape` prose trio. They are kept so the
vendored tree matches upstream exactly and `skills update` stays a clean diff; delete
them if the noise outweighs that.

## Before first use

`setup-matt-pocock-skills` configures the issue-tracker, triage-label, and domain-doc
conventions the engineering skills assume. Run it once per repo:

```
/setup-matt-pocock-skills
```

## Note on `git-guardrails-claude-code`

That skill ships `scripts/block-dangerous-git.sh`, a `PreToolUse` hook that refuses
`git push`, `git reset --hard`, `git clean -f`, and similar. It is inert until someone
wires it into a settings file — being vendored here does not activate it. Wiring it up
would block the pushes this repo's own agent workflows perform, so leave it off unless
that is what you want.

Skills run with full agent permissions. Read one before invoking it.
