# Running at login

`vincent service install` registers the daemon with your operating system so it
starts at login and survives a reboot. Without it the daemon lives only as long
as something started it.

```sh
vincent service install     # register and start
vincent service status      # installed? running?
vincent service uninstall   # stop and remove
```

- [It always runs as you](#it-always-runs-as-you)
- [What gets installed](#what-gets-installed)
- [What is captured at install time](#what-is-captured-at-install-time)
- [Windows specifics](#windows-specifics)
- [Linux and logout](#linux-and-logout)
- [Reinstalling](#reinstalling)
- [Verifying it worked](#verifying-it-worked)

---

## It always runs as you

On every platform, the service is a **per-user** registration and needs **no
elevation** to install, uninstall or query.

That is not a convenience choice. The OS user is vincent's trust boundary: an
agent gets your privileges, your agent-CLI logins and your git identity, and
nothing more. A machine-wide service would run agents as a system account —
with more privilege and none of your credentials, which is the worst of both.

## What gets installed

| Platform | Mechanism | Where |
|---|---|---|
| macOS | LaunchAgent | `~/Library/LaunchAgents` |
| Linux | systemd **user** unit | `~/.config/systemd/user` |
| Windows | Scheduled Task, triggered at logon | Task Scheduler, named `vincent` |

Restart policy is deliberately conditional on a *non-clean* exit — launchd's
`KeepAlive` on failure, systemd's `Restart=on-failure`, the Scheduled Task's
`RestartOnFailure`. A daemon that exited 0 was asked to stop, and relaunching it
would make `vincent daemon stop` impossible.

The registration starts the daemon **at logon**, not at boot, on all three
platforms — that is what a LaunchAgent does, what a systemd user unit without
lingering does, and now what the Windows task does. Running with nobody logged
in requires a service account with a stored password, which is a different
feature and not one vincent has.

## What is captured at install time

A service does not inherit the shell that installed it, so the installer writes
the values in effect into the unit.

### The directories, everywhere

`VINCENT_CONFIG_DIR` and `VINCENT_DATA_DIR` overrides would otherwise apply to
your CLI and not to the service, and the two would silently use different
databases. So the resolved directories travel with the registration — as
environment variables in the plist and the unit, and as `--config-dir` /
`--data-dir` arguments on Windows, where a scheduled task's action has no
environment at all.

### `PATH` too, on macOS and Linux

A service manager supplies its own minimal `PATH`. launchd's is
`/usr/bin:/bin:/usr/sbin:/sbin`; a systemd user manager's is barely wider. Every
agent CLI installs outside it — Homebrew, an npm prefix, an nvm shim directory,
`~/.local/bin`.

Left alone, an installed service therefore resolves **no agent CLI at all**,
while the same daemon started by hand finds every one of them: the daemon runs,
the TUI lists every adapter as missing, and nothing in either says why. The
shell running `service install` has, by construction, the `PATH` that works, so
that is what gets captured.

Two consequences, both deliberate:

- **The captured `PATH` goes stale.** Install a CLI somewhere new and you want
  `vincent service install` again to recapture it — the same "reinstall to
  recapture" contract the directories already have.
- **Windows does not capture it.** The task runs in your logon session and
  therefore already has your own `PATH`, including `%APPDATA%\npm`. Freezing a
  copy would replace a live correct value with a stale one.

On every platform the standing answer to an agent that will not resolve is the
[`agents.<name>.path`](../reference/configuration.md) config key, which is
absolute and never consults `PATH`.

## Windows specifics

The Windows backend is a **Scheduled Task**, not a Windows Service. It appears
in Task Scheduler as `vincent` and never in `services.msc`. It runs with no
visible window, so `vincent service status` and `vincent daemon status` are how
you check on it.

Four scheduler defaults are overridden, because each one would otherwise stop a
long-running daemon: the three-day execution time limit is removed, both battery
settings are off, and stop-on-idle-end is off.

### Install from an ordinary prompt

A task registered by an **elevated** process is owned by
`BUILTIN\Administrators`, and the ACL Task Scheduler writes leaves your own
account read-only — so a later install or uninstall from an ordinary prompt
fails with `ERROR: Access is denied`, naming neither the owner nor the remedy.

Installed unelevated, `CREATOR OWNER` grants your account full control and every
later install and uninstall needs no elevation. Both commands detect the denied
case and tell you the elevated `uninstall` that clears it.

### The console flash

The daemon **releases** its console at startup when it owns it, which is what
keeps a terminal window from sitting on your desktop after every logon — and
what stops that window's close button from killing the daemon. Passed by hand in
a terminal the flag does nothing, rather than taking your own shell down.

One brief flash between the scheduler creating the process and the daemon
reaching that call is what remains. It is cosmetic.

### Upgrading from a pre-Scheduled-Task version

Older versions installed a Windows *Service* that ran as LocalSystem — which is
why your TUI kept starting a daemon of its own: the service resolved
`%LOCALAPPDATA%` to the SYSTEM profile and wrote its database somewhere your
user account never looks.

`vincent service install` detects and refuses to run alongside it.
`vincent service uninstall` removes it — from an elevated prompt, **once**,
because a machine-wide service is machine-wide. After that,
`vincent service install` needs no elevation ever again.

## Linux and logout

The user unit starts at login and stops at logout unless lingering is enabled:

```sh
loginctl enable-linger "$USER"
```

The installer attempts this itself and, if it cannot, prints the exact command
for you to run. Either way the service is installed and running — this is a
warning, not a failed install. Without lingering, the daemon still survives
everything except logging out entirely.

## Reinstalling

`vincent service install` is idempotent: run it again to re-register with
current values. Do that after:

- upgrading vincent, if the binary moved
- installing an agent CLI somewhere new (macOS/Linux — `PATH` capture)
- changing `VINCENT_CONFIG_DIR` or `VINCENT_DATA_DIR`

## Verifying it worked

```sh
vincent service status     # installed / running, per the OS's own view
vincent daemon status      # 0 healthy, 1 not running, 2 unresponsive
vincent                    # the TUI's daemon view lists resolved adapters
```

The check that actually matters is the adapter list: a service that runs but
resolves no agents is the failure mode this page exists to prevent. If that is
what you see, reinstall from a shell where `which claude` works.

Log out and back in — or reboot — and run the same three commands. That is the
whole acceptance test.

---

## See also

- [Windows](../platforms/windows.md) · [macOS](../platforms/macos.md) ·
  [Linux](../platforms/linux.md)
- [Files and directories](../reference/files.md)
- [Troubleshooting](troubleshooting.md)
