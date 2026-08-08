<p align="center">
  <img src="docs/assets/logo.png" alt="vincent" width="520">
</p>

<p align="center">
  <strong>Vendor-independent control plane for executing native agent tooling.</strong>
</p>

<p align="center">
  A local-first orchestrator for AI coding-agent workloads — monitor and manage<br>
  many agent tasks on your machine from one central place.
</p>

<p align="center">
  <a href="https://github.com/lezli01/vincent/actions/workflows/ci.yml"><img src="https://github.com/lezli01/vincent/actions/workflows/ci.yml/badge.svg" alt="CI"></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/License-MIT-yellow.svg" alt="License: MIT"></a>
  <a href="https://www.buymeacoffee.com/lezli01"><img src="https://img.shields.io/badge/Buy_Me_a_Coffee-ffdd00?logo=buymeacoffee&logoColor=black" alt="Buy Me a Coffee"></a>
</p>

<p align="center">
  <a href="#why-vincent">Why</a> &bull;
  <a href="#what-it-does">What It Does</a> &bull;
  <a href="#project-status">Status</a> &bull;
  <a href="#build--test">Build &amp; Test</a> &bull;
  <a href="#contributing">Contributing</a> &bull;
  <a href="docs/versions/v0/spec.md">Spec</a>
</p>

---

## Why vincent?

Coding agents are easy to start and hard to supervise: kick off a handful of
CLI runs across a few repositories and you are soon juggling terminals, losing
transcripts, and hand-managing branches. vincent turns that into a managed
workload — one place to register repositories, author workflows, launch tasks,
and see what every agent is doing, with work continuing when no client is
attached.

The name is an acronym, and every part of it maps to the system:

- **Vendor-independent** — supports multiple agent providers and CLIs.
- **Control plane** — the daemon owns state, workflows, scheduling, and
  execution.
- **Executing** — vincent runs workloads rather than merely observing them.
- **Native agent tooling** — it invokes locally installed tools such as
  Claude Code and Codex.

It is released under the [MIT License](LICENSE) and created by `lezli01` at
[lezli01.is-a.dev](https://lezli01.is-a.dev). Contributions are welcome — see
[Contributing](#contributing).

## What It Does

A background daemon owns all state and execution: register local git
repositories, author reusable workflows (agent prompts, shell commands, manual
gates), and run any number of tasks, each isolated in its own git worktree.
Agent steps drive locally installed agent CLIs (Claude Code and Codex first)
headlessly. A TUI — and later a web UI — is a thin client over the daemon's
API, so work keeps running when no client is attached.

## Project Status

**Implementation in progress.** The v0 specification is complete and work
follows the task breakdown:

- **Phase 0 — scaffolding** (Go module, CLI stubs, dev tooling,
  cross-platform CI): **done**.
- **Phase 1 — the daemon spine** (config, SQLite store, daemon lifecycle +
  HTTP API, projects and worktrees, the Claude agent adapter, first
  end-to-end task run): **done** — the M1 acceptance gate passes in CI.
- **Phase 2 — the workflow engine**: **underway**. Landed so far: the
  workflow registry with live reload, the template engine, the task state
  machine and step executors (agent, command, manual gate, checks, retries,
  timeouts), the scheduler with global and per-project caps, and the human
  actions API (cancel, pause, resume, retry with edit, skip, approve,
  reject, archive, priority). Still to come: events/SSE, crash recovery,
  the Codex adapter, the agent option catalog, interactive input requests,
  and the M2 acceptance gate.

See [docs/versions/v0/spec.md](docs/versions/v0/spec.md) for the product and
implementation spec, and
[docs/versions/v0/tasks.md](docs/versions/v0/tasks.md) for the task breakdown
and progress.

## Build & Test

vincent is a single Go module; Go 1.26 or newer is the only prerequisite.
Build targets run via [mage](https://magefile.org/) with zero install — CI
runs exactly these targets (list them all with `go run mage.go -l`):

```sh
go run mage.go build     # build the vincent binary into bin/
go run mage.go test      # run all tests
go run mage.go testrace  # run all tests with the race detector (needs cgo and a C compiler)
go run mage.go lint      # golangci-lint (pinned via go.mod tool directive)
```

The plain toolchain works too:

```sh
go build ./...   # compile every package
go test ./...    # run the full test suite
```

Tests are self-contained: they run against temporary SQLite databases,
throwaway git repositories, and a fake agent built from `cmd/fakeagent` on
the fly — no real agent CLI, network access, or running daemon required.
CI runs lint, the race-enabled tests, and the build on Linux, macOS, and
Windows, plus `scripts/m1-gate.sh`, the phase-1 acceptance gate.

## Contributing

Contributions of every size are welcome — bug reports, docs, test cases, and
features. Start here:

- Read the [Contributing guide](CONTRIBUTING.md) for development setup, build
  and test commands, and the commit-message convention.
- Be a good neighbor: this project follows a
  [Code of Conduct](CODE_OF_CONDUCT.md).
- Found a bug or want a feature? Open an
  [issue](https://github.com/lezli01/vincent/issues/new/choose).

All changes land via pull request to `master`, merged with merge commits (no
squashing), using [Conventional Commits](https://www.conventionalcommits.org/).
Details are in [CONTRIBUTING.md](CONTRIBUTING.md).

## Security

vincent executes AI agents in full-auto mode by default — a documented design
decision (see [the spec](docs/versions/v0/spec.md), §16), not a vulnerability —
but security reports are taken seriously. Please report vulnerabilities
privately via GitHub's
[security advisories](https://github.com/lezli01/vincent/security/advisories/new)
rather than a public issue. See [SECURITY.md](SECURITY.md) for details.

## License

vincent is released under the [MIT License](LICENSE). © 2026 lezli01.
