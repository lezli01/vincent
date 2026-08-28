package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/lezli01/vincent/examples"
	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/workflow"
)

// workflowNamePattern is the vocabulary `init` accepts for <name>. The §8.2
// rule for a workflow's name: field is looser — anything without whitespace
// or a path separator — but this value is also a file name, exactly the dual
// role behind task 024 decision 10, so it is held to the stricter of the two.
// The point is consistency with create-workflow's workflow_name field: the
// same name must be legal by both routes into the registry.
var workflowNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)

// initResult is the --json shape of `workflow init`. Like validateResult it
// is the command's own type: nothing here crosses the API.
type initResult struct {
	File  string `json:"file"`
	Name  string `json:"name"`
	Scope string `json:"scope"`
	// From is the example the file came from, empty for the skeleton.
	From string `json:"from,omitempty"`
	// Shadows names the entries this file will take the name from, in
	// precedence order. It is a warning, never a refusal (§5.2).
	Shadows []string `json:"shadows"`
}

func newWorkflowInitCmd() *cobra.Command {
	var (
		from      string
		projectID int64
	)
	cmd := &cobra.Command{
		Use:   "init <name>",
		Short: "Write a new workflow file into the registry",
		Long: "Write a valid workflow YAML file into the global or a project's workflow\n" +
			"directory and print the path.\n\n" +
			"With no --from, the file is a commented one-agent-step skeleton. With\n" +
			"--from, it is one of the shipped examples, embedded in this binary, with\n" +
			"its name: rewritten to <name>. Either way the result is yours to edit.\n\n" +
			"The default (global) scope needs no daemon; --project does, because only\n" +
			"the daemon knows which projects exist and where their repositories are.\n\n" +
			"This hands you a file. To have an agent design one from a description\n" +
			"instead, run a task on the built-in create-workflow workflow — that costs\n" +
			"a daemon, an agent CLI, tokens and wall-clock time, and may stop to ask a\n" +
			"design question; this costs none of them and is always the same file.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			if !workflowNamePattern.MatchString(name) {
				_, _ = fmt.Fprintf(cmd.ErrOrStderr(),
					"Error: %q is not a usable workflow name — it is also the file name, so it must match %s\n",
					name, workflowNamePattern)
				return exitError{code: 1}
			}
			src, err := initSource(from, name)
			if err != nil {
				_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "Error:", err)
				return exitError{code: 1}
			}
			if projectID != 0 {
				return withClient(cmd, func(ctx context.Context, c *apiclient.Client) error {
					root, err := projectRoot(ctx, c, projectID)
					if err != nil {
						_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "Error:", err)
						return exitError{code: 1}
					}
					return writeWorkflowFile(cmd, initTarget{
						dir:    filepath.Join(root, workflow.ProjectDirName),
						scope:  string(workflow.ScopeProject),
						name:   name,
						from:   from,
						source: src,
					})
				})
			}
			dirs, err := config.ResolveDirs()
			if err != nil {
				return err
			}
			return writeWorkflowFile(cmd, initTarget{
				dir:    filepath.Join(dirs.Config, workflow.GlobalDirName),
				scope:  string(workflow.ScopeGlobal),
				name:   name,
				from:   from,
				source: src,
			})
		},
	}
	cmd.Flags().StringVar(&from, "from", "",
		"Start from a shipped example instead of the skeleton ("+strings.Join(examples.Names(), ", ")+")")
	cmd.Flags().Int64Var(&projectID, "project", 0,
		"Write into this project's .vincent/workflows instead of the global directory (needs a daemon)")
	jsonFlag(cmd)
	return cmd
}

// initSource resolves the bytes to write, with the top-level name: already
// rewritten to name. An unknown --from lists what would have worked: the
// values come from the embedded filesystem, so a newly added example is
// offered without anyone remembering to extend a list.
func initSource(from, name string) ([]byte, error) {
	src := []byte(workflow.SkeletonSource)
	if from != "" {
		var err error
		src, err = examples.Read(from)
		if err != nil {
			return nil, fmt.Errorf("%w — try one of: %s", err, strings.Join(examples.Names(), ", "))
		}
	}
	return workflow.SetName(src, name)
}

// projectRoot resolves a project id to its repository root. The list
// endpoint has no per-id form and the list is human-sized by construction,
// so this filters it rather than asking for a route that does not exist.
func projectRoot(ctx context.Context, c *apiclient.Client, id int64) (string, error) {
	projects, err := c.ListProjects(ctx)
	if err != nil {
		return "", errors.New(apiMessage(err))
	}
	for _, p := range projects {
		if p.ID == id {
			return p.Path, nil
		}
	}
	return "", fmt.Errorf("no project %d — `vincent project ls` lists them", id)
}

// initTarget is one resolved destination for `workflow init`.
type initTarget struct {
	dir    string
	scope  string
	name   string
	from   string
	source []byte
}

