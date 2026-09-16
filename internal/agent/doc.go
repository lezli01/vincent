// Package agent defines the adapter interface the daemon runs agents
// through: headless CLI subprocesses with JSON event streams (spec §9).
// Implementations live in subpackages (agent/claude, agent/codex,
// agent/cursor) and depend only on this package's types.
//
// An adapter builds its run's Command — binary, argv, directory, environment
// — and starts it through a Launcher the caller chose (§9.1, task 062.1). It
// never spawns the run itself. HostLauncher is the only launcher that ships;
// the short-lived probes (Probe) are not runs and spawn on the host directly.
package agent
