# 021 — Package distribution channels

**Status:** ⚠ verification blocked (6/7) · **Opened:** 2026-08-20

Vincent releases should meet people in the package manager they already use:
WinGet and Scoop on Windows; deb and rpm on Linux; and mise on every
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
  Scoop manifest and the WinGet submission from the same checksummed archives.
  A second release workflow would make
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
  `lezli01/scoop-bucket`. WinGet must both push the owner's fork and open a
  pull request in Microsoft's repository, which a fine-grained token scoped to
  the fork cannot express; its dedicated classic PAT therefore needs
  `public_repo` and can write other public repositories as that account. A bot
  account is the tighter long-term option. Reusing the Release Please token
  would add private repository scope without solving this cross-owner
  permission boundary.

- **The commercial-license boundary is visible in every format.** Package
  metadata names `PolyForm-Noncommercial-1.0.0`, and deb/rpm installs carry
  both `LICENSE` and `COMMERCIAL-LICENSE.md`. A package manager making install
  easier does not broaden the rights granted by the release.

  **Superseded 2026-08-26.** The project returned to the MIT License. Current
  package metadata names `MIT`, and release artifacts carry `LICENSE` only;
  the validation record below remains the historical result for `v0.3.1`.

## Tasks

- [x] **021.1 — Build deb and rpm packages.** Add nFPM packaging for Linux
  amd64 and arm64, including git as a runtime dependency and both license
  documents. Done when a snapshot produces two packages per format and their
  payloads install `/usr/bin/vincent` with the expected metadata. ✓ 2026-08-20
- [x] **021.2 — Generate Windows manifests and wire publishing.** Add Scoop and
  WinGet metadata from the existing zip archives, stable-only publishing,
  isolated credentials, and a cross-repository WinGet pull request.
  ✓ 2026-08-20
- [x] **021.3 — Add the mise path.** Document and execute an isolated install
  through mise's standard GitHub backend, including version pinning and
  upgrades. ✓ 2026-08-20
- [x] **021.4 — Extend release provenance and smoke checks.** Upload and attest
  deb/rpm artifacts, inspect their payloads, and make CI reject an invalid
  GoReleaser schema. ✓ 2026-08-20
- [x] **021.5 — Amend product and operator documentation.** Update §19, README,
  installation/platform guides, and RELEASING without claiming an unpublished
  channel is already live. ✓ 2026-08-20
