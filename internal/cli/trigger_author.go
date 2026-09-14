package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/trigger"
	"github.com/lezli01/vincent/internal/workflow"
)

// The daemon-free half of `vincent trigger` (task 098 decision 3): validate,
// ls and apply. They are how the create-trigger and update-triggers built-ins
// author a trigger without being able to arm one — an agent stages files, and
// apply writes them only when no switch goes from off to on. None of them
// talks to the daemon: the registry's live reload picks a written file up, the
// same as it does an editor's save.

// triggersDir is {config_dir}/triggers, the registry directory.
func triggersDir() (string, error) {
	dirs, err := config.ResolveDirs()
	if err != nil {
		return "", err
	}
	return filepath.Join(dirs.Config, "triggers"), nil
}

type triggerValidateResult struct {
	File   string           `json:"file"`
	ID     string           `json:"id,omitempty"`
	Valid  bool             `json:"valid"`
	Errors []workflow.Error `json:"errors"`
}

func newTriggerValidateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "validate <file>",
		Short: "Validate a trigger file (no daemon required)",
		Long: "Parse and validate a trigger YAML file exactly as POST /v1/triggers/validate\n" +
			"does, plus the check the registry makes at load: the file is named <id>.yaml.\n" +
			"It needs no daemon, so a pre-commit hook or an agent staging a proposal can\n" +
			"run it.\n\n" +
			"Exit 0 when the file is valid; 1 when it is invalid or cannot be read.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runTriggerValidate(cmd, args[0])
		},
	}
	jsonFlag(cmd)
	return cmd
}

func runTriggerValidate(cmd *cobra.Command, file string) error {
	//nolint:gosec // G304: the file the user named
	src, err := os.ReadFile(file)
	if err != nil {
		_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "Error:", err)
		return exitError{code: 1}
	}
	res := triggerValidateResult{File: file, Errors: []workflow.Error{}}
	def, errs := trigger.Parse(src, trigger.Stem(file))
	if !strings.EqualFold(filepath.Ext(file), ".yaml") {
		// The registry loads *.yaml and nothing else, so a valid document in a
		// .yml file is a trigger that never exists.
		errs = append(errs, workflow.Error{Message: "file name must end in .yaml; the registry loads nothing else"})
		def = nil
	}
	if def != nil {
		res.ID = def.ID
	}
	res.Valid = len(errs) == 0
	if !res.Valid {
		res.Errors = errs
	}

	if wantJSON(cmd) {
		if err := emitJSON(cmd.OutOrStdout(), res); err != nil {
			return err
		}
	} else {
		for _, e := range res.Errors {
			_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "  error:", locate(finding{Line: e.Line, Path: e.Path, Message: e.Message}))
		}
		verdict := "ok — trigger " + res.ID
		if !res.Valid {
			verdict = fmt.Sprintf("invalid (%d error(s))", len(res.Errors))
		}
		if _, err := fmt.Fprintf(cmd.OutOrStdout(), "%s: %s\n", file, verdict); err != nil {
			return err
		}
	}
	if !res.Valid {
		return exitError{code: 1}
	}
	return nil
}

func newTriggerLsCmd() *cobra.Command {
	var project int64
	cmd := &cobra.Command{
		Use:   "ls --project <id>",
		Short: "List one project's trigger files (no daemon required)",
		Long: "Print the path of every file under {config_dir}/triggers/ whose source.project\n" +
			"is <id>, one per line. A file that does not validate is still listed when its\n" +
			"source.project can be read, so it can be repaired; a file whose project cannot\n" +
			"be read at all is reported on stderr and left out.\n\n" +
			"--json prints each file's id, version token, validity, and its enabled, on_fire\n" +
			"and permission values as written (\"\" when absent) — what a proposal's\n" +
			"manifest records for `vincent trigger apply`.\n\n" +
			"Exit 0 when at least one file matched; 1 when none did, the same probe shape\n" +
			"as `git ls-files --error-unmatch`.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runTriggerLs(cmd, project)
		},
	}
	cmd.Flags().Int64Var(&project, "project", 0, "the project's numeric id, as source.project names it")
	_ = cmd.MarkFlagRequired("project")
	jsonFlag(cmd)
	return cmd
}

