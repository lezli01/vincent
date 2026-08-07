// Package daemon will own the daemon lifecycle: foreground/detached start,
// graceful stop, single-instance locking, token bootstrap, structured
// logging, and crash recovery (spec §12; T1.3, T2.8).
package daemon
