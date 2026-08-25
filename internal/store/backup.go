package store

import (
	"context"
	"fmt"
)

// BackupTo writes a consistent copy of the database to dst (task 029).
//
// `VACUUM INTO` is the mechanism rather than a file copy, and the difference
// matters: under WAL a committed row lives in `vincent.db-wal` until a
// checkpoint, so copying `vincent.db` while the daemon runs yields a file
// missing recent commits, and copying the three files separately yields a
// non-atomic set. `VACUUM INTO` runs in a read transaction and emits a single
// self-contained file with no `-wal`/`-shm` sidecar. It is also the only
// mechanism available here: the driver is `modernc.org/sqlite`, which does not
// expose SQLite's C online-backup API through `database/sql`.
//
// Unlike Vacuum this needs no quiet moment — it writes elsewhere and never
// takes an exclusive lock on the live file, which is why a backup may be taken
// while tasks run. The cost it does have is the store's single connection
// (phase 1 decision): every other daemon query waits behind the copy for its
// duration, bounded by the size of the database.
//
// dst must not exist: SQLite refuses to overwrite, and the caller is expected
// to have staged a fresh name.
func (s *Store) BackupTo(ctx context.Context, dst string) error {
	if _, err := s.db.ExecContext(ctx, `VACUUM INTO ?`, dst); err != nil {
		return fmt.Errorf("back up database to %s: %w", dst, err)
	}
	return nil
}
