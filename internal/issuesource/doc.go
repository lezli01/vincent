// Package issuesource is the seam between vincent's issue sync and the
// providers it syncs with (spec §5.6, §12.4; task 142 decision 2). The
// importer (internal/daemon/issuesync.go) and the write-back drain
// (internal/daemon/issueoutbox.go) talk to a Source and never to a
// provider's client: listing, single-issue reads, per-issue threads, state
// writes and comment posts all go through it, and so does turning a
// provider's failure into the reason vocabulary the sync row, the outbox
// and the API wire carry.
//
// It supersedes task 130 decision 15.1 ("no internal/issuesource package —
// the first provider does not design for the second"); the dated note is in
// docs/tasks/130-issues-pillar.md. GitHub is the first and, for now, the
// only implementation (github.go). Jira (#908) and task 137 F5's command
// provider are later implementations that live beside it, in this package.
//
// What a Source owns is mechanism: how a project binds to the provider, how
// a remote row is addressed, how a provider issue maps onto
// store.RemoteIssue. What it does not own is policy, which stays in
// internal/daemon: watermarks, the initial import, incremental passes, the
// sweep's cadence, tombstones, failure recording, and the outbox's retry
// classes, backoff and pacing.
//
// Capabilities only one provider has are separate small interfaces a caller
// finds by type assertion, never methods every source must fake (the
// adapters' rule, spec §9): RepoCommentLister (GitHub's repository-wide
// since+ETag comment listing; without it the importer reads each listed
// issue's thread) and PlaceholderAdopter (task 035's backfill placeholders,
// migration 0040, which only GitHub rows have).
//
// The package imports internal/store and internal/github, and neither may
// import it back. The interface takes store.Project and store.IssueRemote
// directly rather than copying them.
package issuesource