- [x] **021.6 — Run repository verification and review the final diff.**
  GoReleaser check and snapshot, docs/link lint and the repository's required
  code checks have run; the deb and rpm payload checks are not runnable on the
  verifying machine and stay explicit, cited to CI instead. The #158 diff
  review's one remaining defect — `winget install --id lezli01.Vincent --exact`
  documented as a working path — was fixed by `88a3c962e` (2026-09-14) and
  [#377](https://github.com/lezli01/vincent/issues/377) is closed. See
  Verification (2026-09-22, #573). ✓ 2026-09-22
- [!] **021.7 — Bootstrap and prove the external channels.** — publication is
  proven for one span only: every stable tag from v0.4.0 to v0.8.0 updated
  Scoop automatically and opened a WinGet pull request; v0.9.0's release run
  failed before any publisher ran. No WinGet submission has passed Microsoft's
  moderator review, and no install through the five paths is recorded;
  installing needs a person on real systems.
  Confirm the Scoop bucket and WinGet fork, install the two destination
  credentials, publish a stable tag, and install that version through all five
  new paths. This is an external repository/account
  mutation and must not be inferred from a successful local snapshot.

## Verification (2026-08-20)

Run with Go 1.26.6, GoReleaser 2.17.1, actionlint 1.7.12, and mise 2026.8.9:

- `goreleaser check` — pass.
- `goreleaser release --snapshot --clean --skip=publish,sign` — pass. It
  produced six OS/architecture archives, two debs, two rpms, three WinGet
  manifests, one Scoop manifest, and the existing Homebrew cask.
  `checksums.txt` contains all ten binary
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
  `lezli01.Vincent`.
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
  live-PID/recovery tests fail for the environment reason on 021.6. `go test
  ./internal/procx ./internal/taskrun -count=1` reproduces the same failures
  without the race detector.

The owner has since created `lezli01/scoop-bucket` and the
`lezli01/winget-pkgs` fork and configured their destination credentials.
Stable tags v0.4.0–v0.8.0 have since proved publication (see below); no
install through these channels is recorded.
AUR support is intentionally deferred to
[#157](https://github.com/lezli01/vincent/issues/157) because new AUR account
registration is temporarily suspended.

## Verification (2026-09-14, #379)

Re-run for 021.6 and 021.7. The 2026-08-20 record above stays as history; its
`procx`/`taskrun` failures were that workspace's `/proc` view and do not
reproduce on a host.

**Required code checks and the GoReleaser configuration**

- PR #158's CI run
  [32457224455](https://github.com/lezli01/vincent/actions/runs/32457224455)
  at head `d9f1ef0` (merged as `9883ba4`, 2026-08-21) — `success`: `ci`
  (`mage lint`, `mage testrace`, `mage build`) and `gates` on ubuntu, macOS
  and windows, and `packaging-config` (`goreleaser check`). This is the run on
  the diff itself.
- `goreleaser check` with GoReleaser 2.18.1 at `b885c53` on macOS — pass,
  `1 configuration file(s) validated`.
- Local run at `b885c53` (`master`) on macOS, 2026-09-14 — a run of today's
  tree, not of the 2026-08-21 diff. Every command passed:
  `go test ./internal/procx -count=1`,
  `go test ./internal/taskrun -run 'Orphan|Recover' -count=1`,
  `go run mage.go test`, `go run mage.go testrace`, `go run mage.go lint`
  (`0 issues.`), the host-built linter with `GOOS=windows`, `darwin` and
  `linux` (`0 issues.` each), and `go run mage.go build`.
- No local `goreleaser release --snapshot` was run; the release runs below
  built and inspected the packages for real.

**Package payload checks.** "Verify generated packages and manifests" was a
step of the `release` job through v0.6.0 and is its own `verify-packages` job
from v0.7.0. At `b885c53` it counts two debs, two rpms, three WinGet YAML files
and the Scoop JSON; checks deb and rpm name, `git` dependency, `/usr/bin/vincent`
and the license file with `dpkg-deb` and `rpm`; extracts the amd64 deb and the
x86_64 rpm and runs both binaries' `version`; and checks the Scoop JSON and the
three WinGet manifests.

- v0.4.0, release run
  [32460147877](https://github.com/lezli01/vincent/actions/runs/32460147877)
  — `failure`: `cpio: /usr/bin/vincent: Cannot open: Permission denied`, then
  `Process completed with exit code 2.`
- v0.4.1, release run
  [32463249226](https://github.com/lezli01/vincent/actions/runs/32463249226),
  which carries `233e839` ("constrain RPM package extraction") — `failure` in
  the same step: its last output is ``cpio: Removing leading `/' from member
  names``, then `Process completed with exit code 1.`
- `a979b8a` ("verify RPM packages through tar") landed before v0.4.2. The
  check has passed on every stable tag since: v0.4.2
  ([32465985805](https://github.com/lezli01/vincent/actions/runs/32465985805)),
  v0.5.0
  ([32568455683](https://github.com/lezli01/vincent/actions/runs/32568455683)),
  v0.6.0
  ([32891537655](https://github.com/lezli01/vincent/actions/runs/32891537655)),
  v0.7.0
  ([33243913259](https://github.com/lezli01/vincent/actions/runs/33243913259))
  and v0.8.0
  ([33895256784](https://github.com/lezli01/vincent/actions/runs/33895256784)).
- The merged workflow attested after this check, so the failed runs published
  unattested assets: the attestations API returns 404 for
  `vincent-0.4.0-1.aarch64.rpm` and one attestation for
  `vincent-0.4.2-1.aarch64.rpm`. At `b885c53` "Attest build provenance" runs in
  the `release` job, before `verify-packages`.

**Docs and link lint.** No repository script exists, so the 2026-08-20 method
was repeated. `git diff --check 9883ba4^1 9883ba4` printed nothing and exited
0. The relative link targets in the nine Markdown files that diff changed were
resolved at `b885c53` by a script following inline, image and
reference-definition links outside fenced code: 189 of 190 resolve, and the
190th is `[label](url "title")` inside inline code in `docs/spec.md`, not a
link.

**Review of the #158 diff** (`git diff 9883ba4^1 9883ba4`, 12 files,
+644/−54):

- Defect, fixed since: `rpm2cpio … | cpio -idm` kept the payload's absolute
  paths, so the rpm binary was never extracted under the temp root — the
  v0.4.0 and v0.4.1 failures above. `233e839` and `a979b8a` replaced it with
  `rpm2archive | tar`.
- Defect, still present at `b885c53`: `README.md`,
  `docs/getting-started/installation.md` and `docs/platforms/windows.md`
  present `winget install --id lezli01.Vincent --exact` as a working path, but
  no submission has merged (see Publication).
  [#377](https://github.com/lezli01/vincent/issues/377) owns those pages.
  *Fixed 2026-09-14 by `88a3c962e`, which is a descendant of `b885c53`: all
  three pages now say no submission has merged. #377 is closed; this record is
  superseded by Verification (2026-09-22,
  [#573](https://github.com/lezli01/vincent/issues/573)), under which those
  pages also gained a link to the open submissions.*
- Checked and not counted as a defect: GoReleaser publishes the release assets
  and the manager metadata before package inspection runs, both at the merge
  and at `b885c53`; `RELEASING.md` describes `verify-packages` as inspection
  after publication, not as a gate. All three manager publishers use
  `skip_upload: auto`.

**Publication (021.7)**, checked 2026-09-14:

- Scoop: `lezli01/scoop-bucket` has one automated commit per stable tag —
  `cb59bea` v0.4.0, `439daee` v0.4.1, `5372f7f` v0.4.2, `f3ef226` v0.5.0,
  `aa4adff` v0.6.0, `73fd7e8` v0.7.0, `17c4f46` v0.8.0.
- WinGet: microsoft/winget-pkgs has one pull request per stable tag, all open
  and each labelled `Azure-Pipeline-Passed`, `Validation-Completed` and
  `New-Package` — #422036 (v0.4.0), #422051 (v0.4.1), #422063 (v0.4.2),
  #422568 (v0.5.0), #424154 (v0.6.0), #426043 (v0.7.0), #429585 (v0.8.0).
  `manifests/l/lezli01` does not exist in microsoft/winget-pkgs (HTTP 404), so
  the package is not in the catalog.
- deb and rpm: every release from v0.4.0 to v0.8.0 carries two debs and two
  rpms.
- Supporting context, task 002's channel rather than one of these five:
  `lezli01/homebrew-tap` moved on the same tags, v0.4.0 `b3f1fc3` through
  v0.8.0 `1b2a254`.
- No install through WinGet, Scoop, mise, deb or rpm is recorded for any stable
  tag. The only recorded install is 021.3's isolated mise install of 0.3.0.

## Verification (2026-09-22, #573)

Re-run for 021.6 alongside the WinGet documentation corrections of
[#573](https://github.com/lezli01/vincent/issues/573). The 2026-08-20 and
2026-09-14 records above stay as history. Run on macOS (darwin/arm64) with Go
1.27.1 and GoReleaser 2.18.1, on this branch over `67a84234`.

**GoReleaser configuration**

- `goreleaser check` — pass, `1 configuration file(s) validated`.
- `goreleaser release --snapshot --clean --skip=publish,sign` — exit 0,
  `release succeeded after 1m1s`. It produced six archives (four `tar.gz`, two
  `zip`), two debs, two rpms, the three WinGet manifests
  (`lezli01.Vincent.yaml`, `lezli01.Vincent.installer.yaml` and
  `lezli01.Vincent.locale.en-US.yaml`, the last carrying `PackageIdentifier:
  lezli01.Vincent`), and the Scoop `vincent.json` with its `64bit` and `arm64`
  architectures. A snapshot builds no `.pkg`: the darwin installer comes out of
  the signing path this run skips.

**Required code checks**

- `go run mage.go lint` — `0 issues.`
- the host-built linter under `GOOS=windows`, `GOOS=darwin` and `GOOS=linux` —
  `0 issues.` each.
- `go run mage.go build` — exit 0.
- `go run mage.go test` — exit 0, 41 packages `ok`, no failures.
- `go run mage.go testrace` — exit 0, 41 packages `ok`, no failures and no race
  reports.

**Package payload checks — not runnable here, cited to CI.** `dpkg-deb` and
`rpm` are both absent from this machine (`command -v` returns nothing for
either), so `verify-packages`' inspection of the deb and rpm names, the `git`
dependency, `/usr/bin/vincent`, the license file and the extracted binaries'
`version` could not be repeated locally. The last green run of that job is
v0.8.0's, release run
[33895256784](https://github.com/lezli01/vincent/actions/runs/33895256784)
(`release: success`, `verify-packages: success`, all three `smoke` legs
`success`). It has not run since: for v0.9.0 the `release` job failed first, and
`verify-packages` and `smoke` are both `skipped`.

**Docs and link lint.** No repository script exists, so the 2026-09-14 method
was repeated over this branch's changed Markdown. `git diff --check` printed
nothing and exited 0. The link targets in the six changed Markdown files were
resolved by a script following inline, image and reference-definition links
outside fenced code and outside inline code: 281 of 281 resolve.

**Publication, re-checked 2026-09-22**

- WinGet: still seven pull requests by `lezli01` against
  `microsoft/winget-pkgs`, all `open`, none merged — #422036 (v0.4.0) through
  #429585 (v0.8.0), the same seven as on 2026-09-14.
  `manifests/l/lezli01` is still HTTP 404, so the package is still not in
  Microsoft's catalog.
- **v0.9.0 submitted nothing.** Its release run
  [35381882773](https://github.com/lezli01/vincent/actions/runs/35381882773)
  (2026-09-18) ended `failure` in the `release` job at the cosign step —
  `Error: signing dist/checksums.txt: create bundle file: open : no such file
  or directory`, after cosign warned that `--output-signature` and
  `--output-certificate` are deprecated and ignored under
  `--new-bundle-format` — before GoReleaser reached any manager publisher. The
  v0.9.0 GitHub release is published with 0 assets where v0.8.0 has 14, and
  `lezli01/scoop-bucket` and `lezli01/homebrew-tap` both still stop at v0.8.0.
  That is a release-signing defect rather than a documentation one, out of
  scope for #573 and untracked by any issue as of this run; the only
  consequence recorded here is that the WinGet submission span is v0.4.0
  through v0.8.0, not "every stable release since v0.4.0", which is the
  sentence `docs/getting-started/installation.md` corrected.
- No install through WinGet, Scoop, mise, deb or rpm is recorded for any stable
  tag. 021.7 is unchanged and still blocked on a person with real systems.

**The #158 diff defect, closed.** The 2026-09-14 review's one remaining defect
— `README.md`, `docs/getting-started/installation.md` and
`docs/platforms/windows.md` presenting `winget install --id lezli01.Vincent
--exact` as a working path — was fixed by `88a3c962e` (2026-09-14), which has
`b885c53`, the commit that review ran at, as an ancestor.
[#377](https://github.com/lezli01/vincent/issues/377) is closed. What those
three pages still lacked, and gain here, is a link to the submissions
themselves: a non-staling `author:lezli01` query rather than a version-specific
pull-request number that every future tag would silently invalidate.
`RELEASING.md` step 9 gains the same link, so a maintainer whose `winget show
--id lezli01.Vincent --exact --versions` finds nothing reads the expected
result rather than a release fault.
