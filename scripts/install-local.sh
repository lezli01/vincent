#!/usr/bin/env bash
# Install the currently checked-out tree the way a release install leaves you
# (README "Install"): one self-contained `vincent` binary on a PATH directory,
# built with the release flags from .goreleaser.yaml and carrying the same
# three version symbols — so `vincent version` names the commit you have
# checked out, and every other command behaves exactly as the shipped binary
# does. Nothing else is written: config and data still come from
# internal/config.ResolveDirs at run time.
#
#   ./scripts/install-local.sh                # /usr/local/bin (sudo if needed)
#   ./scripts/install-local.sh --user         # ~/.local/bin, never sudo
#   ./scripts/install-local.sh --bin-dir DIR  # anywhere; VINCENT_INSTALL_DIR too
#   ./scripts/install-local.sh --dry-run      # build and report, install nothing
#   ./scripts/install-local.sh --uninstall    # remove the binary, keep the data
#
# It is not a substitute for a release: the binary is unsigned and has no
# checksum to verify against. It is also not quarantined, because it was built
# here rather than downloaded, so the Gatekeeper prompt the README describes
# does not apply.
#
# Requirements: bash, go, git.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
VERSION_PKG="github.com/lezli01/vincent/internal/version"

BIN_DIR="${VINCENT_INSTALL_DIR:-}"
USER_SCOPE=0
DRY_RUN=0
UNINSTALL=0
NEED_SUDO=0

die() {
	printf 'error: %s\n' "$*" >&2
	exit 1
}
warn() { printf 'warning: %s\n' "$*" >&2; }
info() { printf '%s\n' "$*"; }

usage() {
	cat <<'EOF'
Install the checked-out tree as a regular local install: one self-contained
`vincent` binary on PATH, built with the release flags and version symbols.

  install-local.sh                Install to /usr/local/bin (sudo if needed)
  install-local.sh --user         Install to ~/.local/bin, never sudo
  install-local.sh --bin-dir DIR  Install to DIR (env: VINCENT_INSTALL_DIR)
  install-local.sh --dry-run      Build and report, install nothing
  install-local.sh --uninstall    Remove the binary; config and data stay
  install-local.sh --help         This text

On Windows the default is %LOCALAPPDATA%\vincent\bin — there is no sudo and no
/usr/local to fall back on.
EOF
}

while [ $# -gt 0 ]; do
	case "$1" in
	--bin-dir)
		[ $# -ge 2 ] || die "--bin-dir needs a directory"
		BIN_DIR="$2"
		shift 2
		;;
	--bin-dir=*)
		BIN_DIR="${1#*=}"
		shift
		;;
	--user)
		USER_SCOPE=1
		shift
		;;
	--dry-run)
		DRY_RUN=1
		shift
		;;
	--uninstall)
		UNINSTALL=1
		shift
		;;
	-h | --help)
		usage
		exit 0
		;;
	*) die "unknown option: $1 (try --help)" ;;
	esac
done

command -v go >/dev/null 2>&1 || die "go is required but not on PATH"
command -v git >/dev/null 2>&1 || die "git is required but not on PATH"

GOOS="$(go env GOOS)"
GOEXE="$(go env GOEXE)"

if [ -z "$BIN_DIR" ]; then
	case "$GOOS" in
	windows)
		# No /usr/local and no sudo here; a per-user program belongs under
		# %LOCALAPPDATA%, which Git Bash exposes as $LOCALAPPDATA.
		BIN_DIR="${LOCALAPPDATA:-$HOME/AppData/Local}/vincent/bin"
		;;
	*)
		if [ "$USER_SCOPE" -eq 1 ]; then
			BIN_DIR="$HOME/.local/bin"
		else
			# What the README tells a release user to do with the archive.
			BIN_DIR="/usr/local/bin"
		fi
		;;
	esac
fi

TARGET="$BIN_DIR/vincent$GOEXE"

# Writing to /usr/local/bin needs root on a stock macOS or Linux box, exactly
# as `sudo mv vincent /usr/local/bin/` does in the README. Creating the
# directory counts: that is why the parent is what gets tested when the
# directory is absent.
if [ -d "$BIN_DIR" ]; then
	[ -w "$BIN_DIR" ] || NEED_SUDO=1
else
	[ -w "$(dirname "$BIN_DIR")" ] || NEED_SUDO=1
fi

if [ "$NEED_SUDO" -eq 1 ]; then
	case "$GOOS" in
	windows) die "$BIN_DIR is not writable; pass --bin-dir with somewhere you own" ;;
	esac
	[ "$USER_SCOPE" -eq 0 ] || die "$BIN_DIR is not writable, and --user rules out sudo"
	command -v sudo >/dev/null 2>&1 ||
		die "$BIN_DIR is not writable and sudo is not available; try --user or --bin-dir DIR"
