// Package agent defines the adapter interface the daemon runs agents
// through: headless CLI subprocesses with JSON event streams (spec §9).
// Implementations live in subpackages (agent/claude, agent/codex,
// agent/cursor) and depend only on this package's types.
//
// An adapter builds its run's Command — binary, argv, directory, environment
// — and starts it through a Launcher the caller chose (§9.1, task 062.1). It
// never spawns the run itself. HostLauncher runs it on this machine; the task
// engine's container launcher runs it inside a task's container (task 062.2).
// A run's binary is resolved, and claude's `--version` probed, through the same
// Launcher, so a containerized run is judged against the image's CLI. Detect,
// Options and the catalog's other probes describe the host and spawn there
// directly (Probe).
package agent
