// fakeagent is a scenario-driven stand-in for an agent CLI (phase 1
// decision): it accepts claude- or codex-shaped argv, reads the prompt from
// stdin, and emits the matching dialect (Claude stream-json or codex exec
// JSONL) on stdout, so adapter tests and the gates never call a real API. It
// is always compiled by ./... and excluded from release packaging (T4.5).
//
// The dialect is selected by argv shape: a first argument of "exec" is
// codex-shaped (T2.9); `--trust` anywhere on argv is cursor-shaped (T5.2 —
// cursor's run argv is otherwise claude-shaped, and --trust is the one flag
// only cursor passes, in both permission modes); anything else is
// claude-shaped. `models` and `status` as argv[1] answer cursor's option and
// login probes (§9.7). Scenario selection is environment-driven so argv stays
// true to the real CLIs:
//
//	FAKEAGENT_SCENARIO    success (default) | error-event | nonzero-exit |
//	                      hang | big-usage | ask-question | ask-permission |
//	                      bad-input-request | no-result (cursor: stderr
//	                      failure with no terminal event) |
//	                      flood (emits until killed — the transcript cap) |
//	                      report-env (echoes FAKEAGENT_REPORT_ENV's named
//	                      variables as its result — the §12.3 environment
//	                      policy) | usage-limit | unauthenticated (task 003) |
//	                      set-status (runs the real `vincent status` command
//	                      from inside the step — task 033) |
//	                      sleep (internal: silent child)
//	FAKEAGENT_REPORT_ENV  comma-separated variable names for report-env
//	FAKEAGENT_VINCENT_BIN set-status: path to the vincent binary to invoke.
//	                      A fake agent has no other way to find one
//	FAKEAGENT_STATUS      set-status: the message to report while running
//	FAKEAGENT_STATUS_HOLD_MS
//	                      set-status: hold this long after reporting, so a
//	                      watcher can see the message on the *running* row
//	FAKEAGENT_STATUS_FINAL
//	                      set-status: a second message set just before the
//	                      step ends — the value that must survive on the
//	                      finished row
//	FAKEAGENT_USAGE_LIMIT_RESET
//	                      usage-limit: seconds from now until the window
//	                      reopens, embedded in the message as the CLI does.
//	                      Unset or non-positive reports no reset time, which
//	                      is the leg where the daemon falls back to
//	                      `usage_limit_recheck_interval` (§12.3)
//	FAKEAGENT_USAGE_LIMIT_MARKER
//	                      usage-limit: path to a marker file. The first
//	                      invocation creates it and reports the limit; every
//	                      later one finds it and behaves like `success`. The
//	                      state is the fake CLI's own, so "the task recovers
//	                      with no human action" is observed rather than staged
//	FAKEAGENT_SCENARIO_CODEX
//	                      overrides FAKEAGENT_SCENARIO for codex-shaped argv
//	                      only — lets one process environment drive two
//	                      adapters pointed at this binary differently
//	FAKEAGENT_SCENARIO_CURSOR
//	                      the same override for cursor-shaped argv
//	FAKEAGENT_CURSOR_LOGGED_OUT
//	                      "1" makes `status` report logged out and exit 1,
//	                      driving the §9.5 logged_in probe's negative leg
//	FAKEAGENT_CODEX_LOGGED_OUT
//	                      the same for codex's `login status` (task 005)
//	FAKEAGENT_CODEX_LOGIN_UNKNOWN
//	                      "1" makes `login status` exit 0 saying nothing the
//	                      parse recognizes — the leg that must stay `null`
//	FAKEAGENT_CODEX_LOGIN_HANG
//	                      "1" makes `login status` never answer, so the
//	                      caller's deadline kills it: the T4.22 leg where a
//	                      Windows timeout exits 1 and must not read as
//	                      "not authenticated"
//	FAKEAGENT_DIALECT     "codex" makes --version print codex-cli style,
//	                      "cursor" the calver+sha style (run dialect is
//	                      argv-driven; this only affects the version probe,
//	                      which carries no dialect hint)
//	FAKEAGENT_VERSION     claude dialect: version number --version reports
//	                      (default 2.1.224) — lets tests drive the §7.4
//	                      supports_input version gate
//	FAKEAGENT_EDIT_FILE   success, ask-question (post-answer): append a line
//	                      to this worktree-relative tracked file, so gate
//	                      runs produce a non-empty diff
//	FAKEAGENT_SPAWN_CHILD hang: spawn a sleeping child first and emit its pid
//	                      as {"type":"fakeagent.child","pid":N} — lets tests
//	                      verify tree-kill reaps grandchildren
//	FAKEAGENT_ASK_MULTI   ask-question: add a second, multi-select question
//	FAKEAGENT_ASK_LONG    ask-question: "1" asks the question as prose well
//	                      past 256 bytes, the length Claude routinely writes.
//	                      The answer is keyed by that text verbatim (§7.4), so
//	                      this drives the whole round trip against the API's
//	                      key bound (issue #197)
//	FAKEAGENT_DELAY_MS    success (both dialects): stretch the run over this
//	                      many milliseconds, emitting one assistant line per
//	                      second — the M3 gate needs tasks that are still
//	                      running when a human looks at the board (T3.8)
//
// With --input-format stream-json on argv the prompt is read as one
// {"type":"user",…} JSONL line (stdin stays open), mirroring the real CLI's
// input mode (spec §9.2); otherwise the prompt is stdin-until-EOF as before.
// The ask-* scenarios emit control_request lines in the captured 2.1.226
// shape and block reading stdin until the matching control_response arrives.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

