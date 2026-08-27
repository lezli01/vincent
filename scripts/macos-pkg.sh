#!/usr/bin/env bash
# Build the universal macOS installer package — signed, notarized and stapled
# when the Developer ID certificates are configured, and plain otherwise
# (task 032, spec §19 †'s 2026-08-26 and 2026-08-27 amendments).
#
# lipo → pkgbuild → sign → notarize → staple → verify, where every step past
# pkgbuild is skipped without credentials. That is not only the dry-run path any
# more: the Apple Developer Program was never bought, so it is what a stable
# release takes today (task 039), and an unsigned .pkg is still a real
# installer — one universal binary into /usr/local/bin — that a user opens with
# right-click → Open instead of a double-click.
#
# It runs *after* GoReleaser, not inside a build hook: a hook fires per target,
# and a universal binary needs both darwin slices to exist. The consequence is
# recorded and deliberate — the .pkg is not in checksums.txt. Its integrity
# story is the GitHub build attestation, plus Apple's own signature when there
# is one, and docs/getting-started/installation.md's claim that checksums.txt
# covers every archive, deb and rpm stays literally true.
#
# The package installs /usr/local/bin/vincent under the identifier
# dev.lezli01.vincent — the same identifier internal/service already uses for
# the LaunchAgent — which is a stable path across upgrades, so task 021's
# recorded-executable-path behaviour needs nothing from us.
#
# Usage: macos-pkg.sh <version> [dist-dir]
#
# Environment:
#   MACOS_SIGN_REQUIRED       "1" when the Developer ID certificates are
#                             configured — missing identities, credentials or
#                             tools are then fatal. Otherwise the package is
#                             still built, just unsigned and un-notarized, which
#                             is both the secretless dry run and, until 032.7,
#                             the stable release.
#   MACOS_SIGN_IDENTITY       Developer ID Application identity (re-signs the
#                             lipo-merged binary; merging produces a new Mach-O).
#   MACOS_INSTALLER_IDENTITY  Developer ID Installer identity (signs the .pkg).
#   MACOS_NOTARY_PROFILE      notarytool keychain profile name.
#   MACOS_KEYCHAIN            Optional keychain holding both of the above.
set -euo pipefail

version="${1:?usage: macos-pkg.sh <version> [dist-dir]}"
dist="${2:-dist}"

required="${MACOS_SIGN_REQUIRED:-0}"
app_identity="${MACOS_SIGN_IDENTITY:-}"
installer_identity="${MACOS_INSTALLER_IDENTITY:-}"
notary_profile="${MACOS_NOTARY_PROFILE:-}"

pkg="$dist/vincent_${version}_darwin_universal.pkg"

fail() {
	echo "macos-pkg: $1" >&2
	exit 1
}

# Everything past the plain `pkgbuild` is optional without certificates and
# mandatory with them. `require` is the one place that split is expressed.
require() {
	if [ "$required" = "1" ]; then
		fail "$1"
	fi
	echo "macos-pkg: $1; continuing without it (MACOS_SIGN_REQUIRED is not 1)" >&2
	return 1
}

for tool in lipo pkgbuild; do
	if ! command -v "$tool" >/dev/null 2>&1; then
		if [ "$required" = "1" ]; then
			fail "$tool is not available on this host"
		fi
		echo "macos-pkg: $tool is not available; skipping the .pkg entirely" >&2
		exit 0
	fi
done

# GoReleaser's per-target output directory carries a microarchitecture suffix
# that moves between versions (`vincent_darwin_arm64_v8.0`), so match on the
# GOOS/GOARCH portion rather than hardcoding the whole name.
amd64_binary=""
arm64_binary=""
while IFS= read -r candidate; do
	case "$candidate" in
	*_darwin_amd64*) amd64_binary="$candidate" ;;
	*_darwin_arm64*) arm64_binary="$candidate" ;;
	esac
done <<EOF
$(find "$dist" -type f -name vincent -path '*_darwin_*' | sort)
EOF

[ -n "$amd64_binary" ] || fail "no darwin/amd64 binary under $dist"
[ -n "$arm64_binary" ] || fail "no darwin/arm64 binary under $dist"

staging="$(mktemp -d)"
trap 'rm -rf -- "$staging"' EXIT
mkdir -p "$staging/root/usr/local/bin"
universal="$staging/root/usr/local/bin/vincent"

echo "macos-pkg: merging $amd64_binary + $arm64_binary"
lipo -create -output "$universal" "$amd64_binary" "$arm64_binary"
chmod 0755 "$universal"
# macOS tags the file lipo just wrote with com.apple.provenance, which would
# then be carried into the payload and applied on install. It records something
# about the build machine and nothing a user needs. (The AppleDouble `._`
# entries in `pkgutil --payload-files` output are pkgbuild's own encoding and
# are there either way.)
xattr -c "$universal"
lipo -info "$universal"

# lipo emits a new Mach-O, so the per-slice signatures macos-sign.sh applied do
# not carry over to the merged file. Sign it again, with the same hardened
# runtime and secure timestamp.
if [ -n "$app_identity" ] || require "MACOS_SIGN_IDENTITY is empty"; then
	codesign_args=(--force --timestamp --options runtime --sign "$app_identity")
	if [ -n "${MACOS_KEYCHAIN:-}" ]; then
		codesign_args+=(--keychain "$MACOS_KEYCHAIN")
	fi
	codesign "${codesign_args[@]}" "$universal"
	codesign --verify --strict --verbose=2 "$universal"
fi

pkgbuild_args=(
	--root "$staging/root"
	--identifier dev.lezli01.vincent
	--version "$version"
	--install-location /
)
if [ -n "$installer_identity" ] || require "MACOS_INSTALLER_IDENTITY is empty"; then
	pkgbuild_args+=(--sign "$installer_identity")
	if [ -n "${MACOS_KEYCHAIN:-}" ]; then
		pkgbuild_args+=(--keychain "$MACOS_KEYCHAIN")
	fi
fi

echo "macos-pkg: building $pkg"
pkgbuild "${pkgbuild_args[@]}" "$pkg"

# Notarization does not modify the artifact — only its success gates
# publication — but stapling does, and stapling is the whole point of shipping a
# .pkg: it is the only artifact here that can carry the ticket, so a first
# launch works with no network. A bare Mach-O cannot be stapled at all, which is
# how the issue's "verifiable offline where supported" resolves.
if [ -n "$notary_profile" ] || require "MACOS_NOTARY_PROFILE is empty"; then
	notary_args=(--keychain-profile "$notary_profile" --wait)
	if [ -n "${MACOS_KEYCHAIN:-}" ]; then
		notary_args+=(--keychain "$MACOS_KEYCHAIN")
	fi
	echo "macos-pkg: notarizing $pkg"
	xcrun notarytool submit "$pkg" "${notary_args[@]}"
	xcrun stapler staple "$pkg"
	xcrun stapler validate "$pkg"

	# The two commands installation.md tells a user to run. Asserting them here
	# means a rejection surfaces in the release job rather than on a laptop.
	pkgutil --check-signature "$pkg"
	spctl --assess --type install -vv "$pkg"
fi

echo "macos-pkg: wrote $pkg"
