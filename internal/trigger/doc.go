// Package trigger is the daemon's inward signal (task 096): it starts vincent
// work from a system rather than from a person. It sits beside
// internal/notify, which is the outward one — notify runs a user-supplied
// argv when a task changes state, trigger runs one on an interval, diffs
// GitHub, or accepts a signed push, and turns what it sees into tasks.
//
// A trigger is a YAML file under {config_dir}/triggers/ (spec §12.2; global
// scope only, task 096 decision 8) with four parts: a source, a filter
// (`match:` then an `if:` guard with §7.7 strictness), an action and a dedupe
// key. The package owns:
//
//   - the definition, its validator and the schema descriptor the TUI form
//     renders from (definition.go, schema.go) — served the way §8.2's
//     workflow descriptor is (task 065 decision 3), with a drift test
//     walking one against the other, and the GitHub trust table that refuses
//     an untrusted event without allowed_actors at load (decision 31F);
//   - the registry (registry.go): the directory's files with fsnotify live
//     reload, and the distinction decision 16 rests on — a file that left is
//     a removal, an invalid file is not, an unreadable directory is not;
//   - the writer (writer.go): a daemon-rendered starter on create, and
//     workflow.Edit's line-oriented ops on edit, so comments survive; every
//     write is 0600 (decision 20) and carries a version token;
//   - the manager (manager.go): arming and disarming as the registry and
//     `triggers.enabled` change, one poller goroutine per armed `type:
//     command` trigger, the seed that fires nothing and records `seeded`
//     ledger rows (decision 31B), the capped catch-up (decision 13), poll
//     health events (decision 24), and both dry runs;
//   - the `type: command` source (source.go, appendix A): argv executed
//     directly, never through a shell, NDJSON on stdout, a fixed timeout and a
//     whole-process-tree kill through internal/procx, exactly as notify runs
//     its children;
//   - the GitHub sources (github.go): state diffs over a snapshot kept in the
//     cursor, judged when internal/daemon's reconciler hands over a listing —
//     this package never reaches the network (decision 31D);
//   - the `type: http` ingress (ingress.go): signature verification and the
//     push path behind POST /v1/triggers/{id}/events (decision 31G);
//   - the firing pipeline (fire.go): match, allowed_actors, `if:`, the ledger
//     dedupe, the hourly limit, the render, reaction target resolution and the
//     replay, each ending in one ledger outcome.
//
// A trigger introduces no execution semantics of its own (decision 1): an
// action is a replay of POST /v1/tasks, or of a §6 action route, into the
// daemon's in-process handler, the way internal/mcp replays a tool call, so
// §13.1's bounds, validation, the FSM's 409 and Idempotency-Key hold by
// construction. That is why this package takes an http.Handler and never
// imports internal/api (decision 30). `.Event` reaches trigger templates only
// and is never snapshotted onto the task (decision 11): from the step path
// down, a triggered task is a hand-created one.
package trigger
