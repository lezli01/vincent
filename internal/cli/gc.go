package cli

import (
	"context"
	"fmt"
	"io"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/lezli01/vincent/internal/apiclient"
)

// newGCCmd is `vincent gc` (task 005, §12.1).
//
// The name breaks §12.1's noun-verb pattern knowingly: `git gc` is the idiom
// users already have, and the scope spans two directory trees, so a
// `worktree` noun would have been wrong on the day it shipped.
func newGCCmd() *cobra.Command {
	var (
		dryRun bool
		force  bool
	)
	cmd := &cobra.Command{
		Use:   "gc",
		Short: "Reclaim worktree and transcript directories no task claims",
		Long: "Reclaim directories under the data dir that no task row claims — " +
			"left behind by a project delete whose worktree removal failed, or by a " +
			"crash between creating a worktree and recording it.\n\n" +
			"A worktree with local changes, or one git cannot judge because its " +
			"repository is gone, is skipped and named in the output; --force removes " +
			"it. Branches are never deleted.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return withClient(cmd, func(ctx context.Context, c *apiclient.Client) error {
				rep, err := c.GC(ctx, force, dryRun)
				if err != nil {
					_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "Error:", apiMessage(err))
					return exitError{code: 1}
				}
				if wantJSON(cmd) {
					return emitJSON(cmd.OutOrStdout(), rep)
				}
				return renderGC(cmd.OutOrStdout(), rep)
			})
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Report what would be reclaimed and remove nothing")
	cmd.Flags().BoolVar(&force, "force", false, "Also remove worktrees with local changes, or ones git cannot judge")
	jsonFlag(cmd)
	return cmd
}

// renderGC prints the report a human reads. Every orphan is listed whatever
// happened to it: a run that only printed a total would leave the user with
// no idea which directory the skipped bytes are in.
func renderGC(w io.Writer, rep apiclient.OrphanReport) error {
	if len(rep.Orphans) == 0 && len(rep.Mismatches) == 0 {
		_, err := fmt.Fprintln(w, "nothing to reclaim")
		return err
	}
	if len(rep.Orphans) > 0 {
		rows := make([][]string, 0, len(rep.Orphans))
		for _, o := range rep.Orphans {
			rows = append(rows, []string{
				o.Kind, o.Path, humanBytes(o.Bytes), gcStatus(o, rep.DryRun),
			})
		}
		if err := table(w, []string{"KIND", "PATH", "SIZE", "STATUS"}, rows); err != nil {
			return err
		}
	}
	for _, m := range rep.Mismatches {
		// The reverse mismatch is not a leak and nothing here removes it; it
		// is printed because a task pointing at a directory that is gone is
		// the other half of the same reconciliation (§18).
		if _, err := fmt.Fprintf(w,
			"task %d (%s) points at a worktree that is gone: %s\n",
			m.TaskID, m.State, m.Path); err != nil {
			return err
		}
	}
	if rep.DryRun {
		_, err := fmt.Fprintf(w, "dry run: %d orphan(s), %s — nothing removed\n",
			len(rep.Orphans), humanBytes(rep.Bytes))
		return err
	}
	_, err := fmt.Fprintf(w, "reclaimed %d of %d orphan(s), %s freed\n",
		rep.Reclaimed, len(rep.Orphans), humanBytes(rep.ReclaimedBytes))
	return err
}

// gcStatus is what happened to one entry, in the words the daemon used. A
// skip reason is the daemon's reason string verbatim so the CLI, the API and
// the docs all name it the same way.
func gcStatus(o apiclient.Orphan, dryRun bool) string {
	switch {
	case o.Error != "":
		return "failed: " + o.Error
	case o.SkipReason != "":
		return "skipped: " + o.SkipReason
	case o.Removed:
		return "removed"
	case dryRun:
		return "would remove"
	default:
		return "not removed"
	}
}

// humanBytes renders a size for the report. Powers of 1024, one decimal past
// KB, which is what a user comparing this against `du -h` expects.
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return strconv.FormatInt(n, 10) + "B"
	}
	div, exp := int64(unit), 0
	for n/div >= unit && exp < 3 {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f%s", float64(n)/float64(div), [...]string{"KB", "MB", "GB", "TB"}[exp])
}
