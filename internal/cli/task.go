package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"slices"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/lezli01/vincent/internal/apiclient"
)

func newTaskCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "task",
		Short: "Create, inspect and act on tasks",
	}
	cmd.AddCommand(newTaskAddCmd(), newTaskLsCmd(), newTaskShowCmd(), newTaskCancelCmd(),
		newTaskFollowUpCmd(), newTaskTranscriptCmd(), newTaskPauseCmd(), newTaskResumeCmd(),
		newTaskSkipCmd(), newTaskApproveCmd(), newTaskRejectCmd(), newTaskRetryCmd(),
		newTaskRepairCmd(), newTaskArchiveCmd(), newTaskAnswerCmd())
	return cmd
}

func newTaskAddCmd() *cobra.Command {
	var (
		projectID   int64
		workflow    string
		title       string
		description string
		baseBranch  string
		priority    int
		branch      string
		agent       string
		model       string
		effort      string
		fields      []string
		fieldsFile  string
		githubIssue int
		githubPull  int
	)
	cmd := &cobra.Command{
		Use:   "add",
		Short: "Create a task",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			flagFields, err := parseFieldFlags(fields)
			if err != nil {
				return err
			}
			var fileFields map[string]string
			if cmd.Flags().Changed("fields-file") {
				if fileFields, err = readFieldsFile(fieldsFile, cmd.InOrStdin()); err != nil {
					return err
				}
			}
			fieldMap := mergeTaskFields(fileFields, flagFields)
			return withClient(cmd, func(ctx context.Context, c *apiclient.Client) error {
				req := apiclient.CreateTaskRequest{ProjectID: projectID, Title: title, Fields: fieldMap}
				for _, f := range []struct {
					name string
					dst  **string
					src  *string
				}{
					{"workflow", &req.Workflow, &workflow},
					{"description", &req.Description, &description},
					{"base-branch", &req.BaseBranch, &baseBranch},
					{"branch", &req.BranchName, &branch},
					{"agent", &req.Agent, &agent},
					{"model", &req.Model, &model},
					{"effort", &req.Effort, &effort},
				} {
					if cmd.Flags().Changed(f.name) {
						v := *f.src
						*f.dst = &v
					}
				}
				if cmd.Flags().Changed("priority") {
					p := priority
					req.Priority = &p
				}
				// Resolved daemon-side, deliberately (task 035 decision 2):
				// the flag carries the number and nothing else, so the CLI
				// and the TUI go through one prefill implementation and
				// cannot drift into producing different tasks from the same
				// issue. Every explicit flag above already sits in req, and
				// the daemon fills only what is still unset.
				if cmd.Flags().Changed("github-issue") {
					n := githubIssue
					req.GitHubIssue = &n
				}
				// The same shape for a pull request (task 064): the flag
				// carries the number and nothing else, and the daemon
				// resolves it — same prefill implementation, and the head
				// branch it names becomes the task's branch server-side.
				if cmd.Flags().Changed("github-pull") {
					n := githubPull
					req.GitHubPull = &n
				}
				t, err := c.CreateTask(ctx, req)
				if err != nil {
					_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "Error:", apiMessage(err))
					return exitError{code: 1}
				}
				if wantJSON(cmd) {
					return emitJSON(cmd.OutOrStdout(), t)
				}
				out := cmd.OutOrStdout()
				if _, err := fmt.Fprintf(out, "task %d created: %s (%s, branch %s)\n",
					t.ID, t.Title, t.Workflow, t.BranchName); err != nil {
					return err
				}
				// Which issue the daemon actually resolved, said out loud: the
				// flag carried a number, and the title it produced came from
				// somewhere the user cannot see from here.
				if summary := githubIssueSummary(t.GitHubIssue); summary != "" {
					if _, err := fmt.Fprintln(out, "  "+summary); err != nil {
						return err
					}
				}
				// And which pull request, with the branch consequence stated:
				// the task's branch is the pull request's head, which is the
				// one thing --branch-name cannot change (task 064).
				if summary := githubPullSummary(t); summary != "" {
					if _, err := fmt.Fprintln(out, "  "+summary); err != nil {
						return err
					}
				}
				// Which fields the task actually carries, confirmed by name.
				// Read off the *response*, not off what was sent, so a field
				// the daemon prefilled from --github-issue (task 035) is
				// confirmed here too.
				if summary := fieldsSummary(t.Fields); summary != "" {
					if _, err := fmt.Fprintln(out, "  "+summary); err != nil {
						return err
					}
				}
				// Warnings are advisory — a catalog-unknown model, say. The
				// task exists and will run, so this is not an error exit; but
				// it goes to stderr so `--json` piping stays clean and a human
				// still sees it.
				for _, w := range t.Warnings {
					_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "warning:", w)
				}
				return nil
			})
		},
	}
	cmd.Flags().Int64Var(&projectID, "project", 0, "Project id (required)")
	cmd.Flags().StringVar(&workflow, "workflow", "", "Workflow name (default: the project's)")
	cmd.Flags().StringVar(&title, "title", "", "Task title (required)")
	cmd.Flags().StringVar(&description, "description", "", "Task description")
	cmd.Flags().StringVar(&baseBranch, "base-branch", "", "Base branch (default: the project's)")
	cmd.Flags().StringVar(&branch, "branch", "",
		"Name for the task's branch, used verbatim (default: the project or config template)")
	cmd.Flags().IntVar(&priority, "priority", 0, "Scheduling priority; higher runs first")
	cmd.Flags().StringVar(&agent, "agent", "", "Agent override (§8.6 level 2)")
	cmd.Flags().StringVar(&model, "model", "", "Model override (§8.6 level 2)")
	cmd.Flags().StringVar(&effort, "effort", "", "Effort override (§8.6 level 2)")
	cmd.Flags().StringArrayVar(&fields, "field", nil,
		"Task field as name=value; repeat for additional fields")
	cmd.Flags().StringVar(&fieldsFile, "fields-file", "",
		"Read task fields from a JSON object of strings in this file, or `-` for stdin; "+
			"a --field of the same name wins")
	cmd.Flags().IntVar(&githubIssue, "github-issue", 0,
		"Create the task from this GitHub issue; explicit flags win over what it would fill in")
	cmd.Flags().IntVar(&githubPull, "github-pull", 0,
		"Create the task from this GitHub pull request and run it on that pull request's head branch; "+
			"explicit flags win over what it would fill in, except --branch-name, which the pull request decides")
	_ = cmd.MarkFlagRequired("project")
	// Both would prefill the same title and description from different
	// sources, and there is no defensible order; the daemon refuses it too.
	cmd.MarkFlagsMutuallyExclusive("github-issue", "github-pull")
	// One of the two, not --title alone: an issue supplies the title, which is
	// the whole point of naming one (task 035). Requiring both would make
	// `--github-issue` a decoration on a title the user had to retype.
	cmd.MarkFlagsOneRequired("title", "github-issue", "github-pull")
	jsonFlag(cmd)
	return cmd
}

