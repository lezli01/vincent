// Package mcp serves the Model Context Protocol from the daemon (spec §13.4),
// so an AI coding agent is a first-class client of the API every other client
// already consumes.
//
// It is a second protocol on the *existing* listener, not a second server: the
// handler is mounted at `/mcp` inside internal/api's recover → log → auth
// chain, so it inherits §13.1 wholesale — loopback only, no TLS, the same
// `Authorization: Bearer {token}` from `{data_dir}/token`, the same discovery
// through `daemon.json` (task 057 decision 1).
//
// # Tools are the route table, replayed
//
// Every tool is one `/v1` route. A call marshals its arguments into an
// in-process http.Request and serves it against the handler internal/api
// already built (decision 3). Parity is then mechanical rather than
// maintained: the §13.1 body bounds, the field bounds, the validation, the
// `409` + `details.state` envelopes and `Idempotency-Key` all apply by
// construction, because the same handler runs. The cost is one
// marshal/unmarshal round trip per call and one place — errorFromEnvelope —
// that turns an HTTP error envelope into an MCP error.
//
// Several routes are deliberately absent, and the exclusion is a design line
// rather than an oversight (decision 4, spec §13.4): an agent must not be able
// to stop, garbage-collect or reconfigure the daemon supervising it, and — since
// task 069 gave vincent one write path to GitHub — must not be the one opening
// a pull request in a human's name. They are
// listed in Excluded and asserted absent by name.
//
// # Dependency direction
//
// This package takes an http.Handler and returns an http.Handler; it knows
// nothing about internal/api. internal/api imports internal/mcp, never the
// reverse. The handler it is given is the *inner* mux, before the auth
// middleware: the MCP request that carries a tool call has already been
// authenticated at `/mcp`, and re-presenting a token to the same process would
// be ceremony, not a check.
//
// # Per-step sessions
//
// The daemon wires each agent step to `/mcp/step/{run_id}` with a secret minted
// for that step run and dead when the step ends (decision 6). Identity comes
// out of band so the agent need not cooperate, and it is what makes the wait
// tool's refusal correct. It is **not** a security boundary and must not be
// documented as one: a full-auto agent can read `{data_dir}/token` and reach
// `/mcp` directly (spec §16).
package mcp
