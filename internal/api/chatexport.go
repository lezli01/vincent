package api

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"time"

	"github.com/lezli01/vincent/internal/chatexport"
	"github.com/lezli01/vincent/internal/chatrun"
	"github.com/lezli01/vincent/internal/chatstate"
	"github.com/lezli01/vincent/internal/store"
)

// handleChatExport is GET /v1/chats/{id}/export (§5.5 "Exporting a chat",
// §13.2, task 156): the chat as one Markdown, HTML or JSON document, rendered
// here so every client writes the same bytes.
//
// It is information, like /skills and /files (§12.2's chats amendments):
// every state exports, archived and handed_off and closed included; no chat
// or turn row is touched; no max_parallel_chats slot is held; and it is not a
// turn. A missing transcript or an unobtainable diff is a note inside the
// document, never a failure of the export.
func (s *Server) handleChatExport(w http.ResponseWriter, r *http.Request) {
	// The parameters are checked before the chat is loaded, the way
	// handleTaskDiff checks `by`: a typo reads as a 400, not as whatever the
	// chat happens to be.
	q := r.URL.Query()
	format, err := chatexport.ParseFormat(q.Get("format"))
	if err != nil {
		writeError(w, http.StatusBadRequest, CodeValidationFailed, err.Error())
		return
	}
	withDiff := false
	switch v := q.Get("diff"); v {
	case "", "false":
	case "true":
		withDiff = true
	default:
		writeError(w, http.StatusBadRequest, CodeValidationFailed,
			fmt.Sprintf("diff must be true or false; got %q", v))
		return
	}
	chat, ok := s.chatFromPath(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	e, err := s.buildChatExport(ctx, chat, withDiff)
	if err != nil {
		s.internalError(w, "build chat export", err)
		return
	}
	var body bytes.Buffer
	if err := chatexport.Render(&body, e, format); err != nil {
		s.internalError(w, "render chat export", err)
		return
	}
	w.Header().Set("Content-Type", format.ContentType())
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment",
		map[string]string{"filename": chatexport.Filename(chat.ID, chat.Title, format)}))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body.Bytes())
}

// buildChatExport fills the document model from the chat row, its turns in
// seq order, each turn's normalized transcript, and — when asked — the diff.
func (s *Server) buildChatExport(ctx context.Context, chat *store.Chat, withDiff bool) (*chatexport.Export, error) {
	e := &chatexport.Export{
		Version:    chatexport.Version,
		ExportedAt: time.Now().UTC(),
		Chat: chatexport.Chat{
			ID: chat.ID, Title: chat.Title, ProjectID: chat.ProjectID,
			Agent: chat.Agent, Model: chat.Model, Effort: chat.Effort,
			PermissionMode: chat.PermissionMode, State: string(chat.State),
			Branch: chat.Branch, BaseBranch: chat.BaseBranch, BaseSHA: chat.BaseSHA,
			LinkedTaskID: chat.LinkedTaskID, HandoffTaskID: chat.HandoffTaskID,
			IssueID: chat.IssueID, CreatedAt: chat.CreatedAt,
		},
		Turns: []chatexport.Turn{},
	}
	if p, err := s.deps.Store.GetProject(ctx, chat.ProjectID); err == nil {
		e.Chat.Project = p.Name
	} else if !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}
	turns, err := s.deps.Store.ListChatTurns(ctx, chat.ID)
	if err != nil {
		return nil, err
	}
	for i := range turns {
		t := &turns[i]
		et := chatexport.Turn{
			Seq: t.Seq, State: string(t.State), Prompt: t.Prompt, Answer: t.ResultText,
			InputTokens: t.InputTokens, OutputTokens: t.OutputTokens,
			CostUSD: t.CostUSD, DurationMS: t.DurationMS,
			FailReason: t.FailReason, ErrorMessage: t.ErrorMessage, StartedAt: t.StartedAt,
		}
		tools, note := s.chatTurnTools(chat, t)
		et.Tools, et.ToolsNote = tools, note
		if et.Tools == nil {
			et.Tools = []chatexport.ToolCall{}
		}
		e.Turns = append(e.Turns, et)
	}
	if withDiff {
		diff, why := s.chatExportDiff(ctx, chat)
		if why != "" {
			e.DiffUnavailable = why
		} else {
			e.Diff = &diff
		}
	}
	return e, nil
}

