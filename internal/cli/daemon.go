package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/daemon"
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
	var dirs config.Dirs
	cmd := &cobra.Command{
		Use:   "daemon",
		Short: "Run the vincent daemon in the foreground",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := applyDirFlags(dirs); err != nil {
				return err
			}
			// RunManaged is Run everywhere but Windows, where it speaks the
			// SCM's control protocol when the process was started as a
			// service (§12.1). Foreground stays true: under a service manager
			// stderr is captured by the manager's own log, and losing it
			// there would make a failed service start undiagnosable.
			return daemon.RunManaged(cmd.Context(), daemon.Options{Foreground: true})
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
	cmd.AddCommand(newDaemonStartCmd(), newDaemonStopCmd(), newDaemonStatusCmd())
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
			return nil
		},
	}
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
