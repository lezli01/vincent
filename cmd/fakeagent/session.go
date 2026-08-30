package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// A fake CLI with a memory (task 063).
//
// Chat continuity is the one feature whose acceptance cannot be staged: the
// question is whether turn 2 sees turn 1, and only a CLI that actually keeps
// its own conversations can answer it. So this file gives fakeagent the same
// shape the real one has — sessions identified by an id it mints, stored
// outside vincent's reach, reloaded by `--resume <id>` — and vincent gets to
// be wrong about it in exactly the ways it could be wrong about claude.
//
// The store is a directory of JSON files named by session id, chosen with
// FAKEAGENT_SESSION_DIR. Unset, there is no store: the run mints an id, stamps
// it on every line as claude does, and remembers nothing — which is every
// existing test, unchanged.

// sessionID is the conversation this run belongs to. Empty until the claude
// dialect resolves one; emit stamps it on every line while it is set, because
// that is what claude does and what internal/agent/claude reads back.
var sessionID string

// session is one conversation as the fake CLI stores it: the prompts it has
// been given, oldest first. It is deliberately the whole context — a resumed
// run answers *from* it, so a vincent that failed to pass --resume produces a
// visibly different answer rather than a subtly stale one.
type session struct {
	Prompts []string `json:"prompts"`
}

// sessionDir is the store's root, or "" when this run keeps no memory.
func sessionDir() string { return os.Getenv("FAKEAGENT_SESSION_DIR") }

// sessionPath names one conversation's file. The id is minted here and never
// arrives from anywhere but a previous run of this binary, but it still goes
// through filepath.Base: an id that walked out of the directory would make a
// fake CLI a file-writing primitive.
func sessionPath(id string) string {
	return filepath.Join(sessionDir(), filepath.Base(id)+".json")
}

// resumeArg returns the value of `--resume` on argv, and whether it was
// present. Matching the flag by hand rather than with the flag package keeps
// fakeagent's argv handling uniform: every other flag here is read the same
// way, because the point is to be faithful to argv, not to parse it well.
func resumeArg() (string, bool) {
	for i, a := range os.Args[1:] {
		if a != "--resume" {
			continue
		}
		if i+2 < len(os.Args) {
			return os.Args[i+2], true
		}
		return "", true
	}
	return "", false
}

// sessionLostMessage is the wording claude uses for an id it does not know.
// internal/agent/claude/failure.go matches it, and like the usage-limit and
// unauthenticated wordings there it is **not fixture-verified** — the same
// caveat, argued in the same place.
const sessionLostMessage = "No conversation found with session ID: "

// openSession resolves this run's conversation before any output is emitted.
//
// It returns the prior prompts a resumed session carries. A resume of an id
// the store does not hold ends the process the way the real CLI does: the
// refusal on stderr and a nonzero exit, with no stream at all. That is the
// `session_lost` leg (decision 4), and it is reached by asking for a session
// that was never written rather than by a scenario flag, so the test exercises
// the same code path a genuinely expired id would.
func openSession() []string {
	id, resuming := resumeArg()
	dir := sessionDir()
	if resuming && id == "" {
		fmt.Fprintln(os.Stderr, "--resume requires a session id")
		os.Exit(1)
	}
	if dir == "" {
		// No memory. Still mint an id: every claude line carries one, and a
		// run that emitted none would hide a parse regression.
		sessionID = id
		if sessionID == "" {
			sessionID = "fake-session-1"
		}
		return nil
	}
	if !resuming {
		sessionID = newSessionID(dir)
		return nil
	}
	var s session
	b, err := os.ReadFile(sessionPath(id))
	if err != nil || json.Unmarshal(b, &s) != nil {
		fmt.Fprintln(os.Stderr, sessionLostMessage+id)
		os.Exit(1)
	}
	sessionID = id
	return s.Prompts
}

// newSessionID mints an id for a conversation that does not exist yet. It
// counts what is already in the store so a test reading two ids can tell them
// apart, and so the second chat in one directory does not silently reuse the
// first's file.
func newSessionID(dir string) string {
	entries, _ := os.ReadDir(dir)
	return fmt.Sprintf("fake-session-%d", len(entries)+1)
}

// rememberPrompt appends this turn's prompt to the conversation, so the next
// `--resume` of it sees this turn. A store that cannot be written is fatal:
// silently forgetting would turn the continuity test into one that passes
// whatever vincent does.
func rememberPrompt(prompt []byte) {
	dir := sessionDir()
	if dir == "" || sessionID == "" {
		return
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "fakeagent: session store:", err)
		os.Exit(1)
	}
	var s session
	if b, err := os.ReadFile(sessionPath(sessionID)); err == nil {
		_ = json.Unmarshal(b, &s)
	}
	s.Prompts = append(s.Prompts, firstLine(string(prompt)))
	b, err := json.Marshal(s)
	if err == nil {
		err = os.WriteFile(sessionPath(sessionID), b, 0o600)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "fakeagent: session store:", err)
		os.Exit(1)
	}
}

// recallLine is what a resumed run says it remembers, and the assertion the
// continuity test makes. A fresh session produces no such line at all, so a
// vincent that dropped --resume fails the test by omission rather than by a
// difference a reader has to squint at.
func recallLine(prior []string) string {
	if len(prior) == 0 {
		return ""
	}
	return "recalled: " + strings.Join(prior, " | ")
}
