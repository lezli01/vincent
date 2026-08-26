# Releasing vincent

Releases are **Release Please-driven**. Conventional Commits on `master` keep a
release pull request current. Merging that pull request makes Release Please
create the `vMAJOR.MINOR.PATCH` tag and GitHub release; that tag triggers
[`.github/workflows/release.yml`](.github/workflows/release.yml), which uses
[`.goreleaser.yaml`](.goreleaser.yaml) to cross-compile, codesign and notarize
the macOS binaries, checksum, sign, attest, upload, smoke-test on all three
OSes, build deb/rpm packages and the macOS installer package, update the stable
Homebrew and Scoop metadata, and submit the stable WinGet manifest. There
is no manual tag or upload step, and no artifact is built on a maintainer's
machine.

The build job runs on **macOS**, not Linux: Apple's signature lives inside the
Mach-O and has to be applied before the archives and `checksums.txt` exist, and
`codesign`, `notarytool`, `stapler` and `pkgbuild` ship nowhere else (task 032).
The deb/rpm and manifest inspection therefore runs in its own `verify-packages`
job on ubuntu.

Only a maintainer with push access can cut a release.

## Channel bootstrap and repository secrets

Before enabling a stable release, both external destinations must exist:

- public `lezli01/scoop-bucket`, with `main` as its default branch;
- a `lezli01/winget-pkgs` fork of `microsoft/winget-pkgs`.

A GoReleaser snapshot renders both manifests without contacting those
destinations. That proves the content, not publication. Do not merge a release
change that names a missing destination: the next stable tag would discover it
after the GitHub release already exists.

Ten secrets are required: four publishing credentials, and six for Apple code
signing.

