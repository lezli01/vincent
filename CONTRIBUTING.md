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

The project is specified in [docs/spec.md](docs/spec.md) — one spec, not
versioned, describing the system as it is now. Behaviour changes are amended into
it in the same PR as the code, never ahead of it.

Planned and in-flight work lives in [docs/tasks/](docs/tasks/README.md), one
document per piece of work. If what you want to do is a task there, mention its
ID in your PR (e.g. "closes 001.4") and mark it `[~]` before you start. That
README also defines the status markers and the rule that a decision recorded in a
task document is binding.

For anything not in that folder, **open an issue first** and let it be agreed
before you invest significant effort, then reference the issue in your PR.

The v0 ledger, [docs/history/v0-tasks.md](docs/history/v0-tasks.md), is a
**closed** record — all 70 tasks are done and `v0.1.0` is released. There is no
open task ID in it to claim, but it is worth reading: the design decisions it
carries are binding, and code comments cite them by name ("phase 2 decision",
"T1.5/T1.6 decision"). If your change touches an area the spec covers, say which section
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

To use the tree you are working on as your everyday `vincent` — the binary on
PATH, with the same release build flags and version symbols, so `vincent
version` names your checkout:

```sh
./scripts/install-local.sh              # /usr/local/bin, sudo if needed
./scripts/install-local.sh --user       # ~/.local/bin
./scripts/install-local.sh --dry-run    # build and report, install nothing
./scripts/install-local.sh --uninstall  # remove the binary; config and data stay
```

It warns when another `vincent` earlier on PATH shadows the install, and when a
daemon is still running the previous build (`vincent daemon stop` hands over).

Cross-platform support is a hard requirement — Windows, macOS and Linux all run
the full suite plus three acceptance gates in CI. If you touch build-tagged
code, lint the *other* platforms too, since a host-only lint cannot see them:

```sh
LINT=$(go tool -n golangci-lint)
for os in windows darwin linux; do GOOS=$os "$LINT" run ./...; done
```

## Licensing of contributions

vincent is distributed under a dual-license model consisting of a non-commercial
source-available license ([PolyForm Noncommercial 1.0.0](LICENSE)) and separate
commercial licenses ([COMMERCIAL-LICENSE.md](COMMERCIAL-LICENSE.md)).

By submitting a contribution, you agree that your contribution may be
distributed by the vincent project under the project's current non-commercial
license and under separate commercial licenses offered by the project owner.

You retain copyright in your contribution unless otherwise agreed.

> **Note:** this is a lightweight statement of intent, not a formal Contributor
> License Agreement, and it is not necessarily a substitute for one. If a CLA
> becomes necessary — for example if the project takes on contributions large
> enough that clear relicensing rights matter — one will be added separately and
> announced, rather than read into this paragraph.

## Pull request checklist

- [ ] Commits follow Conventional Commits
- [ ] Tests added/updated for behavior changes
- [ ] User-visible changes noted under `## [Unreleased]` in
      [CHANGELOG.md](CHANGELOG.md)
- [ ] The relevant page under [docs/](docs/README.md) updated if this changes
      config, the CLI, the API, TUI bindings or a block reason
- [ ] CI green on all three platforms (`ci` and `gates`)

Maintainers: the release process is [RELEASING.md](RELEASING.md).
