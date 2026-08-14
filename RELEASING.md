# Releasing vincent

The release is **tag-driven**: pushing a `v*` tag to this repository is the
whole trigger. Everything else — cross-compiling six archives, checksumming,
signing, attesting, publishing the GitHub release, and smoke-testing the result
on all three OSes — is [`.github/workflows/release.yml`](.github/workflows/release.yml)
plus [`.goreleaser.yaml`](.goreleaser.yaml). There is no manual upload step, and
no artifact is ever built on a maintainer's machine.

Only a maintainer with push access can cut a release.

## Repository secrets

One secret is required, and a release fails at the publish step without it.

| Secret | What it is | Why not `GITHUB_TOKEN` |
|---|---|---|
| `HOMEBREW_TAP_TOKEN` | Fine-grained PAT, **Contents: read and write**, with [`lezli01/homebrew-tap`](https://github.com/lezli01/homebrew-tap) as its *only* selected repository | `GITHUB_TOKEN` is scoped to this repository; pushing the cask is a cross-repo write |

Nothing else needs a secret: cosign signs keylessly with the workflow's own OIDC
identity, and the release itself is published with `GITHUB_TOKEN`.

**The single-repository scope is the point.** This token is handed to a
third-party action (`goreleaser-action`) on every release, so its blast radius
is whatever it can reach. Scoped to the tap, the worst case is a bad cask —
recoverable by force-pushing the tap. A **classic** PAT cannot express that:
its `repo` scope covers every repository the account owns, including this one,
which would let a compromised release job rewrite vincent's own source. Use a
fine-grained token with one repository selected.

The token does not expire, which trades a silent standing credential for never
failing a release on a surprise expiry. Two consequences worth knowing: nothing
will prompt a rotation, and revoking it is a manual step if the account is ever
compromised. Rotate at
[Settings → Developer settings → Fine-grained tokens](https://github.com/settings/personal-access-tokens);
`gh secret set HOMEBREW_TAP_TOKEN --repo lezli01/vincent` updates the secret.

## Version numbers

Tags are `vMAJOR.MINOR.PATCH`, optionally with a pre-release suffix:

| Tag | Effect |
|---|---|
| `v0.2.0` | normal release |
| `v0.1.1` | patch release |
| `v0.2.0-rc1` | marked **pre-release** automatically (goreleaser `prerelease: auto` keys off the hyphen) |

What may change in which release is the policy in
[CHANGELOG.md § Versioning and stability](CHANGELOG.md#versioning-and-stability).
Read it before choosing the number — while vincent is `0.x`, a breaking change
means a minor bump, not a major one.

## Cutting a release

1. **Check `master` is green.** All three `ci` and all three `gates` checks, on
   the exact commit you intend to tag. A release tag on a red commit produces
   signed, attested, broken binaries.

2. **Run the vulnerability sweep** if the last run predates a dependency change:

   ```sh
   go run mage.go vuln
   ```

3. **Update `CHANGELOG.md` in a PR.** Rename `## [Unreleased]` to
   `## [X.Y.Z] — YYYY-MM-DD`, open a fresh empty `## [Unreleased]` above it, and
   fix the two link definitions at the bottom of the file:

   ```
   [Unreleased]: https://github.com/lezli01/vincent/compare/vX.Y.Z...HEAD
   [X.Y.Z]: https://github.com/lezli01/vincent/releases/tag/vX.Y.Z
   ```

   Merge it. This is the last commit before the tag, so the tag points at a
   changelog that describes the release it names.

4. **Dry-run the build** if anything in `.goreleaser.yaml`, the build tags or
   the archive contents changed since the last release. Actions → **Release** →
   *Run workflow*, leaving `dry_run` checked. That runs
   `release --snapshot --clean --skip=publish,sign` — real cross-compilation and
   real archives, published nowhere. Download the `dist` artifact and look at it.

5. **Tag and push.**

   ```sh
   git checkout master && git pull
   git tag -a vX.Y.Z -m "vX.Y.Z"
   git push origin vX.Y.Z
   ```

6. **Watch the workflow.** `gh run watch`, or the Actions tab. Two stages:
   - `release` builds, signs the checksum file with keyless cosign, attests
     build provenance for every archive, publishes the GitHub release, and
     pushes the updated Homebrew cask to `lezli01/homebrew-tap`. The cask push
     is **skipped automatically for a pre-release** (`skip_upload: auto`), so an
     `-rc` tag does not move what `brew install` gives people.
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

9. **Read the published notes.** They are generated from the commits since the
   last tag, grouped into Features / Bug fixes / Other changes, with `docs:`,
   `test:`, `ci:`, `chore:` and merge commits filtered out. If a user-visible
   change landed under a filtered type, that is a commit-message problem worth
   fixing in the notes by hand.

## If a release goes wrong

- **Caught within minutes, nobody has downloaded it:** delete the release and
  the tag (`gh release delete vX.Y.Z --cleanup-tag`), fix, re-tag. Only
  defensible before the tag has propagated — Go module proxies cache tags
  aggressively and `go install …@vX.Y.Z` may already have it.
- **Otherwise: never re-point a published tag.** Ship the fix as the next patch
  and mark the bad release as such in its notes. A moved tag silently
  invalidates every checksum, cosign signature and attestation a user already
  verified against it.

## What is deliberately not automated

- **Version bumping and changelog generation.** No release-please or equivalent:
  the tag is the only version input, `vincent version` reads it from ldflags at
  build time, and no file in the repo records a version number to keep in sync.
  A bot maintaining a version file would add a source of truth this project does
  not have.
- **OS code signing.** Authenticode and Apple notarization are recurring
  certificate costs v0 does not take on — recorded as a descope in spec §19.
  Releases carry cosign signatures and build provenance instead, and the README
  documents the SmartScreen and Gatekeeper prompts users will meet.
- **Scoop and winget publishing.** The Homebrew cask *is* automated (see step 6),
  because it deletes the `xattr -d com.apple.quarantine` step for macOS users.
  No Windows packager erases the SmartScreen prompt the same way, so Windows
  keeps `go install` and the release archives — the reasoning is in
  [docs/tasks/002](docs/tasks/002-homebrew-tap.md).
