# Releasing vincent

Releases are **Release Please-driven**. Conventional Commits on `master` keep a
release pull request current. Merging that pull request makes Release Please
create the `vMAJOR.MINOR.PATCH` tag and GitHub release; that tag triggers
[`.github/workflows/release.yml`](.github/workflows/release.yml), which uses
[`.goreleaser.yaml`](.goreleaser.yaml) to cross-compile, checksum, sign, attest,
upload, smoke-test on all three OSes, build deb/rpm packages, update the stable
Homebrew and Scoop metadata, and submit the stable WinGet manifest. There
is no manual tag or upload step, and no artifact is built on a maintainer's
machine.

Only a maintainer with push access can cut a release.

## Channel bootstrap and repository secrets

Before enabling a stable release, both external destinations must exist:

- public `lezli01/scoop-bucket`, with `main` as its default branch;
- a `lezli01/winget-pkgs` fork of `microsoft/winget-pkgs`.

A GoReleaser snapshot renders both manifests without contacting those
destinations. That proves the content, not publication. Do not merge a release
change that names a missing destination: the next stable tag would discover it
after the GitHub release already exists.

Four secrets are required.

| Secret | Scope and permissions | Why it exists |
|---|---|---|
| `RELEASE_PLEASE_TOKEN` | Fine-grained PAT selecting only `lezli01/vincent`, with **Contents**, **Issues**, and **Pull requests: read and write** | Events created by `GITHUB_TOKEN` do not start other workflows. The PAT lets the release PR run normal CI and lets the generated tag trigger the GoReleaser workflow. |
| `HOMEBREW_TAP_TOKEN` | Fine-grained PAT selecting only [`lezli01/homebrew-tap`](https://github.com/lezli01/homebrew-tap), with **Contents: read and write** | `GITHUB_TOKEN` and the Release Please PAT are scoped to this repository; pushing the cask is a cross-repo write. |
| `SCOOP_BUCKET_TOKEN` | Fine-grained PAT selecting only `lezli01/scoop-bucket`, with **Contents: read and write** | Push the stable `vincent.json` manifest to the bucket root. |
| `WINGET_TOKEN` | Dedicated classic PAT with **`public_repo`** | Push a version branch to `lezli01/winget-pkgs` and open its pull request against `microsoft/winget-pkgs`. GitHub cannot express that cross-owner PR as a fine-grained token scoped only to the fork. |

Nothing else needs a secret: cosign signs keylessly with the packaging
workflow's OIDC identity.

**Narrow scope is the default, not a claim that WinGet has one.** Release Please,
Homebrew, and Scoop each have a single-repository token.
`WINGET_TOKEN` is the exception: `public_repo` can change any public repository
the account can write. Prefer a publisher bot account if that blast radius is
not acceptable. Every credential is passed to the pinned GoReleaser action only
for the tag-driven release job; none is available to pull-request workflows.

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
metadata. The prerelease still carries its deb and rpm assets on GitHub.

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
   real archives/packages/manifests, published nowhere. The job inspects the
   deb/rpm payloads and generated Scoop and WinGet metadata before it
   uploads the `dist` artifact for a manual look.

5. **Merge the Release Please PR.** This is the release action. Release Please
   creates the tag and GitHub release; do not create or push the tag by hand.

6. **Watch both workflows.** `gh run watch`, or the Actions tab.
   - `Release Please` creates the tag and GitHub release from the merged release
     PR. The dedicated PAT makes that tag start the next workflow.
   - `Release` runs GoReleaser, preserves Release Please's notes, builds and
     signs the checksum, inspects deb/rpm payloads, attests every archive and
     native package, uploads the artifacts to the existing GitHub release, and
     updates Homebrew, Scoop, and the WinGet submission for a stable tag.
     A prerelease skips all three manager publishers automatically.
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
   gh attestation verify vincent_X.Y.Z_amd64.deb --repo lezli01/vincent
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
  prevents channel checksums from drifting from the release they name.
- **OS code signing.** Authenticode and Apple notarization are recurring
  certificate costs the project does not currently take on — recorded as a
  descope in spec §19.
  Releases carry cosign signatures and build provenance instead, and the README
  documents the SmartScreen and Gatekeeper prompts users will meet.
- **External catalogs remain external.** Scoop updates a repository the
  maintainer controls. WinGet is a pull request into Microsoft's catalog and
  can remain pending after the GitHub release succeeds. The accepted
  maintenance and credential trade-offs are recorded in
  [docs/tasks/021](docs/tasks/021-package-distribution-channels.md), which
  explicitly supersedes task 002's Windows rejection.