const (
	defaultVersion = "2.1.224"
	codexVersion   = "codex-cli 0.142.5 (fake)"
)

const helpText = `Usage: fakeagent [options] [prompt]

fakeagent - fake Claude Code CLI for vincent tests

Options:
  -p, --print                     Print response and exit (useful for pipes)
  --output-format <format>        Output format (only works with --print):
                                  "text" (default), "json" (single result),
                                  or "stream-json" (realtime streaming)
  --model <model>                 Model for the current session. Provide an
                                  alias for the latest model (e.g. 'sonnet'
                                  or 'opus' or 'haiku') or a model's full
                                  name (e.g.
                                  'claude-sonnet-4-5-20250929')
  --effort <effort>               Constrain how much reasoning effort the
                                  model spends (choices: "low", "medium",
                                  "high", "xhigh", "max")
  --dangerously-skip-permissions  Bypass all permission checks
  -v, --version                   Output the version number
  -h, --help                      Display help for command
`

func main() {
	for _, a := range os.Args[1:] {
		switch a {
		case "--version", "-v", "-V":
			switch os.Getenv("FAKEAGENT_DIALECT") {
			case "codex":
				fmt.Println(codexVersion)
			case "cursor":
				fmt.Println(cursorVersion)
			default:
				v := os.Getenv("FAKEAGENT_VERSION")
				if v == "" {
					v = defaultVersion
				}
				fmt.Printf("%s (Claude Code fake)\n", v)
			}
			return
		case "--help", "-h":
			fmt.Print(helpText)
			return
		}
	}

	scenario := os.Getenv("FAKEAGENT_SCENARIO")
	if scenario == "" {
		scenario = "success"
	}
	if scenario == "sleep" {
		block() // silent child for tree-kill tests; killed by the test
	}

	// The adapters' subcommand probes answer before any dialect dispatch: they
	// are argv[1] with no run flags at all, so neither the codex nor the
	// cursor run-shape test would reach them. `login status` is codex's
	// (task 005) and is matched on both words, because `exec` is the only
	// argv[1] the codex run dialect uses.
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "models":
			fmt.Print(cursorModels)
			return
		case "status":
			if os.Getenv("FAKEAGENT_CURSOR_LOGGED_OUT") == "1" {
				fmt.Println("Not logged in")
				os.Exit(1)
			}
			fmt.Println("Logged in as fake@example.com")
			return
		case "login":
			if len(os.Args) > 2 && os.Args[2] == "status" {
				codexLoginStatus()
				return
			}
		}
	}

	if len(os.Args) > 1 && os.Args[1] == "exec" {
		// A dialect-scoped override, because a gate can point both adapters
		// at this binary and they then share one process environment: the
		// M3 rehearsal needs claude asking a question while codex just works
		// (T3.8), which a single FAKEAGENT_SCENARIO cannot express.
		if s := os.Getenv("FAKEAGENT_SCENARIO_CODEX"); s != "" {
			scenario = s
		}
		codexMain(scenario)
		return
	}

	// Cursor's run argv is claude-shaped (`-p --output-format stream-json`),
	// so the dialect is discriminated on --trust: cursor is the only adapter
	// that passes it, and it passes it in both permission modes (T5.2).
	if hasFlag("--trust") {
		if s := os.Getenv("FAKEAGENT_SCENARIO_CURSOR"); s != "" {
			scenario = s
		}
		cursorMain(scenario)
		return
	}

	prompt, stdin := readPrompt(hasFlag("--input-format"))

	emit(map[string]any{"type": "system", "subtype": "init", "model": "fake-1"})
	switch scenario {
	case "ask-question":
		askQuestion(prompt, stdin)
	case "ask-permission":
		askPermission(prompt, stdin)
	case "bad-input-request":
		emitText("about to violate the control protocol")
		emit(map[string]any{
			"type": "control_request", "request_id": "fake-bad-1",
			"request": map[string]any{"subtype": "definitely_not_can_use_tool"},
		})
		block() // the engine must kill us — never answer, never exit
	case "error-event":
		emitText("something went wrong, giving up")
		emit(map[string]any{
			"type": "result", "subtype": "error_during_execution", "is_error": true,
			"result": "fake agent failed on purpose",
			"usage":  map[string]int64{"input_tokens": 50, "output_tokens": 7},
		})
	case "nonzero-exit":
		emitText("about to crash")
		emitSuccessResult(prompt, 100, 42)
		os.Exit(3)
	case "hang":
		if os.Getenv("FAKEAGENT_SPAWN_CHILD") == "1" {
			spawnChild()
		}
		emitText("hanging until killed")
		block()
	case "big-usage":
		emitText("burning tokens")
		emitSuccessResult(prompt, 2_500_000, 1_200_000)
	case "report-env":
		// Reports the environment the process was actually given, so a test
		// can assert what an agent step inherited (T4.23). The variables are
		// named by the test rather than dumped wholesale: a scenario that
		// printed every variable would put the developer's own environment
		// into a transcript on every run.
		var reported []string
		for _, name := range strings.Split(os.Getenv("FAKEAGENT_REPORT_ENV"), ",") {
			if name = strings.TrimSpace(name); name != "" {
				reported = append(reported, name+"=["+os.Getenv(name)+"]")
			}
		}
		emitText(strings.Join(reported, " "))
		emitSuccessResult([]byte(strings.Join(reported, " ")), 1, 1)
	case "set-status":
		// A step that reports on itself (task 033). It runs the real
		// `vincent status` command rather than emitting a marker the daemon
		// would parse, because that is what the feature *is*: an agent tells
		// the daemon what it is doing by calling it, and nothing in any
		// adapter is involved. Addressing comes from §8.5's VINCENT_TASK_ID
		// and VINCENT_STEP_ID, which reach an agent step's environment — so
		// this scenario failing is also how a regression there is caught.
		reports := setStatus(os.Getenv("FAKEAGENT_STATUS"))
		if ms := envMillis("FAKEAGENT_STATUS_HOLD_MS"); ms > 0 {
			// Long enough for a watcher to see the message on the *running*
			// row, which is the live half of the feature.
			emitText("holding so the status is observable while running")
			time.Sleep(ms)
		}
		if final := os.Getenv("FAKEAGENT_STATUS_FINAL"); final != "" {
			reports += " " + setStatus(final)
		}
		emitText(reports)
		emitSuccessResult([]byte(reports), 1, 1)
	case "flood":
		// An agent that will not stop talking: emits until something kills
		// it, which is exactly what the §12.3 transcript cap must do.
		for {
			emitText(strings.Repeat("flooding the transcript ", 40))
		}
	case "usage-limit":
		// A CLI that stops because the account's quota for this window is
		// spent (task 003). With a marker file it walls once and succeeds
		// thereafter, which is the unattended-recovery path.
		if usageLimitSpent() {
			emitText("stopping: the usage limit for this window is spent")
			emit(map[string]any{
				"type": "result", "subtype": "error_during_execution", "is_error": true,
				"result": usageLimitMessage(),
				"usage":  map[string]int64{"input_tokens": 12, "output_tokens": 0},
			})
			os.Exit(1) // the real CLI exits nonzero here; the reason must still win
		}
		claudeSuccess(prompt)
	case "unauthenticated":
		emit(map[string]any{
			"type": "result", "subtype": "error_during_execution", "is_error": true,
			"result": unauthenticatedMessage,
		})
		os.Exit(1)
	default: // success
		claudeSuccess(prompt)
	}
}