// parseFieldFlags preserves everything after the first '=' so URLs, regexes,
// and other structured values do not need CLI-specific escaping. A repeated
// name follows the task field editor's existing rule: the later value wins.
func parseFieldFlags(values []string) (map[string]string, error) {
	if len(values) == 0 {
		return nil, nil
	}
	fields := make(map[string]string, len(values))
	for _, value := range values {
		name, fieldValue, ok := strings.Cut(value, "=")
		name = strings.TrimSpace(name)
		if !ok || name == "" {
			return nil, fmt.Errorf("field must be name=value, got %q", value)
		}
		fields[name] = fieldValue
	}
	return fields, nil
}

// maxFieldsFileBytes bounds what --fields-file reads, at the same 4 MiB the
// API bounds a large request body at (§13.1). Stdin can be an unbounded pipe,
// so the read is capped rather than slurped: refusing here gives the caller
// the answer the daemon would have given them, sooner, and without buffering
// an arbitrary file first. Nothing is re-checked client-side beyond this —
// the per-field bounds and the workflow's declared field contract stay
// daemon-authoritative (§8.1.2), because the CLI is not the only client.
const maxFieldsFileBytes = 4 << 20

// readFieldsFile reads one JSON object of string values from path, or from in
// when path is "-".
//
// Values must be JSON strings: `.Task.Fields` is a map[string]string (§8.1.2),
// and quietly stringifying a number or a boolean would make `{"retries": 3}`
// and `{"retries": "3"}` the same document while a workflow declaring
// `type: integer` can tell them apart. A rejection names the **key only** —
// never the value — for the same reason the human confirmation line does: a
// field can carry a token, and an error message is scrollback and CI logs.
//
// A key repeated inside the object resolves last-wins, which is what
// encoding/json does; it is documented rather than detected, matching the
// rule --field already follows.
func readFieldsFile(path string, in io.Reader) (map[string]string, error) {
	src := "--fields-file " + path
	r := in
	if path != "-" {
		// G304: the path this command's own operator typed after --fields-file.
		f, err := os.Open(path) //nolint:gosec // G304: see above
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", src, err)
		}
		defer func() { _ = f.Close() }()
		r = f
	}
	// One byte past the bound, so a document that exactly fills it still
	// parses and one byte more is caught rather than silently truncated.
	data, err := io.ReadAll(io.LimitReader(r, maxFieldsFileBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", src, err)
	}
	if len(data) > maxFieldsFileBytes {
		return nil, fmt.Errorf("%s must be at most %d bytes", src, maxFieldsFileBytes)
	}

	dec := json.NewDecoder(bytes.NewReader(data))
	var raw map[string]json.RawMessage
	if err := dec.Decode(&raw); err != nil {
		return nil, fmt.Errorf("%s must be one JSON object of string values: %w", src, err)
	}
	// A second document is never silently discarded, the rule §13.1 applies at
	// the API: a caller who concatenated two objects meant something by both.
	var trailing json.RawMessage
	if err := dec.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("%s must contain one JSON object and nothing after it", src)
	}
	if raw == nil { // a literal `null`, which decodes without error
		return nil, fmt.Errorf("%s must be one JSON object of string values, got null", src)
	}

	fields := make(map[string]string, len(raw))
	for name, value := range raw {
		if strings.TrimSpace(name) == "" {
			return nil, fmt.Errorf("%s has an empty field name", src)
		}
		// The leading quote is checked before unmarshalling because a JSON
		// `null` unmarshals into a string without error, and silently
		// recording an absent value as "" is exactly the confusion this
		// rejects everywhere else.
		text := bytes.TrimSpace(value)
		if len(text) == 0 || text[0] != '"' {
			return nil, fmt.Errorf("%s: field %q must be a JSON string", src, name)
		}
		var s string
		if err := json.Unmarshal(text, &s); err != nil {
			return nil, fmt.Errorf("%s: field %q must be a JSON string", src, name)
		}
		fields[name] = s
	}
	return fields, nil
}

