package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/daemon"
	"github.com/lezli01/vincent/internal/release"
	"github.com/lezli01/vincent/internal/version"
)

const (
	// startTimeout bounds the `daemon start` health poll (phase 1 decision).
	startTimeout = 10 * time.Second
	// stopTimeout exceeds the daemon's own shutdown grace so a graceful stop
	// is never reported as a failure prematurely: §12.4's 15 s process grace
	// plus the HTTP drain must fit inside it (PR D decision).
	stopTimeout = 30 * time.Second
	// pollInterval paces lifecycle polling loops.
	pollInterval = 100 * time.Millisecond
)

func newDaemonCmd() *cobra.Command {
	var (
		dirs        config.Dirs
		hideConsole bool
	)
	cmd := &cobra.Command{
		Use:   "daemon",
		Short: "Run the vincent daemon in the foreground",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := applyDirFlags(dirs); err != nil {
				return err
			}
			// Before anything slow, so the window the Scheduled Task's action
			// gets is gone in the time the daemon takes to open its database
			// rather than for as long as it runs (T4.20). What it did travels
			// into the startup log record (T4.21).
			var console string
			if hideConsole {
				console = daemon.DetachConsole()
			}
			// RunManaged is Run everywhere but Windows, where it speaks the
			// SCM's control protocol when the process was started as a
			// service (§12.1). Foreground stays true: under a service manager
			// stderr is captured by the manager's own log, and losing it
			// there would make a failed service start undiagnosable.
			return daemon.RunManaged(cmd.Context(),
				daemon.Options{Foreground: true, Console: console})
		},
	}
	// These exist for the Windows Scheduled Task (T4.17), whose Exec action
	// carries a command and arguments and has no environment to set — while
	// the launchd plist and the systemd unit pin the same two directories with
	// VINCENT_CONFIG_DIR/VINCENT_DATA_DIR. Both routes end at the environment
	// config.ResolveDirs reads, so §12.2 keeps one resolution point rather
	// than growing a second precedence rule.
	cmd.Flags().StringVar(&dirs.Config, "config-dir", "",
		"config directory to run against (default $VINCENT_CONFIG_DIR, else the platform default)")
	cmd.Flags().StringVar(&dirs.Data, "data-dir", "",
		"data directory to run against (default $VINCENT_DATA_DIR, else the platform default)")
	// Windows-only in effect, and set by the same Scheduled Task action: the
	// scheduler runs it on the user's desktop, where a console-subsystem binary
	// is given a console window that closing would kill the daemon. The console
	// is kept unless this process is its only owner, so passing this in a
	// terminal by hand cannot take that terminal down.
	//
	// The name outlived the mechanism — T4.21 releases the console rather than
	// hiding its window — and is kept because every task registered by T4.20
	// carries this spelling in its definition. Renaming it would leave those
	// registrations passing a flag the daemon no longer accepts: a daemon that
	// fails to start at logon, to fix a word.
	cmd.Flags().BoolVar(&hideConsole, "hide-console", false,
		"detach from the console the OS allocated for this process (Windows only; no-op elsewhere)")
	cmd.AddCommand(newDaemonStartCmd(), newDaemonStopCmd(), newDaemonStatusCmd(),
		newDaemonLogsCmd(), newDaemonBackupCmd(), newDaemonRestoreCmd())
	return cmd
}

// applyDirFlags publishes the flags as the directory overrides, so everything
// downstream — startup, hot reload, and any child process that inherits the
// environment — resolves them the one way §12.2 describes.
func applyDirFlags(dirs config.Dirs) error {
	for _, o := range []struct{ env, val string }{
		{config.EnvConfigDir, dirs.Config},
		{config.EnvDataDir, dirs.Data},
	} {
		if o.val == "" {
			continue
		}
		if err := os.Setenv(o.env, o.val); err != nil {
			return fmt.Errorf("set %s: %w", o.env, err)
		}
	}
	return nil
}

func newDaemonStartCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "start",
		Short: "Start the daemon in the background",
		RunE: func(cmd *cobra.Command, _ []string) error {
			out := cmd.OutOrStdout()
			dirs, err := config.ResolveDirs()
			if err != nil {
				return err
			}
			// Desired-state semantics: an already-healthy daemon is success.
			if running, err := daemon.ProbeRunning(dirs.Data); err != nil {
				return err
			} else if running {
				ri, err := daemon.ReadRuntimeInfo(dirs.Data)
				if err == nil {
					_, _ = fmt.Fprintf(out, "daemon already running (pid %d, port %d)\n", ri.PID, ri.Port)
				} else {
					_, _ = fmt.Fprintln(out, "daemon already running")
				}
				return nil
			}
			pid, err := daemon.StartDetached()
			if err != nil {
				return err
			}
			deadline := time.Now().Add(startTimeout)
			for time.Now().Before(deadline) {
				// Match the child pid so a stale daemon.json from a previous
				// crash can't be mistaken for the daemon we just spawned.
				ri, err := daemon.ReadRuntimeInfo(dirs.Data)
				if err == nil && ri.PID == pid {
					if _, err := daemon.CheckHealth(cmd.Context(), ri.Port); err == nil {
						_, _ = fmt.Fprintf(out, "daemon started (pid %d, port %d)\n", ri.PID, ri.Port)
						return nil
					}
				}
				time.Sleep(pollInterval)
			}
			return fmt.Errorf("daemon did not become healthy within %s\n--- daemon.log tail ---\n%s",
				startTimeout, daemon.LogTail(dirs.Data, 20))
		},
	}
}

func newDaemonStopCmd() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "stop",
		Short: "Stop the daemon gracefully",
		RunE: func(cmd *cobra.Command, _ []string) error {
			out := cmd.OutOrStdout()
			dirs, err := config.ResolveDirs()
			if err != nil {
				return err
			}
			running, err := daemon.ProbeRunning(dirs.Data)
			if err != nil {
				return err
			}
			if !running {
				_, _ = fmt.Fprintln(out, "daemon is not running")
				return nil
			}
			ri, err := daemon.ReadRuntimeInfo(dirs.Data)
			if err != nil {
				return fmt.Errorf("daemon is running but its daemon.json is unreadable: %w", err)
			}
			stopErr := requestGracefulStop(cmd.Context(), dirs.Data, ri.Port)
			if stopErr == nil {
				if waitStopped(dirs.Data, stopTimeout) {
					_, _ = fmt.Fprintln(out, "daemon stopped")
					return nil
				}
				stopErr = fmt.Errorf("daemon did not exit within %s", stopTimeout)
			}
			if !force {
				return fmt.Errorf("%w (use --force to kill pid %d)", stopErr, ri.PID)
			}
			if err := daemon.KillPID(ri.PID); err != nil {
				return err
			}
			if !waitStopped(dirs.Data, stopTimeout) {
				return fmt.Errorf("killed pid %d but the daemon lock is still held", ri.PID)
			}
			_, _ = fmt.Fprintf(out, "daemon killed (pid %d)\n", ri.PID)
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "kill the daemon process if graceful stop fails")
	return cmd
}

func requestGracefulStop(ctx context.Context, dataDir string, port int) error {
	token, err := daemon.ReadToken(dataDir)
	if err != nil {
		return err
	}
	return daemon.RequestStop(ctx, port, token)
}

// waitStopped polls until no daemon holds the lock or the timeout elapses.
func waitStopped(dataDir string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if running, err := daemon.ProbeRunning(dataDir); err == nil && !running {
			return true
		}
		time.Sleep(pollInterval)
	}
	return false
}

func newDaemonStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Report whether the daemon is running (exit 0 healthy, 1 not running, 2 unresponsive)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			out := cmd.OutOrStdout()
			dirs, err := config.ResolveDirs()
			if err != nil {
				return err
			}
			running, err := daemon.ProbeRunning(dirs.Data)
			if err != nil {
				return err
			}
			if !running {
				if _, err := daemon.ReadRuntimeInfo(dirs.Data); err == nil {
					_, _ = fmt.Fprintln(out, "daemon is not running (stale daemon.json from an unclean shutdown)")
				} else {
					_, _ = fmt.Fprintln(out, "daemon is not running")
				}
				return exitError{code: 1}
			}
			ri, err := daemon.ReadRuntimeInfo(dirs.Data)
			if err != nil {
				_, _ = fmt.Fprintln(out, "daemon is running but its daemon.json is unreadable")
				return exitError{code: 2}
			}
			h, err := daemon.CheckHealth(cmd.Context(), ri.Port)
			if err != nil {
				_, _ = fmt.Fprintf(out, "daemon process is alive (pid %d) but not responding on port %d\n", ri.PID, ri.Port)
				return exitError{code: 2}
			}
			_, _ = fmt.Fprintf(out, "daemon is running (pid %d, port %d, version %s, started %s)\n",
				ri.PID, ri.Port, h.Version, ri.StartedAt.Local().Format(time.RFC3339))
			// The post-swap mismatch (task 055). `vincent update` replaces the
			// binary and nothing else — it drains nothing and kills nothing —
			// so a daemon started before the swap keeps its old code until it
			// is restarted. Nothing is broken, which is why this is a line and
			// not an exit code: the status is still healthy.
			if release.IsNewer(h.Version, version.Version()) {
				_, _ = fmt.Fprintf(out,
					"this binary is %s — the running daemon is older; restart it to pick the new build up\n",
					version.Version())
			}
			return nil
		},
	}
}

