# 131 — A versioned documentation site: latest release by default

**Status:** ⚠ verification blocked (5/6)
**Opened:** 2026-10-04
**Issue:** [#712](https://github.com/lezli01/vincent/issues/712)
**Spec:** none — the published site, not the product

## Problem

The classic GitHub Pages branch build published `master` at `/vincent/`. A
reader of the site therefore read documentation for features no release had
shipped yet, and a reader on an older release had no way to read the
documentation that matched it.

## What shipped

A GitHub Actions Pages workflow (`.github/workflows/pages.yml`) builds one
full Jekyll snapshot per release tag (`v0.6.0` through the newest), one for
`master` (`dev`) and one for the newest tag again as `latest`, and assembles
them into a single Pages artifact:

```
/vincent/                       → redirect to /vincent/latest/
/vincent/latest/…               newest release tag, built with baseurl /vincent/latest
/vincent/vX.Y.Z/…               every release tag ≥ v0.6.0 (prerelease tags skipped)
/vincent/dev/…                  master
/vincent/<old path>             redirect stub → same path under /latest/
/vincent/sitemap.xml            latest's sitemap, the only one published
/vincent/robots.txt, 404.html   latest's copies
```

`scripts/site/` holds the deploy-owned builder (a pinned `github-pages`
Gemfile and lock, and `build.sh`, which builds one ref under one baseurl).
`cmd/sitever` lists the published versions and assembles the built trees.
`ci.yml`'s `site` job builds every pull request's ref with the same builder
and assembles it as `dev`.

Task 120 decision 1 (the search index is built in Liquid, because Pages runs
only allowlisted plugins) still holds: each version builds its own
`search.json` under its own baseurl, so the overlay searches only that version
with no change to `assets/js/search.js`. Tags older than task 120 (before
`v0.10.0`) have no search, and stay that way.

## Decisions

### 1. Tags are built unedited with the Pages builder (2026-10-04)

Old tags predate an explicit `plugins:` list and rely on GitHub Pages' default
plugin set. Each ref is therefore built with the `github-pages` gem, pinned in
`scripts/site/Gemfile` (which the deploy owns, never the tagged source). The
baseurl is overridden per version with a deploy-owned config overlay
(`jekyll build --config _config.yml,<overlay>`), written by `build.sh` outside
the source tree. Tagged files are never changed. Tag builds are cached in
Actions, keyed by every tag's commit plus the builder (`Gemfile.lock` and
`build.sh`), so a master push only rebuilds `dev` and `latest`.
`scripts/site/` is excluded in `_config.yml`, so `dev` does not publish it.

*Amended 2026-10-04:* `build.sh` runs Jekyll from inside the source tree.
Jekyll 3 prefers a `_layouts/` in the working directory over the source's,
then fails to open it (only a warning), so the first deploy built every tag
and `latest` from the workflow's master checkout with no layout at all —
bare fragments, no stylesheet, only the selector `sitever` adds. `build.sh`
now also refuses a build whose `index.html` has no `</head>`.

*Amended 2026-10-04, again:* `build.sh` runs Jekyll from
`scripts/site/`, not from the source tree. Jekyll 3.10 requires the
Gemfile's `jekyll_plugins` group only when `./Gemfile` exists in the
working directory — `BUNDLE_GEMFILE` does not count — so from either the
master checkout or the source tree the pinned `github-pages` gem never
loaded, and with it went the configuration GitHub Pages layers over
`_config.yml`, default plugins included. `v0.7.0` on list their plugins and
built anyway; `v0.6.0` lists none, relied on `jekyll-readme-index` for its
`index.html`, and failed the `</head>` check (runs 37215258362,
37219017443). `scripts/site/` has no `_layouts/`, so the layout fault above
stays fixed.

### 2. Version-specific chrome is added after Jekyll, by a Go program (2026-10-04)

`cmd/sitever` takes the built trees and:

- injects the version badge and selector into every HTML page. The selector
  lists `latest (vX.Y.Z)`, `dev`, then every tag newest first, computed at
  assembly time from every version's file list. Each entry links to the same
  path in that version if it exists there, and otherwise to that version's
  homepage. Static links, no runtime fetch.
