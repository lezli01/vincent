// Package backup builds and unpacks the `.tar.gz` artifact behind
// `vincent daemon backup` / `restore` (spec §12.1, §13.2; task 030).
//
// It is deliberately a leaf: it imports nothing internal, because its two
// callers sit on opposite sides of the ownership boundary. The daemon writes
// an archive (internal/api, over a database copy internal/store already made),
// and the CLI unpacks one **client-side** — restore's whole precondition is
// that the daemon is down, so there is no daemon to ask. Putting the layout
// here is what keeps the writer and the reader from drifting apart.
//
// The layout is four things and no more:
//
//	manifest.json     vincent version, schema version, created_at
//	vincent.db        the `VACUUM INTO` output — never the live file
//	transcripts/…     {data_dir}/transcripts, verbatim
//	config/…          {config_dir}/config.yaml and workflows/
//
// Excluded, and excluded on purpose: `worktrees/` (recreatable, and the
// branches survive in the repositories), `token`, `daemon.json`,
// `daemon.lock`, `logs/` and `tui.json` — all of which belong to one
// installation's running identity rather than to its state.
//
// Entry names are relative and forward-slashed on every platform, so an
// archive written on Windows restores on Linux. Restore refuses any entry
// that would land outside the two destination directories, and any entry that
// is not a regular file or a directory: an archive is untrusted input even
// when this package wrote it.
package backup
