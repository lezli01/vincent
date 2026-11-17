# 148 — The board grouped by vincent issue

**Status:** 🔄 in progress (1/3) · **Opened:** 2026-11-17 · **Issues:** #981, #982, #983, #984

The author asked for the task board to be grouped by the vincent issue each
task was started from, and for that to be the default. Issue #981 splits it in
three: the daemon's configuration vocabulary (#982), the TUI's rendering
(#983), and the guide, screenshots and CHANGELOG (#984).

Conventions for this file are in [the tasks README](README.md).

## What this changes

- **Task 009 decision 6** ("`project` and `workflow` are the whole
  vocabulary") — **amended** by decision 2 below.
- **Task 132.9's `[workflow]` default** — **superseded** by decision 3. Its
  `project` deprecation (stripped on load with a warning, refused on write) is
  kept unchanged.

## Decisions

### 1. `issue` joins `workflow`; `workflow` stays a full level

*2026-11-17.* The vocabulary is exactly two levels, `issue` and `workflow`:
either alone, both in either order (`[issue, workflow]`,
`[workflow, issue]`), or `[]`. There is no deprecation cycle for `workflow`.
A repeated level is refused as before; an unknown one is refused with
`want issue, workflow, or [] for a flat table`.

### 2. This amends task 009 decision 6

*2026-11-17.* Decision 6 kept agent, model and priority out because none of
them is how anyone reads a board. The issue *is* how the issues pillar (§5.6)
organises work, so that argument does not reach it. `state` stays absent for
decision 6's own reason: it would fight the band sort. The amendment is also
recorded as a dated note on decision 6 itself.

### 3. The default is `[issue]`

*2026-11-17.* In both `config.Default()` and the bootstrapped `config.yaml`,
whose comment now names both levels and `[]`.

### 4. The migration is literal

*2026-11-17.* An existing file that says `[workflow]` loads as `[workflow]` —
no rewrite and no reinterpretation. A bootstrapped value cannot be told apart
from a deliberate one, and an explicit value is a choice: the same reasoning
as task 132 decision 51.

### 5. No migration notice

*2026-11-17.* Neither a load, the daemon log nor `vincent doctor` reports
anything when the value is exactly `[workflow]`: it is a valid, deliberate
setting, and a notice on it could only be silenced by changing the setting.
The CHANGELOG note (148.3) and the configuration reference are the signal.

### 6. Compatibility window

*2026-11-17.* A TUI that predates the `issue` level, against a daemon serving
`[issue]`, drops the unknown level (task 009 decision 6 allows it) and draws
the flat table. 148.2 lands in the same release as 148.1.

*2026-11-17, amended.* 148.1 merged to `master` without 148.2 (the train
for #875, review F2), so the window is open on `master` and the rolling
`master-latest` build until 148.2 lands: a fresh install's board is the
flat table, the config editor cannot offer `issue`, and the configuration
reference's `[issue]` row describes a grouping no TUI draws yet. The
default stays `[issue]` regardless — a file bootstrapped in the window says
`[issue]` and so groups by issue the moment 148.2 lands, where a temporary
`[workflow]` would stay `[workflow]` for good (decision 4). Two obligations
follow:

- **No release is cut between 148.1 and 148.2.** A tag in the window
  would publish the gap under `/latest/` and to every `vincent update`.
- **148.2 restores the guard.** `TestDefaultGroupingMatchesTheConfigDefault`
  only logs a default level the TUI does not render; once `parseGrouping`
  keeps `issue`, 148.2 deletes that branch so the comparison fails again.

## Tasks

- [x] **148.1** (#982) `internal/config`: the `issue` level, its validation
  message, the `[issue]` default and the bootstrap template; the `PATCH
  /v1/config` and load tests; the configuration reference, the §12.3 and §15
  amendments and task 009's note. ✓ 2026-11-17
- [ ] **148.2** (#983) The TUI renders the `issue` level: the group key, the
  `g` cycle, the headers and the group for tasks started from no issue, and
  the config editor's choices. Before 148.2 merges, no release is cut;
  with it, `TestDefaultGroupingMatchesTheConfigDefault` fails again rather
  than logging (decision 6, amended).
- [ ] **148.3** (#984) The TUI guide, the features page, the screenshots and
  the CHANGELOG entry.
