package cli

import (
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/lezli01/vincent/internal/apiclient"
)

// newTaskImportCmd is `vincent task import <archive.tar.gz> <task-id>` (task
// 117): one archived task back out of a `daemon backup` archive.
//
// A thin API client, not a third exception to §4 beside `daemon restore`
// (decision 1): inserting rows opens the database, and only the daemon does
// that. So this resolves the archive path and POSTs it, the way `backup`
// resolves its destination.
func newTaskImportCmd() *cobra.Command {
	var projectID int64
	cmd := &cobra.Command{
		Use:   "import <archive.tar.gz> <task-id>",
		Short: "Bring one archived task back from a backup archive (needs a running daemon)",
		Long: "Copy one task — its row, its step attempts and its transcripts — out of an archive " +
			"written by `vincent daemon backup` into this installation. It is the undo for " +
			"`vincent task delete`, and also imports a task from another installation's backup.\n\n" +
			"The task keeps its id and comes back archived. Refused when a task here already " +
			"holds that id, when the task was not archived in the backup, when a fan-out lane's " +
			"parent is not here, when transcripts for that id already exist, and when no project " +
			"here has the backed-up project's id and name — pass --project to import into " +
			"another project instead. Step attempt ids are kept when all are free and all " +
			"renumbered otherwise. No branch or worktree is touched.",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := strconv.ParseInt(args[1], 10, 64)
			if err != nil || id <= 0 {
				return fmt.Errorf("invalid task id %q", args[1])
			}
			// Resolved here for backup's reason: the daemon's working
			// directory is not this shell's.
			archive, err := filepath.Abs(args[0])
			if err != nil {
				return err
			}
			c, err := client(cmd)
			if err != nil {
				if errors.Is(err, errDaemonUnreachable) {
					_, _ = fmt.Fprintln(cmd.ErrOrStderr(),
						"import needs a running daemon: only the daemon opens the database, "+
							"and only it can write a task back into it.")
				}
				return err
			}
			res, err := c.ImportTask(cmd.Context(), apiclient.TaskImportRequest{
				Path: archive, TaskID: id, ProjectID: projectID,
			})
			if err != nil {
				_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "Error:", apiMessage(err))
				return exitError{code: 1}
			}
			if wantJSON(cmd) {
				return emitJSON(cmd.OutOrStdout(), res)
			}
			return renderTaskImport(cmd.OutOrStdout(), res)
		},
	}
	cmd.Flags().Int64Var(&projectID, "project", 0,
		"Import into this project instead of the backed-up one")
	jsonFlag(cmd)
	return cmd
}

func renderTaskImport(w io.Writer, res apiclient.TaskImportResult) error {
	ids := "ids kept"
	if res.StepRunsRenumbered {
		ids = "ids renumbered"
	}
	_, err := fmt.Fprintf(w, "imported task %d %q into project %d: %d step run(s), %s, transcripts %s\n",
		res.TaskID, res.Title, res.ProjectID, res.StepRuns, ids, humanBytes(res.TranscriptBytes))
	return err
}
