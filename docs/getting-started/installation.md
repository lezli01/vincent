# Installation

vincent is one self-contained binary. There is no runtime to install, no CGO,
and no database server — the store is an embedded SQLite file.

- [What you need](#what-you-need)
- [Homebrew (macOS)](#homebrew-macos)
- [Installer package (macOS)](#installer-package-macos)
- [WinGet (Windows)](#winget-windows)
- [Scoop (Windows)](#scoop-windows)
- [mise (all platforms)](#mise-all-platforms)
- [deb and rpm (Linux)](#deb-and-rpm-linux)
- [Download a release](#download-a-release)
- [First launch](#first-launch)
- [Verify a download](#verify-a-download)
- [Install an agent CLI](#install-an-agent-cli)
- [Confirm the install](#confirm-the-install)
- [Build from source](#build-from-source)
- [Upgrading](#upgrading)
- [Uninstalling](#uninstalling)

---

## What you need

| Requirement | Why | Notes |
|---|---|---|
| **git** | Projects are git repositories; every task gets a worktree | 2.31 or newer recommended. The daemon logs the detected version at startup and warns below 2.31 rather than refusing to run |
| **At least one agent CLI** | Agent steps drive a CLI you already have | `claude`, `codex`, or `cursor-agent` — see [Install an agent CLI](#install-an-agent-cli) |
| A terminal | The TUI is a full-screen terminal app | Any modern terminal. On Windows, Windows Terminal is what ships with Windows 11 |

vincent stores **no credentials of its own**. It runs the agent CLI you
installed, authenticated as you already authenticated it.

Go is *not* required to run vincent — only to
[build it from source](#build-from-source).

## Homebrew (macOS)

```sh
brew install lezli01/tap/vincent
```

This is the shortest path on macOS. The binary it installs is Developer ID
signed and notarized like every other macOS artifact, so nothing has to clear a
quarantine attribute — the cask used to, and deliberately no longer does.

Homebrew casks are macOS-only. On Linuxbrew, use the archive below.

Upgrade with `brew upgrade vincent`. To remove vincent along with its
LaunchAgent, its config and its task history:

```sh
brew uninstall --zap vincent
```

Plain `brew uninstall vincent` removes the binary and unloads the LaunchAgent
but leaves `~/Library/Application Support/vincent` intact.

## Installer package (macOS)

Stable releases attach `vincent_{version}_darwin_universal.pkg`: one universal
installer covering Apple silicon and Intel, which puts the binary at
`/usr/local/bin/vincent`. Download it from the
[latest release](https://github.com/lezli01/vincent/releases/latest) and
double-click it, or:

```sh
sudo installer -pkg vincent_*_darwin_universal.pkg -target /
vincent version
```

The package is signed with an Apple Developer ID Installer identity, notarized,
and **stapled** — its notarization ticket travels inside the file, so it
installs on a machine with no network. That is the one thing it does that the
archive cannot; everything else here is equivalent. Verify it before installing:

```sh
pkgutil --check-signature vincent_*_darwin_universal.pkg
spctl --assess --type install -vv vincent_*_darwin_universal.pkg
```

The `.pkg` is deliberately absent from `checksums.txt` — it is built after the
checksummed artifacts, from both of them — and carries Apple's installer
signature plus a [build attestation](#verify-a-download) instead.

To remove it, delete `/usr/local/bin/vincent` (after `vincent service
uninstall`, if you registered the background service) and, if you also want the
config, database and transcripts, `~/Library/Application Support/vincent`.

## WinGet (Windows)

```powershell
winget install --id lezli01.Vincent --exact
vincent version
```

The package depends on `Git.Git`, so WinGet installs Git when it is missing.
The manifest points at the same checksummed zip as the GitHub release; it is a
portable package, not an MSI. Releases are not Authenticode-signed, so
SmartScreen may still prompt on first launch.

Upgrade with `winget upgrade --id lezli01.Vincent --exact`. Before uninstalling
a copy used by the background service, run `vincent service uninstall`, then
`winget uninstall --id lezli01.Vincent --exact`.

Microsoft reviews submissions to the public WinGet catalog. A new stable
release can therefore appear here after its GitHub assets do.

## Scoop (Windows)

Add vincent's bucket once, then install its manifest:

```powershell
scoop bucket add vincent https://github.com/lezli01/scoop-bucket
scoop install vincent/vincent
vincent version
```

The manifest installs both x86-64 and ARM64 from the matching GitHub release
zip and declares Scoop's `git` package as a dependency. Upgrade with `scoop
update vincent`. Before `scoop uninstall vincent`, run `vincent service
uninstall` if you registered the background service.

## mise (all platforms)

mise can consume the existing GitHub release archives directly; vincent does
not need a mise plugin or registry entry:

```sh
mise use -g github:lezli01/vincent
vincent version
```

That records `latest` in mise's global config and selects the archive matching
the current OS and architecture. For vincent's releases, mise also verifies the
GitHub artifact attestation before extracting the archive. Pin a project or
machine to a specific release instead with:

```sh
mise use github:lezli01/vincent@0.3.0       # current directory
mise use -g github:lezli01/vincent@0.3.0    # global
```

Use `mise upgrade github:lezli01/vincent` to move an unpinned install forward.
Shell activation or mise shims must be configured for `vincent` to be on
`PATH`; follow mise's own shell setup if `mise which vincent` succeeds but the
shell cannot find it.

## deb and rpm (Linux)

Stable releases attach native packages for x86-64 and ARM64. Download the file
for your system from the [latest release](https://github.com/lezli01/vincent/releases/latest),
then let the system package tool install it and its `git` dependency:

```sh
# Debian / Ubuntu, x86-64 (use _arm64.deb on ARM64)
sudo apt install ./vincent_*_amd64.deb

# Fedora / RHEL family, x86-64 (use .aarch64.rpm on ARM64)
sudo dnf install ./vincent-*.x86_64.rpm

vincent version
```

Both formats put the binary at `/usr/bin/vincent` and the license documents
under `/usr/share`. They deliberately install no system service: vincent's
service is per-user and must capture that user's `PATH`, config, and data
directories, so opt in afterwards with `vincent service install`.

These files are GitHub release assets, not an apt or dnf repository. The system
package database records the install, but it cannot discover a newer release;
download the next deb/rpm and run the same command to upgrade.

WinGet and Scoop metadata is published only for stable releases. If a
newly introduced channel reports that vincent is not found before its first
stable publication, use [mise](#mise-all-platforms) or
[download a release](#download-a-release).

## Download a release

Grab the archive for your platform from the
[latest release](https://github.com/lezli01/vincent/releases/latest). Assets are
named `vincent_{version}_{os}_{arch}.{tar.gz|zip}`:

| Platform | Asset |
|---|---|
| macOS, Apple silicon | `vincent_*_darwin_arm64.tar.gz` |
| macOS, Intel | `vincent_*_darwin_amd64.tar.gz` |
| Linux, x86-64 | `vincent_*_linux_amd64.tar.gz` |
| Linux, ARM64 | `vincent_*_linux_arm64.tar.gz` |
| Windows, x86-64 | `vincent_*_windows_amd64.zip` |
| Windows, ARM64 | `vincent_*_windows_arm64.zip` |

Each archive contains the binary, `LICENSE`, `README.md`, and the
[example workflows](../../examples).

**macOS / Linux**

```sh
tar -xzf vincent_*_darwin_arm64.tar.gz     # adjust for your platform
sudo mv vincent /usr/local/bin/
vincent version
```

`~/.local/bin` works just as well if you would rather not use `sudo` — any
directory on your `PATH` will do.

**Windows (PowerShell)**

```powershell
Expand-Archive vincent_*_windows_amd64.zip -DestinationPath $env:LOCALAPPDATA\Programs\vincent
# add that directory to your user PATH, then reopen the terminal
vincent version
```

Platform-specific detail lives in [Windows](../platforms/windows.md),
[macOS](../platforms/macos.md) and [Linux](../platforms/linux.md).

## First launch

Every release carries cosign signatures, SHA-256 checksums and GitHub build
attestations. On top of those, macOS artifacts carry **Apple code signing**;
Windows ones do not.

- **macOS** — nothing to do. The binaries and the `.pkg` are signed with an
  Apple Developer ID identity under the hardened runtime and notarized, so
  Gatekeeper clears them without a prompt. Do not run `xattr -d
  com.apple.quarantine`: it is no longer needed, and it only turns off the check
  that would tell you the file had been tampered with. Confirm the signature
  yourself with:

  ```sh
  codesign --verify --strict --verbose=2 /usr/local/bin/vincent
  spctl --assess --type execute -vv /usr/local/bin/vincent
  ```

  Only the [`.pkg`](#installer-package-macos) is *stapled*, so it is the one
  artifact whose first launch also works with no network — a bare binary has
  nowhere to hold a notarization ticket, and Gatekeeper fetches its verdict from
  Apple instead.

- **Windows** — SmartScreen shows *"Windows protected your PC"*. Choose
  **More info → Run anyway**. It appears once per binary. Releases are not
  Authenticode-signed: an OV certificate on a hardware token is a recurring
  purchase with no equivalent to Apple's single notary service, and this project
  does not take it on.

- **Linux** — nothing to clear. Make sure the file is executable
  (`chmod +x vincent`) if your extraction tool dropped the bit.

## Verify a download

The signature is over `checksums.txt`, and `checksums.txt` covers every archive,
deb, and rpm — so verifying the signature and then the checksum covers the
package or binary you are about to run. Download `checksums.txt`,
`checksums.txt.sig` and `checksums.txt.pem` from the same release:

```sh
cosign verify-blob checksums.txt \
  --certificate checksums.txt.pem \
  --signature checksums.txt.sig \
  --certificate-identity-regexp 'https://github.com/lezli01/vincent/.*' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com

sha256sum -c checksums.txt --ignore-missing
```

Keyless cosign means there is no vincent public key to trust or rotate: the
certificate binds the signature to the GitHub Actions workflow that produced
it. Every archive, deb, and rpm additionally carries a build provenance
attestation. For example:

```sh
gh attestation verify vincent_*_linux_amd64.tar.gz --repo lezli01/vincent
```

The macOS `.pkg` is the one asset outside `checksums.txt`, so it is verified
from its own two signatures — Apple's, and the attestation:

```sh
pkgutil --check-signature vincent_*_darwin_universal.pkg
gh attestation verify vincent_*_darwin_universal.pkg --repo lezli01/vincent
```

The three signatures answer different questions, which is why all three exist.
**cosign** says which GitHub Actions workflow, at which commit, produced the
file — to anyone, with no vincent key to trust or rotate. The **build
attestation** says the same thing in the format `gh` and `mise` check
automatically. **Apple's Developer ID signature plus notarization** says to
*macOS itself* that a known developer built this and Apple scanned it, which is
what makes Gatekeeper open it. None of them substitutes for another, and no
Windows equivalent of the third exists here.

## Install an agent CLI

vincent orchestrates agent CLIs; it does not embed one. Install and log in to
at least one, following that vendor's own instructions:

| Agent | Binary vincent looks for | `agent:` value in a workflow |
|---|---|---|
| Claude Code | `claude` | `claude` |
| Codex | `codex` | `codex` |
| Cursor | **`cursor-agent`** | `cursor` |

Cursor's binary name is the one thing worth reading twice: `cursor` on your
`PATH` is the **editor launcher** and would open a GUI, so vincent resolves
`cursor-agent` and nothing else.

Authenticate each CLI the normal way and confirm it runs by hand once. An
installed-but-unauthenticated CLI probes as present and then fails every run;
vincent reports that distinction where a CLI can answer it cheaply
(`logged_in` — see [Agent CLIs](../guides/agents.md)).

If a CLI is installed somewhere `PATH` does not reach, point at it directly in
[`config.yaml`](../reference/configuration.md):

```yaml
agents:
  claude: { path: "/opt/homebrew/bin/claude" }
```

That path is absolute and never consults `PATH`, which makes it the standing
answer to "vincent says the agent is missing but my shell finds it".

## Confirm the install

```sh
vincent version          # build info: version, commit, build date
vincent daemon start     # starts the background daemon
vincent daemon status    # exit 0 healthy, 1 not running, 2 unresponsive
```

`vincent daemon status` prints the daemon's identity and which agent CLIs it
resolved. If an agent you installed is missing from that list, read
[Troubleshooting → an agent CLI is not found](../guides/troubleshooting.md#an-agent-cli-is-not-found).

Then go run something: [Quickstart](quickstart.md).

## Build from source

Go 1.26 or newer is the only prerequisite. `go.mod` also pins an exact patch
toolchain, which the default `GOTOOLCHAIN=auto` downloads on the first build;
`GOTOOLCHAIN=local` builds with the Go you already have instead. Build targets
run through [mage](https://magefile.org/) with zero install:

```sh
git clone https://github.com/lezli01/vincent
cd vincent
go run mage.go build      # produces bin/vincent with version info injected
```

The plain toolchain works too (`go build ./cmd/vincent`), though a binary built
that way reports its version from `debug.ReadBuildInfo` rather than from
ldflags. Other targets:

```sh
go run mage.go -l         # list every target
go run mage.go test       # go test ./...
go run mage.go testrace   # go test -race ./...   (needs cgo and a C compiler)
go run mage.go lint       # golangci-lint, pinned via the go.mod tool directive
```

See [CONTRIBUTING.md](../../CONTRIBUTING.md) for the development workflow.

## Upgrading

Use the channel's upgrade command, or replace the archive binary and restart
the daemon:

```sh
vincent daemon stop
# brew upgrade vincent
# winget upgrade --id lezli01.Vincent --exact
# scoop update vincent
# mise upgrade github:lezli01/vincent
# or unpack/install the new archive, deb, or rpm
vincent daemon start
vincent version
```

The database is migrated automatically at startup — migrations are embedded in
the binary, applied in a transaction, and append-only, so an upgrade never asks
you to run anything. Downgrading past a migration is **not** supported; back up
`{data_dir}/vincent.db` first if you plan to try.

If you installed the service, reinstall it after an upgrade that moves the
binary or after installing an agent CLI in a new location — the service records
the binary path, the directories, and (on macOS and Linux) your `PATH` at
install time:

```sh
vincent service install    # idempotent; re-registers with current values
```

## Uninstalling

```sh
vincent service uninstall   # if you installed it
vincent daemon stop
```

Then delete the binary and, if you want the state gone too, the config and data
directories listed in [Files and directories](../reference/files.md). Deleting
the data directory removes the database, transcripts and worktrees.

Installed with Homebrew, `brew uninstall --zap vincent` does all of the above in
one step — it unloads the LaunchAgent, removes the binary, and trashes the
config and data directory.

For the other managers, remove the binary only after `vincent service
uninstall`: `winget uninstall --id lezli01.Vincent --exact`, `scoop uninstall
vincent`, `mise unuse -g github:lezli01/vincent` followed by `mise uninstall
--all github:lezli01/vincent`, `sudo apt remove vincent`, `sudo dnf remove
vincent`. None of these removes vincent's config, database, transcripts, or
worktrees.

**A branch with commits on it is never deleted by vincent.** Archiving a task
deletes its branch only when that branch has no commits past its base, so
everything vincent actually wrote for you stays in your repositories until you
remove it. Ask vincent which branches it made — branch names are configurable, so
a `vincent/*` glob is not guaranteed to find them all:

```sh
vincent task ls --archived      # read the branch column
```