// logTailDefault matches the TUI daemon view's window (tui.logTailLines): the
// two surfaces answer the same question and disagreeing about how much of the
// log that takes would be a difference with no reason behind it.
const logTailDefault = 500

// logFollowInterval paces `--follow`, on the TUI's cadence.
const logFollowInterval = 2 * time.Second

// newDaemonLogsCmd prints the daemon log **from disk**, not over the API, and
// needs no daemon at all — which is the point rather than a shortcut, and the
// reason daemon.LogPath is exported for clients to derive: the log is worth
// reading exactly when the daemon is not there to serve it. It is one of the
// few subcommands that can never exit 2 (task 047 decision).
func newDaemonLogsCmd() *cobra.Command {
	var (
		lines  int
		follow bool
	)
	cmd := &cobra.Command{
		Use:   "logs",
		Short: "Print the daemon log from disk",
		Long: "Print the tail of {data_dir}/logs/daemon.log (§17). It reads the file " +
			"directly and never contacts the daemon, so it still answers when the " +
			"daemon is the thing that is broken. The directory is resolved the way " +
			"every other subcommand resolves it: $VINCENT_DATA_DIR, else the platform " +
			"default (§12.2).",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			dirs, err := config.ResolveDirs()
			if err != nil {
				return err
			}
			path := daemon.LogPath(dirs.Data)
			out := cmd.OutOrStdout()
			// An absent or unreadable file is an error naming the path; an
			// empty log prints nothing and succeeds. TailFile already draws
			// that distinction, and the two facts are not the same.
			tail, err := daemon.TailFile(path, lines)
			if err != nil {
				return fmt.Errorf("read %s: %w", path, err)
			}
			if err := writeLines(out, tail); err != nil {
				return err
			}
			if !follow {
				return nil
			}
			return followLog(cmd.Context(), out, path, lastLine(tail), logFollowInterval)
		},
	}
	cmd.Flags().IntVarP(&lines, "lines", "n", logTailDefault, "Number of trailing lines to print")
	cmd.Flags().BoolVarP(&follow, "follow", "f", false,
		"Keep printing lines as they are appended (Ctrl-C to stop)")
	return cmd
}

// followLog re-reads the file on a timer and prints only what was appended
// since the previous read — never the window again.
//
// Every read opens, reads and closes, because lumberjack rotates by renaming
// the live file and on Windows renaming a file another process holds open
// fails: a follower that kept a handle would break the daemon's own log
// rotation for as long as it was watching. A rotation between two reads is
// therefore ordinary here — the file may be briefly absent, which is waited
// out rather than reported, and the fresh file's lines are all new.
// The poll interval is a parameter so a test can drive the loop faster than a
// human would wait; the command always passes logFollowInterval.
func followLog(ctx context.Context, out io.Writer, path, seen string, every time.Duration) error {
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
		// n=0 is the whole tail window: -n bounds the opening print, not how
		// much a single poll may pick up, or a busy daemon's lines would be
		// dropped between polls.
		cur, err := daemon.TailFile(path, 0)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return fmt.Errorf("read %s: %w", path, err)
		}
		fresh := appendedAfter(cur, seen)
		if len(fresh) == 0 {
			continue
		}
		if err := writeLines(out, fresh); err != nil {
			return err
		}
		seen = lastLine(fresh)
	}
}

// appendedAfter returns the lines of a fresh read that follow the last line
// already printed. The seam is found by matching that line's *last*
// occurrence: log records carry a timestamp, so an identical line later in
// the same window is the newer one. A window that no longer contains it at
// all is a rotated or truncated file, whose lines are all new.
func appendedAfter(lines []string, seen string) []string {
	if seen == "" {
		return lines
	}
	for i := len(lines) - 1; i >= 0; i-- {
		if lines[i] == seen {
			return lines[i+1:]
		}
	}
	return lines
}

func lastLine(lines []string) string {
	if len(lines) == 0 {
		return ""
	}
	return lines[len(lines)-1]
}

func writeLines(w io.Writer, lines []string) error {
	for _, l := range lines {
		if _, err := fmt.Fprintln(w, l); err != nil {
			return err
		}
	}
	return nil
}

// exitError carries a specific process exit code out of a RunE without
// printing anything: the command has already written its report.
type exitError struct{ code int }

func (e exitError) Error() string { return fmt.Sprintf("exit code %d", e.code) }

// asExitCode extracts a specific exit code, defaulting to 1 for plain errors.
func asExitCode(err error) int {
	if err == nil {
		return 0
	}
	var ee exitError
	if errors.As(err, &ee) {
		return ee.code
	}
	return 1
}
