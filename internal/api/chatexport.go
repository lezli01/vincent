package api

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"os"
	"time"

	"github.com/lezli01/vincent/internal/agent"
	"github.com/lezli01/vincent/internal/chatexport"
	"github.com/lezli01/vincent/internal/chatrun"
	"github.com/lezli01/vincent/internal/chatstate"
	"github.com/lezli01/vincent/internal/store"
)

// handleChatExport is GET /v1/chats/{id}/export (§5.5 "Exporting a chat",
// §13.2, task 159): the chat as one Markdown, HTML or JSON document, rendered
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
	// Rewound turns are out of the conversation (task 167 decision 9), so
	// an export leaves them out unless asked for them.
	withRewound := false
	switch v := q.Get("include_rewound"); v {
	case "", "false":
	case "true":
		withRewound = true
	default:
		writeError(w, http.StatusBadRequest, CodeValidationFailed,
			fmt.Sprintf("include_rewound must be true or false; got %q", v))
		return
	}
	chat, ok := s.chatFromPath(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	e, err := s.buildChatExport(ctx, chat, withDiff, withRewound)
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
func (s *Server) buildChatExport(ctx context.Context, chat *store.Chat, withDiff, withRewound bool) (*chatexport.Export, error) {
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
			PendingPreamble: pendingPreamble(chat),
		},
		Turns:     []chatexport.Turn{},
		ShellRuns: []chatexport.ShellRun{},
	}
	if f := renderForkedFrom(chat); f != nil {
		e.Chat.ForkedFrom = &chatexport.ForkedFrom{ChatID: f.ChatID, Seq: f.Seq, Agent: f.Agent}
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
		if t.RewoundAt != nil && !withRewound {
			continue
		}
		et := chatexport.Turn{
			Seq: t.Seq, State: string(t.State), Prompt: t.Prompt, MentionContext: t.MentionContext, Answer: t.ResultText,
			InputTokens: t.InputTokens, OutputTokens: t.OutputTokens,
			CacheReadTokens: t.CacheReadTokens, CacheCreationTokens: t.CacheCreationTokens,
			ContextTokens: t.ContextTokens, ContextWindow: t.ContextWindow,
			CostUSD: t.CostUSD, DurationMS: t.DurationMS,
			FailReason: t.FailReason, ErrorMessage: t.ErrorMessage, StartedAt: t.StartedAt,
			Model: nilIfEmpty(t.Model), Effort: nilIfEmpty(t.Effort), PermissionMode: nilIfEmpty(t.PermissionMode),
			RewoundAt: t.RewoundAt, RewindID: t.RewindID, Seeded: t.Seeded,
		}
		read, note := s.chatTurnTools(chat, t)
		et.Tools, et.ToolsNote, et.SeededContext = read.Tools, note, read.SeededContext
		if et.Tools == nil {
			et.Tools = []chatexport.ToolCall{}
		}
		e.Turns = append(e.Turns, et)
	}
	runs, err := s.deps.Store.ListChatShellRuns(ctx, chat.ID)
	if err != nil {
		return nil, err
	}
	for _, run := range runs {
		e.ShellRuns = append(e.ShellRuns, chatExportShellRun(run))
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

// chatExportShellRun is one shell run as the export carries it (task 170),
// its output read from the file the row names exactly as the output route
// reads it: a run that never opened a file has none, and a file that is gone
// is a note in the document, never a failure of the export. A running run's
// output is what has been written so far.
func chatExportShellRun(run *store.ChatShellRun) chatexport.ShellRun {
	out := chatexport.ShellRun{
		ID: run.ID, Command: run.Command, State: run.State, ExitCode: run.ExitCode,
		StartedAt: run.StartedAt, EndedAt: run.EndedAt, AfterTurn: run.AfterTurn,
		Truncated: run.Truncated, Attach: run.Attach, AttachedTurn: run.AttachedTurn,
	}
	if run.OutputPath != "" {
		//nolint:gosec // G304: the path is one the daemon recorded on the row, never request input
		data, err := os.ReadFile(run.OutputPath)
		if err != nil {
			out.OutputNote = chatexport.NoteShellOutputMissing
		} else {
			out.Output = string(data)
		}
	}
	return out
}

// chatTurnTools reads one turn's tool calls — and a seeded turn's context
// (task 171 decision 4) — from its transcript, normalized by the code that
// serves `?format=normalized` (§13.2), or says why there are none to read. A turn that never wrote a transcript is told apart from one
// whose transcript was pruned by task 103 decision 4's predicate.
func (s *Server) chatTurnTools(chat *store.Chat, turn *store.ChatTurn) (chatexport.FromNormalized, string) {
	read, err := readTurnTools(s.chatTurnTranscriptPath(chat.ID, turn.Seq), s.transcriptParser(chat.Agent))
	if errors.Is(err, errNoTranscript) {
		if chatrun.TurnWritesNoTranscript(turn.FailReason) {
			return read, chatexport.NoteNeverWritten
		}
		return read, chatexport.NotePruned
	}
	if err != nil {
		s.deps.Logger.Error("read chat transcript for export", "chat_id", chat.ID, "seq", turn.Seq, "error", err)
	}
	return read, ""
}

// errNoTranscript is a turn transcript that cannot be opened or sized.
var errNoTranscript = errors.New("no transcript")

// readTurnTools reads the tool calls (and a seeded context) of the transcript at path, normalized
// by the code that serves `?format=normalized` (§13.2). A transcript that
// cannot be read at all is errNoTranscript; any other error comes with the
// calls read before it.
func readTurnTools(path string, parse agent.LineParser) (chatexport.FromNormalized, error) {
	//nolint:gosec // G304: the path is derived from ids the daemon owns, never request input
	f, err := os.Open(path)
	if err != nil {
		return chatexport.FromNormalized{}, errNoTranscript
	}
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	if err != nil {
		return chatexport.FromNormalized{}, errNoTranscript
	}
	// A running turn's file is appended to while it is read; stop at the
	// last whole record, exactly as the transcript route does.
	end, err := lineBoundary(f, fi.Size())
	if err != nil {
		return chatexport.FromNormalized{}, errNoTranscript
	}
	var normalized bytes.Buffer
	normErr := normalizeTranscript(&normalized, io.NewSectionReader(f, 0, end), parse)
	read, err := chatexport.ReadNormalized(&normalized)
	return read, errors.Join(normErr, err)
}

// TurnToolCalls is a chat turn's top-level tool calls, read from its
// transcript under dataDir and normalized by agents' parser for the chat's
// agent: the seam the daemon hands chatrun for a seeded context's tool
// summaries (task 171 decision 3), since normalizing is this package's code.
// A turn with no readable transcript has none, quietly — a pruned or
// never-written transcript is ordinary. Any other failure keeps the calls
// read before it and is logged to log (nil: discarded), as the export logs
// it, so a seeded context that lost its summaries leaves a trace (review F6).
func TurnToolCalls(dataDir string, agents *agent.Registry, log *slog.Logger, chat *store.Chat, turn *store.ChatTurn) []chatexport.ToolCall {
	var parse agent.LineParser
	if agents != nil {
		if a, ok := agents.Get(chat.Agent); ok {
			parse = a.NewLineParser()
		}
	}
	read, err := readTurnTools(chatTurnTranscriptFile(dataDir, chat.ID, turn.Seq), parse)
	if err != nil && !errors.Is(err, errNoTranscript) && log != nil {
		log.Error("read chat transcript for a seeded context", "chat_id", chat.ID, "seq", turn.Seq, "error", err)
	}
	return read.Tools
}

// chatExportDiff is the base-to-tip diff of the directory the chat works in
// today (task 159 decision 1), or the reason there is none. It excludes
// untracked files, as the task diff does: including them is the chat diff
// routes' departure alone (task 169 decision 1).
func (s *Server) chatExportDiff(ctx context.Context, chat *store.Chat) (diff, unavailable string) {
	dir, base, why := s.chatDiffDir(ctx, chat)
	if why != "" {
		return "", why
	}
	mergeBase, why := s.chatDiffMergeBase(ctx, dir, base)
	if why != "" {
		return "", why
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

// chatDiffDir resolves the directory a chat works in today and the base it
// records, or says why there is no directory to diff. It is the one
// resolution the export's `diff=true` and the chat diff routes share (task
// 169 decision 5), so free, issue and linked chats resolve one way: a linked
// chat's directory is its task's worktree and its base is the task's, the
// chat's own base fields being display copies of the task's (§5.5); an issue
// chat records the issue worktree and its base on its own row.
//
// base may come back empty with no reason: only a diff from the base needs
// one, and chatDiffMergeBase is what says so.
func (s *Server) chatDiffDir(ctx context.Context, chat *store.Chat) (dir, base, unavailable string) {
	if chat.State == chatstate.HandedOff {
		if chat.HandoffTaskID != nil {
			return "", "", fmt.Sprintf("the chat's worktree was handed off to task %d", *chat.HandoffTaskID)
		}
		return "", "", "the chat's worktree was handed off to a task"
	}
	dir, base = chat.WorktreePath, diffBase(chat.BaseSHA, chat.BaseBranch)
	if chat.Linked() {
		task, err := s.deps.Store.GetTask(ctx, *chat.LinkedTaskID)
		if err != nil {
			return "", "", fmt.Sprintf("linked task %d could not be read", *chat.LinkedTaskID)
		}
		dir, base = task.WorktreePath, diffBase(task.BaseSHA, task.BaseBranch)
		if dir == "" {
			return "", "", fmt.Sprintf("linked task %d has no worktree", task.ID)
		}
	}
	if dir == "" {
		return "", "", "the chat has no worktree"
	}
	//nolint:gosec // G703: dir is a worktree path the daemon recorded on a row, never request input
	if _, err := os.Stat(dir); err != nil {
		return "", "", "the chat's worktree no longer exists"
	}
	return dir, base, ""
}

// chatDiffMergeBase is the commit a chat's base diff starts from: the
// merge-base of dir's HEAD with the recorded base, or the reason there is
// none.
func (s *Server) chatDiffMergeBase(ctx context.Context, dir, base string) (mergeBase, unavailable string) {
	if base == "" {
		return "", "the chat records no base to diff against"
	}
	mergeBase, err := s.diffMergeBase(ctx, dir, base, "")
	if err != nil {
		return "", fmt.Sprintf("cannot compute merge-base with %q: %v", base, err)
	}
	return mergeBase, ""
}
