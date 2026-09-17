// Package backupsched is the daemon's scheduled-backup timer (spec §12.3,
// §17; task 115): the archive `vincent daemon backup` writes, taken on
// `backup.interval` into `backup.dir`, with the timer's own archives pruned
// past `backup.keep`.
//
// It keeps no state that matters beyond the archives themselves. "Last
// success" is the timestamp in the newest archive's name, so a restart does
// not reset the clock and an overdue backup runs right after startup
// (decision 2). What it holds in memory — the last attempt and its error — is
// a Status that GET /v1/doctor renders, where a failed attempt is a problem
// (decision 4).
//
// Like taskrun.TranscriptPruner it reads the current configuration on every
// check, so an edit takes effect at the next one without a restart, and each
// pass is callable on its own with an injected clock (Check) so tests never
// sleep.
//
// Retention is deliberately narrow (decision 3, §18's never-auto-delete
// stance): only a file named the way this timer names it counts toward
// `keep` or is ever removed, and only after a run that succeeded.
package backupsched