// claudeSuccess is the `success` scenario's body, shared with the scenarios
// that end up succeeding — a usage-limit run whose window has reopened.
func claudeSuccess(prompt []byte) {
	emitText("Working on: " + firstLine(string(prompt)))
	emit(map[string]any{"type": "assistant", "message": map[string]any{
		"content": []any{map[string]any{"type": "tool_use", "name": "Edit", "input": map[string]any{}}},
	}})
	emit(map[string]any{"type": "fake_marker", "note": "unknown event type for tolerant-parsing tests"})
	workFor(emitText)
	if f := os.Getenv("FAKEAGENT_EDIT_FILE"); f != "" {
		editFile(f)
	}
	emitSuccessResult(prompt, 100, 42)
}

// unauthenticatedMessage is the wording an adapter's classifier matches for a
// CLI that is not logged in (task 003). Like the usage-limit wording it is
// **not fixture-verified** — see internal/agent/claude/failure.go, which is
// where the matching lives and where that caveat is argued.
const unauthenticatedMessage = "Invalid API key · Please run /login"

// usageLimitSpent reports whether this invocation should wall.
//
// With FAKEAGENT_USAGE_LIMIT_MARKER set it is true exactly once: the first
// call creates the marker and reports the limit, and every later call finds it
// and behaves like `success`. The state lives in the fake CLI's own
// filesystem rather than in the test process, which is what makes "the task
// recovers with no human action" an observation rather than a staging.
// Without a marker every invocation walls, which is the steady-state case.
func usageLimitSpent() bool {
	marker := os.Getenv("FAKEAGENT_USAGE_LIMIT_MARKER")
	if marker == "" {
		return true
	}
	f, err := os.OpenFile(marker, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return false // the marker exists: the window has reopened
	}
	_ = f.Close()
	return true
}

