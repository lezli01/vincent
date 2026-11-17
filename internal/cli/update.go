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
			"--master installs the newest build of the master branch instead: a rolling " +
			"prerelease the project's CI rebuilds, signs and checksums on every push to " +
			"master, verified exactly as a release is. It is ahead of the latest release " +
			"and untested beyond CI. Its version is the next patch release with the " +
			"commits since the last release and the commit, such as 0.21.1-master.5.gabc1234, " +
			"so a later release is offered to a master build by a plain `vincent update`, " +
			"and running that returns it to the release track.\n\n" +
			"Exit codes — with --check: 0 up to date, 1 the check failed, 2 an update " +
			"is available. Without: 0 nothing to do or swapped successfully, 1 " +
			"verification or the swap failed and the binary is untouched, 2 an update " +
			"exists but this install is package-managed.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			checkOnly, _ := cmd.Flags().GetBool("check")
			dryRun, _ := cmd.Flags().GetBool("dry-run")
			requireSig, _ := cmd.Flags().GetBool("require-signature")
			master, _ := cmd.Flags().GetBool("master")
			return runUpdate(cmd, updateOpts{
				checkOnly:  checkOnly,
				dryRun:     dryRun,
				requireSig: requireSig,
				master:     master,
			})
		},
	}
	cmd.Flags().Bool("check", false, "Report whether an update is available and change nothing")
	cmd.Flags().Bool("dry-run", false, "Print what an update would do, without downloading or swapping")
	cmd.Flags().Bool("master", false,
		"Install the newest build of the master branch rather than the latest release")
	cmd.Flags().Bool("require-signature", false,
		"Fail rather than proceed when cosign is not installed to verify the signature")
	jsonFlag(cmd)
	return cmd
}

type updateOpts struct {
	checkOnly  bool
	dryRun     bool
	requireSig bool
	// master targets the newest master build (task 147) instead of the
	// latest release.
	master bool
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
	feed := release.New(release.Options{BaseURL: opts.baseURL})
	var (
		latest release.Release
		err    error
	)
	if opts.master {
		latest, err = feed.Master(ctx)
	} else {
		latest, err = feed.Latest(ctx)
	}
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
	if opts.master {
		// Any other build is the one asked for: a master build is newer
		// than the release it precedes but sorts below it, and a different
		// master build is newer or older only by its commit, which only the
		// branch knows. The explicit flag is the decision to take it.
		available = release.Canonical(current) != release.Canonical(latest.Version)
	}
	track := updateTrack(opts.master)
	channel, exe := selfupdate.Detect()
	if opts.executable != "" {
		exe = opts.executable
		channel = selfupdate.ChannelSelf
	}

	if opts.checkOnly {
		return updateCheckResult(cmd, out, current, latest, available, track)
	}
	if !available {
		if wantJSON(cmd) {
			return emitJSON(out, updateJSON(current, latest, false, false, string(channel), track))
		}
		_, _ = fmt.Fprintf(out, "vincent %s is the latest %s.\n", displayVersion(current), track.noun())
		return nil
	}
	if !channel.Owned() {
		return updateNotOwned(cmd, out, current, latest, channel, track)
	}
	if opts.dryRun {
		if wantJSON(cmd) {
			return emitJSON(out, updateJSON(current, latest, true, false, string(channel), track))
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
	var result selfupdate.Result
	if opts.master {
		result, err = up.ApplyFrom(cmd.Context(), release.MasterTag, latest.Version)
	} else {
		result, err = up.Apply(cmd.Context(), latest.Version)
	}
	if err != nil {
		// The one promise worth repeating at the point of failure: nothing
		// was replaced. Swap rolls back, and every failure before it happens
		// before anything touched the destination.
		_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "Error:", err)
		_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "%s was not modified.\n", exe)
		return exitError{code: 1}
	}
	if wantJSON(cmd) {
		payload := updateJSON(current, latest, true, true, string(channel), track)
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
	if opts.master {
		_, _ = fmt.Fprintln(out,
			"This is a master build, ahead of the latest release. `vincent update` returns to the "+
				"release track once a newer release is out; `vincent update --master` takes the next master build.")
	}
	printRestartHint(out)
	return nil
}

// updateTrackKind is what an update targets: the latest release, or the
// newest master build (task 147).
type updateTrackKind string

const (
	trackRelease updateTrackKind = "release"
	trackMaster  updateTrackKind = "master"
)

func updateTrack(master bool) updateTrackKind {
	if master {
		return trackMaster
	}
	return trackRelease
}

// noun is how a message names the track's newest build.
func (t updateTrackKind) noun() string {
	if t == trackMaster {
		return "master build"
	}
	return "release"
}

// updateCheckResult prints the --check answer and picks the exit code.
func updateCheckResult(
	cmd *cobra.Command, out io.Writer, current string, latest release.Release, available bool,
	track updateTrackKind,
) error {
	if wantJSON(cmd) {
		if err := emitJSON(out, map[string]any{
			"current_version":  current,
			"latest_version":   latest.Version,
			"update_available": available,
			"published_at":     latest.PublishedAt.UTC().Format(time.RFC3339),
			"release_url":      latest.URL,
			"track":            string(track),
		}); err != nil {
			return err
		}
	} else {
		_, _ = fmt.Fprintf(out, "current  %s\n", displayVersion(current))
		label := "latest "
		if track == trackMaster {
			label = "master "
		}
		_, _ = fmt.Fprintf(out, "%s  %s\n", label, latest.Version)
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
	latest release.Release, channel selfupdate.Channel, track updateTrackKind,
) error {
	upgrade := channel.UpgradeCommand()
	if track == trackMaster {
		// A package manager carries releases only: its upgrade command
		// would not install a master build, so none is offered.
		upgrade = ""
	}
	if wantJSON(cmd) {
		payload := updateJSON(current, latest, true, false, string(channel), track)
		payload["upgrade_command"] = upgrade
		if err := emitJSON(out, payload); err != nil {
			return err
		}
		return exitError{code: 2}
	}
	_, _ = fmt.Fprintf(out, "vincent %s is available; this binary is %s.\n",
		latest.Version, displayVersion(current))
	switch {
	case track == trackMaster:
		_, _ = fmt.Fprintf(out,
			"It was installed by %s, which carries releases only, so vincent will not replace it with a "+
				"master build. Install master from a direct download instead:\n\n    %s\n",
			channel, latest.URL)
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

func updateJSON(
	current string, latest release.Release, available, swapped bool, channel string, track updateTrackKind,
) map[string]any {
	return map[string]any{
		"track":            string(track),
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
