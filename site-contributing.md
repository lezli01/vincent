---
title: Contributing
description: How to contribute code, documentation, and fixes to Vincent.
permalink: /contributing.html
---

# Contributing to Vincent

Contributions are welcome. This page covers the public contributor workflow; the repository's [canonical CONTRIBUTING guide](https://github.com/lezli01/vincent/blob/master/CONTRIBUTING.md) remains the detailed source of truth.

## Before you start

For a substantial change, open an issue first and describe the user problem, expected outcome and alternatives. Small fixes and documentation improvements can go straight to a pull request when the intent is obvious.

- Work on a dedicated branch and open a pull request against `master`.
- Use [Conventional Commits](https://www.conventionalcommits.org/) for commit messages: `type(scope?): summary`.
- Keep user-facing documentation in sync with behavior changes.
- CI must pass on Linux, macOS and Windows before merge.

## Development

Vincent is written in Go. Build and verification tasks are exposed through Mage and require no separate Mage installation:

```sh
go run mage.go build
go run mage.go test
go run mage.go testrace
go run mage.go lint
go run mage.go vuln
```

To install the current checkout as your local `vincent` binary:

```sh
./scripts/install-local.sh
./scripts/install-local.sh --user
./scripts/install-local.sh --dry-run
```

Start with [Concepts](docs/getting-started/concepts.md) for the architecture, then use the [feature overview](docs/features.md), [workflow guide](docs/guides/workflows.md) and [reference documentation](docs/reference/cli.md) for the area you are changing.

## Pull request checklist

- [ ] Commits follow Conventional Commits.
- [ ] Tests cover behavior changes.
- [ ] User-visible behavior is documented in the relevant guide or reference page.
- [ ] Release-impacting changes are represented in the changelog/release notes.
- [ ] CI is green on all supported platforms.

## Licensing

Vincent uses a dual-license model: the source is available under the [PolyForm Noncommercial 1.0.0 license](https://github.com/lezli01/vincent/blob/master/LICENSE), while commercial use requires a separate [commercial license](https://github.com/lezli01/vincent/blob/master/COMMERCIAL-LICENSE.md).

By submitting a contribution, you agree that it may be distributed under Vincent's current non-commercial license and under separate commercial licenses offered by the project owner. You retain copyright in your contribution unless otherwise agreed.

For the complete contributor policy and maintainer details, read [CONTRIBUTING.md on GitHub](https://github.com/lezli01/vincent/blob/master/CONTRIBUTING.md).
