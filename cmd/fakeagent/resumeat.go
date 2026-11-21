package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Resuming a claude session at a point and forking it (task 167 decision A),
// as claude 2.1.330 does it: `--resume <id> --resume-session-at <uuid>
// --fork-session` reloads the session up to the assistant message <uuid> and
// continues it under a new session id (internal/agent/claude/testdata/
// stream_resume_at_2.1.330.jsonl).
//
// The fake's message uuids say which turn wrote them — `fake-msg-<turn>-<n>`,
// the n-th assistant line of the session's <turn>-th prompt — so a resume at
// one keeps exactly the prompts up to and including that turn, and the recall
// line a forked run prints names what the rewind kept. A vincent that passed
// the wrong anchor, or none, prints a visibly different memory.

// noResumeAtEnv makes this claude a build without --resume-session-at: the
// flag is then commander's unknown option, on the adapter's probe and on a
// run alike.
const noResumeAtEnv = "FAKEAGENT_CLAUDE_NO_RESUME_AT"

// assistantTurn is the 1-based turn of the session this run's assistant lines
// belong to, and assistantLines counts them; emit stamps both into a `uuid`.
// Zero is a run whose dialect stamps none.
var assistantTurn, assistantLines int

// claudeResumeAtGate answers the flag the way 2.1.330 does before a run reads
// its stdin: unknown to a build without it, and refused without --resume —
// the two sentences the adapter's capability probe tells apart
// (probe_resume_at_*_2.1.330.txt). It returns only when the run may go on.
func claudeResumeAtGate() {
	if !hasFlag("--resume-session-at") {
		return
	}
	if os.Getenv(noResumeAtEnv) == "1" {
		fmt.Fprintln(os.Stderr, "error: unknown option '--resume-session-at'")
		os.Exit(1)
	}
	if _, resuming := resumeArg(); !resuming {
		fmt.Fprintln(os.Stderr, "Error: --resume-session-at requires --resume")
		os.Exit(1)
	}
}

// messageUUID is the uuid of this run's next assistant line.
func messageUUID() string {
	assistantLines++
	return fmt.Sprintf("fake-msg-%d-%d", assistantTurn, assistantLines)
}

// turnOfUUID is the turn a fake uuid names, or 0 for one this binary never
// wrote.
func turnOfUUID(uuid string) int {
	rest, ok := strings.CutPrefix(uuid, "fake-msg-")
	if !ok {
		return 0
	}
	turn, _, ok := strings.Cut(rest, "-")
	if !ok {
		return 0
	}
	n, err := strconv.Atoi(turn)
	if err != nil || n < 1 {
		return 0
	}
	return n
}

// forkSession turns a resume of a stored session into a fork: the prompts it
// keeps — up to the turn --resume-session-at names, or all of them without
// it — stored under a new session id. (With no store, openSession forks to
// `<id>-fork` and remembers nothing, as every storeless run does.) A uuid the session does not hold
// is refused with claude's own sentence and no stream.
func forkSession(prompts []string) []string {
	kept := prompts
	if at, ok := flagValue(os.Args[1:], "--resume-session-at"); ok {
		n := turnOfUUID(at)
		if n == 0 || n > len(prompts) {
			fmt.Fprintln(os.Stderr, "No message found with message.uuid of: "+at)
			os.Exit(1)
		}
		kept = prompts[:n]
	}
	kept = append([]string(nil), kept...)
	dir := sessionDir()
	sessionID = newSessionID(dir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "fakeagent: session store:", err)
		os.Exit(1)
	}
	b, err := json.Marshal(session{Prompts: kept})
	if err == nil {
		err = os.WriteFile(sessionPath(sessionID), b, 0o600)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "fakeagent: session store:", err)
		os.Exit(1)
	}
	return kept
}
