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
//	                      hang | big-usage | sleep (internal: silent child)
//	FAKEAGENT_DIALECT     "codex" makes --version print codex-cli style
//	                      (run dialect is argv-driven; this only affects the
//	                      version probe, which carries no dialect hint)
//	FAKEAGENT_EDIT_FILE   success: append a line to this worktree-relative
//	                      tracked file, so gate runs produce a non-empty diff
//	FAKEAGENT_SPAWN_CHILD hang: spawn a sleeping child first and emit its pid
//	                      as {"type":"fakeagent.child","pid":N} — lets tests
//	                      verify tree-kill reaps grandchildren
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
)

const (
	version      = "2.1.224 (Claude Code fake)"
	codexVersion = "codex-cli 0.142.5 (fake)"
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
				fmt.Println(version)
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
		codexMain(scenario)
		return
	}

	prompt, _ := io.ReadAll(os.Stdin)

	emit(map[string]any{"type": "system", "subtype": "init", "model": "fake-1"})
	switch scenario {
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
		if f := os.Getenv("FAKEAGENT_EDIT_FILE"); f != "" {
			editFile(f)
		}
		emitSuccessResult(prompt, 100, 42)
	}
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