// mergeTaskFields lays the --field values over the --fields-file ones, key by
// key. The two combine rather than excluding each other (task 045 decision 2):
// a script that wants to change one key should not have to regenerate the
// whole document, and the flag typed on the same command line is the more specific
// of the two — the same last-wins rule --field already documents, one level
// out. Both nil stays nil, so a task created with neither flag sends exactly
// what it sent before.
func mergeTaskFields(file, flags map[string]string) map[string]string {
	if file == nil {
		return flags
	}
	merged := make(map[string]string, len(file)+len(flags))
	maps.Copy(merged, file)
	maps.Copy(merged, flags)
	return merged
}

// fieldsSummary confirms what a created task carries by field **name and
// count, never value** (task 045 decision 4): a field can hold a ticket key or
// a customer name, and this line lands in scrollback, screenshots and CI logs.
// A count on its own was not enough — the mistake worth catching is the
// mistyped key, and only the names catch that. Empty renders as "" so a task
// with no fields prints nothing extra.
func fieldsSummary(fields map[string]string) string {
	if len(fields) == 0 {
		return ""
	}
	names := slices.Sorted(maps.Keys(fields))
	return fmt.Sprintf("fields: %s (%d)", strings.Join(names, ", "), len(names))
}

func newTaskLsCmd() *cobra.Command {
	var (
		projectID       int64
		state           string
		archived        bool
		includeChildren bool
		parentID        int64
		limit           int
	)
	cmd := &cobra.Command{
		Use:   "ls",
		Short: "List tasks",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return withClient(cmd, func(ctx context.Context, c *apiclient.Client) error {
				opts := apiclient.ListTasksOptions{
					ProjectID: projectID, State: state, Limit: limit,
					// Fan-out lanes are hidden by default (§13.2): the list is
					// the work someone asked for, and a 64-task tree buries it.
					IncludeChildren: includeChildren, ParentID: parentID,
				}
				if archived {
					opts.Archived = apiclient.ArchivedAll
				}
				tasks, err := c.ListTasks(ctx, opts)
				if err != nil {
					_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "Error:", apiMessage(err))
					return exitError{code: 1}
				}
				if tasks == nil {
					tasks = []apiclient.Task{}
				}
				if wantJSON(cmd) {
					return emitJSON(cmd.OutOrStdout(), tasks)
				}
				rows := make([][]string, 0, len(tasks))
				for _, t := range tasks {
					rows = append(rows, []string{
						strconv.FormatInt(t.ID, 10), t.State, t.ProjectName,
						t.Workflow, progress(t), t.BranchName, t.Title,
					})
				}
				// BRANCH is what the cleanup guidance reads off `--archived`
				// (task 001): configurable names mean a `vincent/*` glob no
				// longer finds every branch vincent made.
				return table(cmd.OutOrStdout(),
					[]string{"ID", "STATE", "PROJECT", "WORKFLOW", "STEP", "BRANCH", "TITLE"}, rows)
			})
		},
	}
	cmd.Flags().Int64Var(&projectID, "project", 0, "Only tasks in this project")
	cmd.Flags().StringVar(&state, "state", "", "Only tasks in this state")
	cmd.Flags().BoolVar(&archived, "archived", false, "Include archived tasks")
	cmd.Flags().BoolVar(&includeChildren, "include-children", false,
		"Include fan-out lanes, which are hidden by default")
	cmd.Flags().Int64Var(&parentID, "parent", 0,
		"List one fan-out task's lanes, in merge order")
	cmd.Flags().IntVar(&limit, "limit", 0, "Maximum rows")
	jsonFlag(cmd)
	return cmd
}

// progress renders the step cursor as k/n. A finished task's cursor sits one
// past the last step, so it is clamped rather than shown as 4/3.
func progress(t apiclient.Task) string {
	if t.StepTotal == 0 {
		return "-"
	}
	k := min(t.CurrentStep+1, t.StepTotal)
	return fmt.Sprintf("%d/%d", k, t.StepTotal)
}

func newTaskShowCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "show <id>",
		Short: "Show a task and its step runs",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := taskID(args[0])
			if err != nil {
				return err
			}
			return withClient(cmd, func(ctx context.Context, c *apiclient.Client) error {
				t, err := c.GetTask(ctx, id)
				if err != nil {
					_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "Error:", apiMessage(err))
					return exitError{code: 1}
				}
				if wantJSON(cmd) {
					return emitJSON(cmd.OutOrStdout(), t)
				}
				out := cmd.OutOrStdout()
				fields := [][2]string{
					{"id", strconv.FormatInt(t.ID, 10)},
					{"title", t.Title},
					{"state", t.State},
					{"project", t.ProjectName},
					{"workflow", t.Workflow},
					// Which definition that name resolved to (task 043). Always
					// printed, including `unknown` for a task created before the
					// record: "no provenance" is the honest answer for those, and
					// silently omitting the row would read like the built-in.
					{"origin", t.WorkflowOrigin.Display()},
					{"step", progress(t.Task)},
					{"branch", t.BranchName},
				}
				if t.BlockReason != nil && *t.BlockReason != "" {
					fields = append(fields, [2]string{"blocked", *t.BlockReason})
				}
				// What may happen next, from the daemon rather than from a
				// guess: a script reads this instead of probing for 409s, and
				// every name in it is a `vincent task <action>` subcommand.
				if len(t.AvailableActions) > 0 {
					fields = append(fields,
						[2]string{"actions", strings.Join(t.AvailableActions, ", ")})
				}
				if t.InputTokens > 0 || t.OutputTokens > 0 {
					fields = append(fields, [2]string{
						"tokens",
						fmt.Sprintf("%d in / %d out", t.InputTokens, t.OutputTokens),
					})
				}
				// Cost is nil when nothing reported one, which is not the same
				// as free and must not render as $0.00.
				if t.CostUSD != nil {
					fields = append(fields, [2]string{"cost", fmt.Sprintf("$%.4f", *t.CostUSD)})
				}
				for _, f := range fields {
					if _, err := fmt.Fprintf(out, "%-9s %s\n", f[0], f[1]); err != nil {
						return err
					}
				}
				// The pending request is printed before the description because
				// it is the thing that needs doing: `task answer` numbers its
				// questions off exactly this list, and indexed answers are
				// unusable if the questions are invisible from here.
				if err := printPendingRequest(out, t); err != nil {
					return err
				}
				if t.Description != "" {
					if _, err := fmt.Fprintf(out, "\n%s\n", strings.TrimRight(t.Description, "\n")); err != nil {
						return err
					}
				}
				if len(t.Steps) == 0 {
					return nil
				}
				if _, err := fmt.Fprintln(out); err != nil {
					return err
				}
				rows := make([][]string, 0, len(t.Steps))
				for _, s := range t.Steps {
					rows = append(rows, []string{
						strconv.FormatInt(s.ID, 10), s.StepID, s.State,
						dash(deref(s.Agent)), dash(deref(s.FailureReason)),
						dash(deref(s.StatusMessage)),
					})
				}
				// STATUS sits after REASON rather than beside it, and is the
				// last column, because they are different kinds of thing: the
				// reason is the daemon's closed verdict and the status is what
				// the step said about itself (§5.4). A tabwriter's last column
				// is also the one free to be long.
				if err := table(out,
					[]string{"RUN", "STEP", "STATE", "AGENT", "REASON", "STATUS"}, rows); err != nil {
					return err
				}
				// The transcript is the complete record of what the agent did
				// (§17). `vincent task transcript <id> --step RUN` prints it
				// as of task 047; the paths stay because a file is still the
				// thing you grep, copy or attach to a bug report, and the RUN
				// column above is the id that command takes.
				var paths []string
				for _, s := range t.Steps {
					if p := deref(s.TranscriptPath); p != "" {
						paths = append(paths, fmt.Sprintf("  %d  %s", s.ID, p))
					}
				}
				if len(paths) == 0 {
					return nil
				}
				_, err = fmt.Fprintf(out, "\ntranscripts:\n%s\n", strings.Join(paths, "\n"))
				return err
			})
		},
	}
	jsonFlag(cmd)
	return cmd
}

// newTaskFollowUpCmd is the one human action of §6 with a command line
// (task 027 decision 11). Retry, repair, skip and approve are deliberately
// TUI-and-API only (task 025 decision 12) and stay that way; this one breaks
// with them because "rebase these six finished branches onto current master"
// is a batch, and a batch wants a shell loop rather than six visits to a
// form. The unevenness that leaves is accepted rather than papered over.
func newTaskFollowUpCmd() *cobra.Command {
	var (
		prompt   string
		run      string
		workflow string
		agent    string
		model    string
		effort   string
	)
	cmd := &cobra.Command{
		Use:   "follow-up <id>",
		Short: "Run more work in a finished task's worktree, before it is archived",
		Long: "Run one more piece of work in a done or aborted task's existing worktree and " +
			"branch, recorded in that task's own ledger. Exactly one of --prompt, --run and " +
			"--workflow says what to run. The task returns to the state it came from.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := taskID(args[0])
			if err != nil {
				return err
			}
			in := apiclient.FollowUpInput{
				Prompt: prompt, Run: run, Workflow: workflow,
				Agent: agent, Model: model, Effort: effort,
			}
			return withClient(cmd, func(ctx context.Context, c *apiclient.Client) error {
				t, warnings, err := c.FollowUp(ctx, id, in)
				if err != nil {
					// A 409 here is the FSM refusing the action (§6) — the
					// task is neither done nor aborted — which is a rejected
					// request, not a broken one.
					_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "Error:", apiMessage(err))
					return exitError{code: 1}
				}
				if wantJSON(cmd) {
					return emitJSON(cmd.OutOrStdout(), t)
				}
				if _, err := fmt.Fprintf(cmd.OutOrStdout(),
					"task %d is now %s: the follow-up is queued\n", t.ID, t.State); err != nil {
					return err
				}
				for _, w := range warnings {
					_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "warning:", w)
				}
				return nil
			})
		},
	}
	cmd.Flags().StringVar(&prompt, "prompt", "", "Run an agent with this prompt")
	cmd.Flags().StringVar(&run, "run", "", "Run this shell command (§8.3: /bin/sh, or pwsh on Windows)")
	cmd.Flags().StringVar(&workflow, "workflow", "", "Run this registry workflow")
	cmd.Flags().StringVar(&agent, "agent", "", "Agent for the run (§8.6, request level)")
	cmd.Flags().StringVar(&model, "model", "", "Model for the run (§8.6, request level)")
	cmd.Flags().StringVar(&effort, "effort", "", "Effort for the run (§8.6, request level)")
	// One thing runs. Cobra refuses the combination locally so the daemon
	// never sees a request that says two things at once, and the message
	// names the flags rather than the JSON fields behind them.
	cmd.MarkFlagsMutuallyExclusive("prompt", "run", "workflow")
	cmd.MarkFlagsOneRequired("prompt", "run", "workflow")
	jsonFlag(cmd)
	return cmd
}

func newTaskCancelCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cancel <id>",
		Short: "Cancel a task",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := taskID(args[0])
			if err != nil {
				return err
			}
			return withClient(cmd, func(ctx context.Context, c *apiclient.Client) error {
				t, err := c.Cancel(ctx, id)
				if err != nil {
					// A 409 here is the FSM refusing the action (§6), which is
					// a rejected request, not a broken one: exit 1 with the
					// daemon's own wording.
					_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "Error:", apiMessage(err))
					return exitError{code: 1}
				}
				if wantJSON(cmd) {
					return emitJSON(cmd.OutOrStdout(), t)
				}
				_, err = fmt.Fprintf(cmd.OutOrStdout(), "task %d is now %s\n", t.ID, t.State)
				return err
			})
		},
	}
	jsonFlag(cmd)
	return cmd
}

// taskID parses the single <id> argument every task subcommand takes. A
// non-numeric id is a usage error, not a rejected request: it never reaches
// the daemon.
func taskID(arg string) (int64, error) {
	id, err := strconv.ParseInt(arg, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("task id must be a number: %q", arg)
	}
	return id, nil
}

// printTaskAction renders the daemon's post-action view of a task. Every §6
// action command ends here, and none of them predicts the transition: what is
// printed is the state the daemon reported *after* it acted (task 048).
func printTaskAction(cmd *cobra.Command, t apiclient.Task) error {
	if wantJSON(cmd) {
		return emitJSON(cmd.OutOrStdout(), t)
	}
	_, err := fmt.Fprintf(cmd.OutOrStdout(), "task %d is now %s\n", t.ID, t.State)
	return err
}

// newTaskActionCmd builds one of the §6 human actions that takes nothing but
// an id. The error shape is `task cancel`'s: a 409 is the FSM refusing the
// action for the state the task is actually in, which is a rejected request
// rather than a broken one, so it exits 1 carrying the daemon's own wording.
func newTaskActionCmd(name, short, long string,
	act func(context.Context, *apiclient.Client, int64) (apiclient.Task, error),
) *cobra.Command {
	cmd := &cobra.Command{
		Use:   name + " <id>",
		Short: short,
		Long:  long,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := taskID(args[0])
			if err != nil {
				return err
			}
			return withClient(cmd, func(ctx context.Context, c *apiclient.Client) error {
				t, err := act(ctx, c, id)
				if err != nil {
					_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "Error:", apiMessage(err))
					return exitError{code: 1}
				}
				return printTaskAction(cmd, t)
			})
		},
	}
	jsonFlag(cmd)
	return cmd
}

func newTaskPauseCmd() *cobra.Command {
	return newTaskActionCmd("pause", "Hold a task at its next step boundary",
		"Requests a pause. A running step finishes first — the task pauses at the next "+
			"step boundary rather than mid-step. Valid from queued and running.",
		func(ctx context.Context, c *apiclient.Client, id int64) (apiclient.Task, error) {
			return c.Pause(ctx, id)
		})
}

func newTaskResumeCmd() *cobra.Command {
	return newTaskActionCmd("resume", "Re-queue a paused task",
		"Returns a paused task to the queue; the scheduler admits it like any other. "+
			"Valid from paused.",
		func(ctx context.Context, c *apiclient.Client, id int64) (apiclient.Task, error) {
			return c.Resume(ctx, id)
		})
}

func newTaskSkipCmd() *cobra.Command {
	return newTaskActionCmd("skip", "Mark the current step skipped and advance",
		"Abandons the step the task is sitting on and advances to the next one. "+
			"Valid from blocked and awaiting_gate.",
		func(ctx context.Context, c *apiclient.Client, id int64) (apiclient.Task, error) {
			return c.Skip(ctx, id)
		})
}