// usageLimitMessage renders the quota wording, with the reset time the real
// CLI appends as unix seconds when it reports one. FAKEAGENT_USAGE_LIMIT_RESET
// is seconds from now; unset or non-positive reports no reset time at all,
// which is the leg that exercises the interval fallback.
func usageLimitMessage() string {
	const base = "Claude AI usage limit reached"
	secs, err := strconv.Atoi(os.Getenv("FAKEAGENT_USAGE_LIMIT_RESET"))
	if err != nil || secs <= 0 {
		return base
	}
	reset := time.Now().Add(time.Duration(secs) * time.Second).Unix()
	return base + "|" + strconv.FormatInt(reset, 10)
}

// tickInterval bounds how long the stream stays silent while delaying.
const tickInterval = time.Second

// workFor stretches a successful run over FAKEAGENT_DELAY_MS, emitting one
// line per tick in the caller's dialect. A run that returns immediately
// cannot be observed running, which is what the M3 gate needs it to do
// (T3.8): three tasks concurrent on the board long enough to look at, and a
// live tail with output still arriving. Emitting nothing while sleeping would
// leave that tail as empty as no delay at all, so the delay is spent in
// ticks rather than one Sleep.
func workFor(emitLine func(string)) {
	d := delayDuration()
	for elapsed := time.Duration(0); elapsed < d; {
		step := min(tickInterval, d-elapsed)
		time.Sleep(step)
		elapsed += step
		emitLine(fmt.Sprintf("still working (%s elapsed)", elapsed.Round(time.Millisecond)))
	}
}

// delayDuration parses FAKEAGENT_DELAY_MS. Unset, unparseable and
// non-positive all mean no delay: this is test scaffolding, and a typo in a
// gate script must never be the reason a suite hangs or fails.
func delayDuration() time.Duration {
	ms, err := strconv.Atoi(os.Getenv("FAKEAGENT_DELAY_MS"))
	if err != nil || ms <= 0 {
		return 0
	}
	return time.Duration(ms) * time.Millisecond
}

// hasFlag reports whether an argv element equals the flag.
func hasFlag(flag string) bool {
	for _, a := range os.Args[1:] {
		if a == flag {
			return true
		}
	}
	return false
}