| Secret | Scope and permissions | Why it exists |
|---|---|---|
| `RELEASE_PLEASE_TOKEN` | Fine-grained PAT selecting only `lezli01/vincent`, with **Contents**, **Issues**, and **Pull requests: read and write** | Events created by `GITHUB_TOKEN` do not start other workflows. The PAT lets the release PR run normal CI and lets the generated tag trigger the GoReleaser workflow. |
| `HOMEBREW_TAP_TOKEN` | Fine-grained PAT selecting only [`lezli01/homebrew-tap`](https://github.com/lezli01/homebrew-tap), with **Contents: read and write** | `GITHUB_TOKEN` and the Release Please PAT are scoped to this repository; pushing the cask is a cross-repo write. |
| `SCOOP_BUCKET_TOKEN` | Fine-grained PAT selecting only `lezli01/scoop-bucket`, with **Contents: read and write** | Push the stable `vincent.json` manifest to the bucket root. |
| `WINGET_TOKEN` | Dedicated classic PAT with **`public_repo`** | Push a version branch to `lezli01/winget-pkgs` and open its pull request against `microsoft/winget-pkgs`. GitHub cannot express that cross-owner PR as a fine-grained token scoped only to the fork. |
| `MACOS_CERT_APPLICATION_P12` | base64 of a `.p12` holding the **Developer ID Application** certificate and its private key | Signs each darwin binary in a GoReleaser build hook, before the archive and the checksum exist. |
| `MACOS_CERT_INSTALLER_P12` | base64 of a `.p12` holding the **Developer ID Installer** certificate and its private key | Signs `vincent_*_darwin_universal.pkg`. A different certificate type: an Application identity cannot sign an installer, and vice versa. |
| `MACOS_CERT_PASSWORD` | The export password used for **both** `.p12` files | Keychain Access requires one when exporting; export both with the same password so one secret covers them. |
| `MACOS_NOTARY_ISSUER_ID` | App Store Connect API **Issuer ID** (a UUID) | `notarytool` authenticates with an API key rather than an Apple ID and app-specific password, so no interactive two-factor step is involved and the key is revocable on its own. |
| `MACOS_NOTARY_KEY_ID` | App Store Connect API **Key ID** | The identifier of the key below. |
| `MACOS_NOTARY_KEY_P8` | base64 of the App Store Connect API `.p8` private key | Apple lets you download this file exactly once. Keep an offline copy or you will be generating a new key. |

Nothing else needs a secret: cosign signs keylessly with the packaging
workflow's OIDC identity, and the two signing *identity names* are read back
from the imported certificates rather than stored, so rotating a certificate
does not mean editing the workflow.

### Bootstrapping the Apple credentials

Prerequisite: an **Apple Developer Program** membership (~$99/yr). §19 †'s
2026-08-26 amendment records accepting that cost for macOS and declining the
Windows equivalent.

1. In Xcode or on the developer portal, create two certificates for the team:
   **Developer ID Application** and **Developer ID Installer**.
2. In *Keychain Access → My Certificates*, export each one — the certificate
   **with** its private key — as a `.p12`, using the same password for both.
3. In App Store Connect → *Users and Access → Integrations → Keys*, create an
   API key with the **Developer** role and download its `.p8`. Note the Key ID
   and the Issuer ID shown on that page.
4. Install the six secrets:

   ```sh
   base64 < DeveloperIDApplication.p12 |
     gh secret set MACOS_CERT_APPLICATION_P12 --repo lezli01/vincent
   base64 < DeveloperIDInstaller.p12 |
     gh secret set MACOS_CERT_INSTALLER_P12 --repo lezli01/vincent
   gh secret set MACOS_CERT_PASSWORD --repo lezli01/vincent
   base64 < AuthKey_XXXXXXXXXX.p8 |
     gh secret set MACOS_NOTARY_KEY_P8 --repo lezli01/vincent
   gh secret set MACOS_NOTARY_KEY_ID --repo lezli01/vincent
   gh secret set MACOS_NOTARY_ISSUER_ID --repo lezli01/vincent
   ```

The release job imports both `.p12` files into a **throwaway keychain** created
for that run under `$RUNNER_TEMP` and never touches the runner's login keychain;
`notarytool store-credentials` writes its profile into the same keychain. The
keychain dies with the runner.

### Certificate rotation, expiry and revocation

Developer ID certificates are valid for **five years**, and Apple issues a
limited number per team — you cannot simply mint a fresh one each time.

- **Rotation is the same four commands as the bootstrap.** Create the new
  certificate, export it, and overwrite the two `.p12` secrets. Nothing else
  changes: the workflow reads the identity name out of whatever certificate it
  imports.
- **Expiry does not invalidate what already shipped.** Every signature is made
  with `--timestamp`, so Apple's timestamp server attests that the signature
  predates the expiry, and already-notarized artifacts keep verifying forever.
  What expiry breaks is the *next* release.
- **Revoke a compromised identity at the developer portal, immediately**, and
  revoke the App Store Connect key in *Users and Access → Integrations*.
  Revocation is retroactive in a way expiry is not: macOS will refuse binaries
  signed with a revoked certificate, including ones already released. That
  means a revocation is a re-release, not just a secret rotation — cut a new
  patch version signed with the replacement identity, and say so in its notes.
- **Nothing prompts any of this.** Diary the certificate expiry dates the same
  way the GitHub tokens below are diaried, i.e. not at all by the software.

**Narrow scope is the default, not a claim that WinGet has one.** Release Please,
Homebrew, and Scoop each have a single-repository token.
`WINGET_TOKEN` is the exception: `public_repo` can change any public repository
the account can write. Prefer a publisher bot account if that blast radius is
not acceptable. Every credential is read only by the release workflow, which
runs on a `v*` tag or a manual dispatch; none is available to pull-request
workflows or to a fork. A dry run works with none of them present — the Apple
steps warn and produce unsigned artifacts — while a `v*` tag with a missing
certificate or notary key **fails**, which is what makes an unsigned stable
release impossible rather than merely unlikely.

Credentials created without expiration trade a silent standing secret for
never failing a release on a surprise expiry. Nothing will prompt their
rotation, and revoking them is manual if an account is compromised. Rotate
GitHub tokens at
[Settings → Developer settings → Fine-grained tokens](https://github.com/settings/personal-access-tokens).
Update the repository copies with:

```sh
gh secret set RELEASE_PLEASE_TOKEN --repo lezli01/vincent
gh secret set HOMEBREW_TAP_TOKEN --repo lezli01/vincent
gh secret set SCOOP_BUCKET_TOKEN --repo lezli01/vincent
gh secret set WINGET_TOKEN --repo lezli01/vincent
```

## Version numbers

Tags are `vMAJOR.MINOR.PATCH`. Release Please derives the next version from
Conventional Commits:

| Commit | Effect |
|---|---|
| `fix: ...` | patch release |
| `feat: ...` | minor release |
| `type!: ...` or a `BREAKING CHANGE:` footer | breaking release; while vincent is `0.x`, `bump-minor-pre-major` makes this a minor bump |

What may change in which release is the policy in
[CHANGELOG.md § Versioning and stability](CHANGELOG.md#versioning-and-stability).
The manifest records the last released version for Release Please. It is not a
runtime version source: GoReleaser still injects the generated tag into every
binary.

The normal Release Please path creates stable releases. If a maintainer cuts an
exceptional `vX.Y.Z-rcN` tag, GoReleaser marks it as a prerelease and
`skip_upload: auto` prevents it from moving the Homebrew, Scoop, or WinGet
metadata. The prerelease still carries its deb, rpm and macOS `.pkg` assets
on GitHub, signed and notarized like any other tag.

## Cutting a release

1. **Check `master` and the Release Please PR are green.** The
   `packaging-config` check plus all three `ci` and all three `gates` checks must
   pass. `RELEASE_PLEASE_TOKEN` is what lets the bot's pull request trigger
   those checks.

2. **Run the vulnerability sweep** if the last run predates a dependency change:

   ```sh
   go run mage.go vuln
   ```

3. **Review the open Release Please PR.** Confirm the proposed version matches
   [the pre-1.0 policy](CHANGELOG.md#versioning-and-stability), the changelog
   covers every user-visible change, and the base is `master`. Release Please
   updates the same PR as more Conventional Commits land.

   **This PR is the only point at which the changelog gets written**, and it is
   where both changelogs get written. Push two edits onto the bot's branch
   before merging:

   - **`CHANGELOG.md`** — replace Release Please's mechanical commit list for
     the new version with the user-facing context a commit subject cannot
     carry, as [the file's own preamble](CHANGELOG.md) says. Do not park that
     prose under an `## [Unreleased]` heading: Release Please inserts each new
     version directly beneath the preamble, so an `Unreleased` section is
     pushed *below* the release it describes and the released entry keeps the
     commit list. The file deliberately carries no `Unreleased` section for
     that reason. Watch for the same subject appearing twice — GitHub copies a
     PR title into the merge commit body, so a conventional PR title yields
     both the merge and its inner commit (the `PR title` workflow guards
     against this; a duplicate means one slipped through).

   - **`site-changelog.md`** — the human-facing release history published at
     `/changelog.html` and linked from the site navigation. Nothing generates
     it: Release Please writes `CHANGELOG.md` only. Add a
     `## X.Y.Z — <theme>` section with a `Released <date>.` line, deduplicated
     and product-focused, linking the PR for each entry.

4. **Dry-run the build** if anything in `.goreleaser.yaml`, the build tags or
   the archive contents changed since the last release. Actions → **Release** →
   *Run workflow*, leaving `dry_run` checked. That runs
   `release --snapshot --clean --skip=publish,sign` — real cross-compilation and
   real archives/packages/manifests, published nowhere. The `verify-packages`
   job inspects the deb/rpm payloads and the generated Scoop and WinGet metadata
   from the uploaded `dist` artifact, which is also there for a manual look.

   A dry run run **from this repository** does sign and notarize, which is the
   point: it proves the certificates and the notary key still work before a tag
   commits you to them. A dry run run **from a fork**, where every secret
   resolves to empty, instead warns and produces unsigned macOS binaries and an
   unsigned `.pkg` — and must still finish green. That is the contributor path
   and the regression test for the required/skip split; if a dry run ever needs
   a secret to complete, the split has broken.

5. **Merge the Release Please PR.** This is the release action. Release Please
   creates the tag and GitHub release; do not create or push the tag by hand.

6. **Watch both workflows.** `gh run watch`, or the Actions tab.
   - `Release Please` creates the tag and GitHub release from the merged release
     PR. The dedicated PAT makes that tag start the next workflow.
   - `Release` runs on macOS: it codesigns each darwin binary before archiving,
     runs GoReleaser, preserves Release Please's notes, builds and signs the
     checksum, notarizes the darwin binaries, builds/signs/notarizes/staples
     `vincent_*_darwin_universal.pkg` and uploads it, attests every archive,
     native package and the `.pkg`, uploads the artifacts to the existing GitHub
     release, and updates Homebrew, Scoop, and the WinGet submission for a
     stable tag. A prerelease skips all three manager publishers automatically.
   - `verify-packages` (ubuntu) inspects the deb and rpm payloads and the
     generated Scoop and WinGet metadata with the native tools.
   - `smoke` (one job per OS) downloads the **real published archive**, unpacks
     it, asserts `vincent version` reports the tag rather than `dev`, runs
     `vincent workflow validate` and checks that `vincent task ls` exits 2 with
     no daemon running, then verifies the archive against `checksums.txt`. The
     macOS leg additionally writes a synthetic `com.apple.quarantine` attribute
     and asserts the binary still passes `codesign --verify` and
     `spctl --assess`.

   A red `smoke` job means the release is published but the artifact is
   defective — treat it as an incident, not a flake, and go to
   [If a release goes wrong](#if-a-release-goes-wrong).

7. **Verify as a user would**, from a clean directory, following the steps the
   release footer prints:

   ```sh
   gh release download vX.Y.Z
   cosign verify-blob checksums.txt \
     --certificate checksums.txt.pem \
     --signature checksums.txt.sig \
     --certificate-identity-regexp 'https://github.com/lezli01/vincent/.*' \
     --certificate-oidc-issuer https://token.actions.githubusercontent.com
   sha256sum -c checksums.txt --ignore-missing
   gh attestation verify vincent_X.Y.Z_linux_amd64.tar.gz --repo lezli01/vincent
   gh attestation verify vincent_X.Y.Z_amd64.deb --repo lezli01/vincent
   ```

   On a Mac, also check what Gatekeeper will check. The `.pkg` is the only
   asset outside `checksums.txt`, so it is verified from Apple's signature and
   its attestation instead:

   ```sh
   tar -xzf vincent_X.Y.Z_darwin_arm64.tar.gz
   codesign --verify --strict --verbose=2 vincent
   spctl --assess --type execute -vv vincent            # accepted, Notarized Developer ID

   pkgutil --check-signature vincent_X.Y.Z_darwin_universal.pkg
   spctl --assess --type install -vv vincent_X.Y.Z_darwin_universal.pkg
   xcrun stapler validate vincent_X.Y.Z_darwin_universal.pkg
   gh attestation verify vincent_X.Y.Z_darwin_universal.pkg --repo lezli01/vincent
   ```

8. **Check the Homebrew cask** (skip for a pre-release, which does not publish
   one). Confirm the tap got the new version and that it installs:

   ```sh
   git -C "$(brew --repo lezli01/tap)" pull    # or: brew update
   brew info lezli01/tap/vincent               # version must be the tag
   brew reinstall lezli01/tap/vincent
   vincent version
   spctl --assess --type execute -vv "$(readlink -f "$(brew --prefix)/bin/vincent")"
   ```

   The cask no longer carries a quarantine-stripping `postflight` hook — task
   032 removed it — so what proves the install is Gatekeeper accepting the
   binary, not the absence of an attribute. `com.apple.quarantine` being present
   is fine and expected now; `spctl` reporting anything but `accepted` with
   `source=Notarized Developer ID` is the failure.

9. **Check the Windows and Linux channels** (skip for a pre-release).

   ```sh
   # Scoop: the bucket should carry the tag and both Windows architectures.
   scoop bucket add vincent https://github.com/lezli01/scoop-bucket
   scoop update
   scoop info vincent/vincent

   # WinGet: this can lag while Microsoft's catalog PR is reviewed.
   winget show --id lezli01.Vincent --exact --versions
   ```

   Install the deb and rpm on representative clean systems with `apt install
   ./vincent_*_amd64.deb` and `dnf install ./vincent-*.x86_64.rpm`; run
   `vincent version` after each. Confirm the GoReleaser log contains the WinGet
   pull-request URL—cross-repository PR creation errors can be non-fatal, so a
   green release job alone is not that proof.

10. **Read the published notes.** Release Please generates them from the
   Conventional Commits since the last tag and GoReleaser keeps them unchanged
   while attaching artifacts. If a user-visible change is missing, fix the
   commit convention before merging the release PR or edit the notes after the
   release.

## If a release goes wrong

- **Caught within minutes, nobody has downloaded it:** delete the release and
  the tag (`gh release delete vX.Y.Z --cleanup-tag`), fix forward, and let
  Release Please propose the corrected version. This is defensible only before
  the tag has propagated — Go module proxies cache tags aggressively and
  `go install …@vX.Y.Z` may already have it.
- **Otherwise: never re-point a published tag.** Ship the fix as the next patch
  and mark the bad release as such in its notes. A moved tag silently
  invalidates every checksum, cosign signature and attestation a user already
  verified against it.

## Automation boundary

- **Release Please owns release metadata.** It maintains the release PR and
  `CHANGELOG.md`, records its last version in `.release-please-manifest.json`,
  and creates the tag and GitHub release. The tag remains the only version
  injected into the binary; the manifest is automation state, not product state.
- **`site-changelog.md` is maintained by hand.** The GitHub Pages changelog at
  `/changelog.html` is a separate, edited-for-humans file at the repository
  root; no workflow derives it from `CHANGELOG.md` or from the tags. It is
  updated in the Release Please PR (step 3) or it does not get updated at all.
- **GoReleaser owns distribution channels.** It attaches archives, signatures,
  checksums, deb/rpm packages and attestations to Release Please's existing
  GitHub release, then updates Homebrew and Scoop and prepares the WinGet
  catalog PR for stable releases. mise consumes the GitHub archives directly
  and needs no publisher. Keeping artifact and manifest generation in one run
  prevents channel checksums from drifting from the release they name. The one
  asset it does **not** produce is `vincent_*_darwin_universal.pkg`: a universal
  binary needs both darwin slices, which do not both exist until the build is
  over, so `scripts/macos-pkg.sh` builds it afterwards and `gh release upload`
  attaches it. That is also why it is absent from `checksums.txt` — its
  integrity comes from Apple's installer signature and a build attestation.
- **OS code signing is macOS-only, deliberately.** Apple Developer ID signing
  and notarization are paid for and applied to every darwin artifact, including
  a stapled universal `.pkg` — §19 †'s 2026-08-26 amendment records reversing
  the Apple half of the original descope, and task 032 carries the reasoning.
  **Windows Authenticode stays descoped**: an OV certificate on a hardware token
  is a recurring purchase with no equivalent to Apple's single notary service,
  so the README and `docs/platforms/windows.md` continue to document the
  SmartScreen prompt users will meet. cosign signatures and build provenance are
  unchanged on every platform and are not a substitute for either.
- **External catalogs remain external.** Scoop updates a repository the
  maintainer controls. WinGet is a pull request into Microsoft's catalog and
  can remain pending after the GitHub release succeeds. The accepted
  maintenance and credential trade-offs are recorded in
  [docs/tasks/021](docs/tasks/021-package-distribution-channels.md), which
  explicitly supersedes task 002's Windows rejection.
