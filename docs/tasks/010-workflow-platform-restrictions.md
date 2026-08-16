# 010 — Restricting a workflow to platforms

**Status:** ✅ done (6/6) · **Opened:** 2026-08-16

A workflow may now declare where it is allowed to run:

```yaml
name: posix-tools
platforms: [posix]
```

On a host the list does not admit, the workflow stays visible but is never
*offered*: the new-task picker refuses it, `POST /v1/tasks` rejects it with a
400, and a task that somehow already carries such a snapshot blocks at
admission with `platform_unsupported`.

## The problem

§8.3 has always said cross-OS portability of command steps is the author's
responsibility, and vincent has always refused to translate between `/bin/sh`
and `pwsh`. What it lacked was a way for an author to **say** they had not
attempted it. A workflow built around `cat`, pipes and `test -f` was offered
identically on Windows, where the only feedback was a task that ran, failed at
its first command step, consumed its retries and blocked — a run-time discovery
of a fact the author knew when they wrote the file.

The same gap in reverse: a PowerShell-shaped workflow is just as broken on
Linux, and nothing in the schema could express it.

## Decisions

### 1. The restriction is on the workflow, not on the step

*2026-08-16.* One `platforms:` key at the top level, covering the whole file.

**Beat:** a per-step `platforms:`, which is the more flexible design and the
one that quietly changes the task lifecycle. A step that does not apply here is
either skipped — and then `.Steps` has a hole, `{{.Steps.build.Result}}`
renders nothing on one OS and something on another, and "did the task succeed"
depends on how many steps were skipped — or it fails, which is exactly what the
workflow-level restriction already achieves with none of that. That is a §6/§8.4
question, not a schema one, and it is not worth answering for a feature whose
motivating case ("this file is POSIX") is whole-file by nature.

Deferred rather than rejected: if a real workflow turns up that is portable
except for one step, the answer is likely two workflows, and the shadowing rule
(§5.2) already makes a project-scoped host-specific copy cheap.

### 2. `posix` is a token, and the only group token

*2026-08-16.* The accepted set is `linux`, `darwin`, `windows` — GOOS values —
plus `posix`, which matches every non-Windows host.

The motivating sentence is "this needs a POSIX shell", and spelling that
`[linux, darwin]` is both longer and *wrong on the machine that matters*: it
excludes a host vincent builds for but the author did not enumerate. Defining
`posix` as `GOOS != "windows"` rather than as a fixed list means a future
platform inherits the right answer without touching any workflow file.

No negation syntax (`!windows`), and no second group token. One group is what
the problem needs; a second would start a taxonomy.

**Tokens match exactly.** `macos`, `Linux` and `win` are validation errors, not
near-misses that silently never match. That is the same rule every other enum
in §8.2 follows, and the failure mode it avoids is the worst one available
here: a restriction that appears to be set and admits nothing, or everything.

### 3. The daemon decides, and clients report

*2026-08-16.* `GET /v1/workflows` serves both `platforms[]` and
`platform_supported` — the daemon's verdict on its own host. No client compares
the list to its own `runtime.GOOS`.

**Beat:** letting the TUI derive it, which is free today because the API is
localhost-only (§13.1). It is free right up until it isn't, and the property
worth keeping is the one PR L already established for §8.6 resolution: the
process that would *run* the thing is the one that says whether it can. The
client-side field is a `*bool` treating "absent" as "supported", so a `vincent`
CLI talking to an older daemon degrades to today's behavior rather than
declaring every workflow unrunnable.

### 4. Unsupported means "not offered", never "not shown"

*2026-08-16.* The entry stays in the registry, stays in `GET /v1/workflows`,
stays in `vincent workflow ls` (status `unsupported`) and stays in the TUI's
workflow view with the platforms it needs. Only *selection* stops.

This is §5.2's existing rule for an invalid file, applied unchanged: a workflow
that disappears when you look at it from the wrong machine looks like a
workflow you lost, and the first thing a user would do is re-create it. The
project-default picker goes one step further and keeps such a workflow
*selectable*, because a project's default is repository configuration that may
well be shared with hosts where it does run.

### 5. A restricted snapshot blocks at admission, with its own reason

*2026-08-16.* `platform_unsupported`, distinct from `invalid_snapshot`.

Creation already refuses these, so the engine check is only reachable when the
task and its host parted company — a data directory carried to another OS, or a
workflow narrowed after the task was queued. Reporting it as `invalid_snapshot`
would send the reader to look for a broken file that parses perfectly.

## Tasks

- [x] **010.1 — `platforms:` in the schema, with validation.** ✓ 2026-08-16
  `Workflow.Platforms`, the four tokens, `SupportsPlatform`/`SupportsHost`/
  `PlatformMismatch`, and §8.2 validation of unknown and duplicate tokens with
  the offending index located in the source (`platforms[1]`).
- [x] **010.2 — Registry and API.** ✓ 2026-08-16 `Entry.RunsHere`, plus
  `platforms[]` and `platform_supported` on `GET /v1/workflows`.
- [x] **010.3 — `POST /v1/tasks` refuses a workflow this host cannot run.**
  ✓ 2026-08-16 400 naming both the restriction and the host.
- [x] **010.4 — The engine blocks a restricted snapshot.** ✓ 2026-08-16
  `platform_unsupported`, before the worktree and before any step run.
- [x] **010.5 — Clients.** ✓ 2026-08-16 TUI: picker disables it with the
  reason, the default selection skips it, `submit` refuses it locally, the
  workflow view and the new-task summary say why. CLI: `workflow ls` gains a
  `PLATFORMS` column and the `unsupported` status; `workflow validate` reports
  the declared platforms without judging the host.
- [x] **010.6 — Docs.** ✓ 2026-08-16 Spec §8.1.1 (new), §8.2, §8.3, §13.2 and
  §18; the workflow-schema, api, cli, task-lifecycle, workflows-guide,
  troubleshooting and Windows pages.

## Verification

- `go test ./...` green, including the new cases: platform matching (table,
  every token against every GOOS), token validation and its source line,
  snapshot round-trip through `Marshal`, the registry/API verdict, the task
  creation 400, the engine's `platform_unsupported` block with **no** step run
  recorded, and the two TUI paths (picker refusal, registry row).
- The new tests pick their "foreign" platform from `runtime.GOOS`, so they
  assert the same thing on all three CI legs rather than passing vacuously on
  two of them.
