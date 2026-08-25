package apiclient

import "context"

// BackupResult is the body of POST /v1/daemon/backup (task 030).
//
// The sizes are reported rather than trimmed: the archive carries transcripts
// in full, so it is as large as the installation is, and a command that
// printed only "done" would leave a user to discover that from `du`.
type BackupResult struct {
	Path            string `json:"path"`
	Bytes           int64  `json:"bytes"`
	DatabaseBytes   int64  `json:"database_bytes"`
	TranscriptBytes int64  `json:"transcript_bytes"`
	// SchemaVersion is the database's migration high-water mark, the same
	// number the archive's manifest carries and restore checks against the
	// binary's own ceiling.
	SchemaVersion int `json:"schema_version"`
	// CreatedAt is RFC3339 UTC with fixed-width nanoseconds (§14's format).
	CreatedAt string `json:"created_at"`
}

// Backup asks the daemon to write a `.tar.gz` of its state to path
// (POST /v1/daemon/backup). path must be absolute and must not exist: the
// daemon resolves it against its own working directory, and it never
// overwrites a file that is by construction somebody's backup.
//
// There is no client-side counterpart. Restore runs entirely in the client,
// because its precondition is that the daemon is down.
func (c *Client) Backup(ctx context.Context, path string) (BackupResult, error) {
	var out BackupResult
	if err := c.post(ctx, "/v1/daemon/backup", backupRequest{Path: path}, &out); err != nil {
		return BackupResult{}, err
	}
	return out, nil
}

type backupRequest struct {
	Path string `json:"path"`
}
