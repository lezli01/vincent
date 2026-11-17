# 147 — `vincent update --master`: the newest master build

**Status:** ✅ done (1/1) · **Opened:** 2026-11-17

The author asked for a command-line option that upgrades a local install to
the latest release, another that upgrades it to the latest master build, and
for that build to be made on master every time. The first already existed —
`vincent update` (task 055) — so this task adds the second and the build it
installs.

Conventions for this file are in [the tasks README](README.md).

## What this changes

- **Task 055's `vincent update`** — **extended** by `--master`. Its
  verification chain, its refusal to touch a package-managed install and
  its exit codes are kept unchanged, and apply to a master build as they do
  to a release.
- **The release pipeline** — **unchanged**. A new workflow,
  `.github/workflows/master-build.yml`, runs the same goreleaser
  configuration as a snapshot on every push to `master`.

## Tasks

- [x] **147.1** The master build and `--master`: the workflow, the
  snapshot version template, `release.Client.Master` and `MasterTag`,
  `selfupdate.Updater.ApplyFrom`, the flag with its check, dry-run, JSON and
  package-manager answers, the tests, the §12.1 amendment and the guides.
  ✓ 2026-11-17

## Decisions

Taken 2026-11-17 by the implementer, on the author's request.

1. **A rolling prerelease, replaced on every push.** CI publishes the build
   as the GitHub release tagged `master-latest`: a prerelease, never marked
   latest, deleted and recreated at the new commit on each push to `master`.
   It carries the six archives, `checksums.txt` and its cosign signature and
   certificate — not the deb, rpm or package-manager manifests, which only
   a release feeds. *Beaten:* workflow artifacts, which need a token to
   download and expire; and a release per commit, which would bury the
   releases page.
2. **The tag is not `master`.** A tag named like the branch makes `master`
   an ambiguous ref for every clone. `master-latest` matches neither the
   `v*` triggers of the release and pages workflows nor sitever's semver
   filter, and `releases/latest` excludes prereleases, so the stable check
   never sees it.
3. **Verified as a release is.** The workflow signs `checksums.txt` with the
   same keyless `cosign sign-blob` invocation `.goreleaser.yaml` uses; the
   certificate identity is this repository's, which `selfupdate` already
   pins. `vincent update --master` runs the release's chain unchanged from
   the other tag (`ApplyFrom`).
4. **The version is a semantic version that sorts honestly.** It is the
   next patch release, the commits since the last release and the commit:
   `0.21.1-master.5.gabc1234` (the `g` keeps an all-digit hash from reading
   as a number with a leading zero). It sorts above the release it follows
   and below the one it precedes, so a plain `vincent update` and the
   daemon's background check offer a master install the first release after
   it, and taking that returns it to releases. The client reads the version
   off the archive names, so no other metadata has to be published.
5. **With `--master`, any other build is an update.** Two master builds are
   ordered only by the branch, and the flag is the explicit choice, so the
   newest master build replaces anything that is not it — a release, a `dev`
   build, an older master build.
6. **A package-managed install is never moved to master.** Its manager
   carries releases only and would revert the swap; `--master` there prints
   the master build's page and exits 2, and prints no upgrade command.
7. **Master pushes queue, pull requests cancel.** A run cancelled between
   deleting the release and creating it would leave no master build, so
   master runs are not cancelled; a pull request that touches the workflow
   or `.goreleaser.yaml` builds and checks the archives without signing or
   publishing.
