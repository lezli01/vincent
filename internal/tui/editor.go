package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/lezli01/vincent/internal/apiclient"
)

// editRetryMsg carries the result of an editing session: what the human left
// in the file, and what was in it before they started.
type editRetryMsg struct {
	taskID   int64
	field    string
	original string
	text     string
	err      error
}

// execFunc runs a program with the terminal handed over to it. Injected so
// tests can drive the editor path without a terminal or an editor.
type execFunc func(*exec.Cmd, tea.ExecCallback) tea.Cmd

// editorCommand resolves the editor to open, preferring the visual editor a
// user configured, then the line editor, then the platform default.
func editorCommand() []string {
	for _, env := range []string{"VISUAL", "EDITOR"} {
		if v := strings.TrimSpace(os.Getenv(env)); v != "" {
			return strings.Fields(v)
		}
	}
	if runtime.GOOS == "windows" {
		return []string{"notepad"}
	}
	return []string{"vi"}
}

// openEditorPath opens an existing file in $EDITOR and reports when the
// editor exits. It is deliberately a sibling of writeEditorFile rather than a
// flag on it: that one creates a temp file the caller then reads back and
// sends over the wire, while this one touches a file it must not create, must
// not truncate and never reads — the registry reload is what reports the
// result, not this process.
func openEditorPath(run execFunc, path string, done func(error) tea.Msg) tea.Cmd {
	argv := append(editorCommand(), path)
	cmd := exec.Command(argv[0], argv[1:]...) //nolint:gosec // the editor is the user's own choice
	return run(cmd, func(runErr error) tea.Msg { return done(runErr) })
}

// editRetry opens the current step's prompt or command in $EDITOR (§6, §15).
// The step type picks the field, so the client never sends the mismatched
// pair the daemon would reject.
func (d *detail) editRetry() tea.Cmd {
	step, ok := d.task.Step(d.task.CurrentStep)
	if !ok {
		d.actions.setStatus("edit+retry: this task's snapshot is unavailable", true)
		return nil
	}
	text, field, editable := step.EditableText()
	if !editable {
		d.actions.setStatus("edit+retry: a gate has no prompt or command to edit", true)
		return nil
	}
	ext := ".md"
	if step.Type != "agent" {
		ext = ".sh"
	}
	path, err := writeEditorFile(fmt.Sprintf("task%d-%s", d.taskID, safeName(step.ID)), ext, text)
	if err != nil {
		d.actions.setStatus("edit+retry: "+errString(err), true)
		return nil
	}

	argv := append(editorCommand(), path)
	cmd := exec.Command(argv[0], argv[1:]...) //nolint:gosec // the editor is the user's own choice
	taskID := d.taskID
	return d.exec(cmd, func(runErr error) tea.Msg {
		defer func() { _ = os.Remove(path) }()
		msg := editRetryMsg{taskID: taskID, field: field, original: text}
		if runErr != nil {
			msg.err = runErr
			return msg
		}
		edited, readErr := os.ReadFile(path) //nolint:gosec // path is this process's temp file
		if readErr != nil {
			msg.err = readErr
			return msg
		}
		msg.text = string(edited)
		return msg
	})
}

// editRepairPrompt opens the repair prompt in $EDITOR (§6, task 025). A
// repair prompt is prose of a length nobody chose, and the popup's field is
// three lines — the same argument edit+retry already makes for its own text.
//
// Unlike edit+retry it posts nothing: what the editor leaves goes back into
// the form, and the human still has to start the repair.
func (d *detail) editRepairPrompt(text string) tea.Cmd {
	path, err := writeEditorFile(fmt.Sprintf("task%d-repair", d.taskID), ".md", text)
	if err != nil {
		if d.repair != nil {
			d.repair.err = errString(err)
		}
		return nil
	}
	argv := append(editorCommand(), path)
	cmd := exec.Command(argv[0], argv[1:]...) //nolint:gosec // the editor is the user's own choice
	taskID := d.taskID
	return d.exec(cmd, func(runErr error) tea.Msg {
		defer func() { _ = os.Remove(path) }()
		msg := repairEditMsg{taskID: taskID}
		if runErr != nil {
			msg.err = runErr
			return msg
		}
		edited, readErr := os.ReadFile(path) //nolint:gosec // path is this process's temp file
		if readErr != nil {
			msg.err = readErr
			return msg
		}
		msg.text = string(edited)
		return msg
	})
}

// editFollowUpBody opens a follow-up's prompt or command in $EDITOR (§6,
// task 027). Same argument as editRepairPrompt: the popup's field is three
// lines and the text is of a length nobody chose. It posts nothing — what the
// editor leaves goes back into the form, and the human still has to start the
// run.
func (d *detail) editFollowUpBody(text string) tea.Cmd {
	path, err := writeEditorFile(fmt.Sprintf("task%d-follow-up", d.taskID), ".md", text)
	if err != nil {
		if d.followUp != nil {
			d.followUp.err = errString(err)
		}
		return nil
	}
	argv := append(editorCommand(), path)
	cmd := exec.Command(argv[0], argv[1:]...) //nolint:gosec // the editor is the user's own choice
	taskID := d.taskID
	return d.exec(cmd, func(runErr error) tea.Msg {
		defer func() { _ = os.Remove(path) }()
		msg := followUpEditMsg{taskID: taskID}
		if runErr != nil {
			msg.err = runErr
			return msg
		}
		edited, readErr := os.ReadFile(path) //nolint:gosec // path is this process's temp file
		if readErr != nil {
			msg.err = readErr
			return msg
		}
		msg.text = string(edited)
		return msg
	})
}

