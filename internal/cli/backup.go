package cli

import (
	"errors"
	"fmt"
	"io"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/backup"
	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/daemon"
	"github.com/lezli01/vincent/internal/store"
)

// newDaemonBackupCmd is `vincent daemon backup <path.tar.gz>` (task 029).
//
// A thin API client like the rest of §12.1: the daemon assembles the archive,
// this resolves the destination and prints what came back.
func newDaemonBackupCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "backup <path.tar.gz>",
		Short: "Write a portable copy of daemon state (needs a running daemon)",
		Long: "Write one .tar.gz containing the database, every transcript, config.yaml " +
			"and the global workflows.\n\n" +
			"The database copy is taken with SQLite's own VACUUM INTO, so it is consistent " +
			"even while tasks are running — unlike copying vincent.db, which under WAL is " +
			"missing whatever has not been checkpointed yet.\n\n" +
			"Transcripts are included in full, so the archive is as large as your history " +
			"is; the command prints what it wrote. The destination must not already exist.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			// Resolved here rather than daemon-side: the daemon's working
			// directory is not this shell's, so a relative path would mean
			// two different places to the two processes.
			dst, err := filepath.Abs(args[0])
			if err != nil {
				return err
			}
			c, err := client(cmd)
			if err != nil {
				if errors.Is(err, errDaemonUnreachable) {
					// Same policy as `doctor --fix`, said the same way: only
					// the daemon opens SQLite (§4), so only the daemon can
					// copy it. There is no cold-copy fallback in this
					// command, and the honest alternative is in the docs.
					_, _ = fmt.Fprintln(cmd.ErrOrStderr(),
						"backup needs a running daemon: only the daemon opens the database, "+
							"and only it can copy it consistently.\n"+
							"With the daemon stopped, copy vincent.db, vincent.db-wal and "+
							"vincent.db-shm together instead.")
				}
				return err
			}
			res, err := c.Backup(cmd.Context(), dst)
			if err != nil {
				_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "Error:", apiMessage(err))
				return exitError{code: 1}
			}
			if wantJSON(cmd) {
				return emitJSON(cmd.OutOrStdout(), res)
			}
			return renderBackup(cmd.OutOrStdout(), res)
		},
	}
	jsonFlag(cmd)
	return cmd
}

func renderBackup(w io.Writer, res apiclient.BackupResult) error {
	_, err := fmt.Fprintf(w, "wrote %s (%s: database %s, transcripts %s)\n",
		res.Path, humanBytes(res.Bytes),
		humanBytes(res.DatabaseBytes), humanBytes(res.TranscriptBytes))
	return err
}

// restoreReport is `daemon restore --json`. It names where everything went,
// because a restore is the one command whose whole effect is on disk.
type restoreReport struct {
	Archive        string            `json:"archive"`
	DataDir        string            `json:"data_dir"`
	ConfigDir      string            `json:"config_dir"`
	VincentVersion string            `json:"vincent_version"`
	SchemaVersion  int               `json:"schema_version"`
	CreatedAt      string            `json:"created_at"`
	Files          int               `json:"files"`
	Bytes          int64             `json:"bytes"`
	Displaced      []displacedReport `json:"displaced"`
}

