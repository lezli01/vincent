// Package copilot implements the agent adapter for the standalone GitHub
// Copilot CLI: one `copilot --output-format json` subprocess per step, the
// prompt on stdin, and the CLI's session-event JSONL normalized into
// agent.Events (spec §9.1, §9.9; task 138.5). Invocation and stream shapes are
// pinned against build 1.0.105 (task 138 decision 15), captured under
// testdata/.
//
// It is written to be read beside internal/agent/cursor, which it follows file
// for file: the run, its stream reader and its stderr tail are cursor's,
// deliberately not shared (task 138, open question 7). The dialect is not
// cursor's — every line is a `{type, data, id, …}` envelope, assistant text
// arrives as deltas closed by a whole message, the session id appears once on
// the terminal `result` line, and that line carries no answer text — so it
// gets its own parser (§9.9).
//
// The adapter is not registered with the daemon yet: registration, config and
// the gate are task 138.9's.
package copilot
