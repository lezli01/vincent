package selfupdate

import (
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
)

// Channel is how this binary was installed. It decides whether `vincent
// update` may touch the file or must print somebody else's command.
type Channel string

const (
	// ChannelSelf is the direct-download archive, or a binary somebody placed
	// by hand. vincent owns it and may swap it. This is the user the whole
	// feature exists for: every other channel already gets nagged by its own
	// package manager.
	ChannelSelf Channel = "self"

	// ChannelHomebrew is a `brew install`. Like every package-managed channel
	// below it, it would silently revert a file vincent replaced behind its
	// back on the next upgrade, so vincent prints its command instead.
	ChannelHomebrew Channel = "homebrew"
	// ChannelScoop is a Scoop install, under the apps directory.
	ChannelScoop Channel = "scoop"
	// ChannelWinGet is a WinGet install, under %LOCALAPPDATA%.
	ChannelWinGet Channel = "winget"
	// ChannelMise is a mise install or one of its shims.
	ChannelMise Channel = "mise"
	// ChannelGoInstall is `go install …@latest`, in GOBIN or GOPATH/bin.
	ChannelGoInstall Channel = "go-install"
	// ChannelSystemPackage is a deb or rpm: the binary lives in a system bin
	// directory owned by the platform package database.
	ChannelSystemPackage Channel = "system-package"
	// ChannelUnknown is an install this cannot place. It is treated as
	// package-managed, not as self-owned: the cost of guessing "self" wrongly
	// is a clobbered file that the next `brew upgrade` silently reverts,
	// while the cost of guessing "unknown" wrongly is a printed command.
	ChannelUnknown Channel = "unknown"
)

// Owned reports whether vincent may replace the binary itself.
func (c Channel) Owned() bool { return c == ChannelSelf }

// UpgradeCommand is the exact command that upgrades this channel, empty for
// one that has none. These are the strings docs/getting-started/installation.md
// documents; a user who reads either should meet the same line.
func (c Channel) UpgradeCommand() string {
	switch c {
	case ChannelHomebrew:
		return "brew upgrade vincent"
	case ChannelScoop:
		return "scoop update vincent"
	case ChannelWinGet:
		return "winget upgrade --id lezli01.Vincent --exact"
	case ChannelMise:
		return "mise upgrade vincent"
	case ChannelGoInstall:
		return "go install github.com/lezli01/vincent/cmd/vincent@latest"
	case ChannelSystemPackage:
		// Two package managers share this channel and the wrong one is worse
		// than none, so this names the file to reinstall rather than
		// inventing an `apt` line for an rpm host.
		return ""
	case ChannelSelf, ChannelUnknown:
		return ""
	}
	return ""
}