func newTaskApproveCmd() *cobra.Command {
	return newTaskActionCmd("approve", "Pass a manual gate",
		"Passes the manual gate the task is waiting on and continues the workflow. "+
			"Valid from awaiting_gate.",
		func(ctx context.Context, c *apiclient.Client, id int64) (apiclient.Task, error) {
			return c.Approve(ctx, id)
		})
}

func newTaskRejectCmd() *cobra.Command {
	return newTaskActionCmd("reject", "Fail a manual gate, blocking the task",
		"Fails the manual gate the task is waiting on; the task blocks with reason "+
			"gate_rejected. Valid from awaiting_gate.",
		func(ctx context.Context, c *apiclient.Client, id int64) (apiclient.Task, error) {
			return c.Reject(ctx, id)
		})
}

func newTaskRetryCmd() *cobra.Command {
	var branch, prompt, promptFile, run, runFile string
	cmd := &cobra.Command{
		Use:   "retry <id>",
		Short: "Re-run the step a task blocked on",
		Long: "Re-runs the step the task blocked on. With no flags this is a plain retry.\n" +
			"--prompt and --run are edit+retry: the text replaces that step's prompt or\n" +
			"command in this task's workflow snapshot, and in no other task's. --branch\n" +
			"renames the task's branch before the retry re-admits it, which is how a\n" +
			"branch_exists block is cleared without losing the task or its transcripts.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := taskID(args[0])
			if err != nil {
				return err
			}
			ov := apiclient.Override{Branch: branch}
			if ov.Prompt, err = flagText(cmd, prompt, promptFile); err != nil {
				return err
			}
			if ov.Run, err = flagText(cmd, run, runFile); err != nil {
				return err
			}
			return withClient(cmd, func(ctx context.Context, c *apiclient.Client) error {
				t, err := c.Retry(ctx, id, ov)
				if err != nil {
					_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "Error:", apiMessage(err))
					return exitError{code: 1}
				}
				return printTaskAction(cmd, t)
			})
		},
	}
	cmd.Flags().StringVar(&branch, "branch", "",
		"Rename the task's branch first — the branch_exists recovery path (§10, §18)")
	cmd.Flags().StringVar(&prompt, "prompt", "", "Replace the failed step's prompt (edit+retry)")
	cmd.Flags().StringVar(&promptFile, "prompt-file", "",
		"Read the replacement prompt from this file, or from stdin with -")
	cmd.Flags().StringVar(&run, "run", "", "Replace the failed step's command (edit+retry)")
	cmd.Flags().StringVar(&runFile, "run-file", "",
		"Read the replacement command from this file, or from stdin with -")
	// The literal and the file are two spellings of one value, so cobra
	// refuses the pair locally rather than letting one silently win.
	cmd.MarkFlagsMutuallyExclusive("prompt", "prompt-file")
	cmd.MarkFlagsMutuallyExclusive("run", "run-file")
	jsonFlag(cmd)
	return cmd
}

func newTaskRepairCmd() *cobra.Command {
	var prompt, promptFile, agent, model, effort string
	cmd := &cobra.Command{
		Use:   "repair <id>",
		Short: "Run a one-off agent in a blocked task's worktree",
		Long: "Runs one ad-hoc agent with this prompt in the blocked task's existing\n" +
			"worktree (task 025). Whatever the agent does, the task returns to blocked at\n" +
			"the same step with the same reason: a repair changes the worktree, and a\n" +
			"human still decides whether to retry. Valid from blocked.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := taskID(args[0])
			if err != nil {
				return err
			}
			in := apiclient.RepairInput{Agent: agent, Model: model, Effort: effort}
			if in.Prompt, err = flagText(cmd, prompt, promptFile); err != nil {
				return err
			}
			return withClient(cmd, func(ctx context.Context, c *apiclient.Client) error {
				t, warnings, err := c.Repair(ctx, id, in)
				if err != nil {
					_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "Error:", apiMessage(err))
					return exitError{code: 1}
				}
				if err := printTaskAction(cmd, t); err != nil {
					return err
				}
				// The §8.2 catalog warnings the selection raised, on stderr for
				// the reason `task add`'s are: the run is queued and will
				// happen, so this is not a failure, and stdout stays pipeable.
				for _, w := range warnings {
					_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "warning:", w)
				}
				return nil
			})
		},
	}
	cmd.Flags().StringVar(&prompt, "prompt", "", "What the repair agent should do (required)")
	cmd.Flags().StringVar(&promptFile, "prompt-file", "",
		"Read the prompt from this file, or from stdin with -")
	cmd.Flags().StringVar(&agent, "agent", "", "Agent for this run (§8.6, request level)")
	cmd.Flags().StringVar(&model, "model", "", "Model for this run (§8.6, request level)")
	cmd.Flags().StringVar(&effort, "effort", "", "Effort for this run (§8.6, request level)")
	cmd.MarkFlagsMutuallyExclusive("prompt", "prompt-file")
	// A repair with no instructions is a 400 from the daemon; saying so here
	// costs no round trip and names the flags rather than the JSON field.
	cmd.MarkFlagsOneRequired("prompt", "prompt-file")
	jsonFlag(cmd)
	return cmd
}

