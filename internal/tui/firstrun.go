package tui

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// tuiState is {data_dir}/tui.json (§12.2): state the TUI owns and the daemon
// has no opinion about. It is a JSON object rather than a marker file so a
// later preference joins it without a format change — and so a hand-edited
// file is readable. Task 050's collapsed board groups are the first field to
// take that offer up.
//
// This is view state, not configuration: task 009 decision 1's rule that the
// TUI reads no *configuration* from disk is untouched — `tui.board.group_by`
// still arrives over `GET /v1/config`, as it always did.
type tuiState struct {
	FullAutoNoticeAck bool       `json:"full_auto_notice_ack"`
	BoardFolds        []foldPath `json:"board_folds,omitempty"`
	// ChatFolds is the chats board's own set. It is a second field rather
	// than a second use of BoardFolds because the two boards group by the
	// same project names: sharing the list would make folding a project on
	// one board fold it on the other, which is one fold set pretending to be
	// two (task 067).
	ChatFolds []foldPath `json:"chat_folds,omitempty"`
}

func statePath(dataDir string) string { return filepath.Join(dataDir, "tui.json") }

// readTUIState reads the file. Every failure answers the zero value: no file,
// an unreadable one, a half-written one, an object that no longer parses.
// Both readers want that direction — a security warning that suppresses
// itself because a parse failed has failed the wrong way, and a fold set that
// cannot be read means the board everybody had yesterday.
func readTUIState(dataDir string) tuiState {
	var st tuiState
	if dataDir == "" {
		return st
	}
	b, err := os.ReadFile(statePath(dataDir))
	if err != nil {
		return st
	}
	if err := json.Unmarshal(b, &st); err != nil {
		return tuiState{}
	}
	return st
}

// mergeTUIState writes one field into whatever the file already holds rather
// than rewriting it, so a field this build does not know about — and the
// other field it does — survives being written by it.
func mergeTUIState(dataDir, key string, value any) error {
	if dataDir == "" {
		return errors.New("no data directory resolved")
	}
	raw := map[string]any{}
	if b, err := os.ReadFile(statePath(dataDir)); err == nil {
		if err := json.Unmarshal(b, &raw); err != nil {
			raw = map[string]any{}
		}
	}
	raw[key] = value
	b, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return err
	}
	return os.WriteFile(statePath(dataDir), append(b, '\n'), 0o600)
}

// noticeAcknowledged reports whether the §16 full-auto notice has already
// been dismissed. Every read failure answers false, and showing the warning a
// second time costs one keystroke.
func noticeAcknowledged(dataDir string) bool {
	return readTUIState(dataDir).FullAutoNoticeAck
}

// acknowledgeNotice records the dismissal.
func acknowledgeNotice(dataDir string) error {
	return mergeTUIState(dataDir, "full_auto_notice_ack", true)
}

// noticeBody is §16's full-auto risk, compressed. It is a constant and not a
// file read on purpose: noticeAcknowledged already treats every I/O failure
// as "show the warning", so the warning itself must have no I/O to fail at.
const noticeBody = `Coding agents run here unattended. In full-auto an agent can execute
arbitrary commands as your user. It is not confined to the worktree —
a worktree isolates collisions between tasks, not privileges.

What holds it back:

  · a workflow or step marked "restricted", which limits what the agent may do
  · every run fully transcripted, so you can read back what it did
  · nothing merged and nothing pushed unless a workflow step does it

Command and check steps run workflow content you wrote, at the same trust
level as your own shell. Read the full-auto section of the README before
pointing vincent at anything you cannot afford to lose.`

// firstRunNotice is the §16 warning as a blocking overlay. It owns the whole
// screen until it is dismissed: this is the one thing in the TUI that is
// worth interrupting for.
type firstRunNotice struct {
	active bool
	// ackErr is a failed write. It does not keep the overlay up — the person
	// read the notice, which was the point — but it says so, because the next
	// launch will show it again.
	ackErr error
}

// acknowledge dismisses the overlay and persists the flag. The flag is
// written here rather than when the notice is shown: a ctrl+c two seconds in
// must not permanently bury a warning nobody read.
func (f *firstRunNotice) acknowledge(dataDir string) {
	f.active = false
	f.ackErr = acknowledgeNotice(dataDir)
}

// render is width-independent: the notice is hand-wrapped well inside any
// terminal it would be read in, and reflowing a security warning is not
// worth the chance of mangling it.
func (f firstRunNotice) render() string {
	var b strings.Builder
	b.WriteString("\n  ")
	b.WriteString(styleBad.Render("⚠  Full-auto agents run commands as you"))
	b.WriteString("\n\n")
	for _, line := range strings.Split(noticeBody, "\n") {
		b.WriteString("  ")
		b.WriteString(line)
		b.WriteString("\n")
	}
	b.WriteString("\n  ")
	b.WriteString(styleKey.Render("enter"))
	b.WriteString(" continue")
	b.WriteString(styleDim.Render("  ·  "))
	b.WriteString(styleKey.Render("ctrl+c"))
	b.WriteString(" quit")
	b.WriteString(styleDim.Render("  (shown once)"))
	b.WriteString("\n")
	return b.String()
}

// statusLine reports a failed acknowledgment write, after the overlay is
// already gone. It is a line and not a dialog: the notice was read, which is
// what mattered — the only consequence is that the next launch shows it
// again, and saying so is the whole message.
func (f firstRunNotice) statusLine() (string, bool) {
	if f.ackErr == nil {
		return "", false
	}
	return styleWarn.Render(
		" ⚠ could not record the full-auto notice: " + errString(f.ackErr) +
			" — it will be shown again next launch"), true
}