// Detect places the running binary. It resolves symlinks first: Homebrew
// installs a shim in its own prefix and links it into /opt/homebrew/bin, and
// mise's shims point into its installs directory, so the unresolved path
// answers the wrong question.
func Detect() (Channel, string) {
	exe, err := os.Executable()
	if err != nil {
		return ChannelUnknown, ""
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	return classify(exe, os.Getenv, runtime.GOOS), exe
}

// classify is Detect's pure core: it takes the resolved path, an environment
// reader and a GOOS, so the whole table is testable on one platform for all
// three. The order matters — a mise install lives under the user's home and a
// Scoop one under LOCALAPPDATA, and both would otherwise fall through to the
// generic checks.
func classify(exe string, getenv func(string) string, goos string) Channel {
	p := normalize(exe)
	dir := pathDir(p)

	if goos == "windows" {
		if local := normalize(getenv("LOCALAPPDATA")); local != "" {
			if under(p, local+"/microsoft/winget/packages") {
				return ChannelWinGet
			}
		}
		// Scoop's default root is %USERPROFILE%\scoop, overridable by
		// SCOOP; a global install lives under SCOOP_GLOBAL.
		for _, root := range []string{
			normalize(getenv("SCOOP")),
			normalize(getenv("SCOOP_GLOBAL")),
			joinEnv(getenv("USERPROFILE"), "scoop"),
			joinEnv(getenv("ProgramData"), "scoop"),
		} {
			if root != "" && under(p, root+"/apps") {
				return ChannelScoop
			}
		}
	}

	// mise before the generic home-directory checks: its installs directory
	// is inside the user's data dir and would otherwise look like nothing.
	for _, root := range []string{
		normalize(getenv("MISE_DATA_DIR")),
		joinEnv(getenv("XDG_DATA_HOME"), "mise"),
		joinEnv(getenv("HOME"), ".local/share/mise"),
		joinEnv(getenv("USERPROFILE"), "AppData/Local/mise"),
	} {
		if root != "" && (under(p, root+"/installs") || under(p, root+"/shims")) {
			return ChannelMise
		}
	}

	// Homebrew: the prefix from the environment when the caller has one, plus
	// the two standard prefixes, because `brew` need not be on PATH for a
	// binary it installed to be running.
	for _, prefix := range []string{
		normalize(getenv("HOMEBREW_PREFIX")),
		"/opt/homebrew", "/usr/local/Homebrew", "/home/linuxbrew/.linuxbrew",
	} {
		if prefix != "" && (under(p, prefix+"/cellar") || under(p, prefix+"/caskroom") ||
			under(p, prefix+"/bin") || under(p, prefix+"/opt")) {
			return ChannelHomebrew
		}
	}
	// /usr/local/bin is Homebrew's Intel-macOS bin dir and also the canonical
	// place a human copies a binary to. On macOS the Homebrew reading wins
	// only when the file is a Cellar link, which EvalSymlinks has already
	// followed above — so reaching here with this path means nobody owns it.

	// GOBIN / GOPATH/bin — `go install`, a documented install path (README).
	for _, root := range []string{
		normalize(getenv("GOBIN")),
		joinEnv(getenv("GOPATH"), "bin"),
		joinEnv(getenv("HOME"), "go/bin"),
		joinEnv(getenv("USERPROFILE"), "go/bin"),
	} {
		if root != "" && dir == root {
			return ChannelGoInstall
		}
	}

	// A system bin directory on a platform with a package database. This is
	// the deb/rpm reading: nfpm installs to /usr/bin, and nothing else on a
	// Linux host writes there without going through the package manager.
	if goos == "linux" || goos == "darwin" {
		if dir == "/usr/bin" || dir == "/bin" || dir == "/usr/sbin" {
			return ChannelSystemPackage
		}
	}

	// Everything else is vincent's to replace, which is the direct-download
	// archive unpacked wherever the user chose.
	if p != "" {
		return ChannelSelf
	}
	return ChannelUnknown
}

// normalize lowercases and forward-slashes a path so one comparison works on
// all three platforms. Case folding is wrong on a case-sensitive filesystem in
// the strictest sense, but the alternative is missing every Windows match, and
// a path that differs only in case is not a different install of vincent.
func normalize(p string) string {
	if p == "" {
		return ""
	}
	// Backslashes are folded first, not by filepath.ToSlash: that function is
	// a no-op off Windows, and classify has to read a Windows path on Linux
	// for the detection table to be testable on one platform for all three.
	p = path.Clean(strings.ReplaceAll(p, `\`, "/"))
	return strings.ToLower(strings.TrimSuffix(p, "/"))
}

func joinEnv(base, rest string) string {
	if base == "" {
		return ""
	}
	return normalize(base + "/" + rest)
}

func pathDir(p string) string {
	if p == "" {
		return ""
	}
	return normalize(pathParent(p))
}

func pathParent(p string) string {
	i := strings.LastIndex(p, "/")
	if i <= 0 {
		return p
	}
	return p[:i]
}

// under reports whether p is inside dir. It compares whole segments, so
// "/opt/homebrew-staging/bin/vincent" is not under "/opt/homebrew".
func under(p, dir string) bool {
	dir = normalize(dir)
	if p == "" || dir == "" {
		return false
	}
	return strings.HasPrefix(p, dir+"/")
}