- injects the banner on old tags ("You are viewing docs for vX.Y.Z — see the
  latest release") and on `dev` ("Unreleased docs — may describe features not
  in any release yet"), both linking to the same page under `/latest/`, or to
  the `/latest/` homepage when that path no longer exists.
- on every non-latest page, rewrites `<link rel="canonical">`, `og:url` and
  `twitter:url` to the `/latest/` equivalent (falling back to the `/latest/`
  homepage), rewrites `og:image`/`twitter:image` to `/latest/` assets, and sets
  `<meta name="robots">` to `noindex`. A missing tag is inserted rather than
  assumed: old layouts differ.
- drops the per-version `sitemap.xml` from every tree and publishes latest's
  at `/vincent/sitemap.xml`, pointing every page's sitemap link and latest's
  `robots.txt` at it.
- writes the redirect stubs: `/vincent/index.html` → `/latest/`, and one
  meta-refresh + JS stub (keeping `#hash`) per HTML path in latest, plus any
  path only `dev` has, which redirects to its `/dev/` equivalent. Each stub
  carries a canonical to its target. Latest's non-HTML `docs/assets/**` is
  also copied to the root, because meta refresh can't redirect images and
  already shared social-card URLs point there.

Markup injection anchors on `<head>`/`<body>` and the `content-card` main
element where present, so it degrades on older layouts instead of failing. The
badge, selector and banner styles ship inline with the injected markup, so old
tags' stylesheets need no change.

### 3. The newest tag also exists as a full copy at `/vX.Y.Z/`, with no banner (2026-10-04)

It carries a canonical to `/latest/` and `noindex`, so pinned tag URLs stay
stable. When the next release ships, it becomes an ordinary old version and
gains the banner.

### 4. Deploy triggers (2026-10-04)

On push to `master`, on push of `v*` tags (Release Please creates tags with a
PAT, so the tag push event fires, as it already does for `release.yml`) and on
`workflow_dispatch`. Concurrency group `pages`, so a later run supersedes a
queued one. The version list is `git tag` filtered by `sitever versions` to
`>= v0.6.0` without `-`, in semver order, so a new release becomes `latest`
and joins the selector with no site edit.

### 5. PR CI builds the site too (2026-10-04)

`ci.yml`'s `site` job builds the current ref with the same pinned builder and
runs the assembler over it as `dev`, so a Liquid/Jekyll break or an assembler
regression fails the PR rather than the post-merge deploy. A tree with no
`latest` is assembled with `dev` as its primary version, and `dev`'s banner
then carries no release link.

## Tasks

- [x] 131.1 `scripts/site/`: the pinned `github-pages` builder and `build.sh`. ✓ 2026-10-04
- [x] 131.2 `cmd/sitever`: version list, selector, banners, SEO rewrite, root sitemap, redirect stubs, with fixture tests. ✓ 2026-10-04
- [x] 131.3 `.github/workflows/pages.yml`: multi-ref build, tag cache, assembly, `deploy-pages`. ✓ 2026-10-04
- [x] 131.4 `ci.yml`'s `site` job. ✓ 2026-10-04
- [x] 131.5 `RELEASING.md` and `CLAUDE.md`: the site is versioned, the published root is `latest`. ✓ 2026-10-04
- [!] 131.6 The acceptance walk on the first deploy. Blocked on the owner: the
  repository's Pages source must be switched from "Deploy from a branch" to
  "GitHub Actions" by hand (RELEASING.md) before the first deploy can run.

## Verification

Locally, on 2026-10-04, `scripts/site/build.sh` built `v0.6.0`, `v0.10.0`,
`v0.11.0`, `latest` (from `v0.11.0`) and `dev` in a `ruby:3.3` container, and
`sitever assemble` assembled them: `v0.10.0`'s `search.json` carries only
`/vincent/v0.10.0/` URLs, `v0.6.0`'s pages carry the inserted `noindex` and a
`/latest/` canonical, the root holds one sitemap of `/latest/` URLs and a
redirect stub per latest page.

That local run did not catch the layout fault decision 1's amendment
records. The first deploy (run 37212783141)
published unstyled `latest` and tag trees; `dev`, built from the checkout it
ran in, was fine. Rebuilding `v0.11.0` in `ruby:3.3` from a foreign working
directory reproduced the empty pages before the fix and a full, styled page
after it.

The first deploy is the acceptance walk (131.6), recorded here when walked:

- `/vincent/` lands on the newest release.
- the selector lists `v0.6.0` through the newest tag.
- an old deep link redirects.
- the search overlay on `/v0.10.0/` returns only that version's pages.
- `/dev/` changes after a master push.
