# Releasing vincent

Releases are **Release Please-driven**. Conventional Commits on `master` keep a
release pull request current. Merging that pull request makes Release Please
create the `vMAJOR.MINOR.PATCH` tag and GitHub release; that tag triggers
[`.github/workflows/release.yml`](.github/workflows/release.yml), which uses
[`.goreleaser.yaml`](.goreleaser.yaml) to cross-compile, checksum, sign, attest,
upload, smoke-test on all three OSes, and update the stable Homebrew cask. There
is no manual tag or upload step, and no artifact is built on a maintainer's
machine.

Only a maintainer with push access can cut a release.

## Repository secrets

Two narrowly scoped secrets are required.

| Secret | Scope and permissions | Why it exists |
|---|---|---|
| `RELEASE_PLEASE_TOKEN` | Fine-grained PAT selecting only `lezli01/vincent`, with **Contents**, **Issues**, and **Pull requests: read and write** | Events created by `GITHUB_TOKEN` do not start other workflows. The PAT lets the release PR run normal CI and lets the generated tag trigger the GoReleaser workflow. |
| `HOMEBREW_TAP_TOKEN` | Fine-grained PAT selecting only [`lezli01/homebrew-tap`](https://github.com/lezli01/homebrew-tap), with **Contents: read and write** | `GITHUB_TOKEN` and the Release Please PAT are scoped to this repository; pushing the cask is a cross-repo write. |

Nothing else needs a secret: cosign signs keylessly with the packaging
workflow's OIDC identity.

**The single-repository scope of each token is the point.** Each token is handed
to one pinned third-party action, so its blast radius is whatever it can reach.
The two actions do not share credentials: Release Please can change vincent but
not the tap; GoReleaser can change the tap but receives only this workflow's
short-lived `GITHUB_TOKEN` for vincent itself. A classic PAT cannot express that
separation because its `repo` scope covers every repository the account owns.

The token does not expire, which trades a silent standing credential for never
failing a release on a surprise expiry. Two consequences worth knowing: nothing
will prompt a rotation, and revoking it is a manual step if the account is ever
compromised. Rotate at
[Settings → Developer settings → Fine-grained tokens](https://github.com/settings/personal-access-tokens).
Update the repository copies with:

```sh
gh secret set RELEASE_PLEASE_TOKEN --repo lezli01/vincent
gh secret set HOMEBREW_TAP_TOKEN --repo lezli01/vincent
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
`homebrew_casks.skip_upload: auto` prevents it from moving the stable cask.

## Cutting a release

1. **Check `master` and the Release Please PR are green.** All three `ci` and all
   three `gates` checks must pass. `RELEASE_PLEASE_TOKEN` is what lets the bot's
   pull request trigger those checks.

2. **Run the vulnerability sweep** if the last run predates a dependency change:

   ```sh
   go run mage.go vuln
   ```

3. **Review the open Release Please PR.** Confirm the proposed version matches
   [the pre-1.0 policy](CHANGELOG.md#versioning-and-stability), the changelog
   covers every user-visible change, and the base is `master`. Release Please
   updates the same PR as more Conventional Commits land.

4. **Dry-run the build** if anything in `.goreleaser.yaml`, the build tags or
   the archive contents changed since the last release. Actions → **Release** →
   *Run workflow*, leaving `dry_run` checked. That runs
   `release --snapshot --clean --skip=publish,sign` — real cross-compilation and
   real archives, published nowhere. Download the `dist` artifact and look at it.

5. **Merge the Release Please PR.** This is the release action. Release Please
   creates the tag and GitHub release; do not create or push the tag by hand.

6. **Watch both workflows.** `gh run watch`, or the Actions tab.
   - `Release Please` creates the tag and GitHub release from the merged release
     PR. The dedicated PAT makes that tag start the next workflow.
   - `Release` runs GoReleaser, preserves Release Please's notes, builds and
     signs every archive, attests provenance, uploads the artifacts to the
     existing GitHub release, and pushes the updated stable cask to
     `lezli01/homebrew-tap`. A prerelease tag skips the cask automatically.
   - `smoke` (one job per OS) downloads the **real published archive**, unpacks
     it, asserts `vincent version` reports the tag rather than `dev`, runs
     `vincent workflow validate` and checks that `vincent task ls` exits 2 with
     no daemon running, then verifies the archive against `checksums.txt`.

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
   ```

8. **Check the Homebrew cask** (skip for a pre-release, which does not publish
   one). Confirm the tap got the new version and that it installs:

   ```sh
   git -C "$(brew --repo lezli01/tap)" pull    # or: brew update
   brew info lezli01/tap/vincent               # version must be the tag
   brew reinstall lezli01/tap/vincent
   vincent version
   xattr -l "$(readlink -f "$(brew --prefix)/bin/vincent")"   # no com.apple.quarantine
   ```

   `com.apple.provenance` is expected and harmless; `com.apple.quarantine` is
   not, and means the cask's `postflight` hook did not run.

9. **Read the published notes.** Release Please generates them from the
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
  changelog, records its last version in `.release-please-manifest.json`, and
  creates the tag and GitHub release. The tag remains the only version injected
  into the binary; the manifest is automation state, not product state.
- **GoReleaser owns distribution channels.** It attaches archives, signatures,
  checksums and attestations to Release Please's existing GitHub release, then
  updates the Homebrew tap for stable releases. Keeping this in one run means a
  failed cask update fails the release workflow instead of silently leaving the
  install channel stale.
- **OS code signing.** Authenticode and Apple notarization are recurring
  certificate costs v0 does not take on — recorded as a descope in spec §19.
  Releases carry cosign signatures and build provenance instead, and the README
  documents the SmartScreen and Gatekeeper prompts users will meet.
- **Scoop and winget publishing.** The Homebrew cask *is* automated (see step 6),
  because it deletes the `xattr -d com.apple.quarantine` step for macOS users.
  No Windows packager erases the SmartScreen prompt the same way, so Windows
  keeps `go install` and the release archives — the reasoning is in
  [docs/tasks/002](docs/tasks/002-homebrew-tap.md).
