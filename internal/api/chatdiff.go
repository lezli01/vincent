package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/lezli01/vincent/internal/chatrun"
	"github.com/lezli01/vincent/internal/chatstate"
	"github.com/lezli01/vincent/internal/gitx"
	"github.com/lezli01/vincent/internal/store"
)

// A chat's diff and its per-hunk review (task 169, #1018, spec §5.5, §13.2):
// what the chat's directory holds against where the chat — or its latest
// turn — started, a reject that reverse-applies one hunk in the working tree,
// and an accepted mark that is only a mark. None of these routes is an MCP
// tool (task 169 decision 11, under task 063 decision 2).

// The chat diff's 409 codes. Each is its own code rather than invalid_state
// with details, because a client branches on them: a stale hunk means "reload
// the diff", a conflict means "the lines moved on", and a live turn means
// "wait".
const (
	// CodeChatHunkStale: the hash is not in the scope's current diff.
	CodeChatHunkStale = "chat_hunk_stale"
	// CodeChatHunkConflict: git would not reverse-apply the hunk.
	CodeChatHunkConflict = "chat_hunk_conflict"
	// CodeChatHunkNotRejectable: the file is binary, a rename or mode-only.
	CodeChatHunkNotRejectable = "chat_hunk_not_rejectable"
	// CodeChatTurnLive: a reject or an accept while the chat is running,
	// awaiting input, or draining its queue.
	CodeChatTurnLive = "chat_turn_live"
	// CodeChatDiffUnavailable: no directory, no base, or scope=turn with no
	// checkpoint. The message carries the reason.
	CodeChatDiffUnavailable = "chat_diff_unavailable"
)

// The two scopes of a chat diff (task 169 decision 5).
const (
	chatDiffScopeBase = "base"
	chatDiffScopeTurn = "turn"
)

// chatHunkRejectHeading opens the block a reject leaves at the top of the
// chat's draft (task 169 decision 8).
const chatHunkRejectHeading = "The user reverted these hunks; do not reapply them:"

type chatDiffHunkBody struct {
	Hash   string `json:"hash"`
	Header string `json:"header"`
	// Body is the hunk's lines after the header, each with its ' ', '+', '-'
	// or '\' prefix, joined by "\n" with no trailing newline; a "\r" is kept.
	Body     string `json:"body"`
	Added    int    `json:"added"`
	Removed  int    `json:"removed"`
	Accepted bool   `json:"accepted"`
}

type chatDiffFileBody struct {
	Path       string             `json:"path"`
	OldPath    string             `json:"old_path"`
	Status     string             `json:"status"`
	Binary     bool               `json:"binary"`
	Rejectable bool               `json:"rejectable"`
	Added      int                `json:"added"`
	Removed    int                `json:"removed"`
	Hunks      []chatDiffHunkBody `json:"hunks"`
}

type chatDiffBody struct {
	Scope string `json:"scope"`
	// From is the commit the working tree was diffed against: the merge-base
	// for `base`, the latest turn's checkpoint for `turn`.
	From    string             `json:"from"`
	Added   int                `json:"added"`
	Removed int                `json:"removed"`
	Files   []chatDiffFileBody `json:"files"`
}

type chatDiffStatBody struct {
	Files   int `json:"files"`
	Added   int `json:"added"`
	Removed int `json:"removed"`
}

type chatHunkRequest struct {
	Scope string `json:"scope"`
	Path  string `json:"path"`
	Hunk  string `json:"hunk"`
}

type chatHunkRejectBody struct {
	Draft     string `json:"draft"`
	NoteAdded bool   `json:"note_added"`
	Warning   string `json:"warning"`
}

// chatDiffError is a refusal one of the chat diff routes answers with.
type chatDiffError struct {
	status        int
	code, message string
}

func (e *chatDiffError) Error() string { return e.message }

func chatDiffConflict(code, message string) *chatDiffError {
	return &chatDiffError{status: http.StatusConflict, code: code, message: message}
}

