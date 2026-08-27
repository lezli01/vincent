# 039 — A missing certificate must not destroy a release

**Status:** ⚠ verification blocked (5/6) · **Opened:** 2026-08-27

Task 032 built the macOS Developer ID signing path on the assumption that the
Apple Developer Program membership it needs would be bought. It was not — 032.7
is still open — and the pipeline made that a **fatal** condition on a `v*` tag.
`v0.7.0` was therefore not a release at all: [run
33057721082](https://github.com/lezli01/vincent/actions/runs/33057721082) died at
its first signing step, before GoReleaser ran, so the tag produced no archives,
no deb or rpm, no attestations and no Homebrew, Scoop or WinGet metadata, and
`6041bfd` unwound it.

This task changes what a missing certificate does: it makes the release
**unsigned** instead of making it **not exist**. Nothing about task 032's
mechanism is removed — every step of it stays wired and runs the moment the six
`MACOS_*` secrets appear.

Conventions for this file are in [the tasks README](README.md). Behaviour lands
in [the spec](../spec.md) as dated amendments, in the same PR as the release
configuration and user documentation that make them true.

## Decisions (2026-08-27)

- **Signing is keyed on the certificates, not on the tag.** `MACOS_SIGN_REQUIRED`
  was `github.event_name == 'push'`; it is now
  `secrets.MACOS_CERT_APPLICATION_P12 != ''`. The invariant task 032 wanted is
  kept in the form that is actually enforceable — *with* certificates installed,
  a tag cannot publish an unsigned macOS artifact — and the case it did not
  consider, certificates that were never installed, degrades to a warning.

  The alternative it beat was pinning the variable to `'0'` outright. That also
  unblocks releases, but it gives up the guard permanently: a certificate
  deleted or expired by accident would then ship a silently unsigned stable
  release, which is the failure task 032 was right to want caught. Deriving the
  value from the secret keeps the guard and costs nothing.

  A third alternative — buy the membership — is not this task's to take. It is
  032.7, it is owner-only, and it is unchanged by anything here.

- **Half-configured is still fatal, and so is signing without notarizing.** With
  the Application certificate present, a missing Installer certificate or a
  missing notary key is a misconfiguration, not the unenrolled path, and both
  keep their hard error. The notarization half matters on its own: a Developer ID
  signature Apple has not notarized makes Gatekeeper report `source=Unnotarized
  Developer ID` and refuse the file anyway, so it is **worse** than shipping
  plainly unsigned.

- **The Homebrew cask's quarantine hook comes back.** Task 032 deleted
  `xattr -dr com.apple.quarantine` from the cask's `hooks.post.install` on the
  correct grounds that keeping it would bypass notarization that had been paid
  for. There is no notarization to bypass: brew downloads the same unsigned
  archive GitHub Releases serves, Homebrew quarantines what it downloads, and an
  unsigned quarantined binary does not start. Without the hook `brew install
  lezli01/tap/vincent` produces a broken install — the exact condition
  [task 002](002-homebrew-tap.md) added it for.

  It is restored unconditionally rather than templated on the signing env,
  because a GoReleaser template inside a Ruby cask body is not covered by
  `goreleaser check` and would fail where nothing tests it. **Delete it in the
  same change that closes 032.7** — the comment above it in `.goreleaser.yaml`
  says so, and 032.4's reasoning becomes correct again the moment a notarized
  binary ships.

- **The smoke job's Gatekeeper assessment is skipped, not softened.** `codesign
  --verify` and `spctl --assess` on an unsigned binary fail, correctly. Making
  them tolerate failure would leave an assertion that proves nothing on a signed
  release either, so the step is gated on a new `macos_signed` job output — the
  value of `MACOS_SIGN_REQUIRED`, which is now precisely "the certificates are
  configured". Every other smoke assertion, and the checksum verification, run
  unchanged on every release.

- **The documentation describes the artifacts that ship, not the ones intended.**
  Nine user-facing sites asserted that macOS artifacts are signed, notarized and
  Gatekeeper-clean, and one told users *not* to run `xattr -d
  com.apple.quarantine`. Following that instruction against a real release
  leaves a binary that will not start, which is the worst failure mode available
  here: documentation that is wrong in the direction of "this does not work".
  All of it is rewritten to the unsigned reality, including the `.pkg`'s
  right-click → *Open* path, and `RELEASING.md` gains an explicit note that the
  signing machinery is wired and dormant.

- **The `.pkg` stays, unsigned.** Its stapled ticket was the headline in task
  032, and without notarization there is no ticket — but the rest of what it does
  is untouched: one file covering both architectures, installed to a fixed
  `/usr/local/bin/vincent` under `dev.lezli01.vincent`, which is what
  [task 021](021-package-distribution-channels.md)'s recorded-executable-path
  behaviour wants. Dropping it would remove a working installer and force a
  fourth documentation rewrite when 032.7 lands. It keeps its build attestation
  and stays outside `checksums.txt` for the unchanged reason that it is built
  after the checksums are computed.

- **This is not a decision to abandon macOS signing.** 032.7 stays open, the
  secrets table in `RELEASING.md` stays, the keychain import stays, and
  installing the six secrets is the entire switch — no workflow edit follows it.
  What is abandoned is only the coupling that made an unbought certificate
  destroy a release.

## Tasks

- [x] **039.1 — Key `MACOS_SIGN_REQUIRED` on the certificates.**
  `.github/workflows/release.yml`: the job-level env, plus the two error
  messages that named "a v* tag" as the reason a secret was required.
  ✓ 2026-08-27
- [x] **039.2 — Restore the cask's quarantine hook.** `.goreleaser.yaml`
  `homebrew_casks[].hooks.post.install`, with the comment that ties its removal
  to 032.7. ✓ 2026-08-27
- [x] **039.3 — Skip the Gatekeeper assessment on an unsigned release.**
  `Depends: 039.1`. A `macos_signed` output on the `release` job, consumed by the
  `smoke` job's *Assess the macOS signature* step. ✓ 2026-08-27
- [x] **039.4 — Rewrite the macOS signing claims.** `README.md`,
  `docs/features.md`, `docs/README.md`, `docs/getting-started/installation.md`,
  `docs/platforms/macos.md`, `RELEASING.md`, the GoReleaser release footer and
  the `.pkg`/`signs:` comments, and the 032 gate's preamble. ✓ 2026-08-27
- [x] **039.5 — Amend §19.** A dated amendment recording that the 2026-08-26
  amendment's "is now paid" was not true, and what a missing certificate does
  instead. In the same pull request as 039.1–039.4, per the tasks README.
  ✓ 2026-08-27
- [!] **039.6 — Prove a tag publishes without the certificates.** — **owner-only**:
  it needs a real `v*` tag, and the thing being proved is that the release
  workflow completes end to end and attaches every asset. Re-cut `0.7.0` (its
  curated changelog prose is in `2baafbb`, per `6041bfd`) and confirm the release
  carries the thirteen `v0.6.0` assets plus `vincent_0.7.0_darwin_universal.pkg`,
  that Homebrew, Scoop and WinGet moved, and that `brew install
  lezli01/tap/vincent` yields a binary that runs. A green secretless
  `workflow_dispatch` dry run does **not** close this: it never publishes, and
  publishing is what `v0.7.0` failed at.

## What the tests prove, and what they do not

There is no unit-testable surface here — it is a release workflow, a packaging
config and documentation — and none was invented to look like there is.

- `goreleaser check`, in `ci.yml`'s `packaging-config` job, validates the cask
  hook and the rewritten footer on every pull request. It ran clean on this
  change.
- The secretless `workflow_dispatch` dry run exercises exactly the path a tag
  now takes: `MACOS_SIGN_REQUIRED=0`, every signing step warning, an unsigned
  `.pkg` built by `scripts/macos-pkg.sh`. That is the regression test for the
  split, and it is the same run a fork produces.
- Nothing available before a tag proves that a *published* release is complete.
  That is 039.6, and it is why this task is not done.
- The [032 gate](../gates/032-macos-notarization.md) cannot be walked at all
  while releases are unsigned; its preamble now says so rather than sitting there
  looking runnable.

## Risks

- **Users on older documentation.** Anything published before this change tells
  macOS users there is nothing to clear. They will meet a Gatekeeper dialog with
  no instruction, on `v0.6.0` artifacts as much as on new ones — `v0.6.0` shipped
  before any signing existed. The mitigation is that the fix is one documented
  command and the Homebrew path needs none of it.
- **Re-adding the cask hook re-adds a bypass.** It is only correct while the
  binary is unsigned. If 032.7 lands and the hook is forgotten, brew installs
  would go on stripping quarantine from a notarized binary — the precise thing
  032.4 removed. The comment in `.goreleaser.yaml` and 039.6's successor are
  where that is caught; there is no test for it, because the condition is "a
  certificate exists", which CI cannot see.
- **A silent regression to unsigned, later.** With signing keyed on a secret, a
  deleted or expired certificate now produces a quietly unsigned release instead
  of a loud failure. The `::warning::` in the job log is the only signal. This is
  the cost of the decision and it is accepted: the alternative is the failure
  mode that cost `v0.7.0`.
