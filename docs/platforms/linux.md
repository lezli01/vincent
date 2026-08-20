# vincent on Linux

x86-64 and ARM64 are both released, and Linux is the platform CI runs the
acceptance gates on first.

- [Install](#install)
- [Directories](#directories)
- [Running at login](#running-at-login)
- [Surviving logout](#surviving-logout)
- [The PATH problem](#the-path-problem)
- [Terminal notes](#terminal-notes)

---

## Install

Stable releases attach deb and rpm packages for both supported architectures:

```sh
# Debian / Ubuntu (use _arm64.deb on ARM64)
sudo apt install ./vincent_*_amd64.deb

# Fedora / RHEL family (use .aarch64.rpm on ARM64)
sudo dnf install ./vincent-*.x86_64.rpm
```

Download the package from the
[latest release](https://github.com/lezli01/vincent/releases/latest) first.
These are release assets rather than an apt/dnf repository, so install the next
downloaded package the same way to upgrade. Both formats put the binary at
`/usr/bin/vincent`, depend on git, carry the license documents, and deliberately
do not create a root-owned service for Vincent's per-user daemon.

On Arch Linux, install the AUR binary package with your normal helper or build
it directly:

```sh
git clone https://aur.archlinux.org/vincent-agent-bin.git
cd vincent-agent-bin
makepkg -si
```

mise works across distributions without a distro package:

```sh
mise use -g github:lezli01/vincent
```

Package-manager metadata moves on stable releases only. If a newly introduced
channel has not received its first stable release, use mise or the archive:

```sh
tar -xzf vincent_*_linux_amd64.tar.gz       # or _linux_arm64
sudo mv vincent /usr/local/bin/
vincent version
```

`~/.local/bin` avoids `sudo` entirely. Nothing needs clearing on first launch —
Linux has no Gatekeeper or SmartScreen equivalent — though you may need
`chmod +x vincent` if your extraction tool dropped the bit.

Full detail, including signature verification:
[Installation](../getting-started/installation.md).

## Directories

XDG, with the standard fallbacks:

| Purpose | Location | Env override honored |
|---|---|---|
| Config | `~/.config/vincent/` | `XDG_CONFIG_HOME` |
| Data | `~/.local/share/vincent/` | `XDG_DATA_HOME` |

```
~/.config/vincent/config.yaml
~/.config/vincent/workflows/*.yaml
~/.local/share/vincent/vincent.db
~/.local/share/vincent/token             # 0600
~/.local/share/vincent/daemon.json
~/.local/share/vincent/logs/daemon.log
~/.local/share/vincent/worktrees/{task_id}/
~/.local/share/vincent/transcripts/{task_id}/
```

`VINCENT_CONFIG_DIR` and `VINCENT_DATA_DIR` override both outright — see
[Files and directories](../reference/files.md).

## Running at login

```sh
vincent service install
vincent service status
systemctl --user status vincent          # the OS's own view
```

That writes a **systemd user unit** to `~/.config/systemd/user` — per-user, no
elevation, no `sudo systemctl`. The OS user is vincent's trust boundary: an
agent gets your privileges, your agent-CLI logins and your git identity, and
nothing more.

`Restart=on-failure`, not `always`. A daemon that exits 0 was asked to stop, and
restarting it would make `vincent daemon stop` impossible.

Read its logs either way you prefer:

```sh
journalctl --user -u vincent -f
tail -f ~/.local/share/vincent/logs/daemon.log
```

## Surviving logout

A systemd user manager stops when your last session ends, taking the daemon with
it. Lingering is what changes that:

```sh
loginctl enable-linger "$USER"
```

`vincent service install` **attempts this itself** and, if it cannot, prints the
exact command for you to run. The service is installed and running either way —
this is a warning, not a failed install.

Check it:

```sh
loginctl show-user "$USER" --property=Linger
```

Without lingering the daemon still survives everything except logging out
entirely; with it, the daemon comes up at boot and stays up across logins.

## The PATH problem

A systemd user manager supplies a minimal `PATH` — barely wider than
`/usr/bin:/bin` — and every agent CLI installs outside it: an npm prefix, an nvm
shim directory, `~/.local/bin`, a distro package in `/opt`.

Left alone, an installed service therefore resolves **no agent CLI at all**,
while the same daemon started by hand finds every one. So
`vincent service install` **captures the `PATH` of the shell that ran it**,
along with the config and data directories, and writes them into the unit.

Two consequences:

- **It goes stale.** Install a CLI somewhere new, then run
  `vincent service install` again to recapture — the same "reinstall to
  recapture" contract the directories have.
- **It is not the only answer.** `agents.<name>.path` in
  [`config.yaml`](../reference/configuration.md) is absolute and never consults
  `PATH`:

  ```yaml
  agents:
    codex: { path: "/home/me/.local/bin/codex" }
  ```

If `vincent daemon status` under a running service lists no adapters, this is
why. Reinstall from a shell where `which claude` works.

## Terminal notes

- Native text selection needs the TUI's mouse handling off: press `M`, or hold
  shift while dragging.
- `Ctrl+Shift+V` pastes; it arrives as a bracketed paste and lands in the
  focused field.
- `$EDITOR` is honored for edit-and-retry and description editing. It must run
  in the terminal and block until closed.
- All three adapters support `restricted` permission mode here, including
  cursor — the sandbox that is unavailable on Windows works fine on Linux.

---

## See also

- [Running at login](../guides/running-at-login.md)
- [Files and directories](../reference/files.md)
- [Troubleshooting](../guides/troubleshooting.md)
