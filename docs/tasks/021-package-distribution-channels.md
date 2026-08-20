# 021 — Package distribution channels

**Status:** ⚠ verification blocked (6/8) · **Opened:** 2026-08-20

Vincent releases should meet people in the package manager they already use:
WinGet and Scoop on Windows; deb, rpm, and AUR on Linux; and mise on every
platform mise supports. GitHub release archives and the Homebrew cask remain
available and remain the source artifacts behind those channels.

Conventions for this file are in [the tasks README](README.md). Behaviour lands
in [the spec](../spec.md) as dated amendments, in the same PR as the release
configuration and user documentation.

## Decisions (2026-08-20)

- **This explicitly supersedes task 002's Windows rejection and the v0 X
  distribution decision.** Both decisions correctly priced a bucket, a WinGet
  fork, and release-time publishing as permanent maintenance. That cost is now
  accepted for discoverability and one-command upgrades on Windows and Linux;
  it is not being re-described as free. GitHub archives stay the fallback and
  the provenance source for every manager.

- **GoReleaser remains the single artifact and manifest producer.** nFPM emits
  deb and rpm packages from the existing Linux binaries; GoReleaser emits the
  Scoop manifest, the WinGet submission, and `vincent-agent-bin`'s PKGBUILD
  from the same checksummed archives. A second release workflow would make
  version, checksum, and license drift possible. Stable tags publish manager
  metadata; prerelease tags render it for inspection but do not move stable
  channels.

- **mise needs documentation, not another registry.** mise's GitHub backend
  understands the repository's existing GoReleaser archive names, so
  `mise use -g github:lezli01/vincent` installs the same release without a
  checked-in plugin or a repository write. Creating a bespoke mise backend
  would duplicate behavior the standard backend already owns.

- **Linux packages install one binary and documentation, not a system
  service.** Vincent's service is deliberately per-user and captures that
  user's config/data paths and `PATH`. A root deb/rpm maintainer script cannot
  safely choose a desktop user, and package removal must not erase that user's
  task history. Package managers install `/usr/bin/vincent`; users continue to
  opt into `vincent service install` themselves.

- **Package-manager upgrades do not guess about a registered service.** The
  service records the resolved executable path at registration time. Channels
  whose install path can move therefore document the existing idempotent
  `vincent service install` command after an upgrade, and document
  `vincent service uninstall` before package removal. Manager hooks that
  silently rewrite per-user service state would be surprising and cannot cover
  all five channels consistently.

- **Publishing credentials stay separate, with one explicit WinGet
  exception.** Scoop gets a fine-grained token scoped only to
  `lezli01/scoop-bucket`; AUR gets a dedicated unencrypted SSH key used only
  for `vincent-agent-bin`. WinGet must both push the owner's fork and open a
  pull request in Microsoft's repository, which a fine-grained token scoped to
  the fork cannot express; its dedicated classic PAT therefore needs
  `public_repo` and can write other public repositories as that account. A bot
  account is the tighter long-term option. Reusing the Release Please token
  would add private repository scope without solving this cross-owner
  permission boundary.

- **The commercial-license boundary is visible in every format.** Package
  metadata names `PolyForm-Noncommercial-1.0.0`, and deb/rpm/AUR installs carry
  both `LICENSE` and `COMMERCIAL-LICENSE.md`. A package manager making install
  easier does not broaden the rights granted by the release.

- **The AUR name is `vincent-agent-bin`, not `vincent-bin`.** The shorter name
  is already maintained by an unrelated terminal color-scheme project. The
  collision cannot be solved by overwriting someone else's package, so the
  descriptive free name owns this PKGBUILD while `provides=('vincent')` and
  `conflicts=('vincent')` preserve the installed command's identity.

## Tasks

- [x] **021.1 — Build deb and rpm packages.** Add nFPM packaging for Linux
  amd64 and arm64, including git as a runtime dependency and both license
  documents. Done when a snapshot produces two packages per format and their
  payloads install `/usr/bin/vincent` with the expected metadata. ✓ 2026-08-20
- [x] **021.2 — Generate Windows manifests and wire publishing.** Add Scoop and
  WinGet metadata from the existing zip archives, stable-only publishing,
  isolated credentials, and a cross-repository WinGet pull request.
  ✓ 2026-08-20
