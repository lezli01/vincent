package cli

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/spf13/cobra"

	"github.com/lezli01/vincent/internal/release"
	"github.com/lezli01/vincent/internal/selfupdate"
	"github.com/lezli01/vincent/internal/version"
)

// `vincent update` (task 055, spec §12.1).
//
// **This command does not go through the daemon**, and that is the design
// rather than an oversight (decision 2). The operation has to work with no
// daemon running, and a daemon cannot cleanly rewrite its own running image on
// Windows — the same two reasons `daemon restore` is an exception to §4's
// "the daemon owns everything". What the daemon keeps is the background check
// and the cached answer on GET /v1/update.
//
// `--check` likewise queries the release feed itself. That is what makes
// `update.check: false` a literal promise — with the poller off the daemon
// makes no request at all, and only running this command does — and it is what
// makes the check work before the first poll and with no daemon up.
//
// Exit codes overload 0/1/2, which `vincent daemon status` and `vincent
// doctor` already do:
//
//	--check: 0 up to date · 1 the check failed · 2 an update is available
//	update:  0 nothing to do, or swapped · 1 verification or swap failed and
//	         the binary is untouched · 2 an update exists but a package
//	         manager owns this install and its command was printed
//
// `--json` carries `swapped`, so a script can tell the two zeroes apart.
func newUpdateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "update",
		Short: "Check for a newer vincent release, and install it",
		Long: "Ask GitHub for the latest stable release and, unless --check is given, " +
			"install it over this binary (§12.1).\n\n" +
			"This command talks to the release feed directly rather than through the " +
			"daemon, so it works with no daemon running and before the daemon's own " +
			"background check has ever polled. The daemon's cached answer is what " +
			"`vincent doctor` and `vincent daemon status` show; this is a fresh call.\n\n" +
			"An install a package manager owns is never modified: the channel is " +
			"detected and its upgrade command is printed instead, with exit code 2. " +
			"An install vincent owns — the direct-download archive, or a binary placed " +
			"by hand — is verified before anything is executed: the cosign signature " +
			"over checksums.txt, then the archive's SHA-256 against that file. With no " +
			"cosign on PATH the checksum check runs alone and says so; " +
			"--require-signature makes the missing binary fatal.\n\n" +
			"There is no prompt. This command is already the explicit human act, and " +
			"the command tree does not prompt because its purpose is scripting — use " +
			"--dry-run to see what would happen.\n\n" +
			"Exit codes — with --check: 0 up to date, 1 the check failed, 2 an update " +
			"is available. Without: 0 nothing to do or swapped successfully, 1 " +
			"verification or the swap failed and the binary is untouched, 2 an update " +
			"exists but this install is package-managed.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			checkOnly, _ := cmd.Flags().GetBool("check")
			dryRun, _ := cmd.Flags().GetBool("dry-run")
			requireSig, _ := cmd.Flags().GetBool("require-signature")
			return runUpdate(cmd, updateOpts{
				checkOnly:  checkOnly,
				dryRun:     dryRun,
				requireSig: requireSig,
			})
		},
	}
	cmd.Flags().Bool("check", false, "Report whether an update is available and change nothing")
	cmd.Flags().Bool("dry-run", false, "Print what an update would do, without downloading or swapping")
	cmd.Flags().Bool("require-signature", false,
		"Fail rather than proceed when cosign is not installed to verify the signature")
	jsonFlag(cmd)
	return cmd
}

type updateOpts struct {
	checkOnly  bool
	dryRun     bool
	requireSig bool
	// baseURL and downloadBase are test seams pointing at an httptest server.
	// They are unexported fields rather than hidden flags: a user-settable
	// release source is an unaudited URL this command downloads and executes.
	baseURL      string
	downloadBase string
	cosignPath   string
	executable   string
}

