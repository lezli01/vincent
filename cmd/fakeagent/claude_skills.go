package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// claude's skill listing (task 124.7): an input-mode run whose first stdin
// line is an `initialize` control_request gets the control_response the real
// CLI answers it with — captured against 2.1.277 in
// internal/agent/claude/testdata/initialize_*_2.1.277.jsonl — and then exits 0
// on stdin EOF with no further stream:
//
//	FAKEAGENT_CLAUDE_COMMANDS    the reply's `commands`, a JSON array in the
//	                             SDK's SlashCommand shape. Unset answers
//	                             defaultClaudeCommands
//	FAKEAGENT_CLAUDE_INITIALIZE  hang (never answers) | error (a
//	                             `subtype: "error"` reply) | malformed (a
//	                             non-JSON line in place of the reply) | exit
//	                             (a stderr line, then exit 2 before any reply)
//	                             | nocommands (a success reply without
//	                             `commands`) | linger (answers, then outlives
//	                             stdin EOF until killed). Unset answers
//	FAKEAGENT_SKILLS_ECHO_CWD    "1" appends one entry whose description is
//	                             the process's working directory
//
// Whatever those supply, the working directory's own
// `.claude/skills/*/SKILL.md` entries are appended after it — this binary
// standing in for the CLI's own resolution, never vincent scanning.
//
// The reply carries a decoy `account`, as the real one carries the user's
// email and organization, so a test can prove the adapter never decodes it.

// defaultClaudeCommands is a small list holding one of each row the adapter
// treats differently: a built-in it drops, a skill with an argument hint, and
// a plugin skill with aliases.
const defaultClaudeCommands = `[
  {"name":"compact","description":"Free up context by summarizing the conversation so far","argumentHint":"<optional custom summarization instructions>","builtin":true},
  {"name":"fake-skill","description":"A fake project skill. (project)","argumentHint":"[target]"},
  {"name":"fake:tool","description":"(fake) A fake plugin skill.","argumentHint":"","aliases":["tool"]}
]`

// initializeRequestID reports whether line is claude's `initialize`
// control_request, and its request_id.
func initializeRequestID(line string) (string, bool) {
	var req struct {
		Type      string `json:"type"`
		RequestID string `json:"request_id"`
		Request   struct {
			Subtype string `json:"subtype"`
		} `json:"request"`
	}
	if json.Unmarshal([]byte(line), &req) != nil {
		return "", false
	}
	return req.RequestID, req.Type == "control_request" && req.Request.Subtype == "initialize"
}

// answerInitialize answers one initialize request per
// FAKEAGENT_CLAUDE_INITIALIZE, then waits for stdin to close. It never
// returns.
func answerInitialize(id string, rd *bufio.Reader) {
	reply := func(inner map[string]any) {
		b, err := json.Marshal(map[string]any{"type": "control_response", "response": inner})
		if err != nil {
			panic(err)
		}
		fmt.Println(string(b))
	}
	switch os.Getenv("FAKEAGENT_CLAUDE_INITIALIZE") {
	case "hang":
		block()
	case "exit":
		fmt.Fprintln(os.Stderr, "fakeagent: initialize failed on purpose")
		os.Exit(2)
	case "malformed":
		fmt.Println("this line is not json {")
	case "error":
		reply(map[string]any{
			"subtype": "error", "request_id": id, "error": "fakeagent refused initialize",
		})
	case "nocommands":
		reply(map[string]any{
			"subtype": "success", "request_id": id,
			"response": map[string]any{"account": decoyAccount()},
		})
	default:
		commands, err := claudeCommands()
		if err != nil {
			fmt.Fprintln(os.Stderr, "fakeagent: FAKEAGENT_CLAUDE_COMMANDS:", err)
			os.Exit(1)
		}
		reply(map[string]any{
			"subtype": "success", "request_id": id,
			"response": map[string]any{
				"commands": commands,
				"models":   []map[string]any{{"value": "default", "displayName": "Fake model"}},
				"account":  decoyAccount(),
			},
		})
	}
	_, _ = io.Copy(io.Discard, rd)
	if os.Getenv("FAKEAGENT_CLAUDE_INITIALIZE") == "linger" {
		// Deliberately longer than any listing bound: the caller's deadline
		// is what ends this process, after it has already answered.
		time.Sleep(10 * time.Minute)
	}
	os.Exit(0)
}