// writeEditorFile seeds a temp file with the text an editing session starts
// from. The caller supplies the extension so the editor picks sensible
// highlighting, and the name so two concurrent sessions cannot collide. It
// takes a plain name rather than a task and step because a new task's
// description belongs to neither yet.
func writeEditorFile(name, ext, text string) (string, error) {
	path := filepath.Join(os.TempDir(), "vincent-"+safeName(name)+ext)
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		return "", fmt.Errorf("write %s: %w", path, err)
	}
	return path, nil
}

// safeName keeps a step id usable as a file name on every platform.
func safeName(id string) string {
	clean := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			return r
		default:
			return '-'
		}
	}, id)
	if clean == "" {
		return "step"
	}
	return clean
}

// applyEdit decides what an editing session meant. Unchanged text is a plain
// retry: flagging the attempt as edited when nothing was edited would lie to
// the timeline. An emptied file is an abort — an empty prompt is never what
// someone meant, and the daemon would happily run it.
func (d *detail) applyEdit(msg editRetryMsg) tea.Cmd {
	if msg.taskID != d.taskID {
		return nil
	}
	if msg.err != nil {
		d.actions.setStatus("edit+retry: "+errString(msg.err), true)
		return nil
	}
	if strings.TrimSpace(msg.text) == "" {
		d.actions.setStatus("edit+retry cancelled: the file was empty", true)
		return nil
	}
	if msg.text == msg.original {
		d.actions.setStatus("unchanged — retrying as-is", false)
		return d.retryCmd(apiclient.Override{})
	}
	override := apiclient.Override{Prompt: msg.text}
	if msg.field == "run" {
		override = apiclient.Override{Run: msg.text}
	}
	d.actions.setStatus("retrying with your edit…", false)
	return d.retryCmd(override)
}

func (d *detail) retryCmd(override apiclient.Override) tea.Cmd {
	client, id := d.client, d.taskID
	if client == nil {
		d.actions.setStatus("not connected", true)
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), actionTimeout)
		defer cancel()
		task, err := client.Retry(ctx, id, override)
		return actionResultMsg{taskID: id, action: apiclient.ActionRetry, task: task, err: err}
	}
}

// transcriptOpenedMsg reports that the transcript viewer exited. It carries
// no content: the file is read, never written, so there is nothing to send
// back — only a failure worth naming.
type transcriptOpenedMsg struct{ err error }

// openTranscript hands the displayed attempt's whole transcript to $EDITOR
// (T4.11).
//
// The pane holds the last apiclient.DefaultTailBytes of a file §18 allows to
// reach gigabytes, so when a step fails the part a reader wants — how it
// started — is exactly the part not on screen. Paging the pane backwards was
// the alternative and is rejected: the §13.2 range reads *forwards* from a
// byte position, so "the window before this one" has no primitive, and the
// pane would have to grow without bound or refetch from zero on every page.
// Handing the file to a program built for reading whole files costs nothing,
// and `e` already means exactly this in the workflows view.
//
// What opens is the file as it sits on disk — raw JSONL, the lossless record
// — not the pane's normalized rendering. That is the same file
// `vincent task show` prints the path of, so the two ways in agree, and it is
// the only form in which nothing has been dropped.
func (d *detail) openTranscript() tea.Cmd {
	if d.displayRun == 0 {
		d.actions.setStatus("no attempt selected", true)
		return nil
	}
	run := d.runByID(d.displayRun)
	if run.TranscriptPath == nil {
		d.actions.setStatus("this step wrote no transcript", true)
		return nil
	}
	path := *run.TranscriptPath
	// openEditorPath must not create the file, so a pruned transcript would
	// otherwise open as an empty buffer — which reads as "the step produced
	// nothing" rather than "this was deleted". §12.3 prunes archived tasks
	// past transcript_retention_days, so it is a state a reader will meet.
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			d.actions.setStatus("transcript is gone — pruned by retention (§12.3)", true)
			return nil
		}
		d.actions.setStatus("transcript: "+errString(err), true)
		return nil
	}
	return openEditorPath(d.exec, path, func(err error) tea.Msg {
		return transcriptOpenedMsg{err: err}
	})
}

// editCreatePRBody opens the compare-URL editor's focused row in $EDITOR
// (task 052.6). Same argument as the two above: a pull-request body is prose
// of a length nobody chose, and the popup's field is three lines. It opens
// nothing — what the editor leaves goes back into the form, and the human
// still has to press ctrl+s.
func (d *detail) editCreatePRBody(text string) tea.Cmd {
	path, err := writeEditorFile(fmt.Sprintf("task%d-pr", d.taskID), ".md", text)
	if err != nil {
		return func() tea.Msg { return createPREditMsg{taskID: d.taskID, err: err} }
	}
	argv := append(editorCommand(), path)
	cmd := exec.Command(argv[0], argv[1:]...) //nolint:gosec // the editor is the user's own choice
	taskID := d.taskID
	return d.exec(cmd, func(runErr error) tea.Msg {
		defer func() { _ = os.Remove(path) }()
		msg := createPREditMsg{taskID: taskID}
		if runErr != nil {
			msg.err = runErr
			return msg
		}
		edited, readErr := os.ReadFile(path) //nolint:gosec // path is this process's temp file
		if readErr != nil {
			msg.err = readErr
			return msg
		}
		msg.text = string(edited)
		return msg
	})
}
