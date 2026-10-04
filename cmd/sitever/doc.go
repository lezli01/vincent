// sitever assembles the versioned GitHub Pages site (task 131).
//
// The Pages deploy (.github/workflows/pages.yml) builds one full Jekyll
// snapshot per release tag from v0.6.0 on, one for master as `dev`, and one
// for the newest tag again as `latest`, each under its own baseurl
// (/vincent/vX.Y.Z, /vincent/dev, /vincent/latest). The tagged sources are
// built unedited (task 131 decision 1), so nothing version-specific can live
// in them: an old tag does not know a newer one exists. Everything that does
// know is added here, after Jekyll, over the built trees (decision 2):
//
//   - a version badge and selector on every HTML page, listing
//     `latest (vX.Y.Z)`, `dev`, then every tag newest first, each entry
//     linking to the same page in that version when it has one and to that
//     version's homepage when it does not — static links computed from every
//     version's file list, no runtime fetch;
//   - a banner on old tags and on `dev`, pointing at the same page under
//     /latest/ (decision 3: the newest tag's own copy carries none);
//   - on every page outside `latest`, a canonical, og:url and twitter:url
//     pointing at /latest/, social images served from /latest/, and a
//     noindex robots directive — a missing tag is inserted, since old layouts
//     differ;
//   - the site root: latest's sitemap.xml (the only one published), its
//     robots.txt and 404.html, its docs/assets/** copied for already-shared
//     image URLs, and one redirect stub per HTML path in latest (and per path
//     only dev has) so every pre-versioning deep link still lands.
//
// The injected styles ship inline with the markup, so an old tag's
// stylesheet needs no change, and every anchor (<head>, <body>, the
// content-card <main>) degrades to the next one when a layout lacks it.
//
// Usage:
//
//	git tag | sitever versions          # release tags to build, newest first
//	sitever assemble -in BUILT -out SITE [-base /vincent] [-url https://…]
//
// BUILT holds one built tree per version, named `latest`, `dev` or the tag.
// A tree without `latest` is assembled with `dev` as the primary version,
// which is how PR CI smoke-tests the assembler over the current ref alone.
package main
