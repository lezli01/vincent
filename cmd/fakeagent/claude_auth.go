package main

import (
	"fmt"
	"os"
	"strings"
	"time"
)

// claudeAuthStatus answers `claude auth status --json`, the §9.5 auth probe
// the claude adapter gained in task 107. The answers mirror the shape of
// internal/agent/claude/testdata/auth_status_*_2.1.268.json — every field the
// real CLI prints, personal ones filled with placeholders — so the parse is
// exercised against a whole answer rather than the one field it reads:
//
//	FAKEAGENT_CLAUDE_LOGGED_OUT=1      the captured logged-out JSON, exit 1
//	FAKEAGENT_CLAUDE_AUTH_UNKNOWN=1    exit 1 with non-JSON on stderr — the
//	                                   leg that proves exit 1 alone is not
//	                                   "not authenticated"
//	FAKEAGENT_CLAUDE_AUTH_HANG=1       never answers — the T4.22 timeout leg
//
// The default is the logged-in answer, exit 0.
func claudeAuthStatus() {
	switch {
	case os.Getenv("FAKEAGENT_CLAUDE_AUTH_HANG") == "1":
		// Deliberately longer than any probe bound; the caller's deadline is
		// what ends this process, which is the situation being pinned.
		time.Sleep(10 * time.Minute)
	case os.Getenv("FAKEAGENT_CLAUDE_LOGGED_OUT") == "1":
		fmt.Print(claudeAuthLoggedOut)
		os.Exit(1)
	case os.Getenv("FAKEAGENT_CLAUDE_AUTH_UNKNOWN") == "1":
		fmt.Fprintln(os.Stderr, "Error: failed to load settings (fake)")
		os.Exit(1)
	default:
		fmt.Print(claudeAuthLoggedIn)
	}
}

const claudeAuthLoggedIn = `{
  "loggedIn": true,
  "authMethod": "claude.ai",
  "apiProvider": "firstParty",
  "analyticsDisabled": false,
  "projectsDirectory": "/home/user/.claude/projects",
  "configDirectory": "/home/user/.claude",
  "email": "fake@example.com",
  "orgId": "00000000-0000-0000-0000-000000000000",
  "orgName": "Example Organization",
  "subscriptionType": "max"
}
`

const claudeAuthLoggedOut = `{
  "loggedIn": false,
  "authMethod": "none",
  "apiProvider": "firstParty",
  "analyticsDisabled": false,
  "projectsDirectory": "/tmp/empty-claude-config/projects",
  "configDirectory": "/tmp/empty-claude-config"
}
`

// recordArgv appends this invocation's argv to FAKEAGENT_ARGV_FILE, one
// space-joined line, the way fakegh's FAKEGH_ARGV_FILE does — so a test can
// prove a probe was *not* spawned rather than infer it from a nil result.
func recordArgv(args []string) {
	path := os.Getenv("FAKEAGENT_ARGV_FILE")
	if path == "" {
		return
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer func() { _ = f.Close() }()
	_, _ = fmt.Fprintln(f, strings.Join(args, " "))
}
