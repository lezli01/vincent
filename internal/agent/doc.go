// Package agent defines the adapter interface the daemon runs agents
// through: headless CLI subprocesses with JSON event streams (spec §9).
// Implementations live in subpackages (agent/claude; agent/codex in T2.9)
// and depend only on this package's types.
package agent
