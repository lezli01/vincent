#!/usr/bin/env bash
# Codesign one built binary with a Developer ID Application identity
# (task 032, spec §19 †'s 2026-08-26 amendment).
#
# This runs as a GoReleaser `builds[].hooks.post` hook, once per build target,
# so the signature is *inside* the Mach-O before the archive is assembled and
# before checksums.txt is computed. Every non-darwin target no-ops: GoReleaser
# has no per-GOOS filter on a build hook, so the filtering happens here.
#
# Usage: macos-sign.sh <binary-path> <goos>
#
# Environment:
#   MACOS_SIGN_REQUIRED    "1" on a v* tag build — a missing identity is fatal.
#                          Anything else (dry run, fork, contributor) warns and
#                          leaves the binary unsigned, which is what keeps
#                          `workflow_dispatch` working with no secrets at all.
#   MACOS_SIGN_IDENTITY    Common name or SHA-1 of the Developer ID Application
#                          identity, as `security find-identity` prints it.
#   MACOS_KEYCHAIN         Optional keychain to sign from. release.yml builds a
#                          throwaway one rather than touching the login keychain.
set -euo pipefail

binary="${1:?usage: macos-sign.sh <binary-path> <goos>}"
goos="${2:?usage: macos-sign.sh <binary-path> <goos>}"

required="${MACOS_SIGN_REQUIRED:-0}"
identity="${MACOS_SIGN_IDENTITY:-}"

skip() {
	if [ "$required" = "1" ]; then
		echo "macos-sign: $1" >&2
		exit 1
	fi
	echo "macos-sign: $1; leaving $binary unsigned (MACOS_SIGN_REQUIRED is not 1)" >&2
	exit 0
}

if [ "$goos" != "darwin" ]; then
	exit 0
fi

if [ -z "$identity" ]; then
	skip "MACOS_SIGN_IDENTITY is empty"
fi

if ! command -v codesign >/dev/null 2>&1; then
	skip "codesign is not available on this host"
fi

# --options runtime is the hardened runtime, which notarization requires.
# --timestamp asks Apple's timestamp server for a secure timestamp, so the
# signature stays valid after the certificate expires. Both are non-negotiable
# for a Developer ID signature Apple will notarize.
codesign_args=(--force --timestamp --options runtime --sign "$identity")
if [ -n "${MACOS_KEYCHAIN:-}" ]; then
	codesign_args+=(--keychain "$MACOS_KEYCHAIN")
fi

echo "macos-sign: signing $binary"
codesign "${codesign_args[@]}" "$binary"
codesign --verify --strict --verbose=2 "$binary"

# Prove the hardened runtime actually landed. A Developer ID signature without
# it is accepted by codesign and rejected by notarytool, which is a far more
# expensive place to find out. Matched with bash's own regex rather than a pipe
# into `grep -q`: grep exits at the first match, codesign takes SIGPIPE, and
# `set -o pipefail` then reports a failure that never happened.
signature="$(codesign --display --verbose=2 "$binary" 2>&1)"
if [[ ! "$signature" =~ flags=[^[:space:]]*runtime ]]; then
	echo "macos-sign: $binary is signed without the hardened runtime" >&2
	exit 1
fi
