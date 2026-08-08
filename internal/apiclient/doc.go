// Package apiclient is the typed HTTP + SSE client for the vincent daemon
// API (spec §13). It is the one client shared by the TUI (Phase 3) and the
// CLI subcommands (T4.2). The package owns its wire types — the server's
// DTOs stay unexported — and drift cannot ship unnoticed because client and
// server live in the same binary and this package is integration-tested
// against the real handlers (Phase 3 decision).
package apiclient
