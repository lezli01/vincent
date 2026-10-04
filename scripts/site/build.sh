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

export BUNDLE_GEMFILE="$here/Gemfile"
export JEKYLL_ENV=production
bundle exec jekyll build --source "$src" --destination "$dest" \
  --config "$src/_config.yml,$overlay"
