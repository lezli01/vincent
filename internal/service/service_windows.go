//go:build windows

package service

import (
	"context"
	"fmt"
	"os"
	"os/user"
	"strings"
	"unicode/utf16"

	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/daemon"
)

// The Windows backend registers a **Scheduled Task that runs at logon as the
// invoking user**, not a Windows Service (T4.17, revising the PR W decision).
//
// A Windows Service was the original choice and it was the wrong one, for a
// reason no rendering test could show. The SCM has no per-user services, and
// an empty ServiceStartName defaults to LocalSystem — so the daemon resolved
// LOCALAPPDATA to the SYSTEM profile and wrote its database, token and
// daemon.json to C:\Windows\System32\config\systemprofile\..., where the TUI
// never looks. Every TUI launch found nothing, auto-started a second daemon in
// the user's own data dir, and the two never met. Pinning the dirs alone would
// have hidden that symptom behind a worse defect: §16's full-auto agents,
// which are meant to run with exactly the invoking user's privileges, would
// have been running as SYSTEM, without that user's agent-CLI credentials,
// .gitconfig, or PATH.
//
// A logon-triggered task fixes the cause. It runs in the user's own session
// with the user's token, so the dirs, the credentials and the environment are
// the ones the CLI already uses, and it needs no elevation to install. §12.1
// already said service registration is per-user on every platform because the
// OS user is the trust boundary; Windows is now the third platform where that
// is true rather than the exception.
//
// What it costs is logout survival: the task starts at the next logon rather
// than at boot. That is exactly what a launchd LaunchAgent does, and what a
// systemd user unit does without lingering, so the promise §12.1 makes is now
// the same one on all three platforms. A machine that must run a daemon with
// nobody logged in wants a service account with a real password, which is a
// different feature and not this one.
const taskTemplate = `<?xml version="1.0" encoding="UTF-16"?>
<Task version="1.2" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">
  <RegistrationInfo>
    <Author>%[1]s</Author>
    <Description>Runs AI coding-agent workloads in the background (https://github.com/lezli01/vincent).</Description>
    <URI>\%[2]s</URI>
  </RegistrationInfo>
  <Triggers>
    <LogonTrigger>
      <Enabled>true</Enabled>
      <UserId>%[1]s</UserId>
    </LogonTrigger>
  </Triggers>
  <Principals>
    <Principal id="Author">
      <UserId>%[1]s</UserId>
      <LogonType>InteractiveToken</LogonType>
      <RunLevel>LeastPrivilege</RunLevel>
    </Principal>
  </Principals>
  <Settings>
    <MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>
    <DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries>
    <StopIfGoingOnBatteries>false</StopIfGoingOnBatteries>
    <AllowHardTerminate>true</AllowHardTerminate>
    <StartWhenAvailable>false</StartWhenAvailable>
    <RunOnlyIfNetworkAvailable>false</RunOnlyIfNetworkAvailable>
    <IdleSettings>
      <StopOnIdleEnd>false</StopOnIdleEnd>
      <RestartOnIdle>false</RestartOnIdle>
    </IdleSettings>
    <AllowStartOnDemand>true</AllowStartOnDemand>
    <Enabled>true</Enabled>
    <Hidden>false</Hidden>
    <RunOnlyIfIdle>false</RunOnlyIfIdle>
    <WakeToRun>false</WakeToRun>
    <ExecutionTimeLimit>PT0S</ExecutionTimeLimit>
    <Priority>7</Priority>
    <RestartOnFailure>
      <Interval>PT1M</Interval>
      <Count>3</Count>
    </RestartOnFailure>
  </Settings>
  <Actions Context="Author">
    <Exec>
      <Command>%[3]s</Command>
      <Arguments>%[4]s</Arguments>
    </Exec>
  </Actions>
</Task>
`

// Four settings above are load-bearing, and three of them are Task Scheduler
// defaults that would each stop the daemon on their own:
//
//   - ExecutionTimeLimit PT0S — the default is P3D, which kills a task after
//     three days. A daemon whose whole point is surviving reboots must not
//     have a run limit.
//   - DisallowStartIfOnBatteries / StopIfGoingOnBatteries false — both default
//     to true, so on a laptop the daemon would refuse to start on battery and
//     be stopped mid-task when the charger came out.
//   - StopOnIdleEnd false — the default stops the task when the machine stops
//     being idle, i.e. when the user comes back.
//
// RestartOnFailure is the analog of systemd's Restart=on-failure and launchd's
// KeepAlive/SuccessfulExit=false, and works for the same reason: Task
// Scheduler treats a nonzero exit as a failure, so a daemon that exits 0
// because it was asked to stop is left stopped. An unconditional restart would
// make `vincent daemon stop` impossible.

