# vincent on macOS

Apple silicon and Intel are both released and both exercised in CI.

- [Install](#install)
- [Gatekeeper](#gatekeeper)
- [Directories](#directories)
- [Running at login](#running-at-login)
- [The PATH problem](#the-path-problem)
- [Terminal notes](#terminal-notes)

---

## Install

```sh
tar -xzf vincent_*_darwin_arm64.tar.gz      # or _darwin_amd64 on Intel
sudo mv vincent /usr/local/bin/
vincent version
```

`~/.local/bin` works just as well if you would rather avoid `sudo`. Full
detail, including signature verification:
[Installation](../getting-started/installation.md).

## Gatekeeper

First launch shows *"vincent cannot be opened because it is from an
unidentified developer"*. Releases carry cosign signatures, SHA-256 checksums
and GitHub build attestations, but not **Apple notarization**, which is a
recurring cost this project does not take on.

Clear the quarantine attribute once:

```sh
xattr -d com.apple.quarantine /usr/local/bin/vincent
```

If `xattr` reports the attribute is absent, the file was never quarantined and
there is nothing to do.

## Directories

macOS is the one platform where config and data are nested under the same root:

| Purpose | Location |
|---|---|
| Config | `~/Library/Application Support/vincent/` |
| Data | `~/Library/Application Support/vincent/data/` |

```
~/Library/Application Support/vincent/config.yaml
~/Library/Application Support/vincent/workflows/*.yaml
~/Library/Application Support/vincent/data/vincent.db
~/Library/Application Support/vincent/data/token          # 0600
~/Library/Application Support/vincent/data/daemon.json
~/Library/Application Support/vincent/data/logs/daemon.log
~/Library/Application Support/vincent/data/worktrees/{task_id}/
~/Library/Application Support/vincent/data/transcripts/{task_id}/
```

Note the space in the path when scripting — quote it, or use
`~/Library/Application\ Support/vincent`. Both directories are overridable with
`VINCENT_CONFIG_DIR` and `VINCENT_DATA_DIR`; see
[Files and directories](../reference/files.md).

## Running at login

```sh
vincent service install
vincent service status
```

That writes a **LaunchAgent** to `~/Library/LaunchAgents` — a per-user agent,
never a root LaunchDaemon, and no elevation is required. The OS user is
vincent's trust boundary: an agent gets your privileges, your agent-CLI logins
and your git identity, and nothing more.

`KeepAlive` is conditional on a **non-clean** exit. A daemon that exits 0 was
asked to stop, and relaunching it would make `vincent daemon stop` impossible.

The agent starts at login, which is the same promise the Linux and Windows
backends make.

## The PATH problem

This is the macOS-specific gotcha, and it is worth understanding rather than
just working around.

launchd hands a service a **minimal** `PATH` — `/usr/bin:/bin:/usr/sbin:/sbin`.
Every agent CLI installs outside it: Homebrew (`/opt/homebrew/bin` on Apple
silicon, `/usr/local/bin` on Intel), an npm prefix, an nvm shim directory,
`~/.local/bin`.

Left alone, an installed service therefore resolves **no agent CLI at all**,
while the same daemon started by hand finds every one — the daemon runs, the TUI
lists every adapter as missing, and nothing in either says why.

So `vincent service install` **captures the `PATH` of the shell that ran it**,
along with the config and data directories, and writes them into the plist. The
shell you install from has, by construction, the `PATH` that works.

Two consequences:

- **It goes stale.** Install an agent CLI somewhere new and run
  `vincent service install` again to recapture. That is the same
  "reinstall to recapture" contract the directories have.
- **It is not the only answer.** `agents.<name>.path` in
  [`config.yaml`](../reference/configuration.md) is absolute and never consults
  `PATH`:

  ```yaml
  agents:
    claude: { path: "/opt/homebrew/bin/claude" }
  ```

Verify with `vincent daemon status` or the TUI's daemon view: if the adapter list
is empty under a service that is running, this is why.

## Terminal notes

- Native text selection needs the TUI's mouse handling off: press `M`, or
  hold ⇧ while dragging.
- `Cmd+V` pastes; it arrives as a bracketed paste and lands in the focused
  field with no key involved.
- `$EDITOR` is honored for edit-and-retry and description editing. Use something
  that runs in the terminal and blocks until you close it — `vim`, `nano`, or
  `code --wait`.
- Everything on this platform behaves the same as Linux, including `restricted`
  permission mode for all three adapters.

---

## See also

- [Running at login](../guides/running-at-login.md)
- [Files and directories](../reference/files.md)
- [Troubleshooting](../guides/troubleshooting.md)
