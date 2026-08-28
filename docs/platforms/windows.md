# vincent on Windows

Windows is a first-class platform: the full test suite and every acceptance gate
run on it in CI alongside macOS and Linux. This page covers what is genuinely
different.

- [Install](#install)
- [Directories](#directories)
- [The shell command steps use](#the-shell-command-steps-use)
- [Running at login](#running-at-login)
- [Restricted mode and cursor](#restricted-mode-and-cursor)
- [Terminal notes](#terminal-notes)
- [Known gaps](#known-gaps)

---

## Install

WinGet is the shortest system-managed path:

```powershell
winget install --id lezli01.Vincent --exact
```

Or use vincent's Scoop bucket:

```powershell
scoop bucket add vincent https://github.com/lezli01/scoop-bucket
scoop install vincent/vincent
```

Both support x86-64 and ARM64, install Git when it is missing, and consume the
same release zip as the manual path. Stable releases update Scoop immediately;
WinGet can lag while Microsoft reviews the catalog pull request. mise is also
supported: `mise use -g github:lezli01/vincent`.

To install without a manager, unzip the release archive and put `vincent.exe`
somewhere on your `PATH`:

```powershell
Expand-Archive vincent_*_windows_amd64.zip -DestinationPath $env:LOCALAPPDATA\Programs\vincent
# add that directory to your user PATH, then reopen the terminal
vincent version
```

**SmartScreen will prompt on first run** — *"Windows protected your PC"* →
**More info** → **Run anyway**. Releases carry cosign signatures, SHA-256
checksums and GitHub build attestations, but not Authenticode code signing,
which is a recurring certificate cost this project does not take on. The prompt
appears once per binary and can also appear for WinGet, Scoop, or mise: those
channels verify the archive but do not add Authenticode signing.

Package-manager metadata moves on stable releases only. If a newly introduced
channel has not received its first stable release, use mise or the archive.

Full detail, including how to verify a download: [Installation](../getting-started/installation.md).

## Directories

| Purpose | Location |
|---|---|
| Config | `%APPDATA%\vincent\` |
| Data | `%LOCALAPPDATA%\vincent\` |

So, concretely:

```
%APPDATA%\vincent\config.yaml
%APPDATA%\vincent\workflows\*.yaml
%LOCALAPPDATA%\vincent\vincent.db
%LOCALAPPDATA%\vincent\token
%LOCALAPPDATA%\vincent\daemon.json
%LOCALAPPDATA%\vincent\logs\daemon.log
%LOCALAPPDATA%\vincent\worktrees\{task_id}\
%LOCALAPPDATA%\vincent\transcripts\{task_id}\
```

The API token is protected by the per-user ACL that `%LOCALAPPDATA%` inherits,
and `config.yaml` by the one `%APPDATA%` inherits; vincent writes no DACL of its
own. On POSIX the equivalent is an explicit `0600` on both, re-tightened on every
daemon start — which is also why `vincent doctor` never prints a `permissions`
row here: a mode carries no access control on Windows.

Both can be overridden with `VINCENT_CONFIG_DIR` and `VINCENT_DATA_DIR` — see
[Files and directories](../reference/files.md).

## The shell command steps use

`command` and `check` steps run through:

```
pwsh -NoProfile -Command "<rendered>"
```

falling back to `powershell` when PowerShell 7 is absent. Pin a different one
per step with `shell: sh | pwsh | cmd`.

Vincent makes **no attempt to translate** command steps between platforms. If a
workflow is meant to run on Windows and on POSIX, write commands that work in
both — `&&` chaining is fine everywhere, `test -f` is not — or pin the shell and
accept that the workflow is platform-specific.

A workflow that is platform-specific should say so with
[`platforms:`](../reference/workflow-schema.md#platforms). A file declaring
`platforms: [posix]` is listed here with status `unsupported` and is never
offered by the new-task picker, instead of being offered and then failing at
its first `cat`. The reverse works too: `platforms: [windows]` for a workflow
built around PowerShell.

Paths in templates come from the daemon and are Windows paths; a command step
that hard-codes `/` separators will not do what you expect.

## Running at login

The Windows backend is a **Scheduled Task**, not a Windows Service. It shows up
in Task Scheduler as `vincent` and never in `services.msc`.

```powershell
vincent service install     # from an ORDINARY prompt — see below
vincent service status
```

Three things to know:

1. **Install unelevated.** A task registered by an elevated process is owned by
   `BUILTIN\Administrators` and leaves your own account unable to replace or
   remove it — a later install or uninstall fails with `ERROR: Access is denied`.
   Installed from an ordinary prompt, `CREATOR OWNER` grants you full control and
   nothing ever needs elevation again. Both commands detect the denied case and
   print the fix.

2. **It runs with no visible window.** The daemon releases its console at
   startup, which is what keeps a terminal from sitting on your desktop after
   every logon — and what stops that window's close button from killing the
   daemon. Use `vincent service status` and `vincent daemon status` to check on
   it. A brief flash at logon is what remains, and it is cosmetic.

3. **`PATH` is *not* captured**, unlike macOS and Linux. The task runs in your
   own logon session and therefore already has your `PATH`, including
   `%APPDATA%\npm`. Freezing a copy would replace a live correct value with a
   stale one.

The task starts at **logon**, not at boot. Running with nobody logged in needs a
service account with a stored password, which vincent does not do.

### Upgrading from a version that installed a Windows Service

Older versions registered a real Windows Service, which ran as **LocalSystem** —
which is why your TUI kept starting a daemon of its own. LocalSystem resolves
`%LOCALAPPDATA%` to `C:\Windows\System32\config\systemprofile\`, so the service's
database, `daemon.json` and worktrees were written somewhere your user account
never looks, and full-auto agents ran as SYSTEM without your agent-CLI
credentials, `.gitconfig` or `PATH`.

`vincent service install` detects and refuses to run alongside it.
`vincent service uninstall` from an **elevated** prompt removes it — once,
because a machine-wide service is machine-wide. Afterwards
`vincent service install` needs no elevation again.

## Restricted mode and cursor

This is the one place a vincent capability is genuinely platform-dependent, and
it is stated rather than discovered.

**A cursor step with `permission_mode: restricted` is refused on Windows.**
`cursor-agent --sandbox enabled` exits 1 with *"Sandbox mode is enabled but not
available on this system. Sandbox requires macOS or Linux"* before doing any
work. Vincent knows this without asking the CLI — it is a fact about the adapter
and the operating system — so **creating** such a task is refused with a `400`
naming the step and the agent, rather than letting it spend a worktree and a
retry. A task that reaches the engine anyway, because the workflow changed or
the data directory came from another OS, blocks with reason
`restricted_unsupported` under the normal retry policy.

Falling back to `--force` was rejected outright: it would run full-auto a step
that explicitly asked not to be, converting a safety choice into its opposite on
exactly one OS — the failure mode a user would never think to check for.

claude and codex both restrict fine on Windows. If a workflow needs a restricted
step and must run on Windows, use one of those for that step.

## Terminal notes

- Windows Terminal (the Windows 11 default) is what the TUI is exercised
  against. Older consoles work but render box-drawing and colors less well.
- Native text selection needs the TUI's mouse handling off: press `M`, or
  shift-drag.
- `Ctrl+Shift+V` pastes; it arrives as a bracketed paste and lands in the focused
  field. `ctrl+v` is a fallback for terminals that pass the key through instead.
- `$EDITOR` is honored for edit-and-retry and for description editing. Set it to
  something that runs in the terminal and exits when done — a GUI editor that
  forks immediately will look like it did nothing.

## Known gaps

| Thing | Status |
|---|---|
| Cursor `restricted` mode | **Unavailable** — task creation refuses it, and a task that gets past that fails; it never downgrades |
| Authenticode code signing | Not done — SmartScreen prompts once |
| Boot-time start (no logon) | Not supported — the task is logon-triggered |
| Windows Service backend | Removed in favor of a Scheduled Task, deliberately |

Everything else — the daemon, the API, the TUI, all three adapters, the
scheduler, crash recovery, worktrees, transcripts, retention — behaves
identically to the POSIX platforms, and CI proves it on every pull request.

---

## See also

- [Running at login](../guides/running-at-login.md)
- [Files and directories](../reference/files.md)
- [Troubleshooting](../guides/troubleshooting.md)