- [x] **021.3 — Generate `vincent-agent-bin` and wire publishing.** Add a
  stable-only AUR PKGBUILD that installs the binary and both license documents
  and depends on git. ✓ 2026-08-20
- [x] **021.4 — Add the mise path.** Document and execute an isolated install
  through mise's standard GitHub backend, including version pinning and
  upgrades. ✓ 2026-08-20
- [x] **021.5 — Extend release provenance and smoke checks.** Upload and attest
  deb/rpm artifacts, inspect their payloads, and make CI reject an invalid
  GoReleaser schema. ✓ 2026-08-20
- [x] **021.6 — Amend product and operator documentation.** Update §19, README,
  installation/platform guides, and RELEASING without claiming an unpublished
  channel is already live. ✓ 2026-08-20
- [!] **021.7 — Run repository verification and review the final diff.** — the
  container's PID namespace makes the existing `procx` live-process tests and
  dependent `taskrun` recovery tests fail; the same four failures reproduce
  without `-race` and are already recorded by task 020.
  Done only when GoReleaser check/snapshot, package payload checks, docs/link
  lint, and the repository's required code checks have actually run;
  unavailable checks remain explicit.
- [!] **021.8 — Bootstrap and prove the external channels.** — requires the
  owner's authorization and credentials for external repository/account writes.
  Create the public
  Scoop bucket and WinGet fork, register `vincent-agent-bin` in AUR, install
  the three destination credentials, publish a stable tag, and install that
  version through all six new paths. This is an external repository/account
  mutation and must not be inferred from a successful local snapshot.

## Verification (2026-08-20)

Run with Go 1.26.6, GoReleaser 2.17.1, actionlint 1.7.12, and mise 2026.8.9:

- `goreleaser check` — pass.
- `goreleaser release --snapshot --clean --skip=publish,sign` — pass. It
  produced six OS/architecture archives, two debs, two rpms, three WinGet
  manifests, one Scoop manifest, `vincent-agent-bin`'s PKGBUILD and `.SRCINFO`,
  and the existing Homebrew cask. `checksums.txt` contains all ten binary
  artifacts.
- Debian inspection — both architectures present; the amd64 control data names
  package `vincent`, architecture `amd64`, and dependency `git`; its payload
  contains `/usr/bin/vincent` plus both license documents, and the extracted
  binary reports `0.3.1-next`.
- RPM inspection — an independent `go-rpmutils` 0.4.0 reader confirms name
  `vincent`, architecture `x86_64`, license
  `PolyForm-Noncommercial-1.0.0`, dependency `git`, executable mode, both
  license documents, and a runnable `0.3.1-next` binary. The container cannot
  install Ubuntu's `rpm` command because its user namespace rejects apt's
  `setgroups`; the release workflow installs and runs the native query tools on
  a normal GitHub runner.
- Generated metadata — Scoop JSON parses and contains x86-64/ARM64, `git`, and
  the PolyForm license; all three WinGet YAML files name
  `lezli01.Vincent`; the AUR PKGBUILD passes `bash -n` and `.SRCINFO` carries
  both architectures, release URLs, checksums, dependency, provides, conflicts,
  and license.
- mise isolated install — `mise use -g github:lezli01/vincent@0.3.0` selected
  `vincent_0.3.0_linux_amd64.tar.gz`, verified its GitHub artifact attestation,
  installed it, and ran the real binary. `mise unuse` and `mise uninstall`
  removed it cleanly.
- `actionlint .github/workflows/ci.yml .github/workflows/release.yml` — pass.
- `git diff --check` and relative-link validation over all changed Markdown —
  pass.
- `go run mage.go lint` — pass, `0 issues`.
- `go run mage.go build` — pass; the built binary runs.
- `GOOS=windows CGO_ENABLED=0 go build ./...` and the equivalent Darwin build
  — pass.
- `go run mage.go testrace` — all other packages pass; the four existing
  live-PID/recovery tests fail for the environment reason on 021.7. `go test
  ./internal/procx ./internal/taskrun -count=1` reproduces the same failures
  without the race detector.

External readiness checks found that `lezli01/scoop-bucket` and
`lezli01/winget-pkgs` do not yet exist. AUR's `vincent-bin` is already an
unrelated package, so this task uses the currently unclaimed
`vincent-agent-bin`; no AUR package was registered and no destination
credential or stable tag was created in this task.
