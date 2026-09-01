package cli

// `vincent chat handoff` — the CLI half of task 074, so handing a chat's
// worktree to a task is not a TUI-only feature.

import (
	"context"
	"fmt"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/lezli01/vincent/internal/apiclient"
)

// newChatHandoffCmd carries `vincent task add`'s flags, minus the three a
// handoff has no say in: the project, the base branch and the branch name all
// come from the chat, verbatim. Everything else — the workflow, the title, the
// description, the fields, the priority and the §8.6 overrides — is the
// ordinary task-create body, validated daemon-side by the same code
// `POST /v1/tasks` uses.
//
// The description is where conversational context goes (task 074 decision 3).
// Nothing about the chat is injected into the workflow's prompts
// automatically: the objective is asked for here the way `task add` asks for
// it, which is the issue's "not injected wholesale" honoured by having nothing
// to inject.
func newChatHandoffCmd() *cobra.Command {
	var (
		workflow    string
		title       string
		description string
		priority    int
		agent       string
		model       string
		effort      string
		fields      []string
		fieldsFile  string
	)
	cmd := &cobra.Command{
		Use:   "handoff <chat-id>",
		Short: "Hand a chat's worktree and branch to a new task",
		Long: "Creates a task that adopts the chat's worktree, branch, base branch and base " +
			"SHA exactly as they are — no copy, no rename, no implicit commit, so committed " +
			"and uncommitted work are both there when the task's first step runs.\n\n" +
			"The chat becomes terminal (`handed_off`) and links to the task; the task owns " +
			"the worktree and the branch from then on. Only an idle chat can be handed off, " +
			"and a worktree in the middle of a merge or rebase is refused by name.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return fmt.Errorf("chat id: %w", err)
			}
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
			return withClient(cmd, func(ctx context.Context, c *apiclient.Client) error {
				req := apiclient.CreateTaskRequest{
					Title: title, Fields: mergeTaskFields(fileFields, flagFields),
				}
				for _, f := range []struct {
					name string
					dst  **string
					src  *string
				}{
					{"workflow", &req.Workflow, &workflow},
					{"description", &req.Description, &description},
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
				task, chat, err := c.HandoffChat(ctx, id, req)
				if err != nil {
					_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "Error:", apiMessage(err))
					return exitError{code: 1}
				}
				if wantJSON(cmd) {
					return emitJSON(cmd.OutOrStdout(), map[string]any{"task": task, "chat": chat})
				}
				out := cmd.OutOrStdout()
				if _, err := fmt.Fprintf(out, "chat %d handed off to task %d: %s (%s)\n",
					chat.ID, task.ID, task.Title, task.Workflow); err != nil {
					return err
				}
				// The workspace, said out loud: this is the whole claim the
				// command makes, and it is the one thing a reader wants
				// confirmed rather than assumed.
				worktreePath := ""
				if task.WorktreePath != nil {
					worktreePath = *task.WorktreePath
				}
				if _, err := fmt.Fprintf(out, "  branch %s in %s\n",
					task.BranchName, worktreePath); err != nil {
					return err
				}
				for _, w := range task.Warnings {
					_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "warning:", w)
				}
				return nil
			})
		},
	}
	cmd.Flags().StringVar(&workflow, "workflow", "", "Workflow name (default: the project's)")
	cmd.Flags().StringVar(&title, "title", "", "Task title (required)")
	cmd.Flags().StringVar(&description, "description", "",
		"Task description; this is where the conversation's context goes, since nothing "+
			"about the chat reaches the workflow automatically")
	cmd.Flags().IntVar(&priority, "priority", 0, "Scheduling priority; higher runs first")
	cmd.Flags().StringVar(&agent, "agent", "", "Agent override (§8.6 level 2)")
	cmd.Flags().StringVar(&model, "model", "", "Model override (§8.6 level 2)")
	cmd.Flags().StringVar(&effort, "effort", "", "Effort override (§8.6 level 2)")
	cmd.Flags().StringArrayVar(&fields, "field", nil,
		"Task field as name=value; repeat for additional fields")
	cmd.Flags().StringVar(&fieldsFile, "fields-file", "",
		"Read task fields from a JSON object of strings in this file, or `-` for stdin; "+
			"a --field of the same name wins")
	_ = cmd.MarkFlagRequired("title")
	jsonFlag(cmd)
	return cmd
}