func newTaskArchiveCmd() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "archive <id>",
		Short: "Archive a finished task and remove its worktree",
		Long: "Archives the task and removes its worktree. When\n" +
			"delete_empty_branch_on_archive is on, a branch carrying no commits past its\n" +
			"base is deleted too. A worktree with uncommitted changes is refused until\n" +
			"--force is passed — force is the confirmation. Valid from done and aborted.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := taskID(args[0])
			if err != nil {
				return err
			}
			return withClient(cmd, func(ctx context.Context, c *apiclient.Client) error {
				t, branch, err := c.Archive(ctx, id, force)
				if err != nil {
					_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "Error:", apiMessage(err))
					// The one refusal that is a question rather than a failure
					// (§6): the reason lives in details, not in the message, so
					// flattening the error to its prose would lose both the
					// discriminator and the way out.
					if isDirtyWorktreeErr(err) {
						_, _ = fmt.Fprintln(cmd.ErrOrStderr(),
							"  pass --force to archive it anyway, discarding those changes")
					}
					return exitError{code: 1}
				}
				if wantJSON(cmd) {
					// The daemon's own shape: the task's fields at the top
					// level beside `branch`, so a script reads the branch
					// outcome here rather than only from the human line.
					return emitJSON(cmd.OutOrStdout(), struct {
						apiclient.Task
						Branch *apiclient.BranchOutcome `json:"branch,omitempty"`
					}{Task: t, Branch: branchOutcome(branch)})
				}
				out := cmd.OutOrStdout()
				if _, err := fmt.Fprintf(out, "task %d is now %s\n", t.ID, t.State); err != nil {
					return err
				}
				if summary := branch.Summary(); summary != "" {
					_, err = fmt.Fprintln(out, "  "+summary)
				}
				return err
			})
		},
	}
	cmd.Flags().BoolVar(&force, "force", false,
		"Archive even when the worktree has uncommitted changes, discarding them")
	jsonFlag(cmd)
	return cmd
}

// branchOutcome renders the archive's branch result for --json. A zero value
// means the branch step never ran — the setting is off, or the task had no
// branch of its own — which is `null`, not an object full of empty strings.
func branchOutcome(o apiclient.BranchOutcome) *apiclient.BranchOutcome {
	if o == (apiclient.BranchOutcome{}) {
		return nil
	}
	return &o
}

// isDirtyWorktreeErr reports the daemon refused an archive because the
// worktree has uncommitted changes. `details.reason` is the discriminator the
// TUI branches on too (§13.1); the message alone is prose.
func isDirtyWorktreeErr(err error) bool {
	var apiErr *apiclient.Error
	return errors.As(err, &apiErr) && apiErr.Details["reason"] == "worktree_dirty"
}

func newTaskAnswerCmd() *cobra.Command {
	var (
		answers  []string
		allow    bool
		deny     bool
		bodyFile string
	)
	cmd := &cobra.Command{
		Use:   "answer <id>",
		Short: "Answer the input request a task is waiting on",
		Long: "Answers the §7.4 request an awaiting_input task is parked on; the run\n" +
			"resumes in place. Questions are answered by the number `vincent task show`\n" +
			"prints them under — repeat --answer for one index to give a multi-select\n" +
			"several values — and a permission request takes --allow or --deny. --body\n" +
			"passes an answer payload straight through for a script that already has one.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := taskID(args[0])
			if err != nil {
				return err
			}
			if bodyFile != "" {
				payload, err := flagText(cmd, "", bodyFile)
				if err != nil {
					return err
				}
				if !json.Valid([]byte(payload)) {
					return fmt.Errorf("--body %s is not valid JSON", bodyFile)
				}
				return withClient(cmd, func(ctx context.Context, c *apiclient.Client) error {
					t, err := c.AnswerRaw(ctx, id, json.RawMessage(payload))
					if err != nil {
						_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "Error:", apiMessage(err))
						return exitError{code: 1}
					}
					return printTaskAction(cmd, t)
				})
			}
			return withClient(cmd, func(ctx context.Context, c *apiclient.Client) error {
				// The request has to be read before it can be answered: the
				// wire format is keyed by question *text* (§13.2), and the
				// index exists only so nobody retypes quoted prose.
				detail, err := c.GetTask(ctx, id)
				if err != nil {
					_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "Error:", apiMessage(err))
					return exitError{code: 1}
				}
				req, ok, err := detail.PendingRequest()
				if err != nil {
					return err
				}
				if !ok {
					return fmt.Errorf("task %d is %s and is not waiting for input", id, detail.State)
				}
				resp, err := answerResponse(req, answers, allow, deny)
				if err != nil {
					return err
				}
				// Local validation is a convenience, never the authority: the
				// daemon checks the same rules and its answer is the one that
				// counts. Failing here just saves a round trip to be told the
				// obvious.
				if err := req.Validate(resp); err != nil {
					return err
				}
				t, err := c.Answer(ctx, id, resp)
				if err != nil {
					_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "Error:", apiMessage(err))
					return exitError{code: 1}
				}
				return printTaskAction(cmd, t)
			})
		},
	}
	// No backquotes in a flag's usage string: cobra reads the first
	// backquoted word as the argument placeholder, so "`task show`" would
	// render the flag as `--answer task show`.
	cmd.Flags().StringArrayVar(&answers, "answer", nil,
		"Answer as <n>=<value>, n being the question number 'vincent task show' prints; repeat for more")
	cmd.Flags().BoolVar(&allow, "allow", false, "Allow a permission request")
	cmd.Flags().BoolVar(&deny, "deny", false, "Deny a permission request")
	cmd.Flags().StringVar(&bodyFile, "body", "",
		"Read the §13.2 answer payload from this file, or from stdin with -, and post it verbatim")
	// One way of answering per invocation: allow and deny contradict each
	// other, a question is not a permission, and --body is the escape hatch
	// that reconstructs neither.
	cmd.MarkFlagsMutuallyExclusive("answer", "allow", "deny", "body")
	cmd.MarkFlagsOneRequired("answer", "allow", "deny", "body")
	jsonFlag(cmd)
	return cmd
}