func runTriggerLs(cmd *cobra.Command, project int64) error {
	dir, err := triggersDir()
	if err != nil {
		return err
	}
	all, problems := trigger.List(dir)
	for _, p := range problems {
		_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "warning:", p)
	}
	mine := []trigger.Listing{}
	for _, l := range all {
		if l.Project == project {
			mine = append(mine, l)
		}
	}
	if wantJSON(cmd) {
		if err := emitJSON(cmd.OutOrStdout(), mine); err != nil {
			return err
		}
	} else {
		for _, l := range mine {
			if _, err := fmt.Fprintln(cmd.OutOrStdout(), l.File); err != nil {
				return err
			}
		}
	}
	if len(mine) == 0 {
		return exitError{code: 1}
	}
	return nil
}

func newTriggerApplyCmd() *cobra.Command {
	var proposal, project int64
	cmd := &cobra.Command{
		Use:   "apply --proposal <task_id> --project <id>",
		Short: "Install a staged trigger proposal without arming anything",
		Long: "Write the trigger files staged in {data_dir}/trigger-proposals/<task_id>/ into\n" +
			"{config_dir}/triggers/, mode 0600, then remove the staging directory.\n\n" +
			"The directory holds full <id>.yaml files and manifest.json, a JSON object\n" +
			"mapping each id to the version `vincent trigger ls --json` reported for its\n" +
			"file, or \"absent\" for a new one. Every file is checked before any is written,\n" +
			"and one refusal writes nothing: a file must validate, name --project as its\n" +
			"source.project, and match what the manifest recorded on disk; and no file may\n" +
			"arm a trigger — turn enabled on, set on_fire: create, or set permission:\n" +
			"workflow — that was not already armed. Keeping an armed value and disarming\n" +
			"are allowed. There is no override: a human arms a trigger in the TUI's\n" +
			"triggers view or an editor. The global triggers.enabled is never touched.\n\n" +
			"Exit 0 when every file was written; 1 when the proposal was refused or a\n" +
			"write failed.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runTriggerApply(cmd, proposal, project)
		},
	}
	cmd.Flags().Int64Var(&proposal, "proposal", 0, "the id of the task that staged the proposal")
	cmd.Flags().Int64Var(&project, "project", 0, "the project's numeric id every staged file must name")
	_ = cmd.MarkFlagRequired("proposal")
	_ = cmd.MarkFlagRequired("project")
	return cmd
}

func runTriggerApply(cmd *cobra.Command, proposal, project int64) error {
	stderr := cmd.ErrOrStderr()
	if proposal <= 0 || project <= 0 {
		_, _ = fmt.Fprintln(stderr, "Error: --proposal and --project take positive ids")
		return exitError{code: 1}
	}
	dirs, err := config.ResolveDirs()
	if err != nil {
		return err
	}
	staging := trigger.ProposalDir(dirs.Data, proposal)
	w := trigger.NewWriter(filepath.Join(dirs.Config, "triggers"))
	written, refusals, err := trigger.ApplyProposal(staging, w, project)
	for _, path := range written {
		_, _ = fmt.Fprintln(cmd.OutOrStdout(), "wrote", path)
	}
	if len(refusals) > 0 {
		for _, r := range refusals {
			_, _ = fmt.Fprintln(stderr, "  refused:", r)
		}
		_, _ = fmt.Fprintf(stderr, "%s: refused (%d reason(s)); nothing was written\n", staging, len(refusals))
		return exitError{code: 1}
	}
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "Error:", err)
		return exitError{code: 1}
	}
	_, err = fmt.Fprintln(cmd.OutOrStdout(), "removed", staging)
	return err
}
