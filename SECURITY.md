# Security Policy

## Supported versions

| Version | Supported |
|---|---|
| latest `0.x` release | ✅ fixes ship in the next patch or minor |
| any earlier release | ❌ upgrade to the latest |

Vincent is a pre-1.0, single-maintainer project: there are no backport branches
and no long-term support line. A security fix lands on `master` and ships in the
next release, so "supported" means **the newest release** — see
[CHANGELOG.md § Versioning and stability](https://github.com/lezli01/vincent/blob/master/CHANGELOG.md#versioning-and-stability)
for what a minor or patch bump is allowed to change.

Known vulnerabilities in the dependency graph are swept weekly by
[`.github/workflows/vuln.yml`](https://github.com/lezli01/vincent/blob/master/.github/workflows/vuln.yml) (govulncheck across
all three target platforms), and on every change to `go.mod`/`go.sum`.

Vincent's own source is swept separately, by
[gosec](https://github.com/securego/gosec) inside the pinned golangci-lint
configuration. The two look at different things and neither replaces the other:
govulncheck reads the **dependency graph** for published advisories that reach
code vincent actually calls, and can go red without a line of vincent changing;
gosec reads **this repository's code** for insecure patterns — subprocess
construction, file paths, file and directory permissions, SQL building, integer
conversions — and runs on every pull request, on Linux, macOS and Windows, as
part of the ordinary `go run mage.go lint` gate. A new unsuppressed gosec finding
fails CI. Existing findings are suppressed one site at a time with a stated
reason; the adjudication is recorded in
[task 042](https://github.com/lezli01/vincent/blob/master/docs/tasks/042-gosec-static-analysis.md).

## Reporting a vulnerability

Please **do not** open a public issue for security vulnerabilities.

Use GitHub's private vulnerability reporting: go to the repository's
**Security** tab → **Report a vulnerability**, or use this direct link:
<https://github.com/lezli01/vincent/security/advisories/new>. Reports submitted
this way are private and visible only to the maintainers, and are acknowledged
on a best-effort basis.

## Scope notes

Vincent executes AI agents in **full-auto mode by default** — agents can run
arbitrary commands as the invoking user, and git worktrees provide collision
isolation, not security isolation. This is intentional product behavior,
documented in the [security model](docs/security-model.md), and the TUI states
it once on its first run. Reports about
sandbox escapes are only in scope for the opt-in `restricted` permission mode.

The daemon's own trust boundary is the OS user: the API listens on loopback
only and is gated by a bearer token stored `0600` in the data directory. It
stores no agent credentials — agent CLIs use their own auth.
