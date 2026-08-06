# vincent

[![CI](https://github.com/lezli01/vincent/actions/workflows/ci.yml/badge.svg)](https://github.com/lezli01/vincent/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

Local-first orchestrator for AI coding-agent workloads — monitor and manage
many agent tasks on your machine from one central place.

A background daemon owns all state and execution: register local git
repositories, author reusable workflows (agent prompts, shell commands, manual
gates), and run any number of tasks, each isolated in its own git worktree.
Agent steps drive locally installed agent CLIs (Claude Code and Codex first)
headlessly. A TUI — and later a web UI — is a thin client over the daemon's
API, so work keeps running when no client is attached.

## Status

**Design phase.** The v0 specification is complete; implementation follows the
task breakdown.

- [v0 specification](docs/versions/v0/spec.md)
- [v0 task breakdown & progress](docs/versions/v0/tasks.md)

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md). In short: all changes land via pull
request to `master`, merged with merge commits (no squashing), using
[Conventional Commits](https://www.conventionalcommits.org/).

## License

[MIT](LICENSE)
