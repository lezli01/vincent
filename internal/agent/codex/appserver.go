package codex

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"time"

	"github.com/lezli01/vincent/internal/agent"
	"github.com/lezli01/vincent/internal/procx"
	"github.com/lezli01/vincent/internal/version"
)

// This file is codex's answer to "how much of the window is left" (§9.6,
// task 082).
//
// §9.3 ruled quota out for codex on the evidence that it "has no `usage` and
// no `limits` subcommand". Both halves of that sentence are still true; what
// it did not consider is a third route. `codex app-server --stdio` speaks
// JSON-RPC over stdio, and `account/rateLimits/read` answers with the very
// windows the CLI prints when it walls a run — verified against codex-cli
// 0.150.1, a build already in versions.go's verified list, and captured
// verbatim as testdata/app_server_ratelimits_0.150.1.json.
//
// The whole exchange measured 0.80 s on that build. That number is why this
// is allowed to run on a normal request seam at all (CatalogCache.Entry, once
// per quotaTTL) rather than needing a background poller: it is comparable to
// the `--version` probe the same seam already pays for.
//
// The response also carries `credits`, `planType`, `spendControlReached`,
// `individualLimit` and `rateLimitReachedType`. None of them are parsed. This
// reports a *usage window*, and a plan tier is exactly the thing §9.7 already
// declined to call a quota — reading it here would put a billing fact on a
// board that says it is showing capacity.

// appServerTimeout bounds the whole app-server exchange: spawn, handshake,
// one request, one answer.
//
// Deliberately half versionTimeout's 20 s rather than equal to it. The
// version probe's bound was widened for a cold machine at the logon after a
// reboot (T4.22) and is paid once per binary identity; this one is paid every
// quotaTTL on a warm cache, on the endpoint the board polls, and a reading
// that did not arrive costs nothing — the previous one stands and no probe
// fails. 10 s is twelve times the measured round trip and still short enough
// that a hung app-server is a pause rather than a stall.
const appServerTimeout = 10 * time.Second

// JSON-RPC ids for the two requests this makes. Notifications carry none.
const (
	idInitialize = 1
	idRateLimits = 2
)

// rateLimitsMethod is the request that answers with the usage windows.
const rateLimitsMethod = "account/rateLimits/read"

// maxAppServerLines caps how many stdio lines are read waiting for an answer.
// The server interleaves unsolicited notifications with responses — 0.150.1
// emits `remoteControl/status/changed` between the two replies — so the read
// loop must skip, and a skip loop needs a bound that does not depend on the
// peer being well behaved.
const maxAppServerLines = 256

// maxAppServerLine caps one stdio line. The measured rate-limit response is
// under 1 KiB; a megabyte of it is a peer that is not answering this protocol.
const maxAppServerLine = 1 << 20

// The catalog cache finds this adapter by type assertion, so a rename that
// silently dropped the interface would cost a reading rather than a build.
var _ agent.QuotaReporter = (*Adapter)(nil)

// Quota implements agent.QuotaReporter (§9.6, task 082): it asks the codex
// app-server for the account's rate-limit windows.
//
// Every failure degrades to a nil reading. A missing binary, an
// unauthenticated account, a handshake that never completes, an answer that
// does not parse — each returns an error the catalog records off the wire in
// QuotaError, and none of them touches `probe_error`, which keeps meaning
// only "the option probe failed".
func (a *Adapter) Quota(ctx context.Context) (*agent.ReportedQuota, error) {
	path, err := a.resolvePath()
	if err != nil {
		return nil, err
	}
	result, err := readRateLimits(ctx, path)
	if err != nil {
		return nil, err
	}
	return parseRateLimits(result, time.Now())
}

// readRateLimits runs one app-server exchange and returns the raw `result` of
// the rate-limits response.
//
// The subprocess is spawned exactly as Start spawns a run (§9.5, §9.6):
// through procx, so the kill is a tree kill and Windows gets CREATE_NO_WINDOW
// rather than a console flashing on a board refresh. It is killed
// unconditionally on the way out — the app-server is a long-lived server
// process that has no reason to exit just because its one client is done, so
// waiting for it to finish would be waiting for the timeout every time.
func readRateLimits(ctx context.Context, path string) (json.RawMessage, error) {
	ctx, cancel := context.WithTimeout(ctx, appServerTimeout)
	defer cancel()

	cmd := exec.Command(path, "app-server", "--stdio")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("codex app-server stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("codex app-server stdout pipe: %w", err)
	}
	stderr := &tailWriter{max: 8 * 1024}
	cmd.Stderr = stderr
	proc, err := procx.Start(cmd)
	if err != nil {
		return nil, fmt.Errorf("start codex app-server: %w", err)
	}
	defer func() {
		_ = proc.Kill()
		_ = cmd.Wait()
	}()

	type outcome struct {
		result json.RawMessage
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		result, err := exchange(stdin, newLineScanner(stdout))
		done <- outcome{result, err}
	}()

	select {
	case <-ctx.Done():
		// The deferred kill is what unblocks the goroutine's read; it is not
		// waited for, because a peer that ignored the deadline has already
		// cost this request everything it is going to be allowed to cost.
		return nil, fmt.Errorf("codex app-server timed out after %s: %w", appServerTimeout, ctx.Err())
	case o := <-done:
		if o.err != nil {
			if line := stderr.String(); line != "" {
				return nil, fmt.Errorf("%w: %s", o.err, firstLine(line))
			}
			return nil, o.err
		}
		return o.result, nil
	}
}

