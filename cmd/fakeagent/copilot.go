package main

import (
	"fmt"
	"io"
	"os"
	"os/signal"
	"runtime"
	"syscall"
	"time"
)

// copilotVersion is the build every copilot fixture was captured from
// (internal/agent/copilot/testdata/*_1.0.105.*, task 138.1).
const copilotVersion = "1.0.105"

// copilotSessionID stands in for the UUID the real CLI mints when the argv
// carries no --session-id.
const copilotSessionID = "00000000-0000-4000-8000-00000000f4ce"

// copilotHelpText is the clap-style `--help` excerpt the adapter's effort
// probe reads (help_1.0.105.txt, trimmed to the model and effort blocks),
// printed when FAKEAGENT_DIALECT=copilot. The values are reordered and one is
// invented, so a test can tell the probe's output from the curated floor.
const copilotHelpText = `Usage: copilot [OPTIONS] [COMMAND]

Options:
      --model <model>
          Set the AI model to use (use 'auto' to let Copilot pick
          automatically)
      --reasoning-effort <level>
          Set the reasoning effort level [possible values: max, high, low, fake-ultra]
  -h, --help
          Print help
`

// copilotLoggedOut is logged_out_1.0.105.txt verbatim: the stderr of a
// `copilot -p` run with no credentials, which exits 1 having written nothing
// to stdout.
const copilotLoggedOut = `Error: No authentication information found.

Copilot can be authenticated with GitHub using an OAuth Token or a Fine-Grained Personal Access Token.

To authenticate, you can use any of the following methods:
  • Start 'copilot' and run the '/login' command
  • Set the COPILOT_GITHUB_TOKEN, GH_TOKEN, or GITHUB_TOKEN environment variable
  • Run 'gh auth login' to authenticate with the GitHub CLI
`

// copilotQuotaMessage is the quota wording, and it is **not
// fixture-verified**: no throttled account was available to task 138.1, so
// it comes from issue reports (github/copilot-cli #3431's `402 …
// "quota_exceeded"`), and whether --output-format json reports it as a
// session.error was not observed. Same caveat as the claude wordings in
// session.go and main.go.
const copilotQuotaMessage = `402 {"error":{"message":"You have exceeded your premium request quota","code":"quota_exceeded"}}`

// copilotMain is the copilot dialect (task 138.4): argv shaped like
// `copilot -p --no-auto-update --output-format json [--session-id <uuid>] …`,
// prompt from stdin only, copilot's session-event JSONL on stdout. The
// shapes mirror the 1.0.105 fixtures in internal/agent/copilot/testdata.
//
// The prompt is never looked for on argv (task 138 decision 2), so a prompt
// that reads like a flag — `resume`, `-v`, `--trust` — is only ever a prompt.
func copilotMain(scenario string) {
	prompt, _ := io.ReadAll(os.Stdin)

	// Copilot stamps no session id on its event lines: it is reported once,
	// on the terminal result line, as sessionId. The conversation is resolved
	// before any output (task 138.11): a fresh run adopts its --session-id,
	// and a `--resume=` of an id the store does not hold ends here with
	// resume_unknown_1.0.105.txt's refusal, exit 1 and no stream.
	prior := openSession(dialectCopilot)
	rememberPrompt(prompt)
	// The usage checkpoint is the session's running total (§9.9), so each
	// turn of a resumed session reports more than the last.
	turns := int64(len(prior) + 1)

	switch scenario {
	case "unauthenticated":
		// logged_out_1.0.105.txt: stderr only, before any stdout at all.
		fmt.Fprint(os.Stderr, copilotLoggedOut)
		os.Exit(1)
	case "no-result":
		// unavailable_model / unsupported_effort: the MCP servers come up,
		// then the run is refused on stderr with no result line and exit 1.
		c := copilotStream{}
		c.mcpStartup()
		fmt.Fprintln(os.Stderr, `Error: Model "fake-nonexistent" from --model flag is not available.`)
		os.Exit(1)
	case "mcp-callback":
		// Called before any stdout, so a carrier the reader refuses ends the
		// run on stderr with exit 1, like the real CLI's own refusals.
		mcpCallback(dialectCopilot, nil)
	}

	c := copilotStream{}
	c.mcpStartup()
	c.event("user.message", map[string]any{
		"content": string(prompt), "transformedContent": string(prompt),
		"messageId": fakeUUID(idMessage, 0), "interactionId": fakeUUID(idInteraction, 0),
		"turnId": "0", "delivery": "idle",
	}, false)
	c.event("assistant.turn_start", map[string]any{"turnId": "0", "interactionId": fakeUUID(idInteraction, 0)}, false)
	if line := recallLine(prior); line != "" {
		// What a resumed session remembers. A fresh one says nothing here,
		// so a vincent that dropped --resume= fails by omission.
		c.text(line)
	}

	switch scenario {
	case "error-event":
		// The session.error shape is the package schema's (errorType,
		// statusCode), **not fixture-verified**: no captured run emitted one.
		c.text("something went wrong, giving up")
		c.event("session.error", map[string]any{
			"errorType": "model_error", "statusCode": 500,
			"message": "fake copilot failed on purpose",
		}, false)
		c.end(1, 1, 100, 42, nil)
		os.Exit(1)
	case "nonzero-exit":
		c.text("about to crash")
		c.end(3, 1, 100, 42, nil)
		os.Exit(3)
	case "hang":
		if os.Getenv("FAKEAGENT_SPAWN_CHILD") == "1" {
			spawnChild()
		}
		c.text("hanging until killed")
		block()
	case "sigterm":
		c.sigterm()
		os.Exit(0)
	case "big-usage":
		c.text("burning tokens")
		c.end(0, 2_500, 2_500_000, 1_200_000, nil)
	case "usage-limit":
		// Task 003's leg for copilot. The wording is unverified; see
		// copilotQuotaMessage. The adapter deliberately leaves it
		// unclassified (task 138.6): it recognizes captured texts only.
		if usageLimitSpent() {
			c.text("stopping: the premium request quota is spent")
			c.event("session.error", map[string]any{
				"errorType": "quota_exceeded", "statusCode": 402,
				"message": copilotQuotaMessage,
			}, false)
			c.end(1, 1, 100, 42, nil)
			os.Exit(1)
		}
		c.success(prompt, turns)
	default: // success, and the dialect-neutral scenarios cursor treats so
		c.success(prompt, turns)
	}
}

