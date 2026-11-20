package tui

import (
	"context"
	"fmt"
	"os"
	"strings"

	tea "charm.land/bubbletea/v2"
)

// The chat's two $EDITOR operations (task 163): ctrl+g writes the composer's
// draft in the editor and brings it back, and alt+v opens the whole
// conversation — rendered as Markdown by the daemon — read-only.

// chatEditorMsg carries a composer editing session's outcome: what was in
// the composer when it started, what the editor left, and which queued
// message, if any, the composer was editing.
type chatEditorMsg struct {
	chatID   int64
	queued   int64
	original string
	text     string
	err      error
}

// chatMarkdownMsg is the fetched conversation document, or why it is not.
type chatMarkdownMsg struct {
	chatID int64
	doc    string
	err    error
}

// chatMarkdownClosedMsg reports that the editor showing the conversation
// exited. The file is gone by the time it arrives.
type chatMarkdownClosedMsg struct{ err error }

// composeInEditor hands the composer's draft to $EDITOR (task 163 decision
// 3). It never sends: what the editor leaves goes back into the composer,
// and enter is still what sends it — or, while a queued message is being
// edited, what saves that edit.
func (v *chatView) composeInEditor() tea.Cmd {
	if v.chat == nil {
		return nil
	}
	chatID, queued, original := v.chatID, v.editingQueued, v.composer.Value()
	cmd, err := editTextCmd(v.exec, fmt.Sprintf("chat%d-message", chatID), ".md", original,
		func(text string, err error) tea.Msg {
			return chatEditorMsg{chatID: chatID, queued: queued, original: original, text: text, err: err}
		})
	if err != nil {
		v.note, v.noteBad = "editor: "+errString(err), true
		return nil
	}
	return cmd
}

// applyEditorDraft puts an editing session's result in the composer. An
// emptied or untouched file leaves the draft as it was — an empty message is
// never what someone meant, and an untouched one changes nothing — and a
// failure keeps the draft and says why.
func (v *chatView) applyEditorDraft(msg chatEditorMsg) tea.Cmd {
	if msg.chatID != v.chatID {
		return nil
	}
	if msg.err != nil {
		v.note, v.noteBad = "editor: "+errString(msg.err)+" — the draft is unchanged", true
		return nil
	}
	if msg.queued != v.editingQueued {
		// The queued message the session started on is no longer the one in
		// the composer: writing the text over another draft would lose it.
		v.note, v.noteBad = "the queued message being edited changed meanwhile — the editor's text was dropped", true
		return nil
	}
	if msg.text == msg.original {
		v.note, v.noteBad = "the draft is unchanged", false
		return nil
	}
	text := trimEditorNewline(msg.text, msg.original)
	if strings.TrimSpace(text) == "" {
		v.note, v.noteBad = "the file was empty — the draft is unchanged", false
		return nil
	}
	if text == msg.original {
		v.note, v.noteBad = "the draft is unchanged", false
		return nil
	}
	// SetValue leaves the cursor at the end of the text.
	v.composer.SetValue(text)
	v.note, v.noteBad = "", false
	if v.editingQueued != 0 {
		v.note = "editing the queued message — enter saves, " + chatQueueDeleteKey + " deletes it, esc stops editing"
	}
	return tea.Batch(v.syncInlineSkills(), v.syncInlineFiles())
}

// trimEditorNewline drops the one trailing newline an editor adds on save
// (vi always does), so a prompt written in the editor does not grow a blank
// line. A newline the draft already ended with is the human's and stays.
func trimEditorNewline(text, original string) string {
	for _, nl := range []string{"\r\n", "\n"} {
		if strings.HasSuffix(text, nl) && !strings.HasSuffix(original, nl) {
			return strings.TrimSuffix(text, nl)
		}
	}
	return text
}

// fetchChatMarkdown asks the daemon for the conversation's Markdown (task
// 163 decisions 6, 7). The TUI renders none of it: the document is the
// daemon's, the same one `GET /v1/chats/{id}/export?format=md` serves.
func (v *chatView) fetchChatMarkdown() tea.Cmd {
	if v.chat == nil {
		return nil
	}
	client, id := v.client, v.chatID
	if client == nil {
		v.note, v.noteBad = "not connected", true
		return nil
	}
	v.note, v.noteBad = "opening the conversation…", false
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), actionTimeout)
		defer cancel()
		doc, err := client.ChatExportMarkdown(ctx, id)
		return chatMarkdownMsg{chatID: id, doc: doc, err: err}
	}
}

// openChatMarkdown writes the fetched document to a read-only temp file and
// opens it with the user's editor. Read-only is a convention, not a lock: an
// edit has nowhere to go, so the file is removed when the editor exits and
// whatever was typed into it goes with it. $PAGER is deliberately not used
// (decision 7): the editor is the one program this view already hands the
// terminal to.
func (v *chatView) openChatMarkdown(msg chatMarkdownMsg) tea.Cmd {
	if msg.chatID != v.chatID {
		return nil
	}
	if msg.err != nil {
		v.note, v.noteBad = "conversation: "+errString(msg.err), true
		return nil
	}
	path, err := writeReadOnlyFile(fmt.Sprintf("chat%d", msg.chatID), ".md", msg.doc)
	if err != nil {
		v.note, v.noteBad = "conversation: "+errString(err), true
		return nil
	}
	v.note, v.noteBad = "", false
	return openEditorPath(v.exec, path, func(runErr error) tea.Msg {
		removeReadOnly(path)
		return chatMarkdownClosedMsg{err: runErr}
	})
}

// writeReadOnlyFile writes text to the temp file writeEditorFile would name,
// then makes it read-only (0400). A file left behind read-only by a crash is
// removed first, since it could not be written over.
func writeReadOnlyFile(name, ext, text string) (string, error) {
	path := editorFilePath(name, ext)
	removeReadOnly(path)
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		return "", fmt.Errorf("write %s: %w", path, err)
	}
	if err := os.Chmod(path, 0o400); err != nil {
		removeReadOnly(path)
		return "", fmt.Errorf("chmod %s: %w", path, err)
	}
	return path, nil
}

// removeReadOnly removes a file writeReadOnlyFile made. Windows refuses to
// delete a file carrying the read-only attribute, so it is made writable
// first.
func removeReadOnly(path string) {
	_ = os.Chmod(path, 0o600)
	_ = os.Remove(path)
}