// chatTurnTools reads one turn's tool calls from its transcript, normalized
// by the code that serves `?format=normalized` (§13.2), or says why there are
// none to read. A turn that never wrote a transcript is told apart from one
// whose transcript was pruned by task 103 decision 4's predicate.
func (s *Server) chatTurnTools(chat *store.Chat, turn *store.ChatTurn) ([]chatexport.ToolCall, string) {
	f, err := os.Open(s.chatTurnTranscriptPath(chat.ID, turn.Seq))
	if err != nil {
		if chatrun.TurnWritesNoTranscript(turn.FailReason) {
			return nil, chatexport.NoteNeverWritten
		}
		return nil, chatexport.NotePruned
	}
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	if err != nil {
		return nil, chatexport.NotePruned
	}
	// A running turn's file is appended to while it is read; stop at the
	// last whole record, exactly as the transcript route does.
	end, err := lineBoundary(f, fi.Size())
	if err != nil {
		return nil, chatexport.NotePruned
	}
	var normalized bytes.Buffer
	if err := normalizeTranscript(&normalized, io.NewSectionReader(f, 0, end), s.transcriptParser(chat.Agent)); err != nil {
		s.deps.Logger.Error("normalize chat transcript for export", "chat_id", chat.ID, "seq", turn.Seq, "error", err)
	}
	tools, err := chatexport.ToolsFromNormalized(&normalized)
	if err != nil {
		s.deps.Logger.Error("read chat transcript for export", "chat_id", chat.ID, "seq", turn.Seq, "error", err)
	}
	return tools, ""
}

// chatExportDiff is the base-to-tip diff of the directory the chat works in
// today (task 156 decision 1), or the reason there is none. A linked chat's
// directory is its task's worktree and its base is the task's, the chat's
// own base fields being display copies of the task's (§5.5).
func (s *Server) chatExportDiff(ctx context.Context, chat *store.Chat) (diff, unavailable string) {
	if chat.State == chatstate.HandedOff {
		if chat.HandoffTaskID != nil {
			return "", fmt.Sprintf("the chat's worktree was handed off to task %d", *chat.HandoffTaskID)
		}
		return "", "the chat's worktree was handed off to a task"
	}
	dir, base := chat.WorktreePath, diffBase(chat.BaseSHA, chat.BaseBranch)
	if chat.Linked() {
		task, err := s.deps.Store.GetTask(ctx, *chat.LinkedTaskID)
		if err != nil {
			return "", fmt.Sprintf("linked task %d could not be read", *chat.LinkedTaskID)
		}
		dir, base = task.WorktreePath, diffBase(task.BaseSHA, task.BaseBranch)
		if dir == "" {
			return "", fmt.Sprintf("linked task %d has no worktree", task.ID)
		}
	}
	if dir == "" {
		return "", "the chat has no worktree"
	}
	//nolint:gosec // G703: dir is a worktree path the daemon recorded on a row, never request input
	if _, err := os.Stat(dir); err != nil {
		return "", "the chat's worktree no longer exists"
	}
	if base == "" {
		return "", "the chat records no base to diff against"
	}
	mergeBase, err := s.diffMergeBase(ctx, dir, base, "")
	if err != nil {
		return "", fmt.Sprintf("cannot compute merge-base with %q: %v", base, err)
	}
	out, err := s.diffFrom(ctx, dir, mergeBase, "")
	if err != nil {
		return "", fmt.Sprintf("git diff failed: %v", err)
	}
	if out != "" {
		out += "\n"
	}
	return out, ""
}
