package cli

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/lezli01/vincent/internal/apiclient"
)

// `vincent task delete` and `vincent chat delete` (§13.2, task 092): the
// permanent delete of an archived row, from a terminal.
//
// Neither prompts. Task 048 recorded that the command tree exists for
// scripting and that the daemon's 409s are the confirmation story: delete
// applies to archived rows only, and every other refusal names the row that is
// holding on. A 409 is exit 1 carrying the daemon's own wording, which is what
// keeps one refusal from being worded two ways.

// deleteSweep is the two commands' shared body. It is one function because the
// only difference between them is which endpoint the id goes to and what the
// listing that finds the ids is called.
type deleteSweep struct {
	noun   string // "task" or "chat"
	branch bool
	before string
	// list finds the ids a --before sweep covers.
	list func(ctx context.Context, c *apiclient.Client, cutoff time.Time) ([]int64, error)
	// del sends one row's DELETE.
	del func(ctx context.Context, c *apiclient.Client, id int64, branch bool) (apiclient.BranchOutcome, error)
}

// deleteReport is what --json emits: one entry per row the sweep touched, so a
// script can tell a deletion from a refusal without parsing prose.
type deleteReport struct {
	ID      int64                    `json:"id"`
	Deleted bool                     `json:"deleted"`
	Branch  *apiclient.BranchOutcome `json:"branch,omitempty"`
	Error   string                   `json:"error,omitempty"`
	// Reason is the daemon's snake_case refusal code when it refused.
	Reason string `json:"reason,omitempty"`
}

// run resolves the ids and deletes them one at a time, in order.
//
// One call per row, sequentially: there is no bulk endpoint and there is not
// going to be one (task 011's decision, verbatim). The exit code is 1 if any
// row was refused or failed, and the report is emitted either way — a sweep
// cut off halfway leaves no account of which half.
func (s deleteSweep) run(cmd *cobra.Command, args []string) error {
	return withClient(cmd, func(ctx context.Context, c *apiclient.Client) error {
		ids, err := s.resolve(ctx, c, args)
		if err != nil {
			_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "Error:", apiMessage(err))
			return exitError{code: 1}
		}
		out := cmd.OutOrStdout()
		reports := make([]deleteReport, 0, len(ids))
		bad := false
		for _, id := range ids {
			branch, err := s.del(ctx, c, id, s.branch)
			if err != nil {
				bad = true
				reason, _ := apiclient.DeleteRefused(err)
				reports = append(reports, deleteReport{ID: id, Error: apiMessage(err), Reason: reason})
				if !wantJSON(cmd) {
					_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "Error: %s %d: %s\n", s.noun, id, apiMessage(err))
				}
				continue
			}
			reports = append(reports, deleteReport{ID: id, Deleted: true, Branch: branchOutcome(branch)})
			if !wantJSON(cmd) {
				_, _ = fmt.Fprintf(out, "%s %d deleted\n", s.noun, id)
				if summary := branch.Summary(); summary != "" {
					_, _ = fmt.Fprintln(out, "  "+summary)
				}
			}
		}
		if wantJSON(cmd) {
			if err := emitJSON(out, reports); err != nil {
				return err
			}
		} else if len(ids) == 0 {
			_, _ = fmt.Fprintf(out, "No archived %ss to delete.\n", s.noun)
		}
		if bad {
			return exitError{code: 1}
		}
		return nil
	})
}

// resolve turns the arguments into ids: the ones named, or — with --before —
// every archived row older than the cutoff.
func (s deleteSweep) resolve(ctx context.Context, c *apiclient.Client, args []string) ([]int64, error) {
	if s.before == "" {
		ids := make([]int64, 0, len(args))
		for _, a := range args {
			id, err := strconv.ParseInt(a, 10, 64)
			if err != nil {
				return nil, fmt.Errorf("%s id %q: not an integer", s.noun, a)
			}
			ids = append(ids, id)
		}
		return ids, nil
	}
	cutoff, err := parseBefore(s.before, time.Now())
	if err != nil {
		return nil, err
	}
	return s.list(ctx, c, cutoff)
}

// parseBefore reads --before, which takes either an absolute date
// (2026-01-31, or a full RFC3339 instant) or a duration back from now (30d,
// 12h). Days are spelled out here because time.ParseDuration stops at hours,
// and "delete everything older than thirty days" is the thing this flag is
// for.
func parseBefore(v string, now time.Time) (time.Time, error) {
	if d, ok := strings.CutSuffix(v, "d"); ok {
		days, err := strconv.Atoi(d)
		if err == nil && days >= 0 {
			return now.AddDate(0, 0, -days), nil
		}
	}
	if d, err := time.ParseDuration(v); err == nil && d >= 0 {
		return now.Add(-d), nil
	}
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		return t, nil
	}
	if t, err := time.Parse("2006-01-02", v); err == nil {
		return t, nil
	}
	return time.Time{}, fmt.Errorf("--before %q: expected a date (2026-01-31), an RFC3339 instant, or a duration back from now (30d, 12h)", v)
}

