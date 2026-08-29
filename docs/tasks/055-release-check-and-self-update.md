# 055 — Check for a newer release, and offer an in-place update

**Status:** ✅ done (2/2)
**Spec:** amends §12.1 (a `vincent update` row, and the second stated exception
to §4), §12.3 (the `update:` block, and the `github.poll_interval` paragraph's
"first standing outbound traffic" sentence), §13.2 (`GET /v1/update`), §16 (what
the check sends, and what `vincent update` verifies)
**Issue:** [#232](https://github.com/lezli01/vincent/issues/232)

## Problem

Vincent never tells you a newer version exists. `vincent version` and the
`version` row in `vincent doctor` report the running build; nothing compares it
against the latest release, so a user finds out they are behind by visiting
GitHub.

Upgrading is manual and channel-dependent —
[`installation.md`](../getting-started/installation.md) lists Homebrew, the
macOS installer package, WinGet, Scoop, mise, deb/rpm, a plain signed archive
and `go install`, each with a different command. Package-managed installs at
least get nagged by their package manager. The direct-download archive path —
the one with no package manager behind it — gets nothing, and those users sit on
an old build indefinitely. They are the users this task exists for.

## Corrections to the issue as written

- **"This is the first *unsolicited* outbound call the daemon makes" was not
  true.** [052](052-github-pull-requests.md) landed a pull-request reconciler
  that polls every `github.poll_interval`, 5 m by default with
  `github.enabled: true` by default. What *is* true, and is the claim worth
  keeping, is that this check is the first outbound call the daemon makes for
  **every** user: §13.2's gate stops the reconciler at the first "no", so a
  daemon with no GitHub-origin project makes no call today. That is why this
  feature needs no new invariant but does need three standing sentences amended.
- **There is no per-asset cosign signature.** `.goreleaser.yaml`'s `signs:`
  block signs `artifacts: checksum` only — one `checksums.txt.sig` and one
  `checksums.txt.pem`, keyless via Fulcio. The acceptance criterion "verifies
  checksum **and** cosign signature" is met by a two-step chain: verify the
  signature over `checksums.txt`, then verify the asset's SHA-256 against that
  verified file. That is exactly what the release footer tells a human to do.
- **The release asset is an archive, not a bare binary.**
  `vincent_{version}_{os}_{arch}.tar.gz` (`.zip` on Windows) carries
  `README.md`, `LICENSE` and `examples/*.yaml` beside the binary. The swap
  verifies the *archive*, then extracts `vincent`/`vincent.exe` from the
  verified bytes. `vincent_{version}_darwin_universal.pkg` is deliberately
  outside `checksums.txt` ([039](039-macos-installer-package.md)) and is
  therefore never an update source.
- **`vincent status` is the step-status command**
  ([036](036-step-status-message.md)): it runs from inside a step, addresses
  itself with `VINCENT_TASK_ID`/`VINCENT_STEP_ID`, and takes a message. It
  cannot carry a daemon/binary version report. Resolved by decision 4.

## Decisions

1. **Signature verification prefers an installed `cosign`, and degrades rather
   than refusing.** With `cosign` on `PATH`, `vincent update` verifies
   `checksums.txt` against the published certificate and identity regexp and
   refuses the swap on any failure. Without it the SHA-256 check runs alone and
   the command says plainly that the signature was not checked;
   `--require-signature` makes the missing binary fatal for a caller who wants
   the guarantee. This is the shape `internal/github` already uses for `gh`:
   prefer the user's own tool, fall back, never bundle.

   Embedding `sigstore-go` was the alternative and it loses on two counts — a
   very large dependency tree in a project whose runtime `require` block is
   fifteen lines, and a second outbound call (a TUF trust-root refresh) in a
   feature whose whole promise is one unauthenticated GET. Requiring `cosign`
   outright loses because it makes the direct-download user install a second
   tool before they can update, and they are the user this feature exists for.

2. **The CLI downloads, verifies and swaps; the daemon never does.** A stated
   exception to §4's "the daemon owns everything", written into §12.1 beside
   `daemon restore`'s, and the same kind of exception: the operation must work
   with **no daemon**, and a daemon cannot cleanly rewrite its own running image
   on Windows. What the daemon keeps is the background check and the cached
   answer.

3. **`vincent update --check` queries the feed itself and never goes through the
   daemon.** The endpoint serves only the cached poll result. This is what makes
   the issue's own promise literally true — with `update.check: false` the
   daemon makes *no* outbound request, and only an explicit `vincent update`
   does — and it is what makes `--check` work before any poll has happened and
   with no daemon running. The alternative, a refresh parameter on the endpoint
   with a direct-call fallback, has the daemon making a request the user
   disabled and still needs both code paths.

4. **The version mismatch surfaces in `vincent daemon status` and `vincent
   doctor`.** Both already report daemon identity and `GET /v1/info` already
   serves the running daemon's `version`, so the comparison against
   `version.Version()` in the local binary costs nothing. Following
   [035](035-github-issues.md)'s precedent, the doctor rows are **rows, not
   problems**: neither a newer release nor a stale daemon changes the exit code,
   because both leave everything working.

5. **`vincent update` swaps without prompting.** The command is already the
   explicit human act the issue asks for, and [048](048-cli-human-actions.md)
   recorded that this command tree does not prompt because its purpose is
   scripting. `--dry-run` prints what would happen instead.

6. **One task document, two pull requests.** This document carries both halves;
   PR 1 lands the check, PR 2 lands the update action. The halves are
   independently useful, PR 1 is the one that makes the problem visible at all,
   and PR 2's platform-specific file swapping deserves a review surface that is
   not also a config block and an API endpoint.

   *Amended in the branch that landed this:* both halves shipped in **one**
   pull request. The issue-driven workflow that implemented the task produces
   one branch and one pull request, so splitting would have meant delivering
   half the agreed scope. The split is recorded because the reasoning still
   holds for the next feature of this shape.

## Design settled without a question

- **Stable-only is enforced twice.** `GET /repos/lezli01/vincent/releases/latest`
  excludes drafts and prereleases server-side, so `prerelease: auto` in
  `.goreleaser.yaml` is already honoured; a client-side reject of any tag with a
  semver prerelease suffix backs it up so the guarantee does not rest on one
  API's documented behaviour.
- **Version comparison uses `golang.org/x/mod/semver`**, already in `go.sum`. It
  must normalize the `v` prefix: goreleaser injects `{{.Version}}` (no `v`) into
  `internal/version.version`, while tags and the API's `tag_name` carry one. A
  `dev` build — the `go build` fallback in `version.Version()` — never reports
  an update available.
- **The cache is in memory, not SQLite.** No migration; a daemon restart
  re-polls. Nothing here is state a crash may lose (§12.4's "persist before
  acting" is about transitions, and this is not one).
- **Channel detection is a runtime path heuristic, and cannot be a build-time
  stamp.** One archive feeds the direct download, the Homebrew cask, Scoop,
  WinGet and the nfpm packages, so no ldflag can tell them apart. Detection
  reads `os.Executable()` resolved through symlinks against the Homebrew
  prefixes, `/usr/bin` and the other system bin directories, the Scoop apps dir,
  `%LOCALAPPDATA%\Microsoft\WinGet\Packages`, the mise installs dir, and
  `GOBIN`/`GOPATH/bin`. Unidentifiable is treated as package-managed: the cost
  of guessing "self" wrongly is a clobbered file the next `brew upgrade`
  silently reverts, and the cost of guessing "unknown" wrongly is a printed
  command.
- **Exit codes overload 0/1/2**, which `vincent daemon status` and `vincent
  doctor` already do. `update --check`: `0` up to date · `1` the check failed ·
  `2` an update is available. `update`: `0` nothing to do or swapped
  successfully · `1` verification or swap failed and the binary is untouched ·
  `2` an update exists but this install is package-managed and its command was
  printed. `--json` carries `swapped` so a script can tell the two `0`s apart.

## Subtasks

| # | What | Status |
|---|---|---|
| 055.1 | The check: `update:` config, `internal/release`, the daemon poller, `GET /v1/update`, `vincent update --check`, the doctor and `daemon status` rows | ✅ done |
| 055.2 | The update action: `internal/selfupdate` — channel detection, download, checksum and cosign verification, the platform-split swap — and `vincent update`'s swap path | ✅ done |

## What changes, by package

**055.1 — the check**

- `internal/config` — a top-level `update:` block: `check: true`,
  `poll_interval: 24h`. Either `check: false` or `poll_interval: 0` stops the
  poller, mirroring `github.enabled` / `github.poll_interval`; a negative
  interval fails the load. Read per tick, so a hot reload governs the next one.
- `internal/release` — **new leaf package**, importing nothing else from
  `internal/`, like `internal/github`. One unauthenticated GET with a short
  timeout and no identifying header, a normalized `Release` (version, published
  at, URL), the comparison, and the `Status` the daemon caches and the API
  serves — which lives here because both of those packages need it and neither
  may import the other.
- `internal/daemon/updatecheck.go` — the poller wired in `Run` beside the
  scheduler, the notifier and the pull-request reconciler. Failure (offline, 403
  rate limit, malformed body) logs at debug, changes nothing, and leaves the
  previous cached answer in place with the reason attached.
- `internal/api` — `GET /v1/update` in the route table, serving the cache and
  the never-polled state; `internal/apiclient` gains the typed method and owns
  the wire type.
- `internal/cli/update.go` — `vincent update --check`, `--json`.
- `internal/doctor` + `internal/cli/doctor.go` — the `UPDATE` group: the
  latest-known-release row and the daemon-older-than-binary row.
- `internal/cli/daemon.go` — the mismatch line on `daemon status`.

**055.2 — the update action**

- `internal/selfupdate` — **new package**: channel detection, archive download,
  checksum and cosign verification, and the swap. The swap is `swap_unix.go` /
  `swap_windows.go` per the cross-platform convention: stage the download in a
  temp file **in the destination directory** so the rename is same-filesystem,
  verify, preserve the mode bits, rename over the target, roll back on failure.
  Windows renames the running executable aside and the daemon deletes the
  leftover on its next start; macOS clears `com.apple.quarantine`
  (`quarantine_darwin.go`), the same attribute `installation.md` documents for
  direct downloads.
- `internal/cli/update.go` — the swap path, `--dry-run`, `--require-signature`,
  and the restart line for the install.

## What the tests prove

- **`internal/release`** — parsing against a captured real `releases/latest`
  body in `testdata/`, named with the date it was captured. Comparison covers
  `dev`, the missing `v` prefix, equal, older, newer and a prerelease tag.
  Transport is exercised against `httptest`, including a 403 rate-limit body, a
  timeout and a malformed body, proving all three degrade to "unknown". One test
  asserts the request carries no `Authorization` and nothing identifying, which
  is §16's promise written as an assertion.
- **The poller is silent when disabled** — the acceptance criterion "no outbound
  request is made" as an assertion: with `check: false`, and again with
  `poll_interval: 0`, an `httptest` feed whose handler fails the test if it is
  ever hit. A companion test proves a failed tick keeps the previous answer.
- **Wire agreement** — `update_live_test.go` drives `internal/apiclient` against
  the real `GET /v1/update` handler over `httptest`, including the never-polled
  state and a daemon with no poller wired.
- **Channel detection** — table-driven over synthetic executable paths per
  `GOOS`: Homebrew prefixes, `/usr/bin`, the Scoop apps dir, the WinGet packages
  dir, the mise installs dir, `GOBIN`, a near-miss prefix, and a path matching
  nothing, which must resolve to package-managed.
- **Verification refuses correctly** — a fake release built in a temp dir
  (archive, `checksums.txt`, and a stub `cosign` whose exit code the test
  chooses, the way `cmd/fakeagent` stands in for an agent CLI; it is a compiled
  Go program rather than a script because there is no portable shell). Four
  cases: good checksum and good signature swaps; one flipped byte refuses and
  leaves the old binary **byte-identical**; a `cosign` exiting nonzero refuses;
  no `cosign` on `PATH` proceeds with the warning and refuses under
  `--require-signature`.
- **The swap** — a temp-dir test proving mode bits survive, no staging file
  outlives the swap, and a failure leaves the destination untouched. Running on
  all three platforms in CI is what covers the Windows rename-aside path; there
  is no separate gate script for it.
- **CLI exit codes** — `vincent update --check` against an `httptest` feed for
  each of `0`, `1` and `2`, plus `--dry-run` downloading nothing and the
  package-managed paths printing their command and exiting 2.

**What they do not prove**, and is not faked: that a real GitHub release feed is
reachable, that a real keyless cosign signature verifies against Fulcio and
Rekor, and that a swapped binary starts on a machine with Gatekeeper or
SmartScreen in front of it. That leg is
[a gate walkthrough](../gates/055-update-gate.md) against a real published
release, in the shape of [`m5`](../gates/m5-gate.md) — a record of when it was
walked, not a script. There is no scripted acceptance gate here because a gate
drives a real daemon over curl and nothing in the swap path involves one.

## One note on the exit-code tests

They run `runUpdate` in-process rather than exec'ing the built binary, which
every other exit-code suite in `internal/cli` does. The release source is an
unexported field precisely so it cannot be set from outside: it names a URL this
command downloads a binary from and executes, and an env var or hidden flag that
redirected it would be a real hazard existing only so a test could spawn a
subprocess. The exit codes are the contract, and they are asserted on the
function that returns them.
