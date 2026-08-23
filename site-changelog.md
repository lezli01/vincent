---
title: Changelog
description: Product-focused release history for Vincent.
permalink: /changelog.html
---

# Changelog

This is the human-readable Vincent release history. The generated [canonical CHANGELOG](https://github.com/lezli01/vincent/blob/master/CHANGELOG.md) remains the source used by release automation, while this page removes duplicate commit subjects and keeps the product impact clear.

## 0.5.0 — Workflow authoring and structured task input

Released 2026-08-22.

### Added

- **Vincent Workflows agent skill.** The portable authoring skill helps agents design cost-aware workflows, prefers deterministic commands and native control flow, and asks about human gates, interaction, acceptance checks, side effects and failure policy before generating a workflow. ([#165](https://github.com/lezli01/vincent/pull/165))
- **Workflow-declared task fields.** Workflows can define ordered inputs with labels, descriptions, required flags, typed values and optional validation patterns. The TUI pre-renders them while still accepting additional undeclared fields. ([#163](https://github.com/lezli01/vincent/pull/163))

## 0.4.2 — Release verification hardening

- **RPM verification** now normalizes payload paths before extraction, avoiding GNU cpio warning exits while keeping validation isolated. ([#161](https://github.com/lezli01/vincent/pull/161))

## 0.4.1 — Package validation fixes

- **Release package verification** was hardened so provenance generation and Linux, macOS and Windows smoke tests complete correctly for published packages. ([#159](https://github.com/lezli01/vincent/pull/159))

## 0.4.0 — Better TUI workflows and broader distribution

- **Roomier responsive TUI workflows** added guided task creation plus persistent navigation for Projects and Workflows. ([#153](https://github.com/lezli01/vincent/pull/153))
- **More installation channels** added WinGet, Scoop, mise, deb and rpm distribution with checksummed native release packages. ([#158](https://github.com/lezli01/vincent/pull/158))

## 0.3.0 — Workflow language and task operations

- Reusable workflow composition with `type: include`.
- Platform restrictions and interactive-agent capability gating.
- Configurable task-board grouping, bulk actions and file-grouped diffs.
- Workflow graph visualization for structured control flow.
- Loops, breaks, conditions and `allow_failure` for data-driven execution.
- Parallel sub-steps and fan-out child tasks with isolated worktrees and merge-back.

## Older releases

Earlier releases are available in the [canonical changelog](https://github.com/lezli01/vincent/blob/master/CHANGELOG.md) and on [GitHub Releases](https://github.com/lezli01/vincent/releases). This page is intentionally edited for humans; the canonical file remains complete and machine-maintained.
