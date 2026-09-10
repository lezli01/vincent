package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/lezli01/vincent/internal/skill"
)

// `vincent skills` (§9.8, §12.1, task 095): what this binary publishes, what
// is on this machine, and the one command that closes the gap.
//
// **It never talks to the daemon**, and so it never exits 2. Installing a
// skill writes into the invoking user's own agent directories on the client
// machine — there is nothing daemon-owned about it, no database row and no
// worktree — which is exactly why the write lives here and not behind
// `doctor --fix`, where every repair is a daemon-owned write. `vincent
// update` and `vincent daemon logs` are the same shape: 0 fine, 1 the
// operation failed, and no third code because no request was ever made.
func newSkillsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "skills",
		Short: "Report and install the agent skills vincent publishes",
		Long: "Report whether the agent skills this repository publishes are installed " +
			"for your coding agents, and install them (§9.8).\n\n" +
			"vincent publishes skills so that an agent you are talking to directly — " +
			"outside a vincent run — knows how to author a vincent workflow. The " +
			"built-in create-workflow and update-workflows workflows do not need this: " +
			"they carry the skill's text inside their own prompts.\n\n" +
			"Nothing here needs a running daemon. Detection is a filesystem read and " +
			"works on a machine with no node installed; only `install` shells out, to " +
			"`npx skills add`.",
	}
	cmd.AddCommand(newSkillsListCmd(), newSkillsInstallCmd())
	return cmd
}

func newSkillsListCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "ls",
		Aliases: []string{"list"},
		Short:   "List the published skills and whether they are installed",
		Long: "List every skill this binary publishes with the version it ships, the " +
			"version installed in the global skills store, and the agents it is " +
			"linked into.\n\n" +
			"The agent list is what is on disk. It can name agents vincent does not " +
			"drive — one store serves every agent on the machine — and it can disagree " +
			"with `npx skills list -g`, which reports the selection the CLI remembers " +
			"rather than the links it left behind.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			statuses := skill.Detect(skill.Options{})
			if wantJSON(cmd) {
				return emitJSON(cmd.OutOrStdout(), statuses)
			}
			rows := make([][]string, 0, len(statuses))
			for _, s := range statuses {
				rows = append(rows, []string{
					s.Name, dash(s.Shipped), skillStateWord(s), dash(strings.Join(s.Agents(), ", ")),
				})
			}
			return table(cmd.OutOrStdout(), []string{"SKILL", "SHIPPED", "STATE", "AGENTS"}, rows)
		},
	}
	jsonFlag(cmd)
	return cmd
}

func newSkillsInstallCmd() *cobra.Command {
	var adapters []string
	cmd := &cobra.Command{
		Use:   "install [name...]",
		Short: "Install the published skills for your agents",
		Long: "Install the named skills — or every published skill that is not already " +
			"current — into the global skills store, linked into each agent's " +
			"directory.\n\n" +
			"This runs `npx skills add` with the agent selection supplied, because the " +
			"published command with no selection opens an interactive picker and there " +
			"is nothing to pick with here. It needs node on PATH and, on a first run, " +
			"network: the package is downloaded before it does anything. Without npx " +
			"the command exits 1 naming the dependency and printing the line to run " +
			"once node is there — detection is unaffected either way.\n\n" +
			"Exit codes: 0 everything asked for is installed, 1 an install failed.",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSkillsInstall(cmd, args, adapters)
		},
	}
	cmd.Flags().StringSliceVar(&adapters, "agent", nil,
		"Adapters to install for (repeatable, comma-separated): "+
			strings.Join(skill.Adapters(), ", ")+". Default: all of them")
	jsonFlag(cmd)
	return cmd
}

// skillsInstallResult is one skill's outcome, and the --json shape.
type skillsInstallResult struct {
	Skill   string `json:"skill"`
	Command string `json:"command"`
	OK      bool   `json:"ok"`
	Error   string `json:"error,omitempty"`
}

func runSkillsInstall(cmd *cobra.Command, args, adapters []string) error {
	statuses := skill.Detect(skill.Options{})
	names := args
	if len(names) == 0 {
		names = skill.Missing(statuses)
	}
	slugs := skill.Slugs(adapters)
	out := cmd.OutOrStdout()
	results := make([]skillsInstallResult, 0, len(names))
	if len(names) == 0 && !wantJSON(cmd) {
		if _, err := fmt.Fprintln(out, "every published skill is already current"); err != nil {
			return err
		}
	}
	failed := false
	for _, name := range names {
		res := skillsInstallResult{Skill: name, Command: skill.Command(name, slugs)}
		// The progress line goes out before the run, not after: a first `npx`
		// downloads the package, so a command that printed nothing would look
		// hung for as long as that takes.
		if !wantJSON(cmd) {
			if _, err := fmt.Fprintf(out, "installing %s — running %s\n", name, res.Command); err != nil {
				return err
			}
		}
		// The CLI's own output is streamed only in the table mode; --json
		// must stay a single parseable document.
		sink := out
		if wantJSON(cmd) {
			sink = cmd.ErrOrStderr()
		}
		if err := skill.Install(cmd.Context(), name, slugs, sink); err != nil {
			res.Error = err.Error()
			failed = true
		} else {
			res.OK = true
		}
		results = append(results, res)
	}
	if wantJSON(cmd) {
		if err := emitJSON(out, results); err != nil {
			return err
		}
	} else {
		for _, r := range results {
			if r.OK {
				if _, err := fmt.Fprintf(out, "installed %s\n", r.Skill); err != nil {
					return err
				}
				continue
			}
			if _, err := fmt.Fprintf(cmd.ErrOrStderr(), "%s: %s\n", r.Skill, r.Error); err != nil {
				return err
			}
		}
	}
	if failed {
		// Exit 1 and not 2: no daemon was asked, so "the daemon is not
		// running" is not a thing this command can mean.
		return exitError{code: 1}
	}
	return nil
}

// skillStateWord renders one row's state for a human, carrying both versions
// wherever the state is about a difference between them.
func skillStateWord(s skill.Status) string {
	switch s.State {
	case skill.StateCurrent:
		return "installed and current"
	case skill.StateAbsent:
		return "not installed"
	case skill.StateOlder:
		return "out of date: installed " + s.Installed + ", ships " + s.Shipped
	case skill.StateNewer:
		return "installed " + s.Installed + " is newer than the " + s.Shipped + " this binary ships"
	case skill.StateDiffers:
		return "differs: installed " + dash(s.Installed) + ", ships " + dash(s.Shipped)
	default:
		return s.State + ": " + s.Message
	}
}
