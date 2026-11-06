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
// docs/tasks/130-issues-pillar.md. GitHub is the first implementation
// (github.go) and Jira Cloud the second (jira.go: the read side is #908's,
// the write-back through transitions and ADF comments #909's, task 142.13).
// Task 137 F5's
// command provider is a later implementation that lives beside them.
//
// What a Source owns is mechanism: how a project binds to the provider, how
// a remote row is addressed, how a provider issue maps onto
// store.RemoteIssue. What it does not own is policy, which stays in
// internal/daemon: watermarks, the initial import, incremental passes, the
// sweep's cadence, tombstones, failure recording, and the outbox's retry
// classes, backoff and pacing.
//
// Resolve and ResolveWith (resolve.go) are the one definition of a
// project's effective issue source (task 142.7): an explicit binding wins,
// otherwise origin decides — github for a github.com remote, none for any
// other. The daemon's per-provider sync loops, "sync now" and the API all
// read it rather than each deriving their own. It takes no config: whether
// a provider's loop runs at all is the caller's gate.
//
// Capabilities only one provider has are separate small interfaces a caller
// finds by type assertion, never methods every source must fake (the
// adapters' rule, spec §9): RepoCommentLister (GitHub's repository-wide
// since+ETag comment listing; without it the importer reads each listed
// issue's thread) and PlaceholderAdopter (task 035's backfill placeholders,
// migration 0040, which only GitHub rows have) are GitHub's; BulkGetter
// (bulkfetch), BindingResolver (the project key to its stable id),
// ProjectLocator (a row written through its project's binding) and
// LaggingLister (a listing confirmed by id before a state it shows is
// adopted over a write just made) are Jira's (capabilities.go).
//
// The package imports internal/store, internal/github, internal/gitx (for
// RepoOf's `git remote get-url origin`), internal/issuestate (the state
// mapping), internal/issueprovider (the provider ids), and internal/jira,
// internal/jira/jiramap and internal/jira/render (the Jira client, its
// field mapping and its HTML-to-Markdown rendering); neither store, github
// nor jira may import it back. The interface takes store.Project and
// store.IssueRemote directly rather than copying them.
package issuesource
