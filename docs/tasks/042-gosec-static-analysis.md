# 042 — gosec in the normal static-analysis gate, with reviewed suppressions

**Status:** ✅ done (5/5) · **Opened:** 2026-08-28

`.golangci.yml` enabled a strong quality set and no security linter, while the
tree already carried 24 `//nolint:gosec` directives written in anticipation of
one. This task turns gosec on inside the golangci-lint the `go.mod` tool
directive already pins, adjudicates every current finding one site at a time,
and records the adjudication here so a future contributor reads a reason rather
than a bare `//nolint`.

A standalone review run against
[7b621865](https://github.com/lezli01/vincent/commit/7b6218658b040d03aeece9dc4bba78a5369d6a2b)
produced 42 raw findings ([#147](https://github.com/lezli01/vincent/issues/147)).
The two that mattered — workflow file loading and config modes — were promoted to
their own issues and closed as [#136](https://github.com/lezli01/vincent/issues/136)
and [#141](https://github.com/lezli01/vincent/issues/141). What remained, and
what this task is, is the baseline and the gate.

Conventions for this file are in [the tasks README](README.md). Behaviour lands
in [the spec](../spec.md); nothing here changes runtime behaviour, so the spec is
untouched — see the last decision.

## Tasks

- [x] **042.1** Enable gosec in `.golangci.yml`, with the output limits removed
      and the test/fixture scope configured. ✓ 2026-08-28
- [x] **042.2** Replace the five hand-rolled SQL `IN`-list constructions in
      `internal/store` with one `placeholders(n)` helper, so the six G202
      suppressions cite something checkable. ✓ 2026-08-28
- [x] **042.3** Rewrite `grouping.has`/`grouping.equal` on `slices.Contains` /
      `slices.Equal`, which deletes the G602 finding along with the loops, behind
      a test that pins the predicates first. ✓ 2026-08-28
- [x] **042.4** Adjudicate the remaining 32 production sites: an inline
      suppression naming the reason, or an exclusion rule where the finding is
      platform-divergent. ✓ 2026-08-28
- [x] **042.5** Document gosec versus govulncheck in `CONTRIBUTING.md` and
      `SECURITY.md`. ✓ 2026-08-28

## What the numbers are

Measured through the pinned golangci-lint (v2.13.1) with its output limits
removed, before any suppression was added:

| GOOS | findings |
|---|---|
| darwin | 103 |
| linux | 102 |
| windows | 102 |

113 distinct sites across the three platforms: 74 in tests, the fake CLIs and the
test helpers, 39 in production code. By rule: G304 ×45, G301 ×18, G204 ×17,
G306 ×10, G202 ×6, G302 ×5, G115 ×5, G101 ×3, G103 ×2, G703 ×1, G602 ×1.

The 39 production sites are the ones adjudicated below. Nothing in them was a
vulnerability; one — the `slices.Equal` rewrite — was worth changing anyway.

## Decisions

- **gosec runs inside golangci-lint, not as a separately pinned tool
  (2026-08-28).** It then inherits the existing `go.mod` pin, the existing
  three-platform CI matrix and the existing local command, so nothing new has to
  be kept in step and `go run mage.go lint` stays byte-identical between a
  developer's machine and `ci.yml`. The alternative — a pinned standalone gosec
  with its own mage target and its own CI step — buys the newest upstream rule
  set and a SARIF feed into GitHub code scanning, and costs a second pin, a
  second command and a second thing to keep on three platforms. The cost accepted
  here is that the rule set is whatever golangci-lint v2.13.1 vendors. Code
  scanning, CodeQL, secret scanning and SBOM stay out of scope, as
  [#147](https://github.com/lezli01/vincent/issues/147) itself proposes.

- **`issues.max-issues-per-linter` and `issues.max-same-issues` are both `0`
  (2026-08-28).** This is not tidiness. golangci-lint defaults them to 50 and 3,
  and this repository set neither, so a naive `enable: gosec` reports about 25
  findings per platform instead of ~102 — and *which* 25 varies between runs of
  the identical command, because the truncation happens after parallel package
  analysis. Two consecutive runs here listed different G301 sites. A gate that
  hides three quarters of its findings nondeterministically is not a gate, and a
  reviewer should not believe a "only 25 findings" claim about this change.

- **Test, fixture and fake-CLI scope is configured, not annotated (2026-08-28).**
  A single `linters.exclusions.rules` entry names gosec against `_test\.go`,
  `cmd/fakeagent`, `cmd/fakegh`, `internal/testrepo`, `internal/agent/agenttest`
  and `internal/github/githubtest`, carrying the rationale once: those paths
  deliberately mirror real-world permissions, deliberately exec `go build` and
  `git`, and ship in no binary. 74 inline comments would be noise that every new
  test file has to re-earn. Three now-redundant `//nolint:gosec` directives in
  test files were deleted rather than kept, since nolintlint correctly calls them
  unused once the exclusion covers them.

- **Platform-divergent suppressions live in exclusion rules; stable ones stay
  inline (2026-08-28).** gosec analyses one GOOS at a time, so a suppression can
  be required on one platform and stale on another. `internal/doctor/disk_unix.go`
  is exactly that: `Statfs_t.Bsize` is `int64` on Linux (G115 on the `uint64`
  conversion) and `uint32` on Darwin (no finding), so a `//nolint` there fails
  Darwin's nolintlint and its absence fails Linux's gosec. Loosening
  `nolintlint.allow-unused` was rejected: it would blind the repository to stale
  directives for every linter forever, to fix one site. An exclusion rule is
  never "unused", so nolintlint stays strict, and the reason stays at the site as
  an ordinary comment pointing here.

- **Suppression is not a route to relitigating spec §16 or #141 (2026-08-28).**
  G301/G302/G306 demand `0750` directories and `0600` files across the tree.
  §16's 2026-08-25 amendment already settled which files are owner-only and why —
  `config.yaml`, its directory, transcripts — and the code already states where
  world-readable is deliberate: a launchd plist, a systemd unit, a scaffolded
  workflow that gets committed to the user's repository. Tightening any of those
  is a user-visible behaviour change that needs its own spec amendment and its
  own issue. The disposition for those rules here is a suppression whose reason
  names the design decision, never a mode change smuggled in under a lint PR.

- **`internal/store/store.go`'s `0o755` data dir is recorded as follow-up, not
  fixed here (2026-08-28).** It is the one directory mode in the tree that is
  inconsistent with its neighbours: `internal/tui/firstrun.go` creates the *same*
  data directory with `0o700`, and worktrees, logs and transcripts are all
  `0o700`. Whichever is right, changing it alters an existing installation's
  on-disk permissions, which is the previous decision's definition of out of
  scope. It carries a suppression saying so, and this paragraph is the follow-up
  record.

- **The G202 suppressions were made checkable before they were written
  (2026-08-28).** Five sites in `internal/store` built an `IN (?, ?, ?)` list by
  hand, in two different spellings (`strings.Repeat("?,", n-1) + "?"` and
  `"(?" + strings.Repeat(", ?", n-1) + ")"`). They now all call one
  `placeholders(n)` in `store.go`, whose doc comment states the property the
  suppressions rely on: the result is punctuation and bind markers derived from a
  count, never from a value. This is the issue's "prefer a small wrapper that
  proves safety over a blanket disable" applied where the call sites allowed it.
  gosec still reports the six sites, because a function call is not a constant
  operand — but a reviewer can now check all six against one helper. The sixth,
  `transitions.go`, is a `SET` clause joined from literals assigned three lines
  above and is left as-is.

- **The backup suppressions name the real provenance (2026-08-28).** This is the
  issue's criterion about misleading trusted-path assertions, and the backup
  package is where it bites. `archive.go`'s `Create` opens the destination the
  API *caller* named in `POST /v1/backup` — not a daemon-owned path — and its
  reason says so, resting on §16's trust boundary and on `O_EXCL` rather than on
  a provenance claim that is false. `restore.go`'s `extractFile` opens a path
  derived from a **tar header**, which an attacker-supplied archive controls; its
  reason cites the `target()` guard above it, which refuses any entry that does
  not resolve inside the two roots, checking the entry name and the joined path
  separately. Only `writeRegular` and the data-root walk get a daemon-owned
  claim, and only because it is true of them.

- **No spec amendment, and no gate script (2026-08-28).** Nothing in §16 or §19
  describes the lint configuration, and no runtime behaviour changed: every
  production edit is a comment, a suppression, or the two refactors above, both
  covered by existing tests plus the one added here. The acceptance gates drive a
  running daemon over the API and have nothing to assert about a linter.

## What was proved

- `go run mage.go lint` exits 0 on the host, and the CLAUDE.md cross-platform
  loop (`for os in windows darwin linux; do GOOS=$os "$LINT" run ./...; done`)
  exits 0 for all three. That loop is load-bearing rather than advisory here:
  39 of the 113 sites exist under one GOOS only.
- The gate fails on a new unsuppressed finding. A temporary
  `os.WriteFile(..., 0o644)` in `internal/version` — a package no exclusion
  covers — produced `G306: Expect WriteFile permissions to be 0600 or less` and a
  non-zero exit; deleting it returned the package to `0 issues`. Run during
  development, deliberately not committed.
- `TestGroupingEqualAndHas` was added and passing against the hand-written
  `equal` **before** the `slices.Equal` rewrite, so the rewrite is covered by a
  test that distinguishes equal, different-length, different-element and
  reordered groupings. Everywhere else in `boardgroup_test.go`, `equal` is the
  assertion rather than the subject.
- `go run mage.go test` and `go run mage.go testrace` both green, which is what
  says the suppressions changed no behaviour.

## Cost

Measured on the maintainer's machine, host GOOS only, best of the runs taken:

| | with gosec | without |
|---|---|---|
| warm cache | 1.45 s | 1.69 s |
| cold cache (`golangci-lint cache clean`) | 10.7 s | 14.4 s |

gosec came out *faster* in both pairs, which is the honest read: the difference
is run-to-run variance, not a speedup. gosec is an AST pass over packages the run
already loads and type-checks, so its marginal cost is below what this machine can
measure. CI pays a cold Go build cache too and should be trusted over these
numbers. If it ever threatens the 20-minute `ci` budget on Windows, the fallback
is a separate step running gosec alone rather than a slower shared one — nothing
measured suggests that is needed.