// answerResponse maps the CLI's flags onto the §13.2 answer payload.
//
// --answer is indexed because the wire format is keyed by question text, and
// that text is a sentence: indexing off `task show` keeps the format intact
// without making anyone retype it. Values split on the *first* '=', the rule
// parseFieldFlags already documents, so a URL or a regex needs no escaping,
// and a repeated index is a multi-select's several values.
func answerResponse(req apiclient.InputRequest, answers []string, allow, deny bool) (apiclient.InputResponse, error) {
	var resp apiclient.InputResponse
	switch {
	case allow:
		v := true
		resp.Allow = &v
	case deny:
		v := false
		resp.Allow = &v
	}
	if len(answers) == 0 {
		return resp, nil
	}
	if req.Kind == apiclient.InputKindPermission {
		return apiclient.InputResponse{},
			errors.New("this is a permission request: answer it with --allow or --deny")
	}
	resp.Answers = make(map[string][]string, len(answers))
	for _, a := range answers {
		index, value, ok := strings.Cut(a, "=")
		n, convErr := strconv.Atoi(strings.TrimSpace(index))
		if !ok || convErr != nil {
			return apiclient.InputResponse{},
				fmt.Errorf("answer must be <number>=<value>, got %q", a)
		}
		if n < 1 || n > len(req.Questions) {
			return apiclient.InputResponse{},
				fmt.Errorf("no question %d: the task is asking %d, numbered as `vincent task show` prints them",
					n, len(req.Questions))
		}
		text := req.Questions[n-1].Text
		resp.Answers[text] = append(resp.Answers[text], value)
	}
	return resp, nil
}

// flagText resolves a --x / --x-file flag pair to one string. Cobra has
// already refused the combination, so at most one is set here. "-" reads
// stdin, which is what makes a multi-line replacement prompt something a
// pipe can carry rather than something argv has to quote.
func flagText(cmd *cobra.Command, literal, file string) (string, error) {
	if file == "" {
		return literal, nil
	}
	if file == "-" {
		b, err := io.ReadAll(cmd.InOrStdin())
		if err != nil {
			return "", fmt.Errorf("read stdin: %w", err)
		}
		return string(b), nil
	}
	// G304: a path the user of this CLI typed, read with that user's own
	// privileges — the whole purpose of the flag.
	b, err := os.ReadFile(file) //nolint:gosec // G304: see above
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// printPendingRequest renders the §7.4 request an awaiting_input task is
// parked on. The numbering is the contract `task answer --answer <n>=<value>`
// reads: question 1 is the first question the daemon sent, in the order it
// sent them. A task waiting on nothing prints nothing.
func printPendingRequest(w io.Writer, t apiclient.TaskDetail) error {
	req, ok, err := t.PendingRequest()
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	return printInputRequest(w, req, fmt.Sprintf("vincent task answer %d", t.ID))
}

// printInputRequest renders a §7.4 request under a numbering both
// `task answer` and `chat answer` read: question 1 is the first question the
// daemon sent, in the order it sent them. answerCmd is the invocation the
// hint names, which is the only thing that differs between the two.
func printInputRequest(w io.Writer, req apiclient.InputRequest, answerCmd string) error {
	if _, err := fmt.Fprintf(w, "\nawaiting input: %s\n", req.Kind); err != nil {
		return err
	}
	if req.Kind == apiclient.InputKindPermission {
		if req.Permission == nil {
			return nil
		}
		_, err := fmt.Fprintf(w, "  tool     %s\n  summary  %s\n"+
			"  answer with `%s --allow` or `--deny`\n",
			req.Permission.Tool, req.Permission.Summary, answerCmd)
		return err
	}
	for i, q := range req.Questions {
		text := q.Text
		if q.Header != "" {
			text = q.Header + ": " + text
		}
		if q.MultiSelect {
			text += "  (one or more)"
		}
		if _, err := fmt.Fprintf(w, "  %d. %s\n", i+1, text); err != nil {
			return err
		}
		// Options are suggestions, never an enum — §7.4 accepts free text for
		// any question — so they are listed as such rather than as a menu.
		if len(q.Options) > 0 {
			if _, err := fmt.Fprintf(w, "     suggested: %s\n", strings.Join(q.Options, ", ")); err != nil {
				return err
			}
		}
	}
	if len(req.Questions) == 0 {
		return nil
	}
	_, err := fmt.Fprintf(w, "  answer with `%s --answer 1=<value>`\n", answerCmd)
	return err
}
