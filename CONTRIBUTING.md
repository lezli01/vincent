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

The project is specified in [docs/versions/v0/spec.md](docs/versions/v0/spec.md)
and work is tracked in [docs/versions/v0/tasks.md](docs/versions/v0/tasks.md).
Check the task breakdown first — if what you want to do maps to a task, mention
its ID (e.g. `T2.5`) in your PR; if it doesn't, consider opening an issue to
discuss before investing significant effort.

## Development

The daemon and TUI are written in Go (see the spec for the architecture).
Build targets run via [mage](https://magefile.org/) with zero install
(list all targets with `go run mage.go -l`):

```sh
go run mage.go build     # build the vincent binary into bin/
go run mage.go test      # run all tests
go run mage.go testrace  # run all tests with the race detector
go run mage.go lint      # golangci-lint (pinned via go.mod tool directive)
```

## Pull request checklist

- [ ] Commits follow Conventional Commits
- [ ] Tests added/updated for behavior changes
- [ ] `docs/versions/v0/tasks.md` progress updated if the PR completes a task
- [ ] CI green on all three platforms
