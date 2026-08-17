# Vendored skills: mattpocock/skills

These are Matt Pocock's agent skills, copied into this repo as ordinary files we
own, rather than subscribed to as a Claude Code plugin.

| | |
|---|---|
| Upstream | <https://github.com/mattpocock/skills> (MIT) |
| Version | `mattpocock-skills` 1.2.3 |
| Commit | `9c9f36ccd3995266cd675468af71639c8dde1ec5` |
| Skills | the 25 listed in upstream `.claude-plugin/plugin.json` |

## Why vendored and not the plugin

The plugin (`extraKnownMarketplaces` + `enabledPlugins` in `.claude/settings.json`)
delivered the same set as a managed, read-only bundle that updated when upstream
shipped. It was removed when these copies landed: upstream's README is explicit
that installing both ways "leaves you with every skill twice", and two live copies
of `/tdd` or `/code-review` in the picker is worse than a pinned one. The trade is
deliberate — we no longer get upstream changes automatically, and we can now edit
these to fit this repo.

Each skill keeps its `agents/openai.yaml`, so the same set is available to
Agent-Skills-compatible harnesses (Codex, cursor-agent) and not just Claude Code.

## Layout

Upstream groups skills by category (`skills/engineering/tdd`,
`skills/productivity/grilling`). Claude Code discovers a project skill as
`.claude/skills/<name>/SKILL.md` — one level, no category directories — so the
tree is flattened here by skill name. That matches what upstream's own
`scripts/link-skills.sh` does when linking into `~/.claude/skills`. No skill
references a sibling by relative path, so flattening breaks nothing.

Not vendored: `skills/misc/`, `skills/in-progress/`, and `skills/deprecated/`.
Upstream ships none of those in the plugin.

## Refreshing from upstream

There is no automatic update path by design. To pull a newer upstream:

```sh
git clone --depth 1 https://github.com/mattpocock/skills.git /tmp/mp-skills
# for each dir in .claude/skills/, replace it with the matching upstream
# skills/*/<name>/ directory, then review the diff for local edits you want to keep
```

Update the version and commit in the table above in the same change, and check
upstream `plugin.json` for skills added to or dropped from the shipped set.

## First-time setup

Upstream expects `/setup-matt-pocock-skills` to be run once per repo — it records
the issue tracker, triage labels, and where generated docs go. That has not been
run here yet; the skills that depend on those answers (`/triage`, `/to-tickets`)
will ask before they need them.

## Note on `code-review`

This set includes a `code-review` skill. Claude Code may also offer a built-in or
user-level `code-review`; when both are present the project copy is the one
scoped to this repo. Invoke it by name if the distinction matters.
