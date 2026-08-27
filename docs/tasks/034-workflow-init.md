# 034 — `vincent workflow init`: a CLI on-ramp for authoring a workflow

**Status:** ✅ done (5/5)
**Opened:** 2026-08-26
**Issue:** [#96](https://github.com/lezli01/vincent/issues/96)

Nothing in the binary helped you write your first workflow file. The CLI's
workflow surface was `ls` and `validate`; neither creates anything, and the TUI
deliberately does not either — `e` opens an existing entry in `$EDITOR`, and
creating a file from the TUI is out of v1 by decision (spec §15 view 5, and the
M3 footnote under §19). That decision is not touched here: this is a CLI
affordance.

So anything past a one-step ad-hoc run meant reading
`docs/reference/workflow-schema.md`, finding `examples/`, copying one, working
out where it goes (`{config_dir}/workflows/` for global, `{repo}/.Vincent/
workflows/` for project, precedence builtin < global < project), and running
`vincent workflow validate` in hope. The docs carry all of that. The tool never
mentioned it, and a user with the binary on their `$PATH` and no checkout in
front of them had no command that produced a valid file in the right directory.

`vincent workflow init <name> [--from <example>] [--project N] [--json]` writes
one and prints the path.

## Decisions

**1 (2026-08-26). This ships alongside the `create-workflow` built-in, not
instead of it.** The issue's premise that `adhoc` is the only built-in workflow
is stale — task 024 landed `create-workflow` on 2026-08-23, a built-in whose
deliverable is another workflow file, and task 024's own opening frames itself as
closing this same cold start. The gap is nonetheless real and the two do not
overlap in practice: `create-workflow` needs a running daemon, a registered
project, an installed and authenticated agent CLI, and a task run that costs
tokens and wall-clock time, and it holds a concurrency slot while it may park in
`awaiting_input`. `init` is offline, free, instant and deterministic. Task 024
never rejected a CLI affordance, so nothing binding is reopened — but the two
must be positioned against each other rather than left to compete, so the docs
and each command's help text say which one to reach for. *Rejected:* leaving them
undifferentiated, which makes a reader pick by whichever they read about first.

**2 (2026-08-26). `--from` rewrites the top-level `name:` line as text, and only
that line.** The examples' comments are the reason they are worth handing over,
so parsing and re-marshalling through `goccy/go-yaml` is out — it would drop
exactly the teaching material. `workflow.SetName` targets `^name:` at column
zero, which in block context cannot be a `- name:` under `fields:`, a `name:`
inside a step, or a `name:` inside a `prompt:` block scalar: every nested
construct is indented past the key that opens it.

*Accepted cost, recorded so it is not later mistaken for a bug:* the written
file's leading header comment still reads `# feature-pr — implement a change…`
while its `name:` reads `<name>`. Rewriting that prose too was considered and
rejected — it would have `init` editing text it does not own, under a rule that
breaks the moment an example's header changes shape.

**3 (2026-08-26). `--from` accepts every file in `examples/`, derived from the
embedded FS.** There are five, not the four the issue names: `converge.yaml`
landed with task 016's loops. Both the accepted values and the unknown-value
error's list come from `examples.Names()` at run time. *Rejected:* a hardcoded
list, which would silently fail to offer a new example and would drift from the
directory.

**4 (2026-08-26). Refuse on a duplicate `name:` in the target scope, not only on
a duplicate path.** The registry keys on the `name:` field, not the filename, and
within one scope the first file by sorted path wins (`registry.go`, `yamlFiles`
sorts). So `init my-flow` into a directory where `aaa.yaml` already declares
`name: my-flow` produces a file the daemon immediately marks invalid, and into
one where `zzz.yaml` declares it, `init`'s own file wins and *invalidates the
sibling*. Both directions are damage, so `init` scans the target directory,
refuses with a non-zero exit, and names the file holding the name. A sibling that
does not parse has no knowable name and cannot block `init`; it is skipped, being
already visible as an invalid entry in `workflow ls`.

**5 (2026-08-26). Collisions otherwise: refuse on the file, warn on the shadow.**
The target path is created with `O_CREATE|O_EXCL`, so "never clobber" is a
syscall guarantee rather than a stat-then-write race. Shadowing is legitimate
under §5.2 and only ever warns: `--project N` prints which global or built-in
entry the new file will shadow, both readable locally once the repo root is
known. The default global case warns when the name shadows a built-in but cannot
know which projects exist without a daemon and does not ask — a global workflow
later shadowed by a project file is not detectable at write time, and the help
text says so rather than implying a completeness the command does not have.

**6 (2026-08-26). `<name>` is held to `^[a-z0-9][a-z0-9._-]*$`.** The schema's own
rule for a workflow name is looser — anything without whitespace or a path
separator — but this value is also a file name, exactly the dual role behind task
024 decision 10, so it is held to the stricter of the two. Consistency with
`create-workflow`'s `workflow_name` field is the point: the same name must be
legal by both routes.

**7 (2026-08-26). The daemon split mirrors the one already in the package.**
`validate` is deliberately daemon-free; `ls` needs one because only the daemon
knows which projects exist (PR U decision). `init` is daemon-free in its default
shape and daemon-bound only in the branch that genuinely needs a project lookup,
where an unreachable daemon is exit 2 and writes nothing.

**8 (2026-08-26). The skeleton is not an example.** It is not a `--from` value
and does not ship in the release archive. An example teaches one shape of real
work; the skeleton teaches the schema, so its comments name what a first author
otherwise misses — `command` and `manual`, `check` as a *field* on agent and
command steps rather than a fourth type, `max_retries`, `timeout` — and point at
the reference page. It lives in `internal/workflow` beside `AdhocSource` and
`CreateWorkflowSource` so the package that owns `Parse` can hold it to the same
bar a shipped example is held to: zero errors **and** zero warnings.

**9 (2026-08-26). No new gate script.** This is a CLI command with deterministic
output and no daemon choreography worth driving over curl; the e2e tests in
`internal/cli` cover it at the right level, including the live-reload leg against
a real daemon.

## Tasks

- [x] **034.1** — `examples/embed.go`: a root `examples` package holding
  `//go:embed *.yaml` plus `Names()` and `Read(name)`. It lives in `examples/`
  because `go:embed` reads only the embedding package's own tree, the identical
  constraint that put `skills/embed.go` where it is — so the embed reads the real
  published tree rather than a copy that can drift. Depends on nothing else in
  the module and must stay that way. ✓ 2026-08-26
- [x] **034.2** — `internal/workflow/skeleton.go`: `SkeletonSource` beside
  `AdhocSource` and `CreateWorkflowSource`, plus `SetName` (the top-level-`name:`
  rewrite), `DeclaredName` (the name a source declares, "" when it does not
  parse — `fallbackName` now uses it) and `IsBuiltin`. ✓ 2026-08-26
- [x] **034.3** — `internal/cli/workflow_init.go`: the cobra command, name
  validation, source resolution, target resolution (global with no daemon,
  `--project N` through `ListProjects`), the duplicate-name scan, the `O_EXCL`
  write, shadow warnings and `--json`. ✓ 2026-08-26
- [x] **034.4** — Tests. In `internal/workflow`: the skeleton parses with zero
  errors and zero warnings under the curated catalogs `TestShippedExamplesValidate`
  uses; every embedded example still parses clean *after* the rewrite,
  table-driven over `examples.Names()`; the rewrite touches the top-level key and
  nothing else, over a fixture carrying `- name:` under `fields:`, a `name:`
  nested in a step and a `name:` inside a `prompt:` block scalar, with every
  comment byte preserved, and CRLF survives. In `internal/cli`: every refusal
  exits non-zero and leaves the tree byte-identical (existing path; a sibling
  declaring the name, in both sort orders; a bad slug; an unknown `--from`, whose
  error lists the valid ones), `--project` with no daemon exits 2 and writes
  nothing, shadow warnings fire without blocking, and two e2e tests through the
  real binary cover the no-daemon cold start (write, then `validate` the written
  file) and live reload (`init` then `workflow ls` with a daemon up, no restart).
  ✓ 2026-08-26
- [x] **034.5** — Docs: `docs/reference/cli.md` gains
  `### vincent workflow init` and its exit-code row is corrected;
  `docs/getting-started/quickstart.md` and `docs/guides/workflows.md` lead with
  `init` instead of `cp examples/...` and position it against `create-workflow`;
  spec §12.1's CLI table amended. §5.2 needs no amendment — `init` changes no
  registry semantics, it consumes the shadowing and first-file-wins rules already
  written there. ✓ 2026-08-26
