# Contributing to vincent

Thanks for your interest in contributing!

## Ground rules

- **Everything goes through a pull request** against `master`. Direct pushes
  are blocked by branch protection.
- **Merge commits only.** PRs are merged with a merge commit; squash and
  rebase merges are disabled. Keep your branch history clean — it becomes part
  of `master`'s history.
- **Conventional Commits.** Every commit message follows
  [Conventional Commits](https://www.conventionalcommits.org/):
  `type(scope?): summary` with types like `feat`, `fix`, `docs`, `ci`,
  `chore`, `refactor`, `test`.
- **Branch naming:** `type/short-description`, e.g. `feat/scheduler-caps`,
  `docs/v0-spec-fixes`.
- **CI must be green** (build, test, lint on Linux, macOS, and Windows) before
  merging.

## Before you start

User-facing documentation lives in [docs/](docs/README.md) — getting started,
guides, per-platform notes and reference. It is written **from the source**: the
configuration reference tracks `internal/config`, the CLI page the cobra command
tree, the API page the route table in `internal/api/server.go`, the TUI key
tables `internal/tui/bindings.go`, and the failure reasons the `Reason*`
constants. If your PR changes one of those, update its page in the same PR.

The project is specified in [docs/versions/v0/spec.md](docs/versions/v0/spec.md),
and the v0 work is tracked in
[docs/versions/v0/tasks.md](docs/versions/v0/tasks.md).

**v0 is complete — all 70 tasks are done and `v0.1.0` is released.** So the
task breakdown is now a closed record, not a to-do list: read it for the design
decisions it carries (they are binding, and cited from code comments as
"phase 2 decision", "T1.5/T1.6 decision"), but there is no open task ID to claim.

For new work, **open an issue first** and let it be agreed before you invest
significant effort. Reference the issue in your PR the way v0 PRs referenced a
task ID. If your change touches an area the spec covers, say which section
(`§9.7`) and whether you believe the spec needs updating alongside the code —
a change that contradicts the spec without amending it will be sent back.

## Development

The daemon and TUI are written in Go (see the spec for the architecture).
Build targets run via [mage](https://magefile.org/) with zero install
(list all targets with `go run mage.go -l`):

```sh
go run mage.go build     # build the vincent binary into bin/
go run mage.go test      # run all tests
go run mage.go testrace  # run all tests with the race detector
go run mage.go lint      # golangci-lint (pinned via go.mod tool directive)
go run mage.go vuln      # govulncheck across linux, darwin and windows
```

Cross-platform support is a hard requirement — Windows, macOS and Linux all run
the full suite plus three acceptance gates in CI. If you touch build-tagged
code, lint the *other* platforms too, since a host-only lint cannot see them:

```sh
LINT=$(go tool -n golangci-lint)
for os in windows darwin linux; do GOOS=$os "$LINT" run ./...; done
```

## Pull request checklist

- [ ] Commits follow Conventional Commits
- [ ] Tests added/updated for behavior changes
- [ ] User-visible changes noted under `## [Unreleased]` in
      [CHANGELOG.md](CHANGELOG.md)
- [ ] The relevant page under [docs/](docs/README.md) updated if this changes
      config, the CLI, the API, TUI bindings or a block reason
- [ ] CI green on all three platforms (`ci` and `gates`)

Maintainers: the release process is [RELEASING.md](RELEASING.md).
