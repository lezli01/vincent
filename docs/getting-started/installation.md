# Installation

vincent is one self-contained binary. There is no runtime to install, no CGO,
and no database server — the store is an embedded SQLite file.

- [What you need](#what-you-need)
- [Homebrew (macOS)](#homebrew-macos)
- [Download a release](#download-a-release)
- [First launch is flagged](#first-launch-is-flagged)
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

This is the shortest path on macOS, and the only one that does not make you
clear the quarantine attribute by hand — the cask does it during install, so
[First launch is flagged](#first-launch-is-flagged) does not apply here.

Homebrew casks are macOS-only. On Linuxbrew, use the archive below.

Upgrade with `brew upgrade vincent`. To remove vincent along with its
LaunchAgent, its config and its task history:

```sh
brew uninstall --zap vincent
```

Plain `brew uninstall vincent` removes the binary and unloads the LaunchAgent
but leaves `~/Library/Application Support/vincent` intact.

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

Each archive contains the binary, `LICENSE`, `COMMERCIAL-LICENSE.md`,
`README.md`, and the [example workflows](../../examples). vincent is
source-available and dual-licensed: free for personal and non-commercial use
under the PolyForm Noncommercial License 1.0.0, with a separate commercial
license required for business use.

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

## First launch is flagged

Releases carry cosign signatures, SHA-256 checksums and GitHub build
attestations — but **not OS code signing**. Authenticode certificates and Apple
notarization are recurring costs this project does not take on, so the first
launch of a **downloaded archive** prompts. (Installing with
[Homebrew](#homebrew-macos) avoids this — the cask clears the attribute for
you.)

- **macOS** — *"cannot be opened because it is from an unidentified developer"*.
  Clear the quarantine attribute once:

  ```sh
  xattr -d com.apple.quarantine /usr/local/bin/vincent
  ```

- **Windows** — SmartScreen shows *"Windows protected your PC"*. Choose
  **More info → Run anyway**. It appears once per binary.

- **Linux** — nothing to clear. Make sure the file is executable
  (`chmod +x vincent`) if your extraction tool dropped the bit.

## Verify a download

The signature is over `checksums.txt`, and `checksums.txt` covers every
archive — so verifying the signature and then the checksum covers the binary
you are about to run. Download `checksums.txt`, `checksums.txt.sig` and
`checksums.txt.pem` from the same release:

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
it. Every archive additionally carries a build provenance attestation, which
`gh attestation verify vincent_*_linux_amd64.tar.gz --repo lezli01/vincent`
checks.

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

Replace the binary and restart the daemon:

```sh
vincent daemon stop
# unpack the new archive over the old binary — or: brew upgrade vincent
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

**A branch with commits on it is never deleted by vincent.** Archiving a task
deletes its branch only when that branch has no commits past its base, so
everything vincent actually wrote for you stays in your repositories until you
remove it. Ask vincent which branches it made — branch names are configurable, so
a `vincent/*` glob is not guaranteed to find them all:

```sh
vincent task ls --archived      # read the branch column
```
