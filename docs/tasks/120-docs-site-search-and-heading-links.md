# 120 — Searchable documentation site and copyable heading links

**Status:** ✅ done (6/6)
**Opened:** 2026-09-18
**Spec:** none — the published site's presentation, not daemon behavior

## Problem

The GitHub Pages site publishes every page under `docs/` through one layout and
one stylesheet, with no JavaScript at all. Two things were missing for a reader
who arrives with a question rather than a reading plan:

- **No search.** The reference pages are large — `docs/reference/api.md` is
  160 KB, `docs/guides/tui.md` 125 KB, `docs/reference/cli.md` 119 KB — so
  finding `max_tree_cost_usd` meant guessing which page held it and then using
  the browser's in-page find, once per page.
- **No way to link to a section.** kramdown already gives every heading an
  `id`, so `#exit-codes` has always worked; nothing on the page exposed it.
  Quoting a specific section in an issue meant reading the markdown source on
  GitHub to learn the anchor.

## What shipped

`search.json` is a Liquid template that renders the search index at build time:
every published page is markdownified, split on its heading anchors, and emitted
as one record per section — `p` (page title), `t` (section title), `u` (link,
deep where the heading has an anchor), `x` (the section's text). The current
site produces 741 records across 44 pages, 697 of them deep-linked.

`assets/js/search.js` fetches that file the first time the reader opens search —
from the header button, `/`, or `⌘K`/`Ctrl-K` — scores it in the browser, and
renders ranked results with highlighted matches and a snippet. Arrow keys move,
Enter opens, Escape closes, focus is trapped in the panel and restored on close.

`assets/js/heading-links.js` appends a `#` affordance to every `h2`–`h6` in the
prose. Clicking it copies the absolute link to the clipboard, marks the anchor
`copied`, announces it in a live region, and updates the address bar with
`history.replaceState` so the reader is not re-scrolled to the heading they are
already reading.

## Decisions

### 1. The index is built in Liquid, in the repo, and searched in the browser (2026-09-18)

GitHub Pages runs only allowlisted plugins, so `jekyll-lunr-js-search` and
friends are not available, and Pages' build cannot run a Pagefind-style
post-processing step either.

**Beat:** a hosted search service (Algolia DocSearch and similar). A
local-first project whose entire pitch is that nothing leaves the machine
should not make its documentation search a third-party network call, and a
crawler-backed index would have been a second source of truth for content the
repo already owns.

### 2. One record per heading section, not per page (2026-09-18)

Splitting the rendered HTML on `<h2 id="`—with `h3`–`h6` flattened into the
same split first—gives every record a real anchor, so a hit on "exit codes"
lands on `cli.html#exit-codes` rather than at the top of a 119 KB page.

**Beat:** page-level records, which halve the index but make the reference
pages — the ones people search — the least usable results.

### 3. The whole section text is indexed, and the index is fetched lazily (2026-09-18)

The index is 1.16 MB raw, 378 KB gzipped over the wire, and is requested only
when the reader opens search, then cached by the browser. For scale: the site's
background image, loaded on *every* page view, is 1.3 MB.

**Beat:** truncating each section to a fixed prefix. A reference site's value is
in the identifiers buried deep inside long sections — a flag on the fortieth
line of `vincent task add` — and a prefix index silently cannot find them. If
the index outgrows this, the next move is dropping the largest pages' bodies to
their first paragraphs, not truncating everything.

### 4. Heading links copy, and degrade to being plain links (2026-09-18)

The affordance is a real `<a href="#id">`. With a clipboard it copies the
absolute URL and suppresses navigation; without one — or when the browser
refuses the write — it behaves like the anchor it already is. No `execCommand`
fallback, no hidden textarea.

`h1` is deliberately skipped: the page's own link is its URL.

### 5. Both features are progressive enhancements (2026-09-18)

The search trigger ships with `hidden`, and only `search.js` unhides it; the
heading anchors are injected by script. A reader with JavaScript off sees
exactly today's site rather than a button that does nothing.

### 6. A page's index title falls back to its rendered `h1` (2026-09-18)

`jekyll-titles-from-headings` finds no heading on a page that opens with a raw
tag — `docs/reference/api.md`, `configuration.md`, `concepts.md`,
`troubleshooting.md`, `workflow-schema.md` and `guides/workflows.md` all do —
so those pages would have been indexed as "api.md". The index reads the
rendered `<h1>` instead, and falls back to `site.title` for the home page,
which leads with a logo and has no heading at all.

**Beat:** reading `_data/seo_pages.json` for a title. Those entries are written
for search engines ("vincent HTTP API reference"); the reader scanning results
wants the page's own name.

## Tasks

- [x] 120.1 Build the section-level index as a Liquid `search.json`. ✓ 2026-09-18
- [x] 120.2 Search UI: overlay, scoring, highlighting, keyboard, a11y. ✓ 2026-09-18
- [x] 120.3 Copyable heading links with a clipboard-free fallback. ✓ 2026-09-18
- [x] 120.4 Styles for both, matching the existing gruvbox/terminal frame. ✓ 2026-09-18
- [x] 120.5 Wire the layout: trigger, dialog markup, deferred scripts. ✓ 2026-09-18
- [x] 120.6 Verify against a real build and a real DOM. ✓ 2026-09-18

## Verification

CI does not build the site (Pages does), so this was verified locally.

1. `jekyll build` (4.4.1) of the repo as configured: the build's only Liquid
   warnings are the pre-existing ones from `docs/guides/triggers.md`'s Go
   template examples. `search.json` parses as JSON, carries 741 records over 44
   pages, contains no `{% raw %}` markers, and does not appear in `sitemap.xml`.
   `404.html` (`sitemap: false`) is not indexed.
2. The built `docs/reference/cli.html` was loaded into jsdom with the two
   scripts evaluated against it, `fetch` answering with the real index and a
   stubbed clipboard. 31 assertions pass: the trigger unhides, a click opens the
   panel and fetches the index exactly once, "exit codes" ranks
   `cli.html#exit-codes` first with highlighted matches, `max_tree_cost_usd`
   finds its sections, a nonsense query reports no matches, arrow keys move the
   selection and update `aria-activedescendant`, Escape closes and restores
   focus, `/` and `Ctrl-K` open it while `/` typed *inside* the field does not,
   the heading anchor copies the absolute URL, flashes `copied`, announces it in
   the live region, updates the hash without scrolling, and index text never
   injects nodes into the results list.

The jsdom harness is not committed: the repository has no JavaScript toolchain
and CI installs none. Reproduce it with `npm i jsdom` in a scratch directory
against a local `jekyll build`.
