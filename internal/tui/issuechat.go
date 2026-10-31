package tui

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/lezli01/vincent/internal/apiclient"
)

// A chat on an issue (task 140.9, §15): `T` on the issues list and the issue
// detail. The key is the `chat` operation's (keymap.Chat), the one a task's
// actions answer, because the gesture is the same — talk about this thing in
// its worktree, or reopen the chat already doing so.
//
// It posts straight away rather than opening the new-chat form, the way `T`
// on a task does (task 119): the issue decides the project, the worktree and
// the branch, the title is the issue's, and the agent is the daemon's
// default — the form's remaining rows would ask nothing the issue has not
// answered. Nothing here decides whether the issue can take a chat: a main
// task in the worktree, or a worktree moved under a stale row, is the
// daemon's 409, shown as it says it — a row read a moment ago is not proof
// the task is still there.

// issueChatOpenedMsg reports POST /v1/chats with issue_id. It goes through
// the root, which opens the chat workspace on success, and is then
// broadcast so the issue screen that said "opening a chat…" can settle.
type issueChatOpenedMsg struct {
	issueID   int64
	projectID int64
	chatID    int64
	// warnings are the 201's advisory notes: a chat on a closed issue.
	warnings []string
	err      error
}

// issueOccupantChat is the live chat holding iss's main worktree, or 0.
func issueOccupantChat(iss apiclient.Issue) int64 {
	if mw := iss.MainWorktree; mw != nil && mw.OccupantChatID != nil {
		return *mw.OccupantChatID
	}
	return 0
}

// issueChatCmd is `T` on an issue. With a chat already holding the main
// worktree it is a navigation and asks the daemon nothing; otherwise it
// opens one. A 409 naming a live chat — the row was stale, or another client
// opened one first — is the same answer a fresh row would have given, so it
// opens that chat rather than reporting a refusal the human cannot act on.
// note is how the calling screen speaks; nil is returned when the key does
// nothing.
func issueChatCmd(client *apiclient.Client, iss apiclient.Issue, note func(string, bool)) tea.Cmd {
	if iss.ID == 0 {
		return nil
	}
	if id := issueOccupantChat(iss); id != 0 {
		pid := iss.ProjectID
		return func() tea.Msg { return openChatMsg{id: id, projectID: pid} }
	}
	if client == nil {
		note("not connected", true)
		return nil
	}
	note("opening a chat on issue #"+strconv.FormatInt(iss.ID, 10)+"…", false)
	id, pid, title := iss.ID, iss.ProjectID, issueChatTitle(iss)
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), actionTimeout)
		defer cancel()
		chat, err := client.CreateChat(ctx, apiclient.CreateChatRequest{
			ProjectID: pid, Title: title, IssueID: &id,
		})
		if err != nil {
			if chatID, live := apiclient.IssueHasLiveChat(err); live {
				return issueChatOpenedMsg{issueID: id, projectID: pid, chatID: chatID}
			}
			return issueChatOpenedMsg{issueID: id, projectID: pid, err: err}
		}
		return issueChatOpenedMsg{issueID: id, projectID: pid, chatID: chat.ID, warnings: chat.Warnings}
	}
}

// issueChatTitle is the chat's title: the issue's, which is what the
// conversation is about. An issue always has one; the fallback only keeps
// the daemon's "title is required" from answering a malformed row.
func issueChatTitle(iss apiclient.Issue) string {
	if t := strings.TrimSpace(iss.Title); t != "" {
		return t
	}
	return "issue #" + strconv.FormatInt(iss.ID, 10)
}

// issueChatNote is what an issue screen says once the chat is open or
// refused: nothing on a clean open — the chat workspace is on screen — the
// 201's warnings, or the daemon's refusal.
func issueChatNote(msg issueChatOpenedMsg) (string, bool) {
	switch {
	case msg.err != nil:
		return "chat on issue #" + strconv.FormatInt(msg.issueID, 10) + ": " + actionErrString(msg.err), true
	case len(msg.warnings) > 0:
		return "chat opened with warnings: " + strings.Join(msg.warnings, "; "), false
	}
	return "", false
}

// chatEventIssueID is the issue a chat event's payload names — set on every
// event of a chat opened on an issue (task 140.9) — or 0.
func chatEventIssueID(ev apiclient.Event) int64 {
	if !strings.HasPrefix(ev.Type, "chat.") || len(ev.Payload) == 0 {
		return 0
	}
	var body struct {
		IssueID int64 `json:"issue_id"`
	}
	if json.Unmarshal(ev.Payload, &body) != nil {
		return 0
	}
	return body.IssueID
}