// writeChatDiffError answers err: a chatDiffError as itself, a live turn as
// chat_turn_live, anything else as a 500.
func (s *Server) writeChatDiffError(w http.ResponseWriter, op string, err error) {
	var de *chatDiffError
	switch {
	case errors.As(err, &de):
		writeError(w, de.status, de.code, de.message)
	case errors.Is(err, chatrun.ErrTurnLive):
		writeError(w, http.StatusConflict, CodeChatTurnLive,
			"the chat has a live turn; wait for it to end or cancel it first")
	default:
		s.internalError(w, op, err)
	}
}

// parseChatDiffScope reads a scope; omitted means base.
func parseChatDiffScope(v string) (string, error) {
	switch v {
	case "", chatDiffScopeBase:
		return chatDiffScopeBase, nil
	case chatDiffScopeTurn:
		return chatDiffScopeTurn, nil
	default:
		return "", fmt.Errorf("scope must be %q or %q; got %q", chatDiffScopeBase, chatDiffScopeTurn, v)
	}
}

// chatDiffStart resolves the directory the chat works in and the commit the
// named scope's diff starts from (task 169 decision 5), or a
// chat_diff_unavailable carrying the reason.
//
// The turn scope is the latest turn's checkpoint — the snapshot taken before
// that turn's agent started (task 167). A turn that carries none (a chat from
// before migration 0066, a snapshot that failed, or no turn at all) has no
// such start, and the answer says so: it is never faked from the base, which
// would show a whole chat's changes as one turn's.
func (s *Server) chatDiffStart(ctx context.Context, chat *store.Chat, scope string) (dir, from string, err error) {
	dir, base, why := s.chatDiffDir(ctx, chat)
	if why != "" {
		return "", "", chatDiffConflict(CodeChatDiffUnavailable, why)
	}
	if scope == chatDiffScopeBase {
		if from, why = s.chatDiffMergeBase(ctx, dir, base); why != "" {
			return "", "", chatDiffConflict(CodeChatDiffUnavailable, why)
		}
		return dir, from, nil
	}
	turns, err := s.deps.Store.ListChatTurns(ctx, chat.ID)
	if err != nil {
		return "", "", err
	}
	if len(turns) == 0 {
		return "", "", chatDiffConflict(CodeChatDiffUnavailable, "the chat has no turn yet")
	}
	last := turns[len(turns)-1]
	if last.CheckpointSHA == "" {
		return "", "", chatDiffConflict(CodeChatDiffUnavailable,
			fmt.Sprintf("turn %d has no checkpoint to diff from", last.Seq))
	}
	// A checkpoint's ref can be gone while its row remains; asked here so
	// that reads as "unavailable" rather than as a failed diff.
	if _, err := s.git(ctx, dir, "rev-parse", "--verify", "--quiet", last.CheckpointSHA+"^{commit}"); err != nil {
		return "", "", chatDiffConflict(CodeChatDiffUnavailable,
			fmt.Sprintf("turn %d's checkpoint no longer exists", last.Seq))
	}
	return dir, last.CheckpointSHA, nil
}

// chatWorkingTree snapshots dir's working tree as a tree object — untracked
// files included, ignored ones not (task 169 decision 1) — touching neither
// the index nor HEAD. Its temporary index lives where a checkpoint's does.
func (s *Server) chatWorkingTree(ctx context.Context, dir string) (string, error) {
	return s.deps.Git.SnapshotTree(ctx, dir, chatrun.CheckpointScratch(s.deps.Dirs.Data))
}

// chatDiffFiles is the scope's current diff: the working tree against the
// scope's start.
func (s *Server) chatDiffFiles(ctx context.Context, chat *store.Chat, scope string) (dir, from string, files []gitx.FileDiff, err error) {
	if dir, from, err = s.chatDiffStart(ctx, chat, scope); err != nil {
		return "", "", nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, gitx.WorktreeTimeout)
	defer cancel()
	tree, err := s.chatWorkingTree(ctx, dir)
	if err != nil {
		return "", "", nil, err
	}
	if files, err = s.deps.Git.DiffTrees(ctx, dir, from, tree); err != nil {
		return "", "", nil, err
	}
	return dir, from, files, nil
}

