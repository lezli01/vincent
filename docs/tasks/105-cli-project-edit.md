# 105 — `vincent project edit`: change a registered project's settings from the command line

**Status:** ✅ done (4/4)
**Issue:** [#394](https://github.com/lezli01/vincent/issues/394)
**Spec:** amends §12.1

## Problem

`PATCH /v1/projects/{id}` (T1.5) already accepted six fields: `name`, `path`,
`default_branch`, `default_workflow`, `max_parallel_tasks` and `branch_template`.
A missing field stays unchanged, an explicit `null` clears the three optional
ones, and `null` on the other three is `validation_failed`. A new `path` re-runs
registration's checks, and `default_branch` must still exist in the new
repository. `apiclient.PatchProject` and its `Opt[T]` request existed. No CLI
command called them.

`vincent project` had only `add`, `ls` and `rm`. The TUI's project form edits
five fields, not `branch_template`, so a project's branch template could only be
written through the API or MCP `project_patch`. Task 048's "What is still
API-only" list named the project `PATCH` and tracked it as #394.

Three public pages were wrong or incomplete: `docs/reference/cli.md` said
per-project settings "are edited with `vincent project`", which was false, and
`docs/guides/troubleshooting.md` (`project_path_missing`) and
`docs/reference/configuration.md` (the `branch_template` precedence row and the
per-project settings intro) named only `PATCH` and the TUI.

## What shipped

```
vincent project edit <id> [--name NAME] [--path PATH] [--default-branch BRANCH]
                          [--workflow NAME] [--max-parallel N] [--branch-template TMPL]
                          [--json]
```

A thin API client in `internal/cli/project.go`, registered beside `add`, `ls`
and `rm`. No API, store, apiclient, MCP, TUI or §13.2 change, and no migration.

- The id is parsed as `project rm` parses it; a non-number is refused before any
  request.
- Only flags the user set (`cmd.Flags().Changed`) are sent, each as `SetOpt` or
  `NullOpt`. `projectPatchFromFlags` builds the request from the flags alone.
- No field flag is refused on the client: the error lists the six flags and the
  command exits 1 without a client, so it exits 1 even with no daemon. `--json`
  alone counts as no field.
- Output follows `project add`: one line from the returned project
  (`project N updated: …`), or the returned `apiclient.Project` with `--json`. A
  daemon refusal prints `apiMessage` and exits 1; no daemon exits 2; the command
  never starts one (PR U decision).

## Decisions

1. **2026-09-16 — An empty value clears an optional field.** `--workflow ""`,
   `--max-parallel ""` and `--branch-template ""` send `null`, and a value of
   only whitespace counts as empty because the TUI form trims its rows. This is
   the TUI's rule (an empty workflow row clears it, an empty cap row means no
   project cap) and `vincent config set`'s (`""` empties a list), and the server
   already treats an empty `branch_template` as "inherit". So `--max-parallel` is
   a **string** flag: empty sends `null`, an integer is sent as given — `0` and
   negatives reach the daemon and come back with its "must be at least 1 (null =
   unlimited)" — and anything else is refused on the client with exit 1.
   `--name`, `--path` and `--default-branch` have no clear form: an empty value
   is sent as typed and the daemon refuses it, and the CLI does not repeat that
   check. Rejected: separate `--no-X` flags, and a repeatable `--unset FIELD`.
   Both add flags and depart from the TUI's and `config set`'s rule.

2. **2026-09-16 — `edit` covers all six fields; `project add` stays as it is.**
   The issue asks for "a flag per patchable field", so `edit` has
   `--branch-template` although `add` does not and the TUI form cannot edit it.
   Flag names reuse `add`'s (`--name`, `--default-branch`, `--workflow`,
   `--max-parallel`), plus `--path` and `--branch-template`. Adding
   `--branch-template` to `add` here was rejected as scope creep; leaving it out
   of `edit` was rejected because branch templates would stay API-only.

3. **2026-09-16 — `--path` is sent as typed, as `project add <path>` is.**
   Following T1.5 ("absolute path required … explicit beats magic"), the CLI does
   not run `filepath.Abs`. A relative path gets the daemon's "must be absolute
   (the daemon does not share your working directory)" unchanged, exit 1, and so
   does the daemon's repoint hint ("pass default_branch in the same request");
   the reference page says `--path` and `--default-branch` can be given
   together. Resolving on `edit` alone was rejected because `add` and `edit`
   would disagree; resolving on both was rejected because it changes `add` in a
   change about `edit`.

## Work

- [x] **105.1 — The command**: `newProjectEditCmd`, `projectPatchFromFlags` and
  the `AddCommand` line in `internal/cli/project.go`; `project`'s `Short`
  becomes "Register, inspect, edit and remove projects". ✓ 2026-09-16
- [x] **105.2 — Request builder tests**: `internal/cli/project_test.go` — each
  flag alone sends only its own key, empty and whitespace-only optional values
  send `null`, `--max-parallel` sends integers (`0` and negatives included) and
  refuses non-integers, an empty `--name`/`--path`/`--default-branch` is sent as
  `""`, and no field flag (or `--json` alone) is refused with every flag named.
  ✓ 2026-09-16
- [x] **105.3 — E2e tests**: a `project edit` subtest in
  `internal/cli/actions_e2e_test.go` — every field set and read back with
  `project ls --json`, the optional three cleared to `null`, a repoint refused
  for a missing default branch and accepted with `--default-branch`, a relative
  path, a zero cap, a missing branch and a duplicate name each exiting 1 with the
  daemon's wording, `--json` printing the updated project, and an empty edit
  changing nothing. With no daemon, `project edit 1 --name x` exits 2
  (`commands_e2e_test.go`'s table) and `project edit 1` exits 1 and starts
  nothing. ✓ 2026-09-16
- [x] **105.4 — Documentation**: §12.1 amended, `docs/reference/cli.md` (new
  section, exit-code row), `docs/reference/configuration.md`,
  `docs/guides/troubleshooting.md`, `CHANGELOG.md`, and the dated note on task
  048's "What is still API-only". `docs/guides/scripting.md` lists no project
  commands, so it is unchanged. ✓ 2026-09-16