// exchange writes the handshake and the rate-limits request, and returns the
// raw `result` of the latter.
//
// The order is the protocol's, not a preference: `initialize`, then the
// `initialized` notification, then the request. An app-server that has not
// been told the client is ready does not answer.
func exchange(w io.Writer, r *bufio.Scanner) (json.RawMessage, error) {
	id := idInitialize
	if err := writeMessage(w, rpcMessage{JSONRPC: "2.0", ID: &id, Method: "initialize", Params: initializeParams()}); err != nil {
		return nil, err
	}
	if _, err := readResult(r, idInitialize); err != nil {
		return nil, err
	}
	if err := writeMessage(w, rpcMessage{JSONRPC: "2.0", Method: "initialized", Params: json.RawMessage(`{}`)}); err != nil {
		return nil, err
	}
	id2 := idRateLimits
	if err := writeMessage(w, rpcMessage{JSONRPC: "2.0", ID: &id2, Method: rateLimitsMethod, Params: json.RawMessage(`{}`)}); err != nil {
		return nil, err
	}
	return readResult(r, idRateLimits)
}

// initializeParams is the client identity the app-server echoes back in its
// user agent. It is not a capability negotiation — 0.150.1 answers with the
// server's own paths and platform — so the only thing riding on it is that a
// codex log line naming a client names this one.
func initializeParams() json.RawMessage {
	b, _ := json.Marshal(map[string]any{
		"clientInfo": map[string]string{
			"name":    "vincent",
			"title":   "vincent",
			"version": version.Version(),
		},
	})
	return b
}

// rpcMessage is a request or a notification. ID is a pointer because that is
// the only difference between the two on the wire, and a notification that
// carries `"id":0` is a request the server will try to answer.
type rpcMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *int            `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

// rpcResponse is one line off the app-server's stdout. Both ID and Method are
// optional: a response carries an id, a notification carries a method, and
// 0.150.1 sends no `jsonrpc` field on either — so nothing here may require it.
type rpcResponse struct {
	ID     *int            `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *rpcError       `json:"error"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *rpcError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("codex app-server error %d", e.Code)
	}
	return fmt.Sprintf("codex app-server error %d: %s", e.Code, e.Message)
}

func writeMessage(w io.Writer, m rpcMessage) error {
	b, err := json.Marshal(m)
	if err != nil {
		return fmt.Errorf("encode %s: %w", m.Method, err)
	}
	if _, err := w.Write(append(b, '\n')); err != nil {
		return fmt.Errorf("write %s: %w", m.Method, err)
	}
	return nil
}

// readResult reads until the response to want arrives.
//
// Anything else on the stream is skipped rather than treated as an error:
// server-initiated notifications share this pipe (0.150.1 emits
// `remoteControl/status/changed` unprompted), and so does any banner a future
// build decides to print. A line that is not JSON at all is skipped for the
// same reason — it is noise on a stream, not an answer to this request.
func readResult(r *bufio.Scanner, want int) (json.RawMessage, error) {
	for range maxAppServerLines {
		if !r.Scan() {
			if err := r.Err(); err != nil {
				return nil, fmt.Errorf("read codex app-server: %w", err)
			}
			return nil, errors.New("codex app-server closed its output without answering")
		}
		var resp rpcResponse
		if json.Unmarshal(r.Bytes(), &resp) != nil || resp.ID == nil || *resp.ID != want {
			continue
		}
		if resp.Error != nil {
			// The unauthenticated leg lands here: codex answers the request
			// rather than refusing to start, so there is nothing to detect
			// about the process — only about its reply.
			return nil, resp.Error
		}
		if len(resp.Result) == 0 {
			return nil, fmt.Errorf("codex app-server answered request %d with no result", want)
		}
		return resp.Result, nil
	}
	return nil, fmt.Errorf("codex app-server did not answer request %d in %d lines", want, maxAppServerLines)
}

// newLineScanner reads the app-server's stdout a line at a time, bounded by
// maxAppServerLine. A line over the bound ends the scan rather than growing
// the buffer: a peer sending a megabyte in answer to this request is not
// speaking this protocol, and the caller degrades to no reading.
func newLineScanner(r io.Reader) *bufio.Scanner {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), maxAppServerLine)
	return sc
}

// firstLine trims a captured stderr tail to its first line, so a failure
// records the sentence that matters rather than a usage banner.
func firstLine(s string) string {
	for i, c := range s {
		if c == '\n' {
			return s[:i]
		}
	}
	return s
}