func runUpdate(cmd *cobra.Command, opts updateOpts) error {
	out := cmd.OutOrStdout()
	current := version.Version()

	ctx, cancel := context.WithTimeout(cmd.Context(), release.Timeout)
	latest, err := release.New(release.Options{BaseURL: opts.baseURL}).Latest(ctx)
	cancel()
	if err != nil {
		// Exit 1, not 2: the check itself failed. A script cannot tell "you
		// are up to date" from "GitHub said 403" any other way, and treating
		// an unreachable feed as "up to date" is the wrong lie.
		_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "Error:", err)
		if wantJSON(cmd) {
			_ = emitJSON(out, map[string]any{
				"current_version": current, "update_available": false, "error": err.Error(),
			})
		}
		return exitError{code: 1}
	}

	available := release.IsNewer(current, latest.Version)
	channel, exe := selfupdate.Detect()
	if opts.executable != "" {
		exe = opts.executable
		channel = selfupdate.ChannelSelf
	}

	if opts.checkOnly {
		return updateCheckResult(cmd, out, current, latest, available)
	}
	if !available {
		if wantJSON(cmd) {
			return emitJSON(out, updateJSON(current, latest, false, false, string(channel)))
		}
		_, _ = fmt.Fprintf(out, "vincent %s is the latest release.\n", displayVersion(current))
		return nil
	}
	if !channel.Owned() {
		return updateNotOwned(cmd, out, current, latest, channel)
	}
	if opts.dryRun {
		if wantJSON(cmd) {
			return emitJSON(out, updateJSON(current, latest, true, false, string(channel)))
		}
		_, _ = fmt.Fprintf(out,
			"Would download vincent %s, verify it, and replace %s.\nNothing was changed (--dry-run).\n",
			latest.Version, exe)
		return nil
	}

	up := selfupdate.New(selfupdate.Options{
		DownloadBase:     opts.downloadBase,
		CosignPath:       opts.cosignPath,
		RequireSignature: opts.requireSig,
		Executable:       exe,
	})
	result, err := up.Apply(cmd.Context(), latest.Version)
	if err != nil {
		// The one promise worth repeating at the point of failure: nothing
		// was replaced. Swap rolls back, and every failure before it happens
		// before anything touched the destination.
		_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "Error:", err)
		_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "%s was not modified.\n", exe)
		return exitError{code: 1}
	}
	if wantJSON(cmd) {
		payload := updateJSON(current, latest, true, true, string(channel))
		payload["path"] = result.Path
		payload["signature_verified"] = result.SignatureVerified
		return emitJSON(out, payload)
	}
	_, _ = fmt.Fprintf(out, "Updated %s to vincent %s.\n", result.Path, result.Version)
	if !result.SignatureVerified {
		// Said plainly, not buried: the user got a checksum check and not a
		// signature check, and only they can decide whether that is enough.
		_, _ = fmt.Fprintln(out,
			"The SHA-256 checksum matched. The cosign signature was NOT verified — "+
				"install cosign, or pass --require-signature to refuse an unverified update.")
	}
	printRestartHint(out)
	return nil
}

// updateCheckResult prints the --check answer and picks the exit code.
func updateCheckResult(
	cmd *cobra.Command, out io.Writer, current string, latest release.Release, available bool,
) error {
	if wantJSON(cmd) {
		if err := emitJSON(out, map[string]any{
			"current_version":  current,
			"latest_version":   latest.Version,
			"update_available": available,
			"published_at":     latest.PublishedAt.UTC().Format(time.RFC3339),
			"release_url":      latest.URL,
		}); err != nil {
			return err
		}
	} else {
		_, _ = fmt.Fprintf(out, "current  %s\n", displayVersion(current))
		_, _ = fmt.Fprintf(out, "latest   %s\n", latest.Version)
		if available {
			_, _ = fmt.Fprintf(out, "An update is available: %s\n", latest.URL)
		} else {
			_, _ = fmt.Fprintln(out, "You are up to date.")
		}
	}
	if available {
		return exitError{code: 2}
	}
	return nil
}

// updateNotOwned is the package-managed answer: nothing is downloaded and
// nothing is touched, and the exit code is distinct so a script can tell
// "up to date" from "not mine to perform".
func updateNotOwned(
	cmd *cobra.Command, out io.Writer, current string,
	latest release.Release, channel selfupdate.Channel,
) error {
	upgrade := channel.UpgradeCommand()
	if wantJSON(cmd) {
		payload := updateJSON(current, latest, true, false, string(channel))
		payload["upgrade_command"] = upgrade
		if err := emitJSON(out, payload); err != nil {
			return err
		}
		return exitError{code: 2}
	}
	_, _ = fmt.Fprintf(out, "vincent %s is available; this binary is %s.\n",
		latest.Version, displayVersion(current))
	switch {
	case upgrade != "":
		_, _ = fmt.Fprintf(out, "It was installed by %s, so upgrade it with:\n\n    %s\n",
			channel, upgrade)
	case channel == selfupdate.ChannelSystemPackage:
		// deb and rpm share this channel, and printing the wrong package
		// manager's command is worse than printing none.
		_, _ = fmt.Fprintf(out,
			"It came from a system package. Install the new .deb or .rpm from:\n\n    %s\n",
			latest.URL)
	default:
		_, _ = fmt.Fprintf(out,
			"vincent could not tell how this binary was installed, so it will not replace it.\n"+
				"See the upgrading section of the installation guide, or download %s from:\n\n    %s\n",
			latest.Version, latest.URL)
	}
	return exitError{code: 2}
}

func updateJSON(current string, latest release.Release, available, swapped bool, channel string) map[string]any {
	return map[string]any{
		"current_version":  current,
		"latest_version":   latest.Version,
		"update_available": available,
		// swapped is what separates the two zero exits: "nothing to do" and
		// "the binary was replaced" both succeed, and a script needs to know
		// whether to restart the daemon.
		"swapped":     swapped,
		"channel":     channel,
		"release_url": latest.URL,
	}
}

// printRestartHint names the command that picks the new build up. The running
// daemon keeps its old code until it is restarted — `vincent update` swaps the
// binary and nothing else, drains nothing and kills nothing.
func printRestartHint(out io.Writer) {
	_, _ = fmt.Fprintln(out,
		"\nThe running daemon still has the old build. Restart it to pick this one up:\n"+
			"    vincent service restart      # if you registered vincent as a service\n"+
			"    vincent daemon stop && vincent daemon start")
}

// displayVersion spells a `dev` build as itself. A developer running from
// source is not behind and is not told to download anything.
func displayVersion(v string) string {
	if v == "dev" || v == "" {
		return "dev (built from source)"
	}
	return v
}
