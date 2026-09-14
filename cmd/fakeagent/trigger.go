package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
)

// The two argv[1] modes the m16 gate (task 096) needs from a real executable
// on all three platforms. Neither is an agent dialect: they exist because a
// trigger's `source.command` runs its argv directly, never through a shell
// (appendix A), so a gate that wants a poll command on Windows cannot write
// one in bash — and because signing a pushed event needs an HMAC tool that
// Git Bash, macOS and Linux runners do not all agree on.
//
//	fakeagent trigger-poll [events-file]
//	    Prints the events file verbatim — NDJSON, one event per line — so the
//	    gate changes what the next poll sees by appending to that file with its
//	    own bash. The file is argv[2], else FAKEAGENT_TRIGGER_EVENTS; argv wins
//	    because the trigger file names it, and two triggers on one daemon (one
//	    process environment) then read different files. A file that does not
//	    exist is a source with nothing to say, not a failure.
//	    FAKEAGENT_TRIGGER_CURSOR, when set, is echoed as the last line
//	    {"cursor": "..."}. FAKEAGENT_TRIGGER_SEEN names a file each invocation
//	    appends the VINCENT_TRIGGER_CURSOR it was handed to, as one JSON string
//	    per line, so a gate can observe a cursor handed back or dropped.
//	    FAKEAGENT_TRIGGER_EXIT, when non-zero, exits with that status *after*
//	    printing the events: a daemon that read them anyway would be treating a
//	    failing exit as a successful poll.
//
//	fakeagent hmac <secret-env-name>
//	    Reads stdin to EOF and prints the X-Hub-Signature-256 value for it:
//	    "sha256=" + hex HMAC-SHA256 under the secret in the named variable. The
//	    secret is named rather than passed so it never appears on argv.

// triggerPollMain is `fakeagent trigger-poll`.
func triggerPollMain(args []string) {
	if seen := os.Getenv("FAKEAGENT_TRIGGER_SEEN"); seen != "" {
		if err := appendJSONLine(seen, os.Getenv("VINCENT_TRIGGER_CURSOR")); err != nil {
			fmt.Fprintln(os.Stderr, "fakeagent: trigger-poll: record cursor:", err)
			os.Exit(1)
		}
	}
	path := os.Getenv("FAKEAGENT_TRIGGER_EVENTS")
	if len(args) > 0 && args[0] != "" {
		path = args[0]
	}
	if path != "" {
		events, err := os.ReadFile(path)
		switch {
		case errors.Is(err, os.ErrNotExist):
		case err != nil:
			fmt.Fprintln(os.Stderr, "fakeagent: trigger-poll:", err)
			os.Exit(1)
		default:
			_, _ = os.Stdout.Write(events)
			if len(events) > 0 && events[len(events)-1] != '\n' {
				fmt.Println()
			}
		}
	}
	if cursor, ok := os.LookupEnv("FAKEAGENT_TRIGGER_CURSOR"); ok {
		line, _ := json.Marshal(map[string]string{"cursor": cursor})
		fmt.Println(string(line))
	}
	if code, err := strconv.Atoi(os.Getenv("FAKEAGENT_TRIGGER_EXIT")); err == nil && code != 0 {
		fmt.Fprintln(os.Stderr, "fakeagent: trigger-poll failing on purpose with status", code)
		os.Exit(code)
	}
}

// hmacMain is `fakeagent hmac <secret-env-name>`.
func hmacMain(args []string) {
	if len(args) != 1 || args[0] == "" {
		fmt.Fprintln(os.Stderr, "usage: fakeagent hmac <secret-env-name> < body")
		os.Exit(2)
	}
	secret := os.Getenv(args[0])
	if secret == "" {
		fmt.Fprintf(os.Stderr, "fakeagent: hmac: %s is unset or empty\n", args[0])
		os.Exit(2)
	}
	body, err := io.ReadAll(os.Stdin)
	if err != nil {
		fmt.Fprintln(os.Stderr, "fakeagent: hmac: read stdin:", err)
		os.Exit(1)
	}
	fmt.Println(signGitHub(secret, body))
}

// signGitHub computes the value independently of internal/trigger on purpose:
// a gate that signed with the daemon's own helper would pass against a
// verifier that agreed with itself and with nobody else.
func signGitHub(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// appendJSONLine appends v as one JSON line with a single write, so two polls
// racing on one file cannot tear each other's lines.
func appendJSONLine(path string, v any) error {
	line, err := json.Marshal(v)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, err = f.Write(append(line, '\n'))
	return errors.Join(err, f.Close())
}