// displacedReport is one path --force renamed instead of overwriting.
type displacedReport struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// newDaemonRestoreCmd is `vincent daemon restore <path.tar.gz>` (task 029).
//
// This one is **not** an API client, and cannot be: the daemon whose files it
// replaces has to be down for the restore to be safe. §4's "clients never
// touch the DB" still holds — the invariant is that only the daemon *opens*
// SQLite, and restore opens nothing. It reads a manifest and moves files.
func newDaemonRestoreCmd() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "restore <path.tar.gz>",
		Short: "Restore daemon state from a backup archive (the daemon must be stopped)",
		Long: "Unpack an archive written by `vincent daemon backup` into the config and " +
			"data directories in effect.\n\n" +
			"Refused while the daemon is running, when the archive was written by a newer " +
			"schema than this binary understands, and when the destination already holds " +
			"state — unless --force, which moves that state aside as <name>.bak-<timestamp> " +
			"and deletes nothing.\n\n" +
			"Worktrees are not in a backup and are not restored; the branches they held " +
			"live in your repositories. The daemon mints a fresh API token at next start.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			out, errOut := cmd.OutOrStdout(), cmd.ErrOrStderr()
			archive, err := filepath.Abs(args[0])
			if err != nil {
				return err
			}
			dirs, err := config.ResolveDirs()
			if err != nil {
				return err
			}
			running, err := daemon.ProbeRunning(dirs.Data)
			if err != nil {
				return err
			}
			if running {
				_, _ = fmt.Fprintln(errOut,
					"Error: the daemon is running, and restore replaces the files it has open — "+
						"stop it first with `vincent daemon stop`")
				return exitError{code: 1}
			}

			m, err := backup.ReadManifest(archive)
			if err != nil {
				_, _ = fmt.Fprintln(errOut, "Error:", err)
				return exitError{code: 1}
			}
			// Migrations are up-only and append-only (§14): a database written
			// by a newer vincent cannot be stepped back to this binary's
			// schema, and opening it anyway is the one database state §18
			// says needs a human.
			if ceiling := store.NewestMigration(); m.SchemaVersion > ceiling {
				_, _ = fmt.Fprintf(errOut,
					"Error: %s holds schema version %d and this vincent embeds %d — "+
						"restore it with the version that wrote it (%s)\n",
					archive, m.SchemaVersion, ceiling, dash(m.VincentVersion))
				return exitError{code: 1}
			}

			rep, err := backup.Restore(archive, backup.Dirs{Data: dirs.Data, Config: dirs.Config}, force)
			if err != nil {
				if errors.Is(err, backup.ErrOccupied) {
					_, _ = fmt.Fprintf(errOut,
						"Error: %v\nRe-run with --force to move it aside as <name>.bak-<timestamp>; "+
							"nothing is deleted either way\n", err)
					return exitError{code: 1}
				}
				_, _ = fmt.Fprintln(errOut, "Error:", err)
				return exitError{code: 1}
			}
			if wantJSON(cmd) {
				return emitJSON(out, toRestoreReport(archive, dirs, rep))
			}
			return renderRestore(out, archive, dirs, rep)
		},
	}
	cmd.Flags().BoolVar(&force, "force", false,
		"Move existing state aside as <name>.bak-<timestamp> and restore over it")
	jsonFlag(cmd)
	return cmd
}

func toRestoreReport(archive string, dirs config.Dirs, rep backup.Report) restoreReport {
	displaced := make([]displacedReport, 0, len(rep.Displaced))
	for _, d := range rep.Displaced {
		displaced = append(displaced, displacedReport{From: d.From, To: d.To})
	}
	return restoreReport{
		Archive:        archive,
		DataDir:        dirs.Data,
		ConfigDir:      dirs.Config,
		VincentVersion: rep.Manifest.VincentVersion,
		SchemaVersion:  rep.Manifest.SchemaVersion,
		CreatedAt:      rep.Manifest.CreatedAt,
		Files:          rep.Files,
		Bytes:          rep.Bytes,
		Displaced:      displaced,
	}
}

func renderRestore(w io.Writer, archive string, dirs config.Dirs, rep backup.Report) error {
	rows := [][2]string{
		{"taken", fmt.Sprintf("%s by vincent %s (schema %d)",
			dash(rep.Manifest.CreatedAt), dash(rep.Manifest.VincentVersion), rep.Manifest.SchemaVersion)},
		{"contents", fmt.Sprintf("%d file(s), %s", rep.Files, humanBytes(rep.Bytes))},
		{"data", dirs.Data},
		{"config", dirs.Config},
	}
	if _, err := fmt.Fprintf(w, "restored %s\n", archive); err != nil {
		return err
	}
	for _, r := range rows {
		if _, err := fmt.Fprintf(w, "  %-9s %s\n", r[0], r[1]); err != nil {
			return err
		}
	}
	if len(rep.Displaced) > 0 {
		if _, err := fmt.Fprintln(w, "\nmoved aside (nothing was deleted):"); err != nil {
			return err
		}
		for _, d := range rep.Displaced {
			if _, err := fmt.Fprintf(w, "  %s -> %s\n", d.From, d.To); err != nil {
				return err
			}
		}
	}
	_, err := fmt.Fprintln(w,
		"\nWorktrees are not restored; the branches they held are in your repositories.\n"+
			"A fresh API token is minted at next start, so every client re-reads it.\n"+
			"Start the daemon with `vincent daemon start`.")
	return err
}