// deleteArgs validates the argument shape both commands share: ids, or
// --before and no ids at all. Mixing them would make "which rows did that
// touch" unanswerable from the command line alone.
func deleteArgs(before *string) cobra.PositionalArgs {
	return func(_ *cobra.Command, args []string) error {
		switch {
		case *before != "" && len(args) > 0:
			return fmt.Errorf("--before sweeps the archive; do not also name ids")
		case *before == "" && len(args) == 0:
			return fmt.Errorf("name at least one id, or pass --before to sweep the archive")
		}
		return nil
	}
}

func newTaskDeleteCmd() *cobra.Command {
	s := deleteSweep{
		noun: "task",
		list: func(ctx context.Context, c *apiclient.Client, cutoff time.Time) ([]int64, error) {
			tasks, err := c.ListTasks(ctx, apiclient.ListTasksOptions{
				Archived: apiclient.ArchivedOnly, ArchivedBefore: cutoff,
			})
			if err != nil {
				return nil, err
			}
			ids := make([]int64, 0, len(tasks))
			for i := range tasks {
				ids = append(ids, tasks[i].ID)
			}
			return ids, nil
		},
		del: func(ctx context.Context, c *apiclient.Client, id int64, branch bool) (apiclient.BranchOutcome, error) {
			return c.DeleteTask(ctx, id, branch)
		},
	}
	cmd := &cobra.Command{
		Use:     "delete <id>...",
		Aliases: []string{"rm"},
		Short:   "Permanently delete archived tasks",
		Long: "Deletes an archived task for good: the row, its step attempts and its\n" +
			"transcripts. Only an archived task can be deleted; anything else is refused,\n" +
			"and so is a fan-out parent whose lanes still exist or a task a handed-off chat\n" +
			"points at. It does not prompt — the daemon's refusals are the confirmation.\n\n" +
			"--branch additionally deletes the task's local branch, but never one carrying\n" +
			"commits past its base: that branch is reported has_commits and kept. The\n" +
			"remote branch is never touched.",
		RunE: s.run,
	}
	cmd.Args = deleteArgs(&s.before)
	cmd.Flags().BoolVar(&s.branch, "branch", false,
		"Also delete the task's local branch, unless it carries commits past its base")
	cmd.Flags().StringVar(&s.before, "before", "",
		"Delete every task archived before this date (2026-01-31) or duration back from now (30d)")
	jsonFlag(cmd)
	return cmd
}

func newChatDeleteCmd() *cobra.Command {
	s := deleteSweep{
		noun: "chat",
		list: func(ctx context.Context, c *apiclient.Client, cutoff time.Time) ([]int64, error) {
			chats, err := c.ListChats(ctx, apiclient.ListChatsOptions{
				Archived: apiclient.ArchivedOnly, ArchivedBefore: cutoff,
			})
			if err != nil {
				return nil, err
			}
			ids := make([]int64, 0, len(chats))
			for i := range chats {
				// A handed-off chat is refused by the daemon, and a sweep is
				// not the place to collect a refusal it can see coming: the
				// task it was handed to owns the worktree, and that task is
				// what to delete.
				if chats[i].State == "handed_off" {
					continue
				}
				ids = append(ids, chats[i].ID)
			}
			return ids, nil
		},
		del: func(ctx context.Context, c *apiclient.Client, id int64, branch bool) (apiclient.BranchOutcome, error) {
			return c.DeleteChat(ctx, id, branch)
		},
	}
	cmd := &cobra.Command{
		Use:     "delete <chat-id>...",
		Aliases: []string{"rm"},
		Short:   "Permanently delete archived chats",
		Long: "Deletes an archived chat for good: the row, its turns and its transcripts.\n" +
			"Only an archived chat can be deleted — a handed-off one is refused, because\n" +
			"the task it was handed to owns its worktree and branch. It does not prompt.\n\n" +
			"--branch additionally deletes the chat's local branch, but never one carrying\n" +
			"commits past its base.",
		RunE: s.run,
	}
	cmd.Args = deleteArgs(&s.before)
	cmd.Flags().BoolVar(&s.branch, "branch", false,
		"Also delete the chat's local branch, unless it carries commits past its base")
	cmd.Flags().StringVar(&s.before, "before", "",
		"Delete every chat that ended before this date (2026-01-31) or duration back from now (30d)")
	jsonFlag(cmd)
	return cmd
}
