# 038 — Release signing posture for an MIT project

**Status:** ⚠ blocked on the owner (1/7) · **Opened:** 2026-08-27

Two independent pieces of vincent's OS-level signing posture, reopened because
the project is unambiguously under an OSI license:

- **Windows Authenticode**, descoped by §19 † on 2026-08-10 and left descoped by
  §19's 2026-08-26 amendment, is reopened — but only far enough to survey the
  **free-for-OSS** signing routes and apply to whichever one wins. No pipeline
  code, no documentation rewrite and no spec amendment land until a certificate
  actually exists.
- **deb and rpm signing**, undecided since task 021, is decided here: the
  packages stay unsigned, deliberately, and the reasoning is now written down.

[Issue #207](https://github.com/lezli01/vincent/issues/207) asked for a third
strand — re-examining the Apple arrangement set up under the since-reverted dual
license. **That strand is dropped**; the reasoning is in the decisions below.

Conventions for this file are in [the tasks README](README.md). Behaviour lands
in [the spec](../spec.md) as dated amendments, in the same PR as the release
configuration and user documentation that make them true.

## Decisions (2026-08-27)

- **The Windows half of §19 † is reopened, on a new fact and not on a new
  opinion.** Task 032's "Windows Authenticode is dropped, not deferred" and
  §19's 2026-08-26 amendment both priced a **purchased** OV certificate on a
  hardware token, with no equivalent to Apple's single notary service. That
  pricing was correct for what it priced and is not being argued away.
  Free-for-OSS Authenticode signing programs — SignPath Foundation being the
  obvious one — are a fact neither of those sessions had, and vincent is
  unambiguously an OSI-licensed project. The reopening rests on that and on
  nothing else.

- **Free routes only, and the survey comes before any code.** 038.1 is a written
  comparison of every free Authenticode route — eligibility, certificate
  assurance level, and the pipeline shape each one demands — ending in an
  application to whichever wins. **Microsoft Store MSIX stays rejected** on its
  2026-08-26 grounds and is not re-evaluated. **Certum** (~$25/yr) and **Azure
  Trusted Signing** (~$9.99/mo) are named in the survey as priced-and-out-of-scope,
  not evaluated as fallbacks: they are recurring purchases, which is the thing †
  declined. If no free route accepts vincent, this strand stops, and reopening
  the paid question is a separate decision for a separate task.

- **The acceptance bar is "signed and verifiable", not "the prompt is gone".**
  Issue #207 proposed the latter, matching how
  [the macOS gate](../gates/032-macos-notarization.md) proves Gatekeeper. It does
  not transfer. An Authenticode signature and a SmartScreen reputation are
  different things: a Foundation-issued **OV** certificate does not clear
  SmartScreen on day one — reputation accrues over download volume and time,
  whereas EV clears it immediately. The bar is therefore `signtool verify /pa`
  passing in the existing Windows smoke leg against the expected publisher, plus
  a human confirming the dialog now names vincent rather than *"Unknown
  publisher"*. The documentation must say plainly that the prompt clears as
  reputation accrues; it must not promise a prompt-free first run. This is the
  alternative the bar beats — a promise the certificate cannot keep.

- **Strand 2 — re-evaluating the Apple arrangement — is dropped, with the
  reasoning recorded so it is not re-derived a third time.** Apple waives the
  Developer Program fee only for nonprofits, accredited educational institutions
  and government entities, and only on **organization** membership, which
  requires a D-U-N-S number and a legal entity. There is no route a
  single-maintainer MIT project qualifies for. The issue itself concedes the two
  factual halves: [task 032](032-macos-notarization.md) justified the ~$99/yr
  membership by Gatekeeper UX and by removing the cask's `xattr` bypass — never
  by commercial licensing — and `6212fce` already swept the license metadata out
  of `.goreleaser.yaml` and `.github/workflows/release.yml`, so **no signing
  configuration encodes the dual license today**. There is nothing to revisit and
  no spec amendment to write. Task 032's enrolment blocker (032.7) is untouched
  by this and stays 032's job.

- **deb and rpm stay unsigned, deliberately.** This converts an undecided gap
  into a recorded decision; `nfpms` at `.goreleaser.yaml:68` is correct as it
  stands and gains no `signature:` block. The reasoning:

  Vincent publishes **no APT or YUM repository**. `dpkg` and `apt` do not verify
  a per-package signature on a downloaded `.deb` at all — apt verifies the
  repository's `Release` file, and `dpkg-sig` is not part of the path a user
  takes when they `dpkg -i` a file fetched from a GitHub release. So deb signing
  buys precisely nothing on the path vincent's users actually take. `rpm -K`
  *does* verify a package signature once its key is imported, which is the real
  half of the case for signing — but the price is a **release-held GPG key**: a
  long-lived secret with publication and rotation duty, exactly the thing keyless
  cosign exists to avoid. The archives, debs and rpms are already covered by
  cosign over `checksums.txt` and by GitHub build attestations, both of which
  bind the artifact to the workflow and commit that produced it with no vincent
  key for anyone to trust or rotate.

  The alternative it beat: adding `signature:` to `nfpms` with a release-held
  key, publishing that key, and documenting `rpm -K` in
  [`docs/platforms/linux.md`](../platforms/linux.md). It would improve the rpm
  path only, would not touch the deb path at all, and would trade a keyless
  supply chain for a keyed one to do it.

- **Nothing else lands until a certificate exists.** §19's Windows amendment in
  particular waits. The [tasks README](README.md) requires spec amendments in the
  same pull request as the code that makes them true, and a §19 note reversing
  the Windows descope ahead of a working signature would describe a system that
  does not exist. The *reopening* is recorded here, which is where dated
  decisions belong; §19 is amended by 038.5, alongside the pipeline.

## What 038.1 must resolve before any pipeline code is written

Recorded here because it can invalidate the design issue #207 proposed, and
because the application is the only place it can be settled:

Issue #207 proposes a `builds[].hooks.post` script mirroring
`scripts/macos-sign.sh`. **That shape may not be available.** SignPath's model
is not a local `signtool` invocation: its origin verification fetches the
artifact from the GitHub Actions run and hands back a signed copy, which is
awkward to perform from inside a GoReleaser build hook. The clean way to feed a
pre-signed `.exe` back into a release — GoReleaser's `prebuilt` builder — is
**Pro-only**, the same constraint that pushed task 032 onto Apple's own
toolchain. And the release job runs on `macos-latest`
(`.github/workflows/release.yml`), chosen because `codesign` exists nowhere
else.

The ordering constraint issue #207 names is real and unchanged: an Authenticode
signature lives **inside** the PE, so it must be applied before the `.zip` is
assembled and before `checksums.txt` is computed — the same constraint
`scripts/macos-sign.sh` already solves for the Mach-O.

If no shape satisfies both that and the Foundation's origin verification, the
fallback precedent already exists in this tree: task 032 put the `.pkg`
**outside** `checksums.txt`, covered by its own signature plus a build
attestation. Choosing that for Windows would mean signing after the GoReleaser
run and re-uploading, which perturbs the archive every install document names.
That is a decision for 038.1 to make with the Foundation's answer in hand, not
an assumption to make now.

## The documentation surface

Issue #207 lists eight SmartScreen/Authenticode claims. There are **twelve**,
and the four it misses are the awkward ones. 038.4 rewrites all of them:

| Site | What it claims |
|---|---|
| `README.md:268` | Releases are not Authenticode-signed |
| `README.md:331-333` | SmartScreen prompt, plus the OV-cost reasoning |
| `docs/platforms/windows.md:46-51` | The prompt, and that no channel adds signing |
| `docs/platforms/windows.md:195` | Security table row: "Not done — SmartScreen prompts once" |
| `docs/getting-started/installation.md:100-101` | Not Authenticode-signed |
| `docs/getting-started/installation.md:243-246` | The prompt, plus the OV-cost reasoning |
| `docs/getting-started/installation.md:292-293` | "no Windows equivalent of the third exists here" |
| `docs/README.md:71` | The platform table's Windows row names SmartScreen |
| `RELEASING.md:348-351` | Carries the descope **reasoning**, not just the symptom |
| `.goreleaser.yaml:200` | WinGet `installation_notes` |
| `.goreleaser.yaml:226-228` | The `signs:` comment asserts Authenticode stays descoped |
| `.goreleaser.yaml:308-309` | The release footer |

Plus one that is not a claim but an **assertion**:
[`docs/gates/032-macos-notarization.md:117`](../gates/032-macos-notarization.md)
requires that on Windows "the SmartScreen prompt should still" appear. A signed
release makes a *passing gate document false*, which is worse than a stale
sentence in a README, so it is amended in the same work rather than left to
contradict the new behaviour.

None of these move in this pull request. They are listed so 038.4 has no
discovery to do.

## Tasks

- [!] **038.1 — Survey the free Authenticode routes and apply.** A written
  comparison of every free-for-OSS route — eligibility criteria, certificate
  assurance level (OV vs EV, which sets the acceptance bar), and the pipeline
  shape each demands, answering the questions above — ending in an application
  to whichever wins. SignPath Foundation is the likely answer. — **owner-only**:
  an external account action and a third-party review that no CI run can close,
  in the same shape as
  [032.7](032-macos-notarization.md). Done when the survey is recorded in this
  document and the application has an answer, accepted or declined; a decline
  closes 038.2–038.6 as dropped, with the outcome recorded here.
- [ ] **038.2 — Wire Windows signing into the release pipeline.**
  `Depends: 038.1` — its answer decides the shape, and none of it can be written
  before then. `.github/workflows/release.yml`, `.goreleaser.yaml` and a new
  `scripts/windows-sign.sh`, with a `WINDOWS_SIGN_REQUIRED` split mirroring
  `MACOS_SIGN_REQUIRED` (`release.yml`) — set on `push` to a `v*` tag, so an
  unsigned stable release is impossible rather than merely unlikely. Done when a
  tag build produces a signed `vincent.exe` **and** a secretless
  `workflow_dispatch` dry run still completes with unsigned artifacts.
- [ ] **038.3 — Verify the signature in the Windows smoke leg.**
  `Depends: 038.2`. `signtool verify /pa` on the unpacked `vincent.exe` in the
  existing Windows leg of `release.yml`'s `smoke` matrix, plus an assertion on
  the publisher name. `windows-latest` carries the Windows SDK, so this needs no
  new tooling. Done when the leg fails on an unsigned or wrongly-published
  binary.
- [ ] **038.4 — Rewrite the twelve claims, and amend the 032 gate.**
  `Depends: 038.2`. Every site in the table above, plus
  `docs/gates/032-macos-notarization.md`'s Windows assertion. Under the agreed
  bar the new wording says the binary is signed and names the publisher, and
  says plainly that the SmartScreen prompt clears as reputation accrues — it does
  not promise a prompt-free first run. Done when no site still asserts the
  releases are unsigned.
- [ ] **038.5 — Amend §19's Windows half.** `Depends: 038.2`. A dated §19
  amendment reversing the Windows half of †, in the **same** pull request as
  038.2, per the tasks README. Done when §19 describes the signing that actually
  runs.
- [ ] **038.6 — Walk the Windows gate on a clean VM.** `Depends: 038.3`. A new
  `docs/gates/` walkthrough in the `m3`/`017`/`021.7`/`032` pattern: download the
  release `.zip` in a browser on a clean Windows 11 VM, unpack, run, and record
  what the dialog says. Under the agreed bar it records the **publisher name**,
  not the absence of a dialog. Done when the walkthrough has a dated run record.
- [x] **038.7 — Record the deb/rpm decision.** Independent of 038.1–038.6 and
  shippable on its own: a signing note in `docs/platforms/linux.md` beside the
  existing "no Gatekeeper or SmartScreen equivalent" paragraph, a dated §19
  amendment, and the reasoning above. No configuration changes — `nfpms` is
  correct as it stands. ✓ 2026-08-27

## What the tests prove, and what they do not

There is no unit-testable surface in this task — it is a release pipeline and
documentation — and none will be invented to look like there is. 038.7, the only
part landing now, is a decision and two documents, and has nothing to test at
all. For the Windows strand the proof is layered exactly as task 032 layered it:

- `goreleaser check` in `ci.yml`'s `packaging-config` job rejects a malformed
  config on every pull request, covering any new hook's schema.
- The secretless `workflow_dispatch` dry run must still complete, producing
  unsigned artifacts. That is the fork and contributor path, and the regression
  test for the `WINDOWS_SIGN_REQUIRED` split.
- The existing Windows leg of the `smoke` matrix gains `signtool verify /pa` and
  a publisher assertion (038.3).
- A `docs/gates/` walkthrough settles the user-facing claim on a clean Windows 11
  VM (038.6) — and, under the agreed bar, records the publisher name rather than
  the absence of a dialog.

## Risks

- **The application may be declined.** SignPath Foundation evaluates project
  maturity and user base; vincent is at `v0.7.0`. A decline stops the Windows
  strand and the SmartScreen wording stays exactly as written. The outcome is
  recorded in 038.1 either way, so the question is not re-derived a third time.
- **OV will not clear SmartScreen at release time.** Already priced into the
  acceptance bar above. The failure mode is not technical, it is a documentation
  one: overclaiming in 038.4 would leave users meeting a dialog the docs said
  was gone.
