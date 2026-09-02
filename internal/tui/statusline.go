package tui

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Claude Code renders its status line by running `statusLine.command` and
// printing what it writes to stdout. Wiring vincent in there is how the board
// reaches a terminal that is not running the TUI (task 082): the `vincent
// statusline` subcommand reads the session blob on stdin and answers with a
// line about the daemon's tasks.
//
// This file is the write. It is the first time vincent edits another tool's
// configuration file — §16 recorded only cursor's `--model`, persisted to
// ~/.cursor/cli-config.json, as "the one place vincent mutates state outside
// its own data dir" — and what makes that acceptable is a set of terms none of
// which is optional:
//
//   - user-initiated, from the daemon view's `i`. Nothing here runs on daemon
//     start, on TUI start, or on a refresh: the daemon never calls into this
//     file at all.
//   - never silent. The exact JSON is on screen before anything is written,
//     and it is the same bytes the write puts in the file rather than a
//     rendering of them.
//   - reversible. Uninstall restores the original setting verbatim, and
//     restores its absence when there was none.
//   - narrow. Every other key of settings.json, and every other key of the
//     statusLine object, survives untouched.
const (
	// statusLineKey is the one object in that file vincent has an opinion
	// about. Everything else in it belongs to somebody else.
	statusLineKey = "statusLine"
	// statusLineWrapFlag hands the command that was there before to `vincent
	// statusline`, which runs it and prints its output alongside vincent's
	// own. The payload is base64.RawURLEncoding — no `+`, `/` or `=` — so the
	// token needs no quoting in either /bin/sh or pwsh, and Claude Code
	// chooses which of those runs the command.
	statusLineWrapFlag = "--wrap-b64"
)

// claudeSettingsPath is ~/.claude/settings.json. os.UserHomeDir reads $HOME on
// POSIX and %USERPROFILE% on Windows, which is the same home Claude Code
// resolves — and is what lets a test point both at a temp dir and never touch
// the real one.
func claudeSettingsPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".claude", "settings.json"), nil
}

// statusLinePlan is what a write would do, worked out from the file as it
// stands. It is computed once, off the render path, so that the offer, the
// preview and the write are three views of one reading rather than three
// separate readings that can disagree.
type statusLinePlan struct {
	path string
	// exe is the absolute path to this vincent, from os.Executable.
	exe string
	// settings is the file as it stands. The values are raw bytes because
	// vincent has no business decoding a key it did not write: keeping them
	// unparsed is what lets every one of them go back exactly as it came in.
	settings map[string]json.RawMessage
	// current is the statusLine object the file carries, nil when it has
	// none, and exists says whether the file itself is there. The two are
	// different facts: a settings.json with no statusLine is a considered
	// configuration, a missing file is Claude Code's default.
	current json.RawMessage
	exists  bool
	// installed is a statusLine that already runs vincent.
	installed bool
	// restore is what uninstall puts back: the object install wrapped,
	// decoded from the installed command's --wrap-b64 payload. nil means the
	// key is deleted, because "there was no status line" is a state to
	// restore too.
	restore json.RawMessage
	// restoreErr is a --wrap-b64 payload that will not decode. Uninstall
	// refuses on it rather than guessing: deleting the key would throw away
	// the status line vincent promised to give back.
	restoreErr error
}

// readStatusLinePlan reads Claude Code's settings file. A missing file is not
// an error — it is the common case on a machine where nobody has configured
// anything — and neither is an empty one.
func readStatusLinePlan(path, exe string) (statusLinePlan, error) {
	p := statusLinePlan{path: path, exe: exe, settings: map[string]json.RawMessage{}}
	// G304: the path is ~/.claude/settings.json under this user's own home,
	// read as that user. The one caller that supplies another is a test
	// pointing at its temp dir.
	b, err := os.ReadFile(path) //nolint:gosec // G304: see above
	switch {
	case errors.Is(err, os.ErrNotExist):
		return p, nil
	case err != nil:
		return p, err
	}
	p.exists = true
	if len(strings.TrimSpace(string(b))) == 0 {
		return p, nil
	}
	if err := json.Unmarshal(b, &p.settings); err != nil {
		// Refusing here is the point: rewriting a file vincent could not
		// parse would mean writing back an object with everything it did not
		// understand missing.
		return p, fmt.Errorf("parse %s: %w", path, err)
	}
	p.current = p.settings[statusLineKey]
	if cmd, ok := statusLineCommandOf(p.current); ok && commandIsVincent(cmd) {
		p.installed = true
		p.restore, p.restoreErr = wrappedStatusLine(cmd)
	}
	return p, nil
}

// statusLineCommandOf pulls the command out of a statusLine object. An object
// of some other shape answers false and is left entirely alone: install wraps
// it as raw bytes and uninstall hands the same bytes back, neither of which
// needs to understand it.
func statusLineCommandOf(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 {
		return "", false
	}
	var obj struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal(raw, &obj); err != nil || obj.Command == "" {
		return "", false
	}
	return obj.Command, true
}

// commandIsVincent reports a command that already runs `vincent statusline`.
// It matches on the program's base name rather than on the exact path this
// build has: a vincent that moved is still installed, and wrapping it in
// itself is the one outcome this check exists to prevent.
func commandIsVincent(cmd string) bool {
	exe, rest, ok := splitProgram(cmd)
	if !ok {
		return false
	}
	if strings.TrimSuffix(programBase(exe), ".exe") != "vincent" {
		return false
	}
	rest = strings.TrimSpace(rest)
	return rest == "statusline" || strings.HasPrefix(rest, "statusline ")
}