fi

# run_priv keeps the sudo decision in one place. A function rather than an
# array because bash 3.2 — still the /bin/bash on macOS — errors on the
# expansion of an empty array under `set -u`.
run_priv() {
	if [ "$NEED_SUDO" -eq 1 ]; then
		sudo "$@"
	else
		"$@"
	fi
}

if [ "$UNINSTALL" -eq 1 ]; then
	if [ ! -e "$TARGET" ]; then
		info "nothing to remove: $TARGET does not exist"
		exit 0
	fi
	# Guard against --bin-dir pointing somewhere unintended: only remove a file
	# that answers as vincent.
	answer="$("$TARGET" version 2>/dev/null || true)"
	case "$answer" in
	"vincent version "*) ;;
	*) die "$TARGET does not answer 'vincent version'; refusing to remove it" ;;
	esac
	if [ "$DRY_RUN" -eq 1 ]; then
		info "would remove $TARGET ($answer)"
		exit 0
	fi
	run_priv rm -f "$TARGET"
	info "removed $TARGET"
	info "Config, database and transcripts are untouched — see docs/reference/files.md"
	info "for their locations, and 'vincent service uninstall' if you registered the service."
	exit 0
fi

# The same three symbols the mage Build target and .goreleaser.yaml inject, so
# this binary answers `vincent version` in the shipped shape. The date is the
# commit's, not today's: reinstalling the same checkout then produces the same
# binary instead of one that differs by a timestamp.
version="$(git -C "$ROOT" describe --tags --always --dirty 2>/dev/null || true)"
[ -n "$version" ] || version="dev"
commit="$(git -C "$ROOT" rev-parse --short HEAD 2>/dev/null || true)"
[ -n "$commit" ] || commit="unknown"
built="$(git -C "$ROOT" log -1 --format=%cd --date=format:%Y-%m-%d 2>/dev/null || true)"
[ -n "$built" ] || built="unknown"

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
STAGED="$TMP/vincent$GOEXE"

info "Building $version (commit $commit) for $GOOS ..."
(
	cd "$ROOT"
	CGO_ENABLED=0 go build -trimpath \
		-ldflags "-s -w -X $VERSION_PKG.version=$version -X $VERSION_PKG.commit=$commit -X $VERSION_PKG.date=$built" \
		-o "$STAGED" ./cmd/vincent
)

if [ "$DRY_RUN" -eq 1 ]; then
	info "$("$STAGED" version)"
	if [ "$NEED_SUDO" -eq 1 ]; then
		info "would install to $TARGET (via sudo)"
	else
		info "would install to $TARGET"
	fi
	exit 0
fi

# Copy beside the target and rename over it, so a concurrent `vincent` either
# sees the old file or the new one and never a half-written binary. On Unix the
# rename also succeeds while the old binary is running; Windows holds a lock on
# a running .exe, which is what the hint below is for.
INCOMING="$BIN_DIR/.vincent.incoming.$$"
run_priv mkdir -p "$BIN_DIR"
run_priv cp "$STAGED" "$INCOMING"
run_priv chmod 0755 "$INCOMING"
if ! run_priv mv -f "$INCOMING" "$TARGET"; then
	run_priv rm -f "$INCOMING" || true
	case "$GOOS" in
	windows) die "could not replace $TARGET — stop anything running it ('vincent daemon stop') and retry" ;;
	*) die "could not replace $TARGET" ;;
	esac
fi

info "Installed $("$TARGET" version) to $TARGET"

case ":$PATH:" in
*":$BIN_DIR:"*) ;;
*) warn "$BIN_DIR is not on your PATH; add it before 'vincent' resolves anywhere" ;;
esac

resolved="$(command -v vincent 2>/dev/null || true)"
if [ -n "$resolved" ] && [ "$resolved" != "$TARGET" ]; then
	warn "'vincent' still resolves to $resolved, which shadows this install"
	warn "(a Homebrew or archive install earlier on PATH — remove it, or call $TARGET directly)"
fi

# A running daemon keeps executing the binary it started with, so the install
# is not live until it restarts (spec §12.4 recovery finalizes whatever it was
# mid-way through).
if running="$("$TARGET" daemon status 2>/dev/null)"; then
	info ""
	info "A daemon is already running the previous build:"
	info "  $running"
	info "Restart it to pick this one up:  vincent daemon stop"
	info "(the TUI and CLI start it again on demand). If you registered it with"
	info "'vincent service install', run that again so the service points here."
fi