// readPrompt reads the prompt: stdin-until-EOF normally, or one
// {"type":"user",…} JSONL line in input mode, returning the still-open
// reader for control traffic.
func readPrompt(inputMode bool) ([]byte, *bufio.Reader) {
	if !inputMode {
		prompt, _ := io.ReadAll(os.Stdin)
		return prompt, nil
	}
	rd := bufio.NewReader(os.Stdin)
	line, _ := rd.ReadString('\n')
	var msg struct {
		Message struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal([]byte(line), &msg); err != nil {
		return []byte(line), rd
	}
	var texts []string
	for _, c := range msg.Message.Content {
		texts = append(texts, c.Text)
	}
	return []byte(strings.Join(texts, "\n")), rd
}

// controlResponse is the inbound answer shape (captured from 2.1.226).
type controlResponse struct {
	Type     string `json:"type"`
	Response struct {
		Subtype   string `json:"subtype"`
		RequestID string `json:"request_id"`
		Response  struct {
			Behavior     string         `json:"behavior"`
			Message      string         `json:"message"`
			UpdatedInput map[string]any `json:"updatedInput"`
		} `json:"response"`
	} `json:"response"`
}

// awaitControlResponse blocks reading stdin lines until the control_response
// for requestID arrives. Returns false when stdin closes first (the run was
// killed or abandoned).
func awaitControlResponse(rd *bufio.Reader, requestID string) (controlResponse, bool) {
	if rd == nil { // no input mode, nothing will ever arrive
		block()
	}
	for {
		line, err := rd.ReadString('\n')
		if line != "" {
			var resp controlResponse
			if json.Unmarshal([]byte(line), &resp) == nil &&
				resp.Type == "control_response" && resp.Response.RequestID == requestID {
				return resp, true
			}
		}
		if err != nil {
			return controlResponse{}, false
		}
	}
}

// longQuestionText is what the ask-question scenario asks under
// FAKEAGENT_ASK_LONG: one question of agent-authored prose comfortably past
// 256 bytes, which is the length Claude routinely writes. A test answering it
// keys off the text the daemon parked on rather than repeating this literal,
// because that is what a human answering the form does (§7.4).
const longQuestionText = "Two colors would both work for the header, and the " +
	"choice changes more than it looks like it does: red reads as an alert " +
	"everywhere else in this interface, so using it here would blunt that " +
	"signal, while blue matches the rest of the chrome but is much easier to " +
	"miss on a dim screen. Which would you rather I use, and should I apply " +
	"it to the footer at the same time?"

// askQuestion emits an AskUserQuestion control_request in the captured shape
// and blocks until answered; the answers round-trip into the result text.
func askQuestion(prompt []byte, rd *bufio.Reader) {
	question := "Which color do you prefer?"
	if os.Getenv("FAKEAGENT_ASK_LONG") == "1" {
		question = longQuestionText
	}
	questions := []any{map[string]any{
		"question": question,
		"header":   "Color",
		"options": []any{
			map[string]any{"label": "Red", "description": "The color red"},
			map[string]any{"label": "Blue", "description": "The color blue"},
		},
		"multiSelect": false,
	}}
	if os.Getenv("FAKEAGENT_ASK_MULTI") == "1" {
		questions = append(questions, map[string]any{
			"question": "Which toppings?",
			"header":   "Toppings",
			"options": []any{
				map[string]any{"label": "Cheese", "description": ""},
				map[string]any{"label": "Basil", "description": ""},
			},
			"multiSelect": true,
		})
	}
	emit(map[string]any{"type": "assistant", "message": map[string]any{
		"content": []any{map[string]any{"type": "tool_use", "name": "AskUserQuestion", "input": map[string]any{}}},
	}})
	emit(map[string]any{
		"type": "control_request", "request_id": "fake-req-1",
		"request": map[string]any{
			"subtype":   "can_use_tool",
			"tool_name": "AskUserQuestion",
			"input":     map[string]any{"questions": questions},
		},
	})
	resp, ok := awaitControlResponse(rd, "fake-req-1")
	if !ok {
		os.Exit(1)
	}
	answered, _ := json.Marshal(map[string]any{
		"answers":  resp.Response.Response.UpdatedInput["answers"],
		"response": resp.Response.Response.UpdatedInput["response"],
	})
	emitText("question answered: " + string(answered))
	if f := os.Getenv("FAKEAGENT_EDIT_FILE"); f != "" {
		editFile(f) // the answered agent then does work the gate can publish
	}
	emitSuccessResult([]byte(string(prompt)+" | "+string(answered)), 100, 42)
}

// askPermission emits a Write permission control_request in the captured
// shape and blocks until allowed or denied.
func askPermission(prompt []byte, rd *bufio.Reader) {
	emit(map[string]any{
		"type": "control_request", "request_id": "fake-req-2",
		"request": map[string]any{
			"subtype":     "can_use_tool",
			"tool_name":   "Write",
			"description": "hello.txt",
			"input":       map[string]any{"file_path": "hello.txt", "content": "hi"},
		},
	})
	resp, ok := awaitControlResponse(rd, "fake-req-2")
	if !ok {
		os.Exit(1)
	}
	verdict := resp.Response.Response.Behavior
	if verdict == "deny" {
		verdict += ": " + resp.Response.Response.Message
	}
	emitText("permission " + verdict)
	emitSuccessResult([]byte(string(prompt)+" | "+verdict), 100, 42)
}

func emit(v map[string]any) {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	fmt.Println(string(b))
}

func emitText(text string) {
	emit(map[string]any{"type": "assistant", "message": map[string]any{
		"content": []any{map[string]any{"type": "text", "text": text}},
	}})
}

// emitSuccessResult echoes the prompt back in the result text so tests can
// assert the prompt round-tripped through stdin.
func emitSuccessResult(prompt []byte, inTok, outTok int64) {
	emit(map[string]any{
		"type": "result", "subtype": "success", "is_error": false,
		"result":         "done: " + flatten(string(prompt), 1000),
		"total_cost_usd": 0.0123,
		"usage":          map[string]int64{"input_tokens": inTok, "output_tokens": outTok},
	})
}

// setStatus runs `vincent status <message>` the way a step's own process
// would, and reports what happened in one line so the result text carries it
// — a scenario that swallowed the error would make a broken command look like
// a step that simply said nothing.
//
// The binary comes from FAKEAGENT_VINCENT_BIN because a fake agent has no
// other way to find it: there is no install on a test machine's PATH, and
// os.Executable() is this program.
func setStatus(message string) string {
	if message == "" {
		return "status: nothing to say"
	}
	bin := os.Getenv("FAKEAGENT_VINCENT_BIN")
	if bin == "" {
		return "status: FAKEAGENT_VINCENT_BIN is unset"
	}
	out, err := exec.Command(bin, "status", message).CombinedOutput()
	if err != nil {
		return "status: failed: " + err.Error() + ": " + flatten(string(out), 200)
	}
	return "status: set to " + flatten(message, 200)
}

// envMillis reads a millisecond duration from the environment; 0 when unset
// or unparseable.
func envMillis(name string) time.Duration {
	n, err := strconv.Atoi(os.Getenv(name))
	if err != nil || n <= 0 {
		return 0
	}
	return time.Duration(n) * time.Millisecond
}

// block sleeps forever. Not `select {}` — with no other goroutines that
// trips Go's deadlock detector and the "hung" process would exit instantly.
func block() {
	for {
		time.Sleep(time.Hour)
	}
}

func firstLine(s string) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	if len(s) > 120 {
		s = s[:120]
	}
	return s
}

// flatten collapses newlines and truncates, keeping the whole prompt
// assertable from the result summary.
func flatten(s string, limit int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > limit {
		s = s[:limit]
	}
	return s
}

// editFile appends to a tracked file in the cwd (the worktree), giving gate
// runs a diff to assert on. Refuses path escapes.
func editFile(name string) {
	if strings.Contains(name, "..") {
		return
	}
	f, err := os.OpenFile(name, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer func() { _ = f.Close() }()
	_, _ = f.WriteString("fakeagent was here\n")
}

// spawnChild starts a silently-sleeping copy of this binary and reports its
// pid on the stream, so tests can check the whole tree died.
func spawnChild() {
	self, err := os.Executable()
	if err != nil {
		return
	}
	cmd := exec.Command(self)
	cmd.Env = append(os.Environ(), "FAKEAGENT_SCENARIO=sleep")
	if err := cmd.Start(); err != nil {
		return
	}
	emit(map[string]any{"type": "fakeagent.child", "pid": cmd.Process.Pid})
}
