# 139 — A published privacy policy, and `PrivacyUrl` in the WinGet manifest

**Status:** 🔄 in progress (3/4) · **Opened:** 2026-10-30

Issue [#880](https://github.com/lezli01/vincent/issues/880). Microsoft's
WinGet moderators blocked `lezli01.Vincent` v0.8.0 under policy 1.5.1
([microsoft/winget-pkgs#429585](https://github.com/microsoft/winget-pkgs/pull/429585)):
a package must link a privacy policy. vincent had none, and the `winget:`
block in `.goreleaser.yaml` set no `privacy_url`. That block is what keeps
[021.7](021-package-distribution-channels.md) open on the WinGet channel.

This is a documentation and packaging change. vincent's behaviour does not
change — nothing about storage, retention, redaction or network calls — so
the spec gets no amendment: the policy describes vincent as it already works,
and §12.2, §12.3, §14 and §16 remain the normative statements of it.

Conventions for this file are in [the tasks README](README.md).

## Decisions (2026-10-30)

1. **One stable URL: `https://lezli01.is-a.dev/vincent/latest/privacy.html`.**
   The site is served from the custom origin (`cmd/sitever`'s `--url`
   default) with `.html` pages, and its root is `latest` (task 131). The page
   is `docs/privacy.md` with `permalink: /privacy.html`, the same way
   `site-contributing.md` becomes `/contributing.html`, so the URL survives a
   reorganisation of `docs/`. The URL is written verbatim in `privacy_url`
   and in the release workflow's assertion; in-repository links stay
   relative, so each versioned snapshot links its own copy.
2. **Two contact channels, no email.** Public questions go to GitHub issues;
   anything sensitive goes to the private security-advisory form SECURITY.md
   already uses. The publisher named is László Szabó, as in the WinGet
   manifest.
3. **The page states facts traced to source, not the issue's list.** Every
   claim — the retention default, the file modes, the two standing calls and
   their switches, the probe behaviour — is checked against the code as it is
   when the page is written. When one of those changes, the page changes in
   the same pull request, like any other `docs/` page.
4. **A missing `PrivacyUrl` fails the release build, not a moderator.** The
   `verify-packages` job asserts the exact line in the rendered default-locale
   manifest beside the existing `License: MIT` assertion.
5. **The pull request refs #880 and does not close it.** The issue closes once
   a WinGet pull request carrying `PrivacyUrl` is accepted, or at least no
   longer blocked on 1.5.1 — that is 139.4, which needs a stable release
   containing the page.

## Tasks

- [x] **139.1 — The policy page.** `docs/privacy.md`, permalink
  `/privacy.html`, with a "Last updated" line: what is stored locally and
  why, what is passed to agent CLIs (including the daemon's own probes),
  retention and deletion, how tokens and config are protected (and that
  nothing is redacted), telemetry and network calls, contact. ✓ 2026-10-30
- [x] **139.2 — `privacy_url` and its release check.** `.goreleaser.yaml`'s
  `winget:` block sets `privacy_url`; `.github/workflows/release.yml`'s
  `verify-packages` greps the rendered locale manifest for the exact
  `PrivacyUrl` line. ✓ 2026-10-30
- [x] **139.3 — Links and SEO.** Linked from `README.md`, `docs/README.md`,
  `docs/security-model.md` and `docs/faq.md`; `_data/seo_pages.json` carries
  its title and description. ✓ 2026-10-30
- [ ] **139.4 — Resubmit to WinGet.** After the first stable release that
  contains the page, confirm the release run's WinGet pull request carries
  `PrivacyUrl`, and reference #429585 and the moderator's comment in its
  description by hand — GoReleaser's generated body cannot. Record the outcome
  here and under 021.7, then close #880.

  **Known caveat.** The page resolves under `/latest/` only once the Pages
  workflow has built the tag that contains it, and GoReleaser opens the WinGet
  pull request during that same release run. If WinGet validation fetches the
  URL before Pages has finished, the first validation may fail; re-running it
  fixes that.