// copilotStream numbers the envelope every copilot event carries:
// {type, data, id, timestamp, parentId, ephemeral?}, each line's parentId the
// previous line's id, as in the fixtures.
type copilotStream struct {
	seq    int
	parent string
}

func (c *copilotStream) event(typ string, data map[string]any, ephemeral bool) {
	c.seq++
	id := fakeUUID(idEvent, c.seq)
	v := map[string]any{
		"type": typ, "data": data, "id": id,
		"timestamp": copilotNow(), "parentId": c.parent,
	}
	if c.parent == "" {
		v["parentId"] = nil
	}
	if ephemeral {
		v["ephemeral"] = true
	}
	c.parent = id
	emit(v)
}

// text emits a whole assistant.message — simple_1.0.105.jsonl's final
// answer, without the routing and telemetry fields no parser reads.
func (c *copilotStream) text(s string) {
	c.event("assistant.message", map[string]any{
		"messageId": fakeUUID(idMessage, c.seq), "content": s, "toolRequests": []any{},
		"turnId": "0", "phase": "final_answer", "model": "fake-model",
		"providerId": "copilot",
	}, false)
}

// success is simple_1.0.105.jsonl's turn, stretched by FAKEAGENT_DELAY_MS and
// carrying edit_1.0.105.jsonl's file change when FAKEAGENT_EDIT_FILE or
// FAKEAGENT_WRITE_FILE asks for one. turns is how many runs the session
// has had, this one included: the checkpoint reports 100 in and 42 out for
// each, as the real one's session totals grow.
func (c *copilotStream) success(prompt []byte, turns int64) {
	c.text("Working on: " + firstLine(string(prompt)))
	workFor(c.text)
	doFileWork()
	var files []string
	for _, name := range []string{"FAKEAGENT_WRITE_FILE", "FAKEAGENT_EDIT_FILE"} {
		if f := os.Getenv(name); f != "" {
			files = append(files, f)
		}
	}
	c.text("done: " + flatten(string(prompt), 1000))
	c.end(0, 1, 100*turns, 42*turns, files)
}

