// Package daemon owns the daemon lifecycle (spec §12): foreground and
// detached start, graceful stop, single-instance locking, token bootstrap,
// runtime discovery via daemon.json, and structured logging with rotation.
// Crash recovery of step runs arrives with T2.8.
package daemon
