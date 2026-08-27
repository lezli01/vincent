# 007 — Release Please automation

**Status:** done (6/6) · **Opened:** 2026-08-16 · **Completed:** 2026-08-16

Release Please will own the version pull request, `vMAJOR.MINOR.PATCH` tag, and
GitHub release. The existing GoReleaser workflow remains the only artifact
builder and publisher, including the Homebrew cask in `lezli01/homebrew-tap`.

Conventions for this file are in [the tasks README](README.md).

## Decisions (2026-08-16)

- **Release Please orchestrates; GoReleaser distributes.** Replacing
  GoReleaser would discard the six-platform build, checksum, keyless signature,
  provenance, smoke tests, and Homebrew cask already proven by task 002.
  Release Please therefore creates the release metadata and its tag triggers
  the existing packaging workflow.

- **A dedicated PAT is required.** GitHub suppresses workflow events created
  with `GITHUB_TOKEN`. `RELEASE_PLEASE_TOKEN` is scoped to this repository and
  allows the generated release PR to run normal CI and the generated tag to
  trigger `.github/workflows/release.yml`. It is separate from
  `HOMEBREW_TAP_TOKEN`, whose only repository is the tap.

- **Stable and prerelease channels remain distinct.** GoReleaser's
  `homebrew_casks.skip_upload: auto` continues to update the tap for stable
  tags and skip it for prerelease tags. Release Please's existing-release notes
  are preserved with `release.mode: keep-existing`; only assets are replaced on
  a rerun.

- **The tag remains the binary version source.** The manifest records the last
  version for Release Please, but released binaries still receive the tag
  through GoReleaser ldflags. No runtime version file is introduced.

## Tasks

- [x] **007.1 — Maintenance branch and agent memory.** Add root `AGENTS.md` and
  keep the work isolated on `codex/project-maintenance`. ✓ 2026-08-16
- [x] **007.2 — Release Please configuration.** Add the manifest, Go release
  strategy, and pinned GitHub Actions workflow. ✓ 2026-08-16
- [x] **007.3 — Distribution handoff.** Preserve the tag-triggered GoReleaser
  pipeline, existing GitHub release notes, rerunnable assets, and Homebrew tap.
  ✓ 2026-08-16
- [x] **007.4 — Maintainer documentation.** Document tokens, release PR review,
  dry runs, release verification, and stable/prerelease behavior. ✓ 2026-08-16
- [x] **007.5 — Verification.** Parse both JSON files, lint the GitHub Actions
  YAML, run `goreleaser check`, validate the checked-in vincent workflows, and
  run the repository linter. ✓ 2026-08-16
- [x] **007.6 — Repository credential.** Create a fine-grained PAT scoped only
  to `lezli01/vincent` with Contents, Issues, and Pull requests read/write, then
  save it as `RELEASE_PLEASE_TOKEN`. ✓ 2026-08-16

## Verification (2026-08-16)

- `jq -e . release-please-config.json .release-please-manifest.json`
- `actionlint .github/workflows/release-please.yml .github/workflows/release.yml`
- `goreleaser check` with GoReleaser v2.17.1
- `vincent workflow validate` for `release` and `prepare-release`: zero warnings
- `go run mage.go lint`: zero issues
- GitHub settings: both `RELEASE_PLEASE_TOKEN` and `HOMEBREW_TAP_TOKEN` exist;
  `lezli01/homebrew-tap` is public and reachable on `main`
