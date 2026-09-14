# 004 — Pin the Go toolchain and automate its patch bumps

**Status:** ⚠ blocked on the owner (5/6) · **Opened:** 2026-08-15

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

*Amended 2026-09-14 (issue #373).* `go.mod` reads `go 1.26.0`, not `1.26`, and
that is not drift to revert. `go mod tidy` records the highest `go` version any
required module declares, verbatim: golangci-lint v2.13.1, a `tool` dependency,
declares `go 1.26.0`, and tidy raised ours to match (fe5c1cf). Forcing `go 1.26`
back would last until the next tidy. The directive still names the 1.26
language series, which is what this decision protects, and the workflow's
resolve step trims it to its minor before matching go.dev (328f1e3), so a patch
bump still cannot become a language-version change.

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

*Amended 2026-09-14 (issue #373).* Choosing `GITHUB_TOKEN` also depends on a
repository setting, on top of the job's `pull-requests: write`: *Settings →
Actions → General → Workflow permissions → "Allow GitHub Actions to create and
approve pull requests"*. Without it, `gh pr create` fails with "GitHub Actions
is not permitted to create or approve pull requests (createPullRequest)" after
the branch has already been pushed. The setting was off from the start and
nobody recorded the dependency. The job failed on 2026-08-24, 08-31, 09-07 and
09-14, every run that had a patch to adopt, and left `chore/go-toolchain-1.26.7`
and `chore/go-toolchain-1.26.8` on the remote with no pull request. Its one
success, 2026-08-17, took the no-op path. The `GITHUB_TOKEN` choice stands,
with no PAT and no reuse of `RELEASE_PLEASE_TOKEN`. The owner turns the setting
on (004.6), and the workflow's comment on `pull-requests: write` names the
dependency, so the next reader learns it from the file and not from a failed
run.

### 6. A failed run opens an issue

*2026-09-14 (issue #373).* A scheduled workflow that fails tells nobody, and
four failures of this one went unnoticed: the vuln sweep an hour later was red
too, for a reason people already expected. The job's last step runs
`if: failure()`. It opens one issue titled "Go toolchain bump workflow is
failing", or comments on that issue while it is open, and links the failed run.
The job's `GITHUB_TOKEN` gains `issues: write`. Unlike opening a pull request,
creating an issue depends on no repository setting, so the alert still works
when that setting is the fault. A run cancelled by `timeout-minutes` is not a
failure to `failure()` and is not reported.

**Beat:** GitHub's failed-run notification. For a scheduled workflow it goes
only to the user who last edited the cron line, and only if their notification
settings allow it. Nothing in the repository states it.

**Beat:** an issue per failure. A job that fails every week until someone acts
would open an issue every week. A comment on the one open issue keeps a single
place to look, and closing the issue re-arms the alert.

## Tasks

- [x] **004.1** — `toolchain go1.26.6` in `go.mod`. ✓ 2026-08-15
- [x] **004.2** — `.github/workflows/go-toolchain.yml`: weekly patch resolution,
      `go mod edit`, build/test gate, govulncheck report, pull request. ✓ 2026-08-15
- [x] **004.3** — The build-from-source prerequisite in `README.md` and
      `docs/getting-started/installation.md` states the pin. ✓ 2026-08-15
- [x] **004.4** — `toolchain go1.26.8` in `go.mod`, adopted by hand because
      004.2's pull request step had never succeeded (#373). The README and
      installation page state the pin without naming a patch and are
      unchanged. ✓ 2026-09-14
- [x] **004.5** — `go-toolchain.yml` reports its own failure: `issues: write`
      and an `if: failure()` step that opens or comments on one tracking issue
      (decision 6), plus the comment on `pull-requests: write` naming the
      repository setting it depends on (decision 5 amendment). ✓ 2026-09-14
- [!] **004.6** — "Allow GitHub Actions to create and approve pull requests" is
      on, and a run of the job has opened a bump pull request. Blocked on the
      owner: a repository setting cannot be changed from a branch.

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
- 004.4: go1.26.8 is the newest 1.26 patch on the Go module proxy
  (`golang.org/toolchain`) on 2026-09-14. With `GOTOOLCHAIN=go1.26.8`,
  `go run mage.go build` passes and `go run mage.go vuln` reports "No
  vulnerabilities found." for linux, darwin and windows (macOS host).
- 004.5: `actionlint` on `.github/workflows/go-toolchain.yml` reports nothing
  for the new failure step. Its one finding, SC2016 (info) on the pull request
  step's single-quoted backticks, is older than this change and intended: that
  line is Markdown, not an expansion.
- 004.6 is unproven. With the pin already at go1.26.8, a `workflow_dispatch`
  run takes the no-op path and never reaches `gh pr create`. The first real
  proof that the pull request path works is the first Monday after go1.26.9
  ships with the repository setting on. If that run fails, the 004.5 issue
  reports it.
