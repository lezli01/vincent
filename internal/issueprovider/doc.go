// Package issueprovider is the registry of issue providers: the values the
// issue_remotes.provider column may hold, and what vincent may do with an
// issue imported from each. It is task 142.5 (#901), and is where task 142
// decision 2's provider seam declares its ids, so a "github" literal is no
// longer what decides whether a remote issue writes back (spec §5.6, §12.3).
//
// It is a leaf — the standard library only — because both ends of the seam
// consult it: internal/store decides inside a transaction whether a state
// change enqueues an outbox row, and internal/issuesource, which imports the
// store, implements the providers. Keeping the answer here keeps the
// dependency direction one-way.
//
// A provider that writes back (WritesBack) has its live remotes' state
// changes and granted handoff comments carried back through the outbox, and
// its issues guarded by forge_write_needs_human (spec §5.7). Whether a writer
// for it is registered in the daemon is a separate question: a row for a
// write-back provider with no writer waits as no_client and is never failed
// (spec §12.3). An unknown provider is neither Known nor written back, so its
// state stays local.
package issueprovider
