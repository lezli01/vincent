// Package doctor composes the `vincent doctor` diagnostic report: the one
// surface that answers "why is nothing running?" (§17 observability, §12.1
// commands, §13.2 `GET /v1/doctor`).
//
// It owns the report types and every probe that needs no database — paths and
// whether config.yaml parses (§12.2, §12.3), adapter detection (§9.5), the
// daemon log's stat and tail, disk free under the data dir, and the worktree
// scan (§10) — because the report has to be composable **without** a daemon.
// The daemon-only rows (database size, schema version, integrity, task counts)
// are filled in by the API handler on top of what Compose returns; the CLI's
// no-daemon path just leaves them unknown.
//
// Two rules shape the package boundary:
//
//   - It must not import internal/daemon. internal/daemon imports internal/api,
//     which imports this package, so api → doctor → daemon would close a cycle.
//     Every fact only the daemon package knows — liveness, pid, port, uptime,
//     version, the log path — arrives through Options, supplied by whichever
//     caller is composing (task 005 decision 6).
//   - It never opens SQLite. "Only the daemon opens the database" is an
//     ownership invariant, and a diagnostic is not a reason to carve an
//     exception into it: without a daemon those rows read "unknown", which is
//     the truth.
package doctor
