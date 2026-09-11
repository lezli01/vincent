// Package trigger is the daemon's inward signal (task 096): it starts vincent
// work from a system rather than from a person. It sits beside
// internal/notify, which is the outward one — notify runs a user-supplied
// argv when a task changes state, trigger runs a user-supplied argv on an
// interval and turns what it prints into tasks.
//
// A trigger is a YAML file under {config_dir}/triggers/ (spec §12.2; global
// scope only, task 096 decision 8) with four parts: a source, a filter
// (`match:` then an `if:` guard with §7.7 strictness), an action and a dedupe
// key. The package owns:
//
//   - the definition, its validator and the schema descriptor the TUI form
//     renders from (definition.go, schema.go) — served the way §8.2's
//     workflow descriptor is (task 065 decision 3), with a drift test
//     walking one against the other;
//   - the writer (writer.go): a daemon-rendered starter on create, and
//     workflow.Edit's line-oriented ops on edit, so comments survive; every
//     write is 0600 (decision 20) and carries a version token;
//   - the `type: command` source (source.go, appendix A): argv executed
//     directly, never through a shell, NDJSON on stdout, a fixed timeout and a
//     whole-process-tree kill through internal/procx, exactly as notify runs
//     its children;
//   - the firing pipeline (fire.go): match, `if:`, the ledger dedupe, the
//     hourly limit, the render and the replay, each ending in one ledger
//     outcome.
//
// The registry and its live reload, the poller that drives the pipeline
// under the cursor rules of decisions 6 and 16, and the two dry runs arrive
// with the rest of task 096.2; nothing wires this package into the daemon
// yet.
//
// A trigger introduces no execution semantics of its own (decision 1): the
// action is a replay of POST /v1/tasks into the daemon's in-process handler,
// the way internal/mcp replays a tool call, so §13.1's bounds, validation
// and Idempotency-Key hold by construction. That is why this package takes an
// http.Handler and never imports internal/api (decision 30). `.Event` reaches
// trigger templates only and is never snapshotted onto the task (decision
// 11): from the step path down, a triggered task is a hand-created one.
package trigger