// findChatHunk finds path's file in files, and in it the hunk carrying hash.
// file is nil for an unknown path; hunk is nil when the file has no such hunk.
func findChatHunk(files []gitx.FileDiff, path, hash string) (file *gitx.FileDiff, hunk *gitx.Hunk) {
	for i := range files {
		if files[i].Path != path {
			continue
		}
		file = &files[i]
		for j := range file.Hunks {
			if file.Hunks[j].Hash == hash {
				return file, &file.Hunks[j]
			}
		}
		return file, nil
	}
	return nil, nil
}

func chatHunkStale(path string) *chatDiffError {
	return chatDiffConflict(CodeChatHunkStale,
		fmt.Sprintf("%s has no such hunk in the chat's current diff; read the diff again", path))
}

// handleChatDiff serves GET /v1/chats/{id}/diff?scope=base|turn.
//
// Reading is information, like the export: every state reads while the
// directory remains, a live turn included (task 169 decision 7).
func (s *Server) handleChatDiff(w http.ResponseWriter, r *http.Request) {
	// Checked before the chat is loaded, as handleChatExport checks its own
	// parameters: a typo is a 400 whatever the chat happens to be.
	scope, err := parseChatDiffScope(r.URL.Query().Get("scope"))
	if err != nil {
		writeError(w, http.StatusBadRequest, CodeValidationFailed, err.Error())
		return
	}
	chat, ok := s.chatFromPath(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	_, from, files, err := s.chatDiffFiles(ctx, chat, scope)
	if err != nil {
		s.writeChatDiffError(w, "chat diff", err)
		return
	}
	reviews, err := s.deps.Store.ListChatHunkReviews(ctx, chat.ID)
	if err != nil {
		s.internalError(w, "list chat hunk reviews", err)
		return
	}
	// A mark is keyed by content (task 169 decision 9): a hunk whose lines
	// changed has another hash, and its old mark simply matches nothing.
	type key struct{ path, hash string }
	accepted := make(map[key]bool, len(reviews))
	for _, rv := range reviews {
		accepted[key{rv.Path, rv.HunkHash}] = true
	}
	out := chatDiffBody{Scope: scope, From: from, Files: make([]chatDiffFileBody, 0, len(files))}
	for _, f := range files {
		fb := chatDiffFileBody{
			Path: f.Path, OldPath: f.OldPath, Status: f.Status, Binary: f.Binary,
			Rejectable: f.Rejectable(), Added: f.Added, Removed: f.Removed,
			Hunks: make([]chatDiffHunkBody, 0, len(f.Hunks)),
		}
		for _, h := range f.Hunks {
			fb.Hunks = append(fb.Hunks, chatDiffHunkBody{
				Hash: h.Hash, Header: h.Header, Body: strings.Join(h.Lines, "\n"),
				Added: h.Added, Removed: h.Removed, Accepted: accepted[key{f.Path, h.Hash}],
			})
		}
		out.Added += f.Added
		out.Removed += f.Removed
		out.Files = append(out.Files, fb)
	}
	writeJSON(w, http.StatusOK, out)
}

// handleChatDiffStat serves GET /v1/chats/{id}/diff/stat: the base scope's
// totals in one cheap call (task 169 decision 10), for a client that shows a
// count without holding the diff.
func (s *Server) handleChatDiffStat(w http.ResponseWriter, r *http.Request) {
	chat, ok := s.chatFromPath(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	dir, from, err := s.chatDiffStart(ctx, chat, chatDiffScopeBase)
	if err != nil {
		s.writeChatDiffError(w, "chat diff stat", err)
		return
	}
	ctx, cancel := context.WithTimeout(ctx, gitx.WorktreeTimeout)
	defer cancel()
	tree, err := s.chatWorkingTree(ctx, dir)
	if err != nil {
		s.internalError(w, "chat diff stat", err)
		return
	}
	files, added, removed, err := s.deps.Git.DiffTreesStat(ctx, dir, from, tree)
	if err != nil {
		s.internalError(w, "chat diff stat", err)
		return
	}
	writeJSON(w, http.StatusOK, chatDiffStatBody{Files: files, Added: added, Removed: removed})
}

// decodeChatHunk reads a reject's or an accept's body.
func decodeChatHunk(w http.ResponseWriter, r *http.Request, withScope bool) (req chatHunkRequest, ok bool) {
	if !decodeJSON(w, r, &req) {
		return req, false
	}
	if withScope {
		scope, err := parseChatDiffScope(req.Scope)
		if err != nil {
			writeError(w, http.StatusBadRequest, CodeValidationFailed, err.Error())
			return req, false
		}
		req.Scope = scope
	}
	if req.Path == "" || req.Hunk == "" {
		writeError(w, http.StatusBadRequest, CodeValidationFailed, "path and hunk are required")
		return req, false
	}
	return req, true
}

// whileNoChatTurn runs fn under the runner's guarantee that the chat has no
// turn and none starts (task 169 decision 7). A server with no runner can
// start no turn, so there the chat's own state is the whole question.
func (s *Server) whileNoChatTurn(ctx context.Context, chat *store.Chat, fn func() error) error {
	if s.deps.Chats != nil {
		return s.deps.Chats.WhileNoTurn(ctx, chat.ID, fn)
	}
	if chatstate.HoldsProcess(chat.State) {
		return chatrun.ErrTurnLive
	}
	return fn()
}

// handleChatDiffReject serves POST /v1/chats/{id}/diff/reject (task 169
// decision 6): the scope's diff is generated again, the hunk found in it by
// path and hash, and reverse-applied to the working tree — never the index,
// never HEAD. The note for the agent then goes to the top of the chat's
// draft, in the transaction that drops the hunk's accepted mark.
func (s *Server) handleChatDiffReject(w http.ResponseWriter, r *http.Request) {
	req, ok := decodeChatHunk(w, r, true)
	if !ok {
		return
	}
	chat, ok := s.chatFromPath(w, r)
	if !ok {
		return
	}
	if chatstate.Terminal(chat.State) {
		// A chat that has ended is read, never edited (task 169 decision 7):
		// nobody is left to be told the hunk went, and its worktree may be
		// another owner's by now.
		writeConflict(w, "this chat has ended; its hunks can be read but no longer rejected",
			map[string]string{"state": string(chat.State), "action": "reject"})
		return
	}
	ctx := r.Context()
	var out chatHunkRejectBody
	err := s.whileNoChatTurn(ctx, chat, func() error {
		dir, _, files, err := s.chatDiffFiles(ctx, chat, req.Scope)
		if err != nil {
			return err
		}
		file, hunk := findChatHunk(files, req.Path, req.Hunk)
		if file == nil {
			return chatHunkStale(req.Path)
		}
		applyCtx, cancel := context.WithTimeout(ctx, gitx.QueryTimeout)
		defer cancel()
		err = s.deps.Git.ReverseHunk(applyCtx, dir, *file, req.Hunk)
		switch {
		case errors.Is(err, gitx.ErrHunkNotRejectable):
			return chatDiffConflict(CodeChatHunkNotRejectable,
				fmt.Sprintf("%s is binary, a rename or a mode-only change; its hunks cannot be rejected one at a time", req.Path))
		case errors.Is(err, gitx.ErrHunkNotFound) || (err == nil && hunk == nil):
			return chatHunkStale(req.Path)
		case errors.Is(err, gitx.ErrHunkConflict):
			return chatDiffConflict(CodeChatHunkConflict,
				fmt.Sprintf("git would not reverse-apply the hunk to %s: %v", req.Path, err))
		case err != nil:
			return err
		}
		// The worktree has changed, so the note is written even if the
		// client has gone: a revert the agent is never told of is the one
		// outcome the note exists to prevent.
		header := hunk.Header
		c, added, err := s.deps.Store.RecordChatHunkReject(context.WithoutCancel(ctx), chat.ID, req.Path, req.Hunk,
			func(draft string) (string, bool) { return addChatHunkRejectNote(draft, req.Path, header), true })
		if err != nil {
			return err
		}
		out = chatHunkRejectBody{Draft: c.Draft, NoteAdded: added}
		if !added {
			// The reject stands (task 169 decision 8): the hunk is reverted
			// either way, and refusing it for the draft's sake would leave
			// the user with a worktree they cannot fix from here.
			out.Warning = fmt.Sprintf(
				"the hunk was reverted, but the note for the agent did not fit the draft's %d KiB limit; tell the agent yourself",
				store.MaxChatDraftBytes>>10)
		}
		return nil
	})
	if err != nil {
		s.writeChatDiffError(w, "reject chat hunk", err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// addChatHunkRejectNote puts one rejected hunk's line into the block at the
// very top of draft (task 169 decision 8): the heading, one "- <path> <@@
// header>" line per hunk, a blank line, then what the draft held. A draft
// that already starts with the heading gets the line appended to the end of
// that block; one the user edited so it no longer does gets a new block.
//
// It is pure, and the same for every client: an API or CLI reject leaves the
// note a TUI one does.
func addChatHunkRejectNote(draft, path, header string) string {
	// A hunk of a CRLF file can carry the line's CR on its header.
	line := "- " + path + " " + strings.TrimRight(header, "\r")
	rest, ok := strings.CutPrefix(draft, chatHunkRejectHeading)
	if !ok || (rest != "" && rest[0] != '\n') {
		return chatHunkRejectHeading + "\n" + line + "\n\n" + draft
	}
	// end is where the block stops: the index of the "\n" ending its last
	// line, or the draft's end.
	end := len(chatHunkRejectHeading)
	for end < len(draft) {
		next := draft[end+1:]
		if !strings.HasPrefix(next, "- ") {
			break
		}
		nl := strings.IndexByte(next, '\n')
		if nl < 0 {
			end = len(draft)
			break
		}
		end += 1 + nl
	}
	return draft[:end] + "\n" + line + draft[end:]
}

// handleChatDiffAccept serves POST /v1/chats/{id}/diff/accept (task 169
// decision 9): only a mark. The hunk must be in the named scope's current
// diff; nothing in git changes.
func (s *Server) handleChatDiffAccept(w http.ResponseWriter, r *http.Request) {
	req, ok := decodeChatHunk(w, r, true)
	if !ok {
		return
	}
	chat, ok := s.chatFromPath(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	// The guard is decision 7's, shared with reject: a mark made while the
	// agent is still writing would be on a hunk the reader has not seen the
	// end of.
	err := s.whileNoChatTurn(ctx, chat, func() error {
		_, _, files, err := s.chatDiffFiles(ctx, chat, req.Scope)
		if err != nil {
			return err
		}
		if _, hunk := findChatHunk(files, req.Path, req.Hunk); hunk == nil {
			return chatHunkStale(req.Path)
		}
		return s.deps.Store.AcceptChatHunk(ctx, chat.ID, req.Path, req.Hunk)
	})
	if err != nil {
		s.writeChatDiffError(w, "accept chat hunk", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleChatDiffUnaccept serves POST /v1/chats/{id}/diff/unaccept: the mark
// goes, and removing one that is not there is not an error. It names no
// scope and reads no diff — a mark for a hunk that no longer exists is
// exactly the one a reader may want gone.
func (s *Server) handleChatDiffUnaccept(w http.ResponseWriter, r *http.Request) {
	req, ok := decodeChatHunk(w, r, false)
	if !ok {
		return
	}
	chat, ok := s.chatFromPath(w, r)
	if !ok {
		return
	}
	if err := s.deps.Store.UnacceptChatHunk(r.Context(), chat.ID, req.Path, req.Hunk); err != nil {
		s.internalError(w, "unaccept chat hunk", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
