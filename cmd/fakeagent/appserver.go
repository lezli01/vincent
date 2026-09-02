package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"time"
)

// The `app-server` dialect: codex's JSON-RPC-over-stdio server, as much of it
// as the §9.6 quota reader touches (task 082).
//
// It exists so no test and no gate ever calls the real codex to read a real
// account's rate limits. Selection is argv shape like every other dialect
// here — `app-server` as argv[1] — and behaviour comes from the environment,
// so argv stays faithful to the CLI:
//
//	FAKEAGENT_CODEX_APP_SERVER  healthy (default) | malformed |
//	                            unauthenticated | hang
//
// It is a FAKEAGENT_CODEX_* variable rather than a FAKEAGENT_SCENARIO value
// for the same reason `login status` has its own: a run scenario and a probe
// answer are set independently, and the m2 gate proves this while
// FAKEAGENT_SCENARIO is already pinned to `usage-limit` for a claude run in
// the same daemon environment.

// appServerRateLimits is the healthy reading, with the real 0.150.1 capture's
// numbers (28 % of a 300-minute window, 53 % of a 10080-minute one).
//
// The reset times are relative to now rather than the captured epochs: a
// reading whose windows have all elapsed is dropped on the wire in favour of
// the older observation, so a frozen timestamp would turn this into a fixture
// that quietly stopped proving anything the day after it was taken.
func appServerRateLimits() map[string]any {
	now := time.Now()
	snapshot := func() map[string]any {
		return map[string]any{
			"limitId":   "codex",
			"limitName": nil,
			"primary": map[string]any{
				"usedPercent":        28,
				"windowDurationMins": 300,
				"resetsAt":           now.Add(5 * time.Hour).Unix(),
			},
			"secondary": map[string]any{
				"usedPercent":        53,
				"windowDurationMins": 10080,
				"resetsAt":           now.Add(7 * 24 * time.Hour).Unix(),
			},
			// Carried because the real response carries them, and a stand-in
			// that omitted the fields the reader must ignore could not prove
			// that it ignores them.
			"credits":              map[string]any{"hasCredits": false, "unlimited": false, "balance": "0"},
			"individualLimit":      nil,
			"spendControlReached":  false,
			"rateLimitReachedType": nil,
			"planType":             "plus",
		}
	}
	return map[string]any{
		// Both shapes, identical, exactly as 0.150.1 answers.
		"rateLimits":            snapshot(),
		"rateLimitsByLimitId":   map[string]any{"codex": snapshot()},
		"rateLimitResetCredits": map[string]any{"availableCount": 0, "credits": []any{}},
	}
}

// appServerMain speaks the handshake on stdio until stdin closes.
func appServerMain() {
	mode := os.Getenv("FAKEAGENT_CODEX_APP_SERVER")
	if mode == "hang" {
		// The handshake never completes: nothing is written at all, so the
		// caller's deadline is the only thing that ends this. block() is the
		// same silent wait the `sleep` scenario uses.
		block()
	}
	out := bufio.NewWriter(os.Stdout)
	defer func() { _ = out.Flush() }()

	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for in.Scan() {
		var msg struct {
			ID     *int   `json:"id"`
			Method string `json:"method"`
		}
		if json.Unmarshal(in.Bytes(), &msg) != nil {
			continue
		}
		if msg.ID == nil {
			// A notification — `initialized` is the one that matters, and
			// nothing is owed in reply to any of them.
			continue
		}
		switch msg.Method {
		case "initialize":
			appServerReply(out, *msg.ID, map[string]any{
				"userAgent":      "fakeagent/0.150.1",
				"codexHome":      "/nonexistent",
				"platformFamily": "unix",
			}, nil)
			// The real server interleaves unsolicited notifications with its
			// replies, so this one does too: a reader that treated the next
			// line as its answer would pass against a silent stand-in and
			// fail against codex.
			appServerNotify(out, "remoteControl/status/changed", map[string]any{"status": "disabled"})
		case "account/rateLimits/read":
			switch mode {
			case "unauthenticated":
				// codex answers the request and refuses in the reply rather
				// than failing to start, so this is a JSON-RPC error, not a
				// non-zero exit.
				appServerReply(out, *msg.ID, nil, map[string]any{
					"code": -32000, "message": "not logged in",
				})
			case "malformed":
				// Well-formed JSON-RPC carrying a result that cannot be what
				// the reader expects: the windows are a string.
				appServerReply(out, *msg.ID, map[string]any{"rateLimits": "nonsense"}, nil)
			default:
				appServerReply(out, *msg.ID, appServerRateLimits(), nil)
			}
		default:
			appServerReply(out, *msg.ID, nil, map[string]any{
				"code": -32601, "message": "method not found: " + msg.Method,
			})
		}
	}
}

func appServerReply(out *bufio.Writer, id int, result, rpcErr map[string]any) {
	msg := map[string]any{"id": id}
	if rpcErr != nil {
		msg["error"] = rpcErr
	} else {
		msg["result"] = result
	}
	appServerWrite(out, msg)
}

func appServerNotify(out *bufio.Writer, method string, params map[string]any) {
	appServerWrite(out, map[string]any{"method": method, "params": params})
}

func appServerWrite(out *bufio.Writer, msg map[string]any) {
	b, err := json.Marshal(msg)
	if err != nil {
		return
	}
	_, _ = fmt.Fprintf(out, "%s\n", b)
	_ = out.Flush()
}
