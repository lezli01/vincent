// Package notify is the daemon's outward signal (spec §12.3, §13.3, §16 —
// task 046): a subscriber on the post-commit event fan-out that spawns a
// user-supplied command when a task enters one of the states named in
// `notify.on`.
//
// It exists because the daemon is designed to run with zero clients attached
// (§2, goal 3), and the one thing it could not do with zero clients attached
// was say it needed a human. The only alert in the tree is the TUI's terminal
// bell, which rings on a transition into `awaiting_input` and only while a
// board is open, so a task could sit in `awaiting_input` for the whole
// 24-hour `input_timeout`, fail on expiry, and the first anyone knew was the
// next time they opened the board.
//
// It introduces **no new event type** (§13.3). The selector reads the `to`
// field of `task.state_changed`, which is what the TUI bell already does;
// `task.blocked` and `task.gate_pending` do not exist and are not invented.
//
// Two goroutine kinds, split by cost:
//
//   - The hook (OnEvent) runs on the store's writing goroutine, one hop
//     downstream of store.SetEventHook, and must not block. It does only
//     in-memory checks — the event type, the `to` state against `notify.on`,
//     a configured command — and enqueues.
//   - The workers do every database read (task, project, workflow snapshot),
//     the root-task check, envelope assembly and the child spawn.
//
// Delivery is fire-and-forget and bounded: a fixed-size queue drained by at
// most maxWorkers concurrent children, a fixed perChildTimeout after which
// the child's whole process tree is killed, failures logged and never
// retried, and no replay of events the daemon did not observe — a weekend of
// downtime must not produce a notification storm on the next start.
//
// Security (§16): `notify.command` is arbitrary code the daemon runs as the
// invoking user, and its argv can carry a secret such as a webhook URL. That
// is consistent with §16's posture — agents already run full-auto as the
// invoking user — and is part of why config.yaml is owner-only. The command
// is argv, never a shell string: there is no portable shell to assume.
package notify
