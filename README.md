# vincent

[![CI](https://github.com/lezli01/vincent/actions/workflows/ci.yml/badge.svg)](https://github.com/lezli01/vincent/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

**VINCENT** — **v**endor-**in**dependent **c**ontrol plane for **e**xecuting
**n**ative agent **t**ooling.

A local-first orchestrator for AI coding-agent workloads: monitor and manage
many agent tasks on your machine from one central place.

A background daemon owns all state and execution: register local git
repositories, author reusable workflows (agent prompts, shell commands, manual
gates), and run any number of tasks, each isolated in its own git worktree.
Agent steps drive locally installed agent CLIs (Claude Code and Codex first)
headlessly. A TUI — and later a web UI — is a thin client over the daemon's
API, so work keeps running when no client is attached.

## Why "vincent"?

The name is an acronym, and every part of it maps to the system:

- **Vendor-independent** — supports multiple agent providers and CLIs.
- **Control plane** — the daemon owns state, workflows, scheduling, and
  execution.
- **Executing** — vincent runs workloads rather than merely observing them.
- **Native agent tooling** — it invokes locally installed tools such as
  Claude Code and Codex.

## Status

**Implementation in progress.** The v0 specification is complete and work
follows the task breakdown: phase 0 (scaffolding — Go module, CLI stubs, dev
tooling, cross-platform CI) is done, and phase 1 (the daemon spine — config,
SQLite store, daemon + API, first end-to-end task run) is underway.

- [v0 specification](docs/versions/v0/spec.md)
- [v0 task breakdown & progress](docs/versions/v0/tasks.md)

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md). In short: all changes land via pull
request to `master`, merged with merge commits (no squashing), using
[Conventional Commits](https://www.conventionalcommits.org/).

## License

[MIT](LICENSE)