// renderTask fills the template. Split out so the definition is testable
// without a Task Scheduler to register it into.
func renderTask(o Options, userID string) string {
	args := fmt.Sprintf("daemon --config-dir %s --data-dir %s",
		quoteArg(o.Dirs.Config), quoteArg(o.Dirs.Data))
	return fmt.Sprintf(taskTemplate,
		xmlEscape(userID), Label, xmlEscape(o.Exe), xmlEscape(args))
}

// The dirs travel as **arguments**, not as environment variables, because a
// task's Exec action has no environment to set: the definition holds a
// command, its arguments and a working directory, and nothing else. The flags
// land in the same place the plist's and the unit's environment does —
// config.ResolveDirs — so §12.2 still has one resolution point.
//
// PATH is deliberately *not* pinned here, and Windows is the one platform
// where that is right. A task with an InteractiveToken principal runs with the
// user's own logon environment, so it already has the user's PATH, including
// the npm prefix in %APPDATA%\npm that T4.15 was about. Freezing a captured
// copy of it would replace a live correct value with a stale one.

// quoteArg renders one Windows command-line argument. Task Scheduler hands
// <Arguments> to CreateProcess verbatim and the daemon parses it back with
// CommandLineToArgvW rules, under which a run of backslashes immediately
// before the closing quote is doubled — without that, a data dir ending in a
// separator would escape its own quote and swallow the rest of the line.
func quoteArg(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	slashes := 0
	for _, r := range s {
		switch r {
		case '\\':
			slashes++
		case '"':
			// Escape the quote, and the backslashes that precede it.
			b.WriteString(strings.Repeat(`\`, slashes+1))
			slashes = 0
		default:
			slashes = 0
		}
		b.WriteRune(r)
	}
	b.WriteString(strings.Repeat(`\`, slashes))
	b.WriteByte('"')
	return b.String()
}

// currentUserID identifies the account the task runs as.
//
// It is the SID rather than DOMAIN\name on purpose: an Entra-joined machine
// reports names like AzureAD\First.Last, whose resolution depends on the
// account being cached, while the SID is what Task Scheduler stores anyway and
// always validates.
func currentUserID() (string, error) {
	u, err := user.Current()
	if err != nil {
		return "", fmt.Errorf("resolve current user: %w", err)
	}
	if u.Uid != "" {
		return u.Uid, nil
	}
	return u.Username, nil
}

func install(ctx context.Context, o Options) error {
	// A leftover LocalSystem service from before T4.17 would keep its own
	// daemon, its own database and its own SYSTEM-privileged agents running
	// beside the new task. Refuse rather than stack them.
	if legacyServiceInstalled(ctx) {
		return errLegacyService
	}
	uid, err := currentUserID()
	if err != nil {
		return err
	}
	path, err := writeTaskXML(renderTask(o, uid))
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(path) }()

	// /F replaces an existing registration rather than failing, matching the
	// idempotence of `daemon start` and of the other two backends.
	if _, err := schtasks(ctx, "/Create", "/TN", Label, "/XML", path, "/F"); err != nil {
		return err
	}
	// The trigger covers every future logon; /Run covers *now*, so install and
	// start are one step as they are on the other two platforms. A daemon that
	// is already serving this data dir is the desired state already — starting
	// the task would only have it exit on the single-instance lock and burn
	// its three restart attempts.
	if running, err := daemon.ProbeRunning(o.Dirs.Data); err == nil && running {
		return nil
	}
	if _, err := schtasks(ctx, "/Run", "/TN", Label); err != nil {
		return err
	}
	return nil
}

// LingerFailed has no Windows analog: a logon-triggered task starts at every
// logon on its own. It exists so callers stay platform-agnostic.
func LingerFailed(error) bool { return false }

func uninstall(ctx context.Context) error {
	legacy := legacyServiceInstalled(ctx)
	installed := taskInstalled(ctx)
	if !legacy && !installed {
		return ErrNotInstalled
	}
	if legacy {
		if err := removeLegacyService(); err != nil {
			return err
		}
	}
	if !installed {
		return nil
	}
	// /End is a hard terminate. The SCM's Stop control used to give the daemon
	// §12.4's shutdown, and losing that would kill running agent processes
	// mid-step on the one platform this change is meant to fix, so ask the
	// daemon to stop itself first. Best effort throughout: an unreachable or
	// already-stopped daemon must not block removing the registration, which
	// is the thing actually asked for.
	stopDaemon(ctx)
	_, _ = schtasks(ctx, "/End", "/TN", Label)
	if _, err := schtasks(ctx, "/Delete", "/TN", Label, "/F"); err != nil {
		return err
	}
	return nil
}

// stopDaemon asks a running daemon to shut down gracefully, ignoring every
// failure. It is only ever called when the task is registered, so the daemon
// holding the lock is the one the task started (§12.1's single instance).
func stopDaemon(ctx context.Context) {
	dirs, err := config.ResolveDirs()
	if err != nil {
		return
	}
	if running, err := daemon.ProbeRunning(dirs.Data); err != nil || !running {
		return
	}
	ri, err := daemon.ReadRuntimeInfo(dirs.Data)
	if err != nil {
		return
	}
	token, err := daemon.ReadToken(dirs.Data)
	if err != nil {
		return
	}
	_ = daemon.RequestStop(ctx, ri.Port, token)
}

func query(ctx context.Context) (Status, error) {
	st := Status{Name: Label, Unit: `\` + Label}
	if taskInstalled(ctx) {
		st.Installed = true
		st.Detail = "Scheduled Task, at logon"
		st.Running = taskRunning(ctx)
		return st, nil
	}
	// A pre-T4.17 service is still something installed, and saying so is the
	// only way its owner learns why their TUI keeps starting a second daemon.
	if legacyServiceInstalled(ctx) {
		st.Installed = true
		st.Detail = "legacy Windows Service, running as LocalSystem — " +
			"run `vincent service uninstall` from an elevated prompt, then install again"
		return st, nil
	}
	return st, nil
}

// taskInstalled reports whether the registration exists. `schtasks /Query`
// exits nonzero when the name is unknown, which is the answer rather than an
// error.
func taskInstalled(ctx context.Context) bool {
	_, err := schtasks(ctx, "/Query", "/TN", Label)
	return err == nil
}

// taskRunning reports whether the task's process is alive.
//
// It asks PowerShell rather than parsing `schtasks /Query`, whose Status
// column is **localized** — "Wird ausgeführt" on a German Windows, and so on
// for every UI language — while Get-ScheduledTask returns the invariant CIM
// enum name. A missing or unreadable PowerShell reports "not running" rather
// than failing the status command.
func taskRunning(ctx context.Context) bool {
	out, err := powershell(ctx,
		"(Get-ScheduledTask -TaskName '"+Label+"' -ErrorAction SilentlyContinue).State")
	if err != nil {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(out), "Running")
}

// writeTaskXML writes the definition to a temp file for `schtasks /Create
// /XML`, encoded UTF-16LE with a BOM.
//
// The encoding is not incidental. schtasks rejects a UTF-8 file that is not
// pure ASCII with "the task XML contains a value which is incorrectly
// formatted or out of range" — naming neither the value nor the encoding — and
// this definition is not pure ASCII: a Windows profile directory carries
// whatever characters the account name has.
func writeTaskXML(doc string) (path string, err error) {
	f, err := os.CreateTemp("", "vincent-task-*.xml")
	if err != nil {
		return "", fmt.Errorf("create task definition: %w", err)
	}
	defer func() {
		if cerr := f.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("write task definition: %w", cerr)
		}
		if err != nil {
			_ = os.Remove(f.Name())
		}
	}()
	if _, err := f.Write(utf16LE(doc)); err != nil {
		return "", fmt.Errorf("write task definition: %w", err)
	}
	return f.Name(), nil
}

// utf16LE encodes s as little-endian UTF-16 with a byte-order mark.
func utf16LE(s string) []byte {
	units := utf16.Encode([]rune(s))
	b := make([]byte, 0, 2+len(units)*2)
	b = append(b, 0xFF, 0xFE) // BOM
	for _, u := range units {
		b = append(b, byte(u), byte(u>>8))
	}
	return b
}
