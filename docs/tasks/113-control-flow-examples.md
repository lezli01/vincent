# 113 — Example workflows that show control flow

**Status:** ✅ done (4/4)
**Opened:** 2026-09-17

*Issue [#408](https://github.com/lezli01/vincent/issues/408).*

`examples/` shipped five workflows, embedded by `examples/embed.go` and offered
by `vincent workflow init --from` (task 034). Only `converge.yaml` used control
flow (`loop`, `break`, `if:`, `allow_failure`). None used `parallel`, `fan_out`
(declared or derived), `type: condition`, `include`, `on_input: require` or
`platforms:`. Task 034 decision 3 derives the `--from` values from the embedded
filesystem, so a new file is offered with no code change.

This task adds three examples — `go-checks`, `ship` and `split-work` — that
between them cover those features, each as a realistic shape of work rather
than one feature per file. It also fixes three existing problems found in the
same files and sentences:

- `fix-and-test.yaml`'s `check: '! go test ./...'` cannot run under pwsh, which
  reads `!` as `-not` and rejects the bare command after it, and its comment
  claimed the syntax was understood by "bash, cmd and PowerShell 7 alike". On
  Windows the check always failed, so the step retried and then blocked.
- `cursor-review.yaml`'s comment named bash, cmd and PowerShell 7 as the shells
  §8.3 may hand a command step. §8.3 hands it `/bin/sh` or `pwsh`.
- The quickstart said `docs-update` "runs `restricted`". It runs full-auto, and
  its header explains why.

`docs/spec.md` is not amended: examples are not specified behaviour, and §12.1's
`init` row describes `--from` as "an embedded `examples/*.yaml`" with no count.

## Decisions

**1. `include` is shown with a fragment and a workflow that includes it, and
`init --from` is not changed.** *(2026-09-17)*

`go-checks.yaml` is a fragment, and `ship.yaml` and `split-work.yaml` include it
by name. That is the only honest way to show `include` in files installed one at
a time: `init --from` installs one file and rewrites its `name:` (task 034
decision 2), and `workflow.Parse` and `vincent workflow validate` never resolve
an include target, so a missing fragment surfaces only as a `400` at task
creation (§7.9). Every one of the three headers therefore says to install the
fragment under its shipped name (`vincent workflow init go-checks --from
go-checks`) and why. The fragment also runs as a workflow on its own, so it is
an example rather than a stub, and declares no `platforms:`, because a fragment
whose `platforms:` rules out the host is refused at task creation.

The alternative it beat: teaching `init --from` to install an example's include
targets too. That would reopen task 034's rules for collisions and renames — a
dependency installed under a renamed includer, or colliding with a file already
there — for a problem one documented command solves.

**2. `platforms:` is shown on `fix-and-test`, where the restriction is real.**
*(2026-09-17)*

`fix-and-test.yaml` declares `platforms: [posix]`, and its wrong comment about
`!` is replaced by one pointing at the guide's §10.3. None of the new examples
declares `platforms:`: their bodies run in both shells, so the declaration would
be false. The cost is accepted: `fix-and-test` can no longer be picked on a
Windows daemon — it is listed as `unsupported` and `POST /v1/tasks` refuses it —
where before it could be picked and then blocked. The scripting guide's "Starting
tasks from CI" snippets create `fix-and-test` tasks from a runner on the
daemon's machine, so it now says a Windows host needs another workflow there.

The alternative it beat: pinning `shell: sh` on the check. On Windows that
resolves to whatever `sh` is on `PATH`, which makes the example depend on Git
Bash being installed and on `PATH` for the daemon, and fails with
`shell_unavailable` when it is not — a machine-dependent block instead of a
declared one.

**3. The portability rule is enforced by a static test, not by review.**
*(2026-09-17)*

`TestShippedExamplesArePortable` in `internal/workflow/examples_test.go` walks
every step of every shipped example at every depth — top level, `parallel`
members, `loop` bodies, declared and templated lanes' inline steps, and
`merge.agent`, through `PreviewSteps` — and checks each `run:` and `check:` body
against the "Does not" column of the guide's §10.2: `!`, `[ … ]`, `test -…`,
`for`/`if`/`case`, `touch`/`seq`/`cat`/`grep` in command position, and `exit`
that is not the whole body. It skips a file whose `platforms:` leaves Windows
out and a step that pins `shell:`. It is a heuristic over unrendered text and
says so. It cannot pass vacuously: `TestPortabilityScanCatchesUndeclaredPOSIX`
removes `fix-and-test`'s `platforms:` line and requires the scan to name
`reproduce`'s check, and `TestPortabilityScanPatterns` pins what each pattern
does and does not match.

The alternative it beat: an end-to-end gate running the examples on all three
platforms. The bodies need a Go module and toolchain, and parsing, rendering and
the static scan cover YAML files at the right level (task 034 decision 9's
reasoning).

## Work

- [x] **113.1 — `examples/go-checks.yaml`, `examples/ship.yaml`, `examples/split-work.yaml`, each with a header saying what shape of work it is and why each control-flow feature is there.** ✓ 2026-09-17
- [x] **113.2 — `fix-and-test.yaml` declares `platforms: [posix]` with a corrected comment; `cursor-review.yaml`'s shell comment names `/bin/sh` and `pwsh`.** ✓ 2026-09-17
- [x] **113.3 — Tests: the portability scan with its negative case and pattern table; `renderAll` renders a `lane:` template's own `if`, `id`, `needs` and `fields` through `RenderLane`, as `vincent workflow render` does.** Depends: 113.1, 113.2. ✓ 2026-09-17
- [x] **113.4 — Docs: a table of every example in the workflow guide with links from §4.5, §4.6, §4.7, §4.10, §7.3, §9.4 and §10.3; counts and name lists in the quickstart, `README.md`, `docs/README.md`, the CLI reference and the schema reference; the scripting guide's Windows note; `CHANGELOG.md`.** ✓ 2026-09-17

## What the tests prove

- Each new example parses with no errors and no warnings, and every template in
  it renders, including the derived fan-out's lane template and its own `id`,
  `needs` and `fields` (`TestShippedExamplesValidate`).
- Each new example survives `SetName` and still parses clean
  (`TestEmbeddedExamplesRenameAndValidate`).
- No shipped example that may run on Windows has a `run:` or `check:` body
  outside the sh∩pwsh intersection, and the scan catches `fix-and-test`'s `!`
  once its `platforms:` line is gone.
- The quickstart names every example and gives the right count
  (`TestDocsClaimsQuickstartListsEveryExample`).
- `internal/cli/workflow_init_e2e_test.go` still installs `fix-and-test` and
  lists it: it rejects only an `invalid` entry, and on Windows the entry is now
  `unsupported`, which it accepts.