// programBase is filepath.Base that understands the other platform's
// separator too, lowercased. A settings.json is a file people copy between
// machines, and reading `C:\bin\vincent.exe` on macOS as one long file name
// would make vincent wrap itself in itself.
func programBase(path string) string {
	if i := strings.LastIndexAny(path, `/\`); i >= 0 {
		path = path[i+1:]
	}
	return strings.ToLower(path)
}

// splitProgram takes the program off the front of a command line: the quoted
// form vincent writes, or the bare form a hand-edited file may carry. It is
// not a shell parser and does not need to be — it answers one question, and
// anything it cannot read is somebody else's command, which is the safe
// answer.
func splitProgram(cmd string) (program, rest string, ok bool) {
	cmd = strings.TrimSpace(cmd)
	if after, found := strings.CutPrefix(cmd, `"`); found {
		program, rest, ok = strings.Cut(after, `"`)
		return program, rest, ok
	}
	program, rest, _ = strings.Cut(cmd, " ")
	return program, rest, program != ""
}

// wrappedStatusLine decodes the original object out of an installed command's
// --wrap-b64 payload. No flag means nothing was wrapped, which is the "there
// was no status line before" case and restores as a deleted key.
//
// Splitting on whitespace is enough even though the command carries a quoted
// path that may contain spaces: the flag and its payload are single tokens by
// construction, and RawURLEncoding cannot produce a space.
func wrappedStatusLine(cmd string) (json.RawMessage, error) {
	fields := strings.Fields(cmd)
	for i, f := range fields {
		if f != statusLineWrapFlag {
			continue
		}
		if i+1 >= len(fields) {
			return nil, errors.New(statusLineWrapFlag + " carries no payload")
		}
		raw, err := base64.RawURLEncoding.DecodeString(fields[i+1])
		if err != nil {
			return nil, fmt.Errorf("decode the wrapped status line: %w", err)
		}
		if !json.Valid(raw) {
			return nil, errors.New("the wrapped status line is not JSON")
		}
		return raw, nil
	}
	return nil, nil
}

// command is the argv install writes.
//
// The binary path is always double-quoted, because it can contain spaces —
// macOS keeps vincent's own data under "Application Support" and nothing stops
// the binary living somewhere similar. The payload is
// base64.RawURLEncoding of the original object's raw bytes: unquoted-safe in
// both shells, and byte-exact, which is what makes uninstall a restoration
// rather than a re-derivation.
func (p statusLinePlan) command() string {
	wrap := p.current
	if p.installed {
		// Re-installing must not wrap vincent in itself: what it preserves is
		// whatever the command already there is carrying.
		wrap = p.restore
	}
	cmd := `"` + p.exe + `" statusline`
	if len(wrap) > 0 {
		cmd += " " + statusLineWrapFlag + " " + base64.RawURLEncoding.EncodeToString(wrap)
	}
	return cmd
}

// object is the exact JSON install writes under `statusLine`. The preview and
// the write both come from here, and that is deliberate: a preview built
// separately is a preview that can be wrong about what it is previewing.
func (p statusLinePlan) object() json.RawMessage {
	b, err := json.MarshalIndent(map[string]string{
		"type":    "command",
		"command": p.command(),
	}, "", "  ")
	if err != nil {
		// Marshalling two strings cannot fail, and a plan that showed
		// nothing would still be shown.
		return json.RawMessage(`{"type":"command"}`)
	}
	return b
}

// preview is the fragment the flow puts on screen before it writes anything.
func (p statusLinePlan) preview() string {
	return `"` + statusLineKey + `": ` + string(p.object())
}

// restorePreview is the same for uninstall: what the file gets back. An
// absent original says so in words rather than showing `null`, which is a
// JSON value and would read as one.
func (p statusLinePlan) restorePreview() string {
	if len(p.restore) == 0 {
		return "the " + statusLineKey + " key is removed — there was none before it"
	}
	return `"` + statusLineKey + `": ` + string(p.restore)
}

func (p statusLinePlan) install() error {
	settings := p.copySettings()
	settings[statusLineKey] = p.object()
	return writeSettings(p.path, settings)
}

func (p statusLinePlan) uninstall() error {
	if p.restoreErr != nil {
		return p.restoreErr
	}
	settings := p.copySettings()
	if len(p.restore) == 0 {
		delete(settings, statusLineKey)
	} else {
		settings[statusLineKey] = p.restore
	}
	return writeSettings(p.path, settings)
}

// copySettings keeps the plan itself immutable: the view holds one across
// keystrokes, and a write that mutated it would leave the second press
// describing a file that no longer looks like that.
func (p statusLinePlan) copySettings() map[string]json.RawMessage {
	out := make(map[string]json.RawMessage, len(p.settings)+1)
	for k, v := range p.settings {
		out[k] = v
	}
	return out
}

// writeSettings rewrites Claude Code's file.
//
// encoding/json sorts an object's keys and normalises indentation, and that is
// the whole of the reformatting: every value vincent did not touch goes back
// as the bytes it came in as. The write is a temp file and a rename in the
// same directory, because a half-written settings.json is a Claude Code that
// no longer starts, and this is not vincent's file to break.
func writeSettings(path string, settings map[string]json.RawMessage) error {
	b, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "settings-*.json")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer func() { _ = os.Remove(name) }()
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

// readStatusLineDecline and writeStatusLineDecline are the offer's memory,
// kept in the TUI's own tui.json (§12.2) beside the first-run
// acknowledgment — a preference about a prompt, which is exactly what that
// file is for. A read failure answers false, the way every other read of that
// file does: the cost of being wrong is one more offer, on one more view.
func readStatusLineDecline(dataDir string) bool {
	return readTUIState(dataDir).StatusLineDeclined
}

func writeStatusLineDecline(dataDir string) error {
	return mergeTUIState(dataDir, "status_line_declined", true)
}