// writeWorkflowFile performs the collision checks and the write.
//
// The two collisions are not the same kind of thing, and are not treated
// alike (§5.2). A name already taken *inside the target scope* is damage in
// both directions — the loser by filename order becomes an invalid duplicate
// entry, so the new file either arrives broken or breaks a sibling — so it is
// refused. Shadowing a name from a *lower* scope is the mechanism working as
// designed, so it only warns.
func writeWorkflowFile(cmd *cobra.Command, t initTarget) error {
	path := filepath.Join(t.dir, t.name+examples.Ext)
	// The target path itself is excluded: a file already sitting there is the
	// plainer "already exists" below, and reporting it as a name clash would
	// describe a collision with itself.
	if holder, taken := nameTakenIn(t.dir, t.name, path); taken {
		_, _ = fmt.Fprintf(cmd.ErrOrStderr(),
			"Error: %s already declares `name: %s`, and one scope may not hold that name twice.\n"+
				"       Whichever file sorted first would keep it; the other would be listed as invalid.\n"+
				"       Pick another name, or edit that file.\n",
			holder, t.name)
		return exitError{code: 1}
	}
	shadows := shadowedBy(t)

	// G301/G302: a scaffolded workflow is a repository file. `.vincent/workflows`
	// is committed and read by everyone who clones the project, so it takes the
	// ordinary repository modes rather than owner-only ones. In the global scope
	// the parent is the 0700 config dir (§16), which is what actually bounds it.
	if err := os.MkdirAll(t.dir, 0o755); err != nil { //nolint:gosec // G301: see above
		return fmt.Errorf("create %s: %w", t.dir, err)
	}
	// O_EXCL makes "never clobber" a syscall guarantee rather than a
	// stat-then-write race: nothing this command does can overwrite a file a
	// user wrote by hand.
	//nolint:gosec // G302/G304: repository file modes, path built from the scope dir above
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			_, _ = fmt.Fprintf(cmd.ErrOrStderr(),
				"Error: %s already exists and was left untouched\n", path)
			return exitError{code: 1}
		}
		return fmt.Errorf("create %s: %w", path, err)
	}
	if _, err := f.Write(t.source); err != nil {
		_ = f.Close()
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}

	if wantJSON(cmd) {
		return emitJSON(cmd.OutOrStdout(), initResult{
			File: path, Name: t.name, Scope: t.scope, From: t.from, Shadows: shadows,
		})
	}
	for _, s := range shadows {
		_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "  warning: shadows the %s workflow %q\n", s, t.name)
	}
	_, err = fmt.Fprintf(cmd.OutOrStdout(),
		"%s\nEdit it, then `vincent workflow validate %s`. The daemon picks it up on save.\n",
		path, path)
	return err
}

// nameTakenIn reports whether a file already in dir declares name, and which
// one. Paths in except are not considered.
//
// A sibling that does not parse as YAML at all has no knowable name and
// cannot block anything; it is skipped, being already visible as an invalid
// entry in `vincent workflow ls`.
func nameTakenIn(dir, name string, except ...string) (string, bool) {
	des, err := os.ReadDir(dir)
	if err != nil {
		// A missing directory holds nothing. Any other read failure is left
		// to the write below to report against the path the user can see.
		return "", false
	}
	for _, de := range des {
		if de.IsDir() {
			continue
		}
		switch strings.ToLower(filepath.Ext(de.Name())) {
		case ".yaml", ".yml":
		default:
			continue
		}
		path := filepath.Join(dir, de.Name())
		if slices.Contains(except, path) {
			continue
		}
		// G304: a directory entry of the workflow dir this command just scanned.
		src, err := os.ReadFile(path) //nolint:gosec // G304: see above
		if err != nil {
			continue
		}
		if workflow.DeclaredName(src) == name {
			return path, true
		}
	}
	return "", false
}

// shadowedBy lists the lower-precedence scopes whose entries this file will
// take the name from, in precedence order.
//
// It is deliberately incomplete in one direction, and says so in the help
// text rather than implying otherwise: a global file may later be shadowed by
// a project file in any repository, and without a daemon this command cannot
// know which repositories exist. It does not ask for one — a shadow that has
// not happened yet is not a fact about this write.
func shadowedBy(t initTarget) []string {
	shadows := []string{}
	if t.scope == string(workflow.ScopeProject) {
		if dirs, err := config.ResolveDirs(); err == nil {
			if _, taken := nameTakenIn(filepath.Join(dirs.Config, workflow.GlobalDirName), t.name); taken {
				shadows = append(shadows, string(workflow.ScopeGlobal))
			}
		}
	}
	if workflow.IsBuiltin(t.name) {
		shadows = append(shadows, string(workflow.ScopeBuiltin))
	}
	return shadows
}
