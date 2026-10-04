#!/usr/bin/env bash
# Build one version of the GitHub Pages site (task 131 decision 1).
#
#   scripts/site/build.sh SRC DEST NAME
#
# SRC is a checkout of the ref (a release tag, or master as `dev`), built
# unedited: the baseurl is overridden by a config overlay this script writes
# outside SRC, and the builder is this directory's pinned github-pages gem,
# never anything the tagged source carries. NAME is the version's URL
# segment — vX.Y.Z, dev or latest — so it is served at $SITE_BASE/NAME/.
# cmd/sitever assembles the built trees into the published site.
set -euo pipefail

if [[ $# -ne 3 ]]; then
  echo "usage: $0 SRC DEST NAME" >&2
  exit 2
fi
src="$(cd "$1" && pwd)"
dest="$2"
name="$3"
here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
base="${SITE_BASE:-/vincent}"

# Jekyll picks a config file's parser from its extension, so the overlay
# must end in .yml.
overlay="$(mktemp -d)/overlay.yml"
trap 'rm -rf "$(dirname "$overlay")"' EXIT
printf 'baseurl: "%s/%s"\n' "$base" "$name" >"$overlay"

mkdir -p "$dest"
dest="$(cd "$dest" && pwd)"

export BUNDLE_GEMFILE="$here/Gemfile"
export JEKYLL_ENV=production
# The working directory decides two things in Jekyll 3, so it must be this
# directory, not SRC and not the repository root:
#
#  - Jekyll requires the Gemfile's jekyll_plugins group only when a file
#    named Gemfile is in the working directory; BUNDLE_GEMFILE alone is not
#    enough. Without it the github-pages gem never loads, and neither does
#    the configuration GitHub Pages applies on top of _config.yml — its
#    default plugins among it. v0.6.0 lists no plugins and relies on those
#    defaults (jekyll-readme-index makes README.md its index.html), so it
#    built with no index at all.
#  - Jekyll reads layouts from ./_layouts in the working directory in
#    preference to SRC's, then joins that absolute path onto SRC and fails
#    to open it — a warning, not an error — so every page is built with no
#    layout: no <head>, no stylesheet. This directory has no _layouts, so
#    SRC's own are read.
cd "$here"
bundle exec jekyll build --source "$src" --destination "$dest" \
  --config "$src/_config.yml,$overlay"

# A layout that failed to load leaves a successful build of bare fragments;
# refuse it here rather than publish an unstyled site.
index="$(cat "$dest/index.html")"
if ! grep -qi '</head>' <<<"$index"; then
  echo "$dest/index.html has no <head>: the layout was not applied" >&2
  exit 1
fi
