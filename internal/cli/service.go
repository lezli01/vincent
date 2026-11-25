package cli

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/lezli01/vincent/internal/service"
)

func newServiceCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "service",
		Short: "Install the daemon as an OS-managed background service",
		Long: "Register the daemon with the platform service manager so it starts at\n" +
			"login and survives reboot: a launchd user agent, a systemd user unit, or a\n" +
			"Windows Scheduled Task (§12.1).\n\n" +
			"On Windows it is a Scheduled Task and not a Windows Service, so it appears\n" +
			"in Task Scheduler as \"vincent\" and never in services.msc. It runs without\n" +
			"a visible window; `vincent service status` and `vincent daemon status` are\n" +
			"how you check on it.\n\n" +
			"It runs as you on every platform, since the OS user is vincent's trust\n" +
			"boundary — an agent gets exactly your privileges, your agent-CLI logins and\n" +
			"your git identity, no more.\n\n" +
			"The config and data directories in effect at install time are baked in,\n" +
			"because a service does not inherit the shell that installed it. On macOS\n" +
			"and Linux so is PATH, so the service resolves the same agent CLIs this\n" +
			"shell does: install again after installing an agent CLI somewhere new.\n" +
			"Windows needs no capture — the task runs in your logon session with your\n" +
			"own PATH. If an agent still will not resolve, set agents.<name>.path in\n" +
			"config.yaml, which is absolute and never consults PATH.",
	}
	cmd.AddCommand(newServiceInstallCmd(), newServiceUninstallCmd(), newServiceRestartCmd(),
		newServiceStatusCmd())
	return cmd
}

func newServiceInstallCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "install",
		Short: "Install and start the service",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			err := service.Install(cmd.Context(), service.Options{})
			switch {
			case service.LingerFailed(err):
				// Installed and running, but not yet surviving logout. A
				// warning, not a failure: the user has a working service and
				// one command left to run.
				_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "Warning:", err)
			case err != nil:
				_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "Error:", err)
				return exitError{code: 1}
			}
			return printServiceStatus(cmd, "installed")
		},
	}
	jsonFlag(cmd)
	return cmd
}

func newServiceUninstallCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "uninstall",
		Short: "Stop and remove the service",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			err := service.Uninstall(cmd.Context())
			if errors.Is(err, service.ErrNotInstalled) {
				// Desired-state semantics, matching `daemon stop`: nothing to
				// remove is success, not a failure to report.
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), "no vincent service is installed")
				return nil
			}
			if err != nil {
				_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "Error:", err)
				return exitError{code: 1}
			}
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), "service uninstalled")
			return nil
		},
	}
	return cmd
}

// newServiceRestartCmd restarts the daemon through the service manager
// (#1160). `vincent daemon stop` followed by a start would not: every
// definition restarts on failure only, so the stop's clean exit leaves the
// service stopped and the start runs a daemon outside it.
func newServiceRestartCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "restart",
		Short: "Restart the daemon through the service manager",
		Long: "Have the platform service manager stop the daemon and start it again, so\n" +
			"the new daemon is the service's own process: `launchctl kickstart -k` on\n" +
			"macOS, `systemctl --user restart` on Linux, and on Windows a graceful stop\n" +
			"followed by ending and running the Scheduled Task. Running steps are\n" +
			"interrupted and re-run when the daemon is back (§12.4).\n\n" +
			"Use it instead of `vincent daemon stop` and a start when the daemon runs as\n" +
			"a service: the stop exits cleanly, which no service definition restarts,\n" +
			"and the start would run a daemon outside the service.\n\n" +
			"It returns once the service manager has started the daemon again; check\n" +
			"`vincent daemon status` for it serving. With no service installed it\n" +
			"exits 1.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			err := service.Restart(cmd.Context())
			if errors.Is(err, service.ErrNotInstalled) {
				// Not desired-state like uninstall: there is no service to
				// restart, and the daemon (if any) was left as it was.
				_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "Error: no vincent service is installed; "+
					"`vincent service install` registers one")
				return exitError{code: 1}
			}
			if err != nil {
				_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "Error:", err)
				return exitError{code: 1}
			}
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), "service restarted")
			return nil
		},
	}
	return cmd
}

func newServiceStatusCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Report whether the service is installed and running",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return printServiceStatus(cmd, "")
		},
	}
	jsonFlag(cmd)
	return cmd
}

// printServiceStatus renders the current status. verb, when set, prefixes the
// human line so install can report what it just did in the same shape status
// reports later.
func printServiceStatus(cmd *cobra.Command, verb string) error {
	st, err := service.Query(cmd.Context())
	if err != nil {
		_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "Error:", err)
		return exitError{code: 1}
	}
	if wantJSON(cmd) {
		return emitJSON(cmd.OutOrStdout(), st)
	}
	out := cmd.OutOrStdout()
	if !st.Installed {
		_, _ = fmt.Fprintln(out, "no vincent service is installed")
		return nil
	}
	state := "stopped"
	if st.Running {
		state = "running"
	}
	if verb != "" {
		_, _ = fmt.Fprintf(out, "service %s (%s)\n", verb, state)
	} else {
		_, _ = fmt.Fprintf(out, "service is installed and %s\n", state)
	}
	_, _ = fmt.Fprintf(out, "  name    %s\n", st.Name)
	if st.Unit != "" {
		_, _ = fmt.Fprintf(out, "  unit    %s\n", st.Unit)
	}
	if st.Detail != "" {
		_, _ = fmt.Fprintf(out, "  kind    %s\n", st.Detail)
	}
	return nil
}
