package cli

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/lezli01/vincent/internal/agent"
	"github.com/lezli01/vincent/internal/agent/claude"
	"github.com/lezli01/vincent/internal/agent/codex"
	"github.com/lezli01/vincent/internal/agent/cursor"
	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/workflow"
)

func newWorkflowCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "workflow",
		Aliases: []string{"wf"},
		Short:   "Create, list and validate workflows",
	}
	cmd.AddCommand(newWorkflowLsCmd(), newWorkflowValidateCmd(), newWorkflowInitCmd())
	return cmd
}

func newWorkflowLsCmd() *cobra.Command {
	var projectID int64
	cmd := &cobra.Command{
		Use:   "ls",
		Short: "List the merged workflow registry",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			// Unlike validate, this needs a daemon: the merged registry is
			// global + project scope with shadowing, and only the daemon
			// knows which projects exist (PR U decision).
			return withClient(cmd, func(ctx context.Context, c *apiclient.Client) error {
				entries, err := c.ListWorkflows(ctx, projectID)
				if err != nil {
					_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "Error:", apiMessage(err))
					return exitError{code: 1}
				}
				if entries == nil {
					entries = []apiclient.WorkflowEntry{}
				}
				if wantJSON(cmd) {
					return emitJSON(cmd.OutOrStdout(), entries)
				}
				rows := make([][]string, 0, len(entries))
				for _, e := range entries {
					status := "ok"
					switch {
					case len(e.Errors) > 0 || e.Error != nil:
						status = "invalid"
					case !e.RunsHere():
						// Ranked above warnings: a workflow this host cannot
						// run is the more actionable fact about it (task 010).
						status = "unsupported"
					case len(e.Warnings) > 0:
						status = "warnings"
					}
					rows = append(rows, []string{
						e.Name, e.Scope, status, strconv.Itoa(len(e.Steps)),
						dash(strings.Join(e.Platforms, ",")), dash(e.Description),
					})
				}
				return table(cmd.OutOrStdout(),
					[]string{"NAME", "SCOPE", "STATUS", "STEPS", "PLATFORMS", "DESCRIPTION"}, rows)
			})
		},
	}
	cmd.Flags().Int64Var(&projectID, "project", 0, "Include this project's scoped workflows")
	jsonFlag(cmd)
	return cmd
}

// validateResult is the --json shape of `workflow validate`. It is the
// command's own type rather than a reused wire DTO: nothing here crosses the
// API, and borrowing a server type would tie a local command to a remote
// contract it never speaks.
type validateResult struct {
	File  string `json:"file"`
	Valid bool   `json:"valid"`
	Name  string `json:"name,omitempty"`
	Steps int    `json:"steps"`
	// Platforms is the §8.1.1 restriction the file declares. It is reported,
	// never judged: validation is host-independent by design, so a POSIX-only
	// workflow validates on a Windows CI runner exactly as it does on Linux.
	Platforms []string  `json:"platforms,omitempty"`
	Errors    []finding `json:"errors"`
	Warnings  []finding `json:"warnings"`
}

type finding struct {
	Line    int    `json:"line,omitempty"`
	Path    string `json:"path,omitempty"`
	Message string `json:"message"`
}

func newWorkflowValidateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "validate <file>",
		Short: "Validate a workflow file (no daemon required)",
		Long: "Parse and validate a workflow YAML file against the §8.2 rules and the\n" +
			"curated agent catalogs. This runs entirely locally: it needs no daemon,\n" +
			"which is what makes it usable from a pre-commit hook or CI.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			src, err := os.ReadFile(args[0])
			if err != nil {
				return err
			}
			wf, warns, parseErr := workflow.Parse(src, localValidateOptions())

			res := validateResult{
				File:     args[0],
				Valid:    parseErr == nil,
				Errors:   []finding{},
				Warnings: []finding{},
			}
			if wf != nil {
				res.Name, res.Steps, res.Platforms = wf.Name, len(wf.Steps), wf.Platforms
			}
			var errs workflow.Errors
			if parseErr != nil {
				// Parse's error is always workflow.Errors; the fallback keeps
				// an unexpected shape reportable instead of panicking.
				if !asWorkflowErrors(parseErr, &errs) {
					errs = workflow.Errors{{Message: parseErr.Error()}}
				}
			}
			for _, e := range errs {
				res.Errors = append(res.Errors, finding{Line: e.Line, Path: e.Path, Message: e.Message})
			}
			for _, w := range warns {
				res.Warnings = append(res.Warnings, finding{Line: w.Line, Path: w.Path, Message: w.Message})
			}

			if wantJSON(cmd) {
				if err := emitJSON(cmd.OutOrStdout(), res); err != nil {
					return err
				}
			} else if err := printValidation(cmd, res); err != nil {
				return err
			}
			if !res.Valid {
				// Exit 1: the file was read and judged invalid. That is a
				// rejected input, not an unreachable daemon (which is 2).
				return exitError{code: 1}
			}
			return nil
		},
	}
	jsonFlag(cmd)
	return cmd
}

func printValidation(cmd *cobra.Command, res validateResult) error {
	out := cmd.OutOrStdout()
	for _, e := range res.Errors {
		_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "  error:", locate(e))
	}
	for _, w := range res.Warnings {
		_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "  warning:", locate(w))
	}
	if !res.Valid {
		_, err := fmt.Fprintf(out, "%s: invalid (%d error(s))\n", res.File, len(res.Errors))
		return err
	}
	platforms := ""
	if len(res.Platforms) > 0 {
		platforms = fmt.Sprintf(", platforms: %s", strings.Join(res.Platforms, ", "))
	}
	_, err := fmt.Fprintf(out, "%s: ok — %s, %d step(s), %d warning(s)%s\n",
		res.File, res.Name, res.Steps, len(res.Warnings), platforms)
	return err
}

func locate(f finding) string {
	switch {
	case f.Line > 0 && f.Path != "":
		return fmt.Sprintf("line %d: %s: %s", f.Line, f.Path, f.Message)
	case f.Line > 0:
		return fmt.Sprintf("line %d: %s", f.Line, f.Message)
	case f.Path != "":
		return fmt.Sprintf("%s: %s", f.Path, f.Message)
	}
	return f.Message
}

// localValidateOptions builds the same validation context the daemon uses,
// from curated catalogs only. No probe is spawned and no CLI needs to be
// installed: a workflow must validate the same way on a CI runner with no
// agent CLIs as on the developer's laptop.
func localValidateOptions() workflow.Options {
	reg := agent.NewRegistry(
		claude.New(func() string { return "" }),
		codex.New(func() string { return "" }),
		cursor.New(func() string { return "" }),
	)
	catalogs := make(agent.Catalogs, len(reg.Names()))
	for _, a := range reg.All() {
		catalogs[a.Name()] = a.Curated()
	}
	// The loop ceiling comes from the built-in default rather than the user's
	// config: `vincent workflow validate` runs wherever the file does — a CI
	// runner with no config file at all — and a workflow that validates on a
	// laptop must validate there too.
	ceiling := config.Default().Loop.MaxIterations
	return workflow.Options{
		KnownAgents:   reg.Names(),
		Catalogs:      func() agent.Catalogs { return catalogs },
		MaxIterations: func() int { return ceiling },
	}
}

// asWorkflowErrors is errors.As for the concrete slice type.
func asWorkflowErrors(err error, dst *workflow.Errors) bool {
	if errs, ok := err.(workflow.Errors); ok { //nolint:errorlint // Parse returns the concrete type by contract.
		*dst = errs
		return true
	}
	return false
}