// claudeCommands is the reply's `commands`: the configured or default rows,
// the working directory appended as one entry's description under
// FAKEAGENT_SKILLS_ECHO_CWD, and then the directory's own skills.
func claudeCommands() ([]json.RawMessage, error) {
	raw := os.Getenv("FAKEAGENT_CLAUDE_COMMANDS")
	if raw == "" {
		raw = defaultClaudeCommands
	}
	var commands []json.RawMessage
	if err := json.Unmarshal([]byte(raw), &commands); err != nil {
		return nil, err
	}
	wd, wdErr := os.Getwd()
	if os.Getenv("FAKEAGENT_SKILLS_ECHO_CWD") == "1" {
		if wdErr != nil {
			return nil, wdErr
		}
		entry, err := json.Marshal(map[string]any{"name": "fake-cwd", "description": wd, "argumentHint": ""})
		if err != nil {
			return nil, err
		}
		commands = append(commands, entry)
	}
	// Last, and a cwd that cannot be resolved adds nothing — claudeRepoSkills'
	// own rule for a directory it cannot read. So a run anywhere without a
	// `.claude/skills` answers with exactly the rows above, which is every
	// existing caller.
	if wdErr != nil {
		return commands, nil
	}
	return append(commands, claudeRepoSkills(wd)...), nil
}

// claudeRepoSkills is every `.claude/skills/*/SKILL.md` under cwd as one
// `initialize` command row, named, described and hinted by its front matter,
// in directory order (task 124 decision 44). It is the claude dialect's
// repoSkills: this binary standing in for the CLI's own resolution, so a
// caller can prove a name appears because a file is in the directory the
// listing ran in.
//
// A missing or unreadable directory, and an entry whose front matter names
// nothing, are no-ops rather than errors — a stand-in that refused to start
// over a seed it could not read would fail a run that never asked about
// skills at all.
func claudeRepoSkills(cwd string) []json.RawMessage {
	entries, err := os.ReadDir(filepath.Join(cwd, ".claude", "skills"))
	if err != nil {
		return nil
	}
	var commands []json.RawMessage
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		b, err := os.ReadFile(filepath.Join(cwd, ".claude", "skills", e.Name(), "SKILL.md"))
		if err != nil {
			continue
		}
		fm := frontMatter(string(b))
		if fm["name"] == "" {
			continue
		}
		// `argument-hint` is read because the m14 leg asserts the hint
		// reaches the wire; the SDK spells it `argumentHint`.
		row, err := json.Marshal(map[string]any{
			"name":         fm["name"],
			"description":  fm["description"],
			"argumentHint": fm["argument-hint"],
		})
		if err != nil {
			continue
		}
		commands = append(commands, row)
	}
	return commands
}

// decoyAccount is the reply's `account`: placeholders in the real one's shape.
func decoyAccount() map[string]any {
	return map[string]any{
		"email":            "fake@example.com",
		"organization":     "Fake Organization",
		"subscriptionType": "Claude Max",
		"apiProvider":      "firstParty",
	}
}

// claudeInitLine is the `system`/`init` line a claude-dialect run opens with
// (task 124.16). Beside the model it carries `skills`: the names the run's
// process "loaded", which is what vincent reads to tell claude's bundled
// skills from its built-in commands.
//
// The names come from the same source `initialize` answers with — the
// configured or default commands, plus the working directory's own
// `.claude/skills` (decision 44) — so a test that makes a row `builtin` and
// wants the init line to claim it need do nothing, and one that wants it
// disclaimed sets FAKEAGENT_CLAUDE_INIT_SKILLS. The real CLI's array leaves
// its built-in commands out; which of its own rows are commands is a fact
// only the real CLI has, so here the choice is the test's.
//
//	FAKEAGENT_CLAUDE_INIT_SKILLS  a comma-separated list of names to claim in
//	                              place of the derived ones. "none" claims
//	                              nothing and omits the key entirely, which is
//	                              what a pre-2.1.263 build does
func claudeInitLine() map[string]any {
	line := map[string]any{"type": "system", "subtype": "init", "model": "fake-1"}
	switch override, set := os.LookupEnv("FAKEAGENT_CLAUDE_INIT_SKILLS"); {
	case set && (override == "none" || override == ""):
		return line
	case set:
		line["skills"] = strings.Split(override, ",")
		return line
	}
	// A listing this binary could not build is no reason to fail a run that
	// never asked about skills: the key is simply left out, the same as a
	// build too old to send it.
	commands, err := claudeCommands()
	if err != nil {
		return line
	}
	names := make([]string, 0, len(commands))
	for _, c := range commands {
		var row struct {
			Name string `json:"name"`
		}
		if json.Unmarshal(c, &row) == nil && row.Name != "" {
			names = append(names, row.Name)
		}
	}
	if len(names) > 0 {
		line["skills"] = names
	}
	return line
}
