// fakeagent is a scenario-driven stand-in for an agent CLI (phase 1
// decision): it accepts claude- or codex-shaped argv, reads the prompt from
// stdin, and emits the matching dialect (Claude stream-json or codex exec
// JSONL) on stdout, so adapter tests and the gates never call a real API. It
// is always compiled by ./... and excluded from release packaging (T4.5).
//
// The dialect is selected by argv shape: a first argument of "exec" is
// codex-shaped (T2.9); anything else is claude-shaped. Scenario selection is
// environment-driven so argv stays true to the real CLIs:
//
//	FAKEAGENT_SCENARIO    success (default) | error-event | nonzero-exit |
//	                      hang | big-usage | ask-question | ask-permission |
//	                      bad-input-request | sleep (internal: silent child)
//	FAKEAGENT_SCENARIO_CODEX
//	                      overrides FAKEAGENT_SCENARIO for codex-shaped argv
//	                      only — lets one process environment drive two
//	                      adapters pointed at this binary differently
//	FAKEAGENT_DIALECT     "codex" makes --version print codex-cli style
//	                      (run dialect is argv-driven; this only affects the
//	                      version probe, which carries no dialect hint)
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
			if os.Getenv("FAKEAGENT_DIALECT") == "codex" {
				fmt.Println(codexVersion)
			} else {
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
	default: // success
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

// askQuestion emits an AskUserQuestion control_request in the captured shape
// and blocks until answered; the answers round-trip into the result text.
func askQuestion(prompt []byte, rd *bufio.Reader) {
	questions := []any{map[string]any{
		"question": "Which color do you prefer?",
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
