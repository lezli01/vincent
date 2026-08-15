# 004 — Pin the Go toolchain and automate its patch bumps

**Status:** ✅ done (3/3) · **Opened:** 2026-08-15

`go.mod` names an exact patch toolchain, and a scheduled workflow opens the pull
request that moves it. The Vulnerabilities workflow's most common failure needs
no vincent code change — only a Go patch release we had no mechanism to adopt.

## The problem

Eight consecutive runs of `.github/workflows/vuln.yml` failed on `master` with
five reachable findings, every one of them in the standard library and every one
of them `Fixed in: …@go1.26.6`:

| Advisory | Package | Reached from |
|---|---|---|
| GO-2026-6218 | `net/url` | `apiclient.Client.Diff` → `http.Client.Do` |
| GO-2026-6090 | `crypto/tls` | `api.Server.Serve` → `http.Server.Serve` |
| GO-2026-6089 | `net/http` | `api.Server.Serve` |
| GO-2026-5972 | `encoding/asn1` | `claude.run.Wait` |
| GO-2026-5026 | `net/http` (x/net/idna) | `apiclient.Client.Diff` |

The runner was on go1.26.5. `go.mod` said `go 1.26` with no patch, and
`actions/setup-go` defaults to `check-latest: false` — its log reads
`Setup go version spec 1.26` then `Found in cache @ /opt/hostedtoolcache/go/1.26.5/x64`.
A version spec with no patch is satisfied by whatever patch the runner image
already carries, so a released fix is adopted only when GitHub rebuilds the
image. go1.26.6 had been stable and present in the setup-go manifest for days.

The scoreboard was the smaller half of this. `release.yml` sets Go up the same
way, so the signed, attested binaries vincent publishes were being linked
against a standard library with five known holes in the code paths the daemon
actually runs — its HTTP server and its API client.

## Decisions

### 1. An exact `toolchain` directive, not `check-latest: true`

*2026-08-15.* `go.mod` gains `toolchain go1.26.6`. `actions/setup-go@v7` reads
the `toolchain` directive when one is present and falls back to `go` otherwise,
so every workflow already passing `go-version-file: go.mod` picks it up with no
edit, and `GOTOOLCHAIN=auto` makes a developer's local `go run mage.go vuln`
resolve the same toolchain the runners use.

**Beat:** `check-latest: true` on the setup-go steps. It is one line shorter and
strictly worse: it fixes only what runs on a runner, leaves a local govulncheck
reporting five findings CI does not see, and — because `release.yml` would also
follow "latest" — makes the toolchain a published binary was built with a
property of the day it was built rather than of the commit it was built from.
That is the opposite of what the attestation in `release.yml` exists to promise.

The `go` directive stays at `1.26`. It states the language version vincent
requires; the toolchain states what builds it. Conflating them would make every
patch bump a claim about the minimum Go a consumer needs.

### 2. This supersedes the v0 "bumped manually each Go release" decision

*2026-08-15.* `docs/history/v0-tasks.md` records: "*Go version:* latest stable
minor, minor-only directive (`go 1.26`); CI reads `go-version-file: go.mod`;
bumped manually each Go release." Half of that stands — latest stable minor,
`go-version-file: go.mod`, no version matrix. The other half is amended here,
explicitly rather than by drift: the directive is no longer minor-only, and the
bump is no longer manual.

What changed since v0 is that a scheduled govulncheck now exists. Manual bumps
were adequate when nothing failed in between; with a weekly sweep, "we adopt
patches when someone notices" is a workflow that goes red for days at a time
over a fix that shipped upstream and has a one-line diff. The v0 ledger is
frozen and is not edited — this document is where the amendment lives.

### 3. The bump is an in-repo scheduled workflow, not a bot

*2026-08-15.* `.github/workflows/go-toolchain.yml` resolves the newest stable
patch in the `go` directive's minor series from `go.dev/dl`, rewrites the
directive with `go mod edit -toolchain=`, and opens the pull request.

**Beat:** Dependabot, which cannot do it — `gomod` updates module requirements
only, and `dependabot-core#13520`, "Bump Go toolchain directive in go.mod
files", is still open. The existing `gomod` entry in `.github/dependabot.yml`
remains the right tool for the three non-reachable findings in required modules
that the same scan reported; it is simply not a tool that can touch this.

**Beat:** Renovate, which *can* — its `gomod` manager updates `go` and
`toolchain` via the `golang-version` datasource. It would mean a second update
bot, an app installation, and a config file, to own one line of one file that a
thirty-line workflow already owns. If Renovate is ever adopted for other
reasons, this workflow should be deleted in favour of it.

### 4. Patch releases only, gated on build and test, reporting govulncheck

*2026-08-15.* The candidate is constrained to the minor series named by the `go`
directive, which the job never edits: 1.26 → 1.27 changes the language version
and the minimum Go a consumer needs, and belongs to a human. Within a series,
`go run mage.go build` and `go run mage.go test` gate the pull request — a bump
that does not compile is not worth a reviewer.

`go run mage.go vuln` runs `continue-on-error` and its outcome goes into the
pull request body instead of gating. An advisory whose fix has not been released
yet fails that step, and the bump is still worth landing; whether to merge it
anyway is exactly the judgement the body is written to inform.

### 5. The pull request does not trigger CI, and says so

*2026-08-15.* GitHub does not run workflows for a pull request opened with
`GITHUB_TOKEN`, so this one arrives with no checks. Rather than add a PAT secret
— a token with write scope, held for a cosmetic gain — the job runs build, test
and the govulncheck sweep itself before opening the pull request, links its own
run, and tells the reviewer to close-and-reopen or push an empty commit to get
the full three-platform matrix. The evidence is present either way; only its
placement differs.

## Tasks

- [x] **004.1** — `toolchain go1.26.6` in `go.mod`. ✓ 2026-08-15
- [x] **004.2** — `.github/workflows/go-toolchain.yml`: weekly patch resolution,
      `go mod edit`, build/test gate, govulncheck report, pull request. ✓ 2026-08-15
- [x] **004.3** — The build-from-source prerequisite in `README.md` and
      `docs/getting-started/installation.md` states the pin. ✓ 2026-08-15

## Out of scope

- **Minor-version upgrades** (1.26 → 1.27). Decision 4; still manual, still a
  judgement about the language version vincent requires.
- **Suppressing advisories.** govulncheck has no allowlist file; filtering would
  mean parsing `-json` in the `Vuln` mage target and carrying OSV IDs in-repo.
  Nothing yet needs it, and a suppression that outlives its reason is worse than
  a red workflow.
- **The three non-reachable module findings** the same scan reported. Dependabot's
  weekly `gomod` pass owns those.

## Verification

- `go run mage.go vuln` on go1.26.6, all three GOOS values: "No vulnerabilities
  found." (2026-08-15, macOS host; the same sweep on go1.26.5 reported the five
  findings in the table above.)
- `actionlint` clean on `.github/workflows/go-toolchain.yml`.
- The resolve step's `go.dev/dl` query, run against the live release list, picks
  `go1.26.6` for `go 1.26` and reports `changed=false` against the new pin — the
  no-op path a Monday with no new patch takes.