// sigterm is sigterm_1.0.105.jsonl's ending: copilot traps SIGTERM, writes
// `agent.interrupted` and `abort{reason:user_initiated}`, closes the turn
// with `assistant.idle{aborted:true}` and a `result{exitCode:0}`, and exits
// 0 — so the stream, not the exit code, says the run was stopped. The
// handler is installed before the first line so a SIGTERM sent on seeing
// output always finds it. Windows has no SIGTERM to trap (procx terminates
// the job outright), so there the ending is written straight away.
func (c *copilotStream) sigterm() {
	term := make(chan os.Signal, 1)
	signal.Notify(term, syscall.SIGTERM)
	c.text("working until terminated")
	if runtime.GOOS != "windows" {
		<-term
	}
	c.event("agent.interrupted", map[string]any{"activity": "idle", "turn": 0}, true)
	c.event("abort", map[string]any{"reason": "user_initiated"}, false)
	c.event("assistant.turn_end", map[string]any{"turnId": "0"}, false)
	c.checkpoint(1, 100, 42)
	c.event("assistant.idle", map[string]any{"aborted": true}, true)
	c.result(0, 1, nil)
}

// end closes the turn and writes the terminal line (simple_1.0.105.jsonl's
// last lines), reporting inTok and outTok as the session's usage.
func (c *copilotStream) end(exitCode, premium int, inTok, outTok int64, files []string) {
	c.event("assistant.turn_end", map[string]any{"turnId": "0"}, false)
	c.checkpoint(premium, inTok, outTok)
	c.event("assistant.idle", map[string]any{}, true)
	c.result(exitCode, premium, files)
}

// checkpoint is the root session.usage_checkpoint every captured run that
// reached a result line writes between turn_end and idle — the line the
// adapter takes its tokens from (§9.9, task 138.7). It is
// simple_1.0.105.jsonl's, without the cache, routing and per-agent fields no
// parser reads: the snapshot's tokenDetails split the spend (all of inTok
// uncached), and modelMetrics carries the same figures under fake-model.
func (c *copilotStream) checkpoint(premium int, inTok, outTok int64) {
	details := map[string]any{
		"input":       map[string]any{"tokenCount": inTok},
		"cache_read":  map[string]any{"tokenCount": 0},
		"cache_write": map[string]any{"tokenCount": 0},
		"output":      map[string]any{"tokenCount": outTok},
	}
	c.event("session.usage_checkpoint", map[string]any{
		"totalPremiumRequests": premium,
		"accountingSnapshot": map[string]any{
			"totalPremiumRequestCost": premium, "totalUserRequests": 1,
			"tokenDetails": details,
			"modelMetrics": map[string]any{
				"fake-model": map[string]any{
					"requests": map[string]any{"count": premium, "cost": premium},
					"usage": map[string]any{
						"inputTokens": inTok, "outputTokens": outTok,
						"cacheReadTokens": 0, "cacheWriteTokens": 0, "reasoningTokens": 0,
					},
					"tokenDetails": details,
				},
			},
			"currentModel": "fake-model",
		},
	}, false)
}

// result writes the terminal line, which — unlike every event before it —
// has no data and no id.
func (c *copilotStream) result(exitCode, premium int, files []string) {
	if files == nil {
		files = []string{}
	}
	emit(map[string]any{
		"type": "result", "timestamp": copilotNow(), "sessionId": sessionID,
		"exitCode": exitCode,
		"usage": map[string]any{
			"premiumRequests": premium, "totalApiDurationMs": 1000 * premium,
			"sessionDurationMs": 2000 * premium,
			"codeChanges": map[string]any{
				"linesAdded": len(files), "linesRemoved": 0, "filesModified": files,
			},
		},
	})
}

// mcpStartup is the built-in GitHub MCP server coming up, the first lines
// of every captured run — the refused ones included.
func (c *copilotStream) mcpStartup() {
	c.event("session.mcp_server_status_changed", map[string]any{
		"serverName": "github-mcp-server", "status": "pending",
	}, true)
	c.event("session.mcp_server_status_changed", map[string]any{
		"serverName": "github-mcp-server", "status": "connected", "configSource": "builtin",
	}, true)
	c.event("session.mcp_server_tools_loaded", map[string]any{
		"serverName": "github-mcp-server", "toolCount": 5,
	}, true)
}

// The kinds of id a fake copilot stream mints, kept in separate ranges so an
// event id never collides with a message id however long the run.
const (
	idEvent = iota
	idMessage
	idInteraction
)

// fakeUUID is a stable, recognizably fake id in the UUID shape the fixtures
// carry.
func fakeUUID(kind, n int) string {
	return fmt.Sprintf("%08x-0000-4000-8000-%012x", kind, n)
}

// copilotNow is the fixtures' timestamp: UTC, millisecond precision.
func copilotNow() string {
	return time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
}
