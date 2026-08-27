# 002 — Homebrew tap for macOS

**Status:** done (6/6) · **Opened:** 2026-08-14 · **Completed:** 2026-08-14

The macOS install is `brew install lezli01/tap/vincent`, published as a Homebrew
**cask** to [lezli01/homebrew-tap](https://github.com/lezli01/homebrew-tap) by
the same goreleaser run that cuts a release.

Conventions for this file are in [the tasks README](README.md). Behaviour lands
in [the spec](../spec.md) as dated amendments, in the same PR as the code.

## Decisions (2026-08-14)

Binding, in the same way v0's phase decisions are. Each records the alternative
it beat.

- **This reverses the v0 X decision, for macOS only.** That decision
  ([history/v0-tasks.md](../history/v0-tasks.md), *Distribution (X)*) said
  "GitHub Releases only — no Homebrew tap, scoop bucket, or winget manifest",
  because each is a second repo and a release-time publish step maintained
  forever. **That cost is accepted, not argued away** — the tap repo exists now
  and a failed cask push can fail a release. What changed the trade is a cost the
  *packaging* half of the same X decision created: Apple notarization is
  descoped, so the archive path makes every macOS user run
  `xattr -d com.apple.quarantine` by hand before the binary will start. A cask's
  `postflight` hook does that for them. The X decision weighed a tap against a
  quickstart that was already "download, unzip, run"; on macOS it is really
  "download, unzip, clear the quarantine attribute, run", and that is the step
  worth deleting.

  **Reinstated 2026-08-27 by [task 039](039-unsigned-releases-by-default.md).**
  The Apple Developer Program membership task 032 assumed was never bought, so
  the archive is unsigned again and the `postflight` hook is back in the cask,
  for exactly the reason recorded below. The 032 supersession that follows
  applies again once 032.7 closes.

  **Partly superseded 2026-08-26 by [task 032](032-macos-notarization.md).** The
  tap itself stands; what no longer holds is the *reason* recorded above. Apple
  notarization is no longer descoped, so the archive path does not make anyone
  run `xattr -d com.apple.quarantine` and the cask's `postflight` hook has been
  removed rather than left harmless — keeping it would go on stripping the
  attribute that the signature now makes meaningful. The quickstart really is
  "download, unzip, run" again on macOS; the cask earns its keep on upgrades and
  one-command install, which is the argument the X decision originally weighed.

- **Scoop and winget stay rejected.** The argument above does not carry over: no
  Windows packager erases SmartScreen the way a cask erases Gatekeeper's prompt,
  so Windows would pay the X decision's cost and get only a shorter command. The
  Windows and Linux install paths are unchanged.

  **Superseded 2026-08-20 by [task 021](021-package-distribution-channels.md).**
  The owner now accepts the second-repository and release-publisher cost for
  discoverability and one-command upgrades on Windows and Linux. The historical
  reasoning remains here; task 021 is the current distribution decision.

- **A cask, not a formula.** GoReleaser deprecated `brews` in v2.10 and Homebrew
  packages pre-built binaries as casks; formulas are for what brew builds from
  source. Not a stylistic choice — `goreleaser check` fails the config on the
  deprecated section, and within `homebrew_casks` the singular `binary:` field is
  itself deprecated in favour of `binaries:`.

- **homebrew-core was never on the table.** It requires notability the project
  does not have (roughly 75 stars/forks/watchers). A tap has no such gate and
  costs the user one extra path segment in the install command.

- **`skip_upload: auto`.** An rc tag must not move the cask that users install.
  goreleaser skips the push whenever the release is a prerelease, which is
  exactly the `v0.1.0-rc*` tags this repo already cuts.

- **Publishing needs a PAT, not `GITHUB_TOKEN`.** `GITHUB_TOKEN` is scoped to
  this repository and cannot write to the tap. `HOMEBREW_TAP_TOKEN` holds a
  fine-grained PAT with `contents: write` on `lezli01/homebrew-tap`; the workflow
  falls back to a placeholder so a fork's dry run — which never publishes — still
  runs. See [RELEASING.md](../../RELEASING.md).

- **The cask carries `uninstall.launchctl`, not only `zap`.** `vincent service
  install` writes a LaunchAgent (`internal/service.LaunchdName`). Without the
  stanza, `brew uninstall` deletes the binary out from under a loaded launchd job
  that still holds the SQLite file open. `zap.trash` additionally clears
  `~/Library/Application Support/vincent` and the plist, and only on an explicit
  `brew uninstall --zap`, which is what makes it safe to name a directory holding
  task history.

## Tasks

- [x] **002.1 — `homebrew_casks` block in `.goreleaser.yaml`.** Cask published to
  `lezli01/homebrew-tap`, with the quarantine `postflight` hook, the security
  caveat, `uninstall`/`zap`, and `skip_upload: auto`. ✓ 2026-08-14
- [x] **002.2 — Tap repository.** `lezli01/homebrew-tap` created public; a public
  tap is required because brew cannot read a private one without a token.
  ✓ 2026-08-14
- [x] **002.3 — `HOMEBREW_TAP_TOKEN` wired through the release workflow.**
  ✓ 2026-08-14
- [x] **002.4 — Docs.** README, `getting-started/installation.md`,
  `platforms/macos.md` and `RELEASING.md`. The Gatekeeper text is now scoped to
  the archive path, since `brew install` does not hit it. ✓ 2026-08-14
- [x] **002.5 — Config verified.** `goreleaser check` clean;
  `goreleaser release --snapshot` renders the cask with the `postflight`,
  `uninstall` and `zap` stanzas. ✓ 2026-08-14
- [x] **002.6 — Installed end to end against a real release.** ✓ 2026-08-14

## Verification (2026-08-14)

Done against the **published v0.1.0 release**, not a snapshot: the tap was seeded
by hand with a v0.1.0 cask carrying v0.1.0's published checksums, then installed
on macOS arm64 (Homebrew 6.0.15).

- `vincent version` → `vincent version 0.1.0 (commit ca7c0fe, built 2026-08-12T18:47:57Z)`
- `xattr -l` on the linked binary → no `com.apple.quarantine` (only
  `com.apple.provenance`, which macOS adds to everything and does not block
  execution). The `postflight` hook works.
- `vincent task ls` with no daemon → exit 2, the documented "nothing answered"
  code, proving the binary actually runs.

**Homebrew 6 tap trust needs no extra step.** Homebrew 6 requires non-official
taps to be trusted before it loads them, but installing by the full
`lezli01/tap/vincent` path records the trust itself — verified by untapping and
reinstalling non-interactively, which exited 0 and added `lezli01/tap/vincent` to
`trustedcasks`. No `brew trust` line is needed in the install docs.

**Still unproven:** the automated publish. The tap's current cask was pushed by
hand, so the first tagged release after this lands is what exercises
`HOMEBREW_TAP_TOKEN` and the goreleaser publish step for real.
