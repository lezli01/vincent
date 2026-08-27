# 032 — macOS Developer ID signing and notarization

**Status:** ✅ built, never activated (6/6, 032.7 dropped) · **Opened:**
2026-08-26 · **Closed:** 2026-08-27

> **2026-08-27 — the signature this task describes has never existed, and now
> will not.** Every mechanism below was built and works; the Apple Developer
> Program membership it needs was never bought, and 032.7 — the enrolment — is
> **dropped**, with the reasoning in
> [task 039](039-unsigned-releases-by-default.md). Releases ship unsigned; the
> cask's quarantine hook is back; §19's 2026-08-26 amendment is superseded by its
> two 2026-08-27 successors. **This document is retained as the design, not as a
> description of what ships.** Read every sentence below in the future tense: it
> is what happens if the six `MACOS_*` secrets are ever installed, which is the
> whole of the switch.

macOS artifacts are signed with an Apple Developer ID identity, notarized, and —
for a new universal `.pkg` — stapled, so a downloaded release clears Gatekeeper
on its own. The Homebrew cask's quarantine-stripping hook goes away, because it
existed only to work around not having done this.

[Issue #150](https://github.com/lezli01/vincent/issues/150) asked for this *and*
for Windows Authenticode. **Windows is dropped from this task's scope by the
owner**, 2026-08-26, and §19 †'s Authenticode descope stays in force unamended.

Conventions for this file are in [the tasks README](README.md). Behaviour lands
in [the spec](../spec.md) as dated amendments, in the same PR as the release
configuration and user documentation.

## Decisions (2026-08-26)

- **This explicitly reverses the Apple half of the v0 X packaging decision**
  ([`docs/history/v0-tasks.md`](../history/v0-tasks.md), *Packaging (X)*,
  2026-08-10: "**Signing is descoped** … Authenticode and Apple notarization are
  recurring cert purchases (~$99/yr Apple Developer ID, hardware-token OV for
  Windows) that v1 does not make"), and the 2026-08-14 amendment to §19 † that
  kept the `xattr` instructions on the grounds that "the binaries are still not
  notarized". X priced the cost correctly and it is not being argued away: the
  ~$99/yr Apple Developer Program membership is now **paid**, in exchange for a
  macOS install path that clears Gatekeeper rather than one that bypasses it.
  The alternative it beat is the status quo — telling every macOS user to run
  `xattr -d com.apple.quarantine`, which is instructing them to disable the
  check that would catch a tampered download.

- **Windows Authenticode is dropped, not deferred to the end of this task.** The
  same session weighed it and declined: an OV certificate lives on a hardware
  token, is a recurring purchase, and has no equivalent to Apple's single notary
  service that would let one CI job produce a signature. A **Microsoft Store
  MSIX** was weighed as a way to obtain a Microsoft-applied signature without
  buying a certificate, and rejected too. Consequently *no* Windows file changes
  in this task: `docs/platforms/windows.md`, the WinGet `installation_notes`,
  the README's SmartScreen sentence and the release footer's Windows wording all
  keep their current text, and the issue's Authenticode acceptance criterion is
  recorded as dropped rather than as met.

- **The release job moves to `macos-latest`.** Signing must happen *before*
  archiving — the signature lives inside the Mach-O, so a hook that ran later
  would sign a binary the archive and `checksums.txt` no longer describe — and
  `codesign`, `notarytool`, `stapler` and `pkgbuild` exist only on macOS.
  GoReleaser's native `notarize:` block is Pro-only (its own CI examples specify
  `distribution: goreleaser-pro`). Signing from Linux with `rcodesign` or
  `quill` was rejected in favour of Apple's own toolchain: a third-party
  reimplementation of the format Gatekeeper checks is the wrong place to save a
  runner. The X decision's single-runner shape survives — it is still **one**
  build runner, and the per-OS runners still only smoke-test what it produced.

- **The deb/rpm inspection moves to its own ubuntu `verify-packages` job.** The
  step used `apt-get`, `dpkg-deb`, `rpm` and `rpm2archive`, none of which a
  macOS runner has. It now consumes the uploaded `dist` artifact, which
  therefore had to start carrying `dist/winget/*.yaml` and
  `dist/scoop/vincent.json` too. Nothing that matters about ordering changed:
  that verification already ran after GoReleaser had published.

- **The `.pkg` is built after the GoReleaser run and is not in
  `checksums.txt`.** Getting it into the checksum file would mean producing it
  inside a build hook, before both architectures are guaranteed built, or adding
  a `darwin_all` archive via `universal_binaries` — a seventh release archive
  that would perturb the cask and every install document. Instead
  `scripts/macos-pkg.sh` runs in the same macOS job afterwards, `gh release
  upload` attaches it, and it is added to `actions/attest-build-provenance`'s
  `subject-path`. Its integrity story is Apple's own installer signature plus
  the build attestation, which is *stronger* than a line in a checksum file, and
  `docs/getting-started/installation.md`'s claim that `checksums.txt` covers
  "every archive, deb, and rpm" stays literally true.

- **A missing identity fails a tag build and no-ops a dry run.** Both scripts
  require the identity when the workflow passes `MACOS_SIGN_REQUIRED=1` — set on
  `push` to a `v*` tag — and skip with a warning otherwise. This is what keeps
  `workflow_dispatch` dry runs and fork runs working with no secrets at all, the
  same property the existing placeholder-token fallbacks give the publishers,
  while making an unsigned stable release **impossible** rather than merely
  unlikely.

- **The `.pkg` installs `/usr/local/bin/vincent` under `dev.lezli01.vincent`** —
  the identifier `internal/service` already uses for the LaunchAgent, and a path
  that is stable across upgrades, so task 021's recorded-executable-path
  decision needs nothing from this task. There are **no Go source changes** in
  it at all.

- **The cask's quarantine hook is removed, not left harmless.** It exists only
  because of the descope. Keeping `xattr -dr com.apple.quarantine` after paying
  for notarization would go on bypassing exactly the protection just bought, and
  would leave a hook nobody could later distinguish from a necessary one. This
  supersedes [task 002](002-homebrew-tap.md)'s `postflight` hook and its
  `xattr -l` done-criterion; `RELEASING.md` step 8 now asserts `spctl` accepts
  the linked binary instead of asserting the attribute is absent.

## Tasks

- [x] **032.1 — Sign the darwin binaries inside the build.** A
  `builds[].hooks.post` hook running `scripts/macos-sign.sh` with `codesign
  --options runtime --timestamp`, no-opping for every non-darwin target and for
  a run with no identity unless `MACOS_SIGN_REQUIRED=1`. Done when a tag build
  produces two darwin binaries that pass `codesign --verify --strict` and report
  the runtime flag, and a secretless dry run still completes. ✓ 2026-08-26
- [x] **032.2 — Move the release job to macOS and split out `verify-packages`.**
  Keychain and notary-profile bootstrap from six secrets, the deb/rpm and
  manifest inspection relocated to an ubuntu job, wider `upload-artifact` paths.
  ✓ 2026-08-26
- [x] **032.3 — Notarize the binaries and build the stapled `.pkg`.**
  `notarytool submit --wait` over a zip of the signed binaries; then
  `scripts/macos-pkg.sh` doing lipo → pkgbuild → sign → notarize → staple →
  verify, uploaded with `gh release upload` and added to the attestation
  subjects. ✓ 2026-08-26
- [x] **032.4 — Remove the cask's quarantine hook.** ✓ 2026-08-26
- [x] **032.5 — Rewrite the documentation rather than patch it.** §19 † dated
  amendment; `installation.md` gains the `.pkg` path and loses `xattr`;
  `platforms/macos.md`'s Gatekeeper section rewritten around notarization;
  README, `docs/README.md`'s macOS row, and `RELEASING.md`'s secrets, keychain
  bootstrap and certificate rotation/revocation guidance. Windows wording
  untouched throughout. ✓ 2026-08-26
- [x] **032.6 — Run repository verification and review the final diff.** All
  three tools were absent from the environment 032 was written in; they are
  present now and have run against the task 039 diff, which touches every file
  032 added. `goreleaser check` validates, `actionlint` reports nothing in
  `release.yml` (its two findings are pre-existing and deliberate — `ci.yml`'s
  `./bin/vincent*` glob and a `go-toolchain.yml` SC2016), `shellcheck` is clean
  on both `scripts/macos-sign.sh` and `scripts/macos-pkg.sh`, and `go run mage.go
  test` and `lint` are green. ✓ 2026-08-27
- ~~**032.7 — Enrol with Apple and prove a signed release.**~~ **Dropped
  2026-08-27** by the owner: the ~$99/yr Apple Developer Program membership is
  not being bought, so the enrolment this task waited on will not happen and
  there is nothing left to unblock. Reasoning in
  [task 039](039-unsigned-releases-by-default.md)'s 2026-08-27 decisions; the
  pipeline it would have activated is retained and dormant, and installing the
  six `MACOS_*` secrets is still the whole of the switch if this is ever
  revived. Its original text: *requires the owner's Apple Developer Program
  enrolment, the two Developer ID identities, an App Store Connect API key, and
  the six repository secrets; all are owner-only account mutations. Done when a
  **stable tag** produces artifacts that a human verifies through
  [the gate](../gates/032-macos-notarization.md). A green dry run does not close
  this — a dry run with no secrets is designed to produce unsigned artifacts, so
  it cannot evidence a working signature.*

## What the tests prove, and what they do not

There is no unit-testable surface in this task — it is a release pipeline, two
shell scripts and documentation — and no test was invented to look like there
is. The proof is layered instead:

- `goreleaser check` in `ci.yml`'s `packaging-config` job already rejects a
  malformed config, so the new build hook's schema is checked on every PR.
- The dry run (`workflow_dispatch`, `--snapshot --skip=publish,sign`) must still
  complete on a macOS runner with **no** signing secrets present, producing
  unsigned artifacts. That is the fork/contributor path and the regression test
  for the required/skip logic.
- In the release job: `codesign --verify --strict --verbose=2` on each darwin
  binary, `codesign -dv` asserting the hardened-runtime flag, `notarytool`
  exiting non-zero on rejection, and `stapler validate`, `pkgutil
  --check-signature` and `spctl --assess --type install` on the `.pkg`.
- In the macOS smoke leg: unpack the real archive, write a synthetic
  `com.apple.quarantine` attribute, and assert the binary still verifies and
  assesses. **Stated honestly:** an artifact fetched by `download-artifact` is
  not genuinely quarantined — the action does not carry extended attributes —
  so this is a strong proxy for the Gatekeeper dialog, not a proof of it.
- [`docs/gates/032-macos-notarization.md`](../gates/032-macos-notarization.md)
  is where the actual claim is settled, in the `m3`/`017`/`021.7` pattern: a
  human downloads the archive and the `.pkg` in a browser on a clean machine,
  runs both, and confirms no dialog — plus one **offline** first launch from the
  stapled `.pkg`, which is the only thing distinguishing it from the archive,
  and one `brew install` proving the cask still works with its quarantine hook
  gone.

## Acceptance criteria, as amended

Issue #150's criteria, and what this task does with each:

| Issue criterion | Disposition |
|---|---|
| macOS codesign + Gatekeeper assessment; notarization stapled/verifiable offline "where supported" | Accepted. "Where supported" resolves to the `.pkg`: a stapler ticket cannot attach to a bare Mach-O, so the archive's binary relies on the online notarization check |
| Windows Authenticode verified in the Windows smoke job | **Dropped** — out of scope by the owner's decision, 2026-08-26 |
| Signing secrets unavailable to PR workflows and forks | Accepted, and already structurally true: `release.yml` runs only on `v*` tags and `workflow_dispatch` |
| A failed/missing signature blocks publishing, not dry runs | Accepted — the `MACOS_SIGN_REQUIRED` split |
| Cosign/provenance/checksum verification remains intact | Accepted, with the recorded exception that the new `.pkg` is covered by attestation and Apple's signature rather than `checksums.txt` |
| Docs cover verification and certificate rotation/revocation | Accepted; rotation and revocation live in `RELEASING.md` beside the existing token-rotation guidance, and the user-facing verification commands in `installation.md` and `platforms/macos.md` |
