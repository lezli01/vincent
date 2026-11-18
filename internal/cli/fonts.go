package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/nerdfont"
)

// `vincent fonts` installs the Nerd Font behind `tui.icons: nerd` (task 151). A
// font belongs to the machine with the screen, so this runs here and never asks
// the daemon to do it; like `vincent skills`, it exits 0 or 1.
func newFontsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "fonts",
		Short: "Report, install and remove the Nerd Font behind tui.icons: nerd",
		Long: "Install " + nerdfont.Family + " for your user, on macOS and Linux.\n\n" +
			"The font is downloaded from the Nerd Fonts project and checked against a " +
			"checksum built into vincent. Installing it does not change your terminal: " +
			"choose the font in your terminal's settings, then run " +
			"`vincent config set tui.icons nerd`.\n\n" +
			"Exit codes: 0 done, 1 the operation failed.",
	}
	cmd.AddCommand(newFontsStatusCmd(), newFontsInstallCmd(), newFontsUninstallCmd())
	return cmd
}

// fontsReport is `vincent fonts status --json`.
type fontsReport struct {
	Family   string `json:"family"`
	Version  string `json:"version"`
	Dir      string `json:"dir,omitempty"`
	State    string `json:"state"`
	Terminal string `json:"terminal,omitempty"`
	Bundled  bool   `json:"bundled"`
	Remote   bool   `json:"remote"`
	Steps    string `json:"steps,omitempty"`
}

func newFontsStatusCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Report whether the font is installed and what this terminal needs",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			term := nerdfont.Terminal(os.Getenv)
			report := fontsReport{
				Family: nerdfont.Family, Version: nerdfont.Pinned.Version, State: "unsupported",
				Terminal: term.Name, Bundled: term.Bundled, Remote: term.Remote, Steps: term.Steps,
			}
			if dir, err := nerdfont.DefaultDir(); err == nil {
				state, err := nerdfont.Detect(nerdfont.Pinned, dir)
				if err != nil {
					return fontsFail(cmd, err)
				}
				report.Dir, report.State = dir, string(state)
			}
			if wantJSON(cmd) {
				return emitJSON(cmd.OutOrStdout(), report)
			}
			_, err := fmt.Fprintf(cmd.OutOrStdout(), "%s %s: %s\n%s\n", report.Family, report.Version, report.State, fontsAdvice(term))
			return err
		},
	}
	jsonFlag(cmd)
	return cmd
}

// fontsAdvice is what this terminal needs next.
func fontsAdvice(term nerdfont.TerminalInfo) string {
	switch {
	case term.Bundled:
		return term.Name + " already includes these icons. Run: vincent config set tui.icons nerd"
	case term.Remote:
		return "You are connected over SSH: the font has to be on the machine in front of you."
	}
	return "Choose " + nerdfont.Family + " in " + term.Steps + ", then run: vincent config set tui.icons nerd"
}

func newFontsInstallCmd() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "install",
		Short: "Download the font, verify it and install it for your user",
		Long: "Download " + nerdfont.Family + " " + nerdfont.Pinned.Version + " from " + nerdfont.Pinned.URL +
			", verify it, and install four font files into your per-user font directory.\n\n" +
			"Over SSH this refuses, because a font installed on the remote machine never " +
			"reaches your screen. --force installs anyway.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			term := nerdfont.Terminal(os.Getenv)
			if term.Remote && !force {
				return fontsFail(cmd, errors.New("this is an SSH session, so the font would not reach your screen; run this on the machine in front of you, or pass --force"))
			}
			dir, err := nerdfont.DefaultDir()
			if err != nil {
				return fontsFail(cmd, err)
			}
			out := cmd.OutOrStdout()
			if _, err := fmt.Fprintf(out, "downloading %.0f MB from %s\n", float64(nerdfont.Pinned.Size)/1e6, nerdfont.Pinned.URL); err != nil {
				return err
			}
			if err := nerdfont.Install(cmd.Context(), nerdfont.Pinned, dir); err != nil {
				return fontsFail(cmd, err)
			}
			_, err = fmt.Fprintf(out, "installed %s into %s\n%s\n", nerdfont.Family, dir, fontsAdvice(term))
			return err
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "Install even over SSH")
	return cmd
}

func newFontsUninstallCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "uninstall",
		Short: "Remove the font files vincent installed and go back to Unicode icons",
		Long: "Remove only the files whose content is exactly what `vincent fonts install` " +
			"wrote, and set tui.icons back to unicode when the daemon is running.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			dir, err := nerdfont.DefaultDir()
			if err != nil {
				return fontsFail(cmd, err)
			}
			removed, err := nerdfont.Uninstall(cmd.Context(), nerdfont.Pinned, dir)
			if err != nil {
				return fontsFail(cmd, err)
			}
			out := cmd.OutOrStdout()
			if _, err := fmt.Fprintf(out, "removed %d font files from %s\n", removed, dir); err != nil {
				return err
			}
			if err := fontsResetIcons(cmd); err != nil {
				_, err = fmt.Fprintln(out, "the daemon is not running, so run this once it is: vincent config set tui.icons unicode")
				return err
			}
			_, err = fmt.Fprintln(out, "tui.icons is back to unicode")
			return err
		},
	}
}

// fontsResetIcons puts tui.icons back through a running daemon.
func fontsResetIcons(cmd *cobra.Command) error {
	return withClient(cmd, func(ctx context.Context, c *apiclient.Client) error {
		ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		set := "unicode"
		_, err := c.PatchConfig(ctx, apiclient.ConfigPatch{TUI: &apiclient.ConfigTUIPatch{Icons: &set}})
		return err
	})
}

// fontsFail reports err and exits 1: no daemon was asked, so 2 cannot apply.
func fontsFail(cmd *cobra.Command, err error) error {
	if _, werr := fmt.Fprintln(cmd.ErrOrStderr(), err); werr != nil {
		return werr
	}
	return exitError{code: 1}
}
