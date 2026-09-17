package backupsched

import "time"

// Status is what the timer knows about how backups are going, as of its most
// recent check. It lives in memory only: the archives on disk are the durable
// record (decision 2), and everything here that matters across a restart —
// when the last success was — is re-read from their names.
type Status struct {
	// Dir is the resolved directory the last check looked in.
	Dir string
	// LastSuccessAt is the timestamp in the newest timer-written archive's
	// name; zero when there is none.
	LastSuccessAt time.Time
	// LastAttemptAt is when this daemon last started a run; zero when it has
	// not tried since it started.
	LastAttemptAt time.Time
	// LastError is why the most recent attempt failed, empty once one
	// succeeds. It is the one field doctor turns into a problem.
	LastError string
	// NextDueAt is when the next run is due; zero when backups are off.
	NextDueAt time.Time
	// LastBytes is the size of the archive the last successful run wrote in
	// this daemon's lifetime.
	LastBytes int64
	// Retained is how many timer-written archives the last check counted.
	Retained int
	// PruneError is why the last retention pass failed. It is logged and
	// reported but is not a doctor problem: the backup itself succeeded.
	PruneError string
}
