package api

// Rewinding a chat to a turn's checkpoint and forking one from it (task 167,
// spec §5.5, §13.2): POST /v1/chats/{id}/rewind and POST /v1/chats/{id}/fork.
// Neither runs an agent process (decision A): a rewind moves refs, the
// worktree and turn rows and leaves the conversation's plan for the next
// turn; a fork creates an idle chat and starts no turn.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/lezli01/vincent/internal/agent"
	"github.com/lezli01/vincent/internal/chatrun"
	"github.com/lezli01/vincent/internal/chatstate"
	"github.com/lezli01/vincent/internal/store"
)

// chatTurnRewindBody is what a turn picker may offer for one turn: each
// field "" when allowed, else the reason it is not — one of the 409 codes
// the routes below answer with.
type chatTurnRewindBody struct {
	Code               string `json:"code"`
	Conversation       string `json:"conversation"`
	Fork               string `json:"fork"`
	ConversationSeeded bool   `json:"conversation_seeded"`
}

// chatRewoundStateBody is one state a code rewind saved, a forward rewind's
// target.
type chatRewoundStateBody struct {
	K       int    `json:"k"`
	Ref     string `json:"ref"`
	SHA     string `json:"sha"`
	Blocked string `json:"blocked"`
}

// chatForkedFromBody names the chat and turn a fork came from.
type chatForkedFromBody struct {
	ChatID int64 `json:"chat_id"`
	Seq    int   `json:"seq"`
}

// resumePending is how the chat's next turn will start after a rewind or a
// fork: "resumed" at a recorded anchor, "seeded" from a summary, "" as usual.
func resumePending(c *store.Chat) string {
	switch {
	case c.ResumeAt != "":
		return "resumed"
	case c.PendingPreamble != "":
		return "seeded"
	}
	return ""
}

func renderForkedFrom(c *store.Chat) *chatForkedFromBody {
	if c.ForkedFromChatID == nil {
		return nil
	}
	f := &chatForkedFromBody{ChatID: *c.ForkedFromChatID}
	if c.ForkedFromSeq != nil {
		f.Seq = *c.ForkedFromSeq
	}
	return f
}

// addRewindOptions fills GET /v1/chats/{id}'s rewind fields: each turn's
// options and the chat's forward targets. They are the one read that costs
// git, so only the single-chat read carries them; a failure to read them is
// logged and leaves them out rather than failing the read.
func (s *Server) addRewindOptions(ctx context.Context, chat *store.Chat, turns []store.ChatTurn,
	rendered *chatBody, body []chatTurnBody,
) {
	if s.deps.Chats == nil {
		return
	}
	opts := s.deps.Chats.RewindOptions(ctx, chat, turns)
	for i := range body {
		if o, ok := opts[body[i].Seq]; ok {
			body[i].Rewind = &chatTurnRewindBody{
				Code: o.Code, Conversation: o.Conversation, Fork: o.Fork, ConversationSeeded: o.ConversationSeeded,
			}
		}
	}
	states, err := s.deps.Chats.RewoundStates(ctx, chat)
	if err != nil {
		s.deps.Logger.Warn("chat read: rewound states", "chat", chat.ID, "error", err)
		return
	}
	for _, st := range states {
		rendered.RewoundStates = append(rendered.RewoundStates, chatRewoundStateBody{
			K: st.K, Ref: st.Ref, SHA: st.SHA, Blocked: st.Blocked,
		})
	}
}

type rewindChatRequest struct {
	Turn    int    `json:"turn"`
	Rewound int    `json:"rewound"`
	Mode    string `json:"mode"`
}

// writeRewindError maps a rewind or fork refusal onto §13.1 and reports
// whether one was written: every refusal is a 409 whose code is its reason.
func (s *Server) writeRewindError(w http.ResponseWriter, err error) {
	var refusal *chatrun.RewindRefusal
	switch {
	case errors.Is(err, chatrun.ErrTurnLive):
		writeError(w, http.StatusConflict, CodeChatTurnLive, "the chat has a live turn; wait for it or cancel it first")
	case errors.As(err, &refusal):
		writeError(w, http.StatusConflict, refusal.Reason, refusal.Message)
	case errors.Is(err, chatrun.ErrBadRewindRequest):
		writeError(w, http.StatusBadRequest, CodeValidationFailed, err.Error())
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, CodeNotFound, err.Error())
	default:
		writeError(w, http.StatusInternalServerError, CodeInternal, err.Error())
	}
}

// handleChatRewind rewinds a chat (task 167.3-167.5). The body names a turn
// — the state before it ran — with a mode, or a rewound-<k> state to go
// forward to. It runs no agent: a conversation rewind's effect is the plan
// the next turn starts from.
func (s *Server) handleChatRewind(w http.ResponseWriter, r *http.Request) {
	chat, ok := s.chatFromPath(w, r)
	if !ok {
		return
	}
	var req rewindChatRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	// The engine refuses the same shapes; checking here first answers a
	// malformed request with a 400 before the chat's hold is taken.
	switch {
	case (req.Turn > 0) == (req.Rewound > 0):
		writeError(w, http.StatusBadRequest, CodeValidationFailed,
			"name exactly one of turn (a turn's seq) or rewound (a rewound state's k)")
		return
	case req.Turn < 0 || req.Rewound < 0:
		writeError(w, http.StatusBadRequest, CodeValidationFailed, "turn and rewound are positive numbers")
		return
	case req.Rewound > 0 && req.Mode != "" && req.Mode != string(chatrun.RewindCode):
		writeError(w, http.StatusBadRequest, CodeValidationFailed,
			"going forward to a rewound state moves code only; omit mode or send \"code\"")
		return
	}
	switch chatrun.RewindMode(req.Mode) {
	case "", chatrun.RewindCode, chatrun.RewindConversation, chatrun.RewindBoth:
	default:
		writeError(w, http.StatusBadRequest, CodeValidationFailed,
			fmt.Sprintf("mode must be code, conversation or both; got %q", req.Mode))
		return
	}
	if s.deps.Chats == nil {
		writeError(w, http.StatusInternalServerError, CodeInternal, "no chat runner is wired")
		return
	}
	// The rewind outlives its caller, as a create does: once git has started
	// moving the worktree, a client giving up must not cancel it halfway.
	ctx := context.WithoutCancel(r.Context())
	res, err := s.deps.Chats.Rewind(ctx, chat.ID, chatrun.RewindRequest{
		Turn: req.Turn, Rewound: req.Rewound, Mode: chatrun.RewindMode(req.Mode),
	})
	if err != nil {
		s.writeRewindError(w, err)
		return
	}
	if len(res.Skipped) > 0 {
		// Decision D: the restore left these alone because the state it
		// replaced could not save them. The response names them too.
		s.deps.Logger.Warn("chat rewind kept unreadable paths", "chat", chat.ID, "paths", res.Skipped)
	}
	updated := res.Chat
	if updated == nil {
		if updated, err = s.deps.Store.GetChat(ctx, chat.ID); err != nil {
			s.internalError(w, "reread chat after rewind", err)
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"chat": renderChat(updated), "rewind_id": res.RewindID, "ref": res.Ref,
		"skipped": nonNilStrings(res.Skipped), "seeded": res.Seeded, "rewound_turns": nonNilInts(res.RewoundSeqs),
	})
}

type forkChatRequest struct {
	Turn  int    `json:"turn"`
	Title string `json:"title"`
}

// handleChatFork forks a new free chat from a turn of this one (task 167.6,
// decision 11). The new chat is created through createFreeChat, the path
// POST /v1/chats takes, with its branch cut at the turn's head_before and the
// checkpoint's dirty state laid on top unstaged. It inherits the source's
// agent and settings — under task 162's restricted checks, as a create's are
// — and its base (decision C), and nothing else: no link, no issue, no hold
// on a worktree. It starts no turn.
func (s *Server) handleChatFork(w http.ResponseWriter, r *http.Request) {
	src, ok := s.chatFromPath(w, r)
	if !ok {
		return
	}
	var req forkChatRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Turn <= 0 {
		writeError(w, http.StatusBadRequest, CodeValidationFailed, "turn (the seq of the turn to fork from) is required")
		return
	}
	if s.deps.Chats == nil {
		writeError(w, http.StatusInternalServerError, CodeInternal, "no chat runner is wired")
		return
	}
	ctx := context.WithoutCancel(r.Context())
	plan, err := s.deps.Chats.PlanFork(ctx, src.ID, req.Turn)
	if err != nil {
		s.writeRewindError(w, err)
		return
	}
	project, err := s.deps.Store.GetProject(ctx, src.ProjectID)
	if err != nil {
		s.internalError(w, "fork: read project", err)
		return
	}
	if _, ok := s.deps.Agents.Get(plan.Agent); !ok {
		writeError(w, http.StatusBadRequest, CodeValidationFailed, fmt.Sprintf("unknown agent %q", plan.Agent))
		return
	}
	mode := plan.PermissionMode
	if mode == "" {
		mode = string(agent.FullAuto)
	}
	if mode != string(agent.FullAuto) {
		// The probe is a free chat, as the fork will be: a linked source's
		// clamp is the task's, and the fork has no task.
		probe := &store.Chat{Agent: plan.Agent, PermissionMode: string(agent.FullAuto), State: chatstate.Idle}
		if _, refusal := s.checkChatSettings(ctx, probe,
			store.TurnSettings{Model: plan.Model, Effort: plan.Effort, PermissionMode: mode}); refusal != nil {
			refusal.write(w)
			return
		}
	}
	baseBranch, baseSHA, err := s.forkBase(ctx, src)
	if err != nil {
		s.internalError(w, "fork: read source base", err)
		return
	}
	title := strings.TrimSpace(req.Title)
	if title == "" {
		title = cutTitle(fmt.Sprintf("%s (fork of turn %d)", src.Title, plan.Seq))
	}
	srcID, seq := src.ID, plan.Seq
	chat := &store.Chat{
		ProjectID: project.ID, Title: title, State: chatstate.Idle, Agent: plan.Agent,
		Model: plan.Model, Effort: plan.Effort, PermissionMode: mode,
		BaseBranch: baseBranch, BaseSHA: baseSHA,
		SessionID: plan.SessionID, ResumeAt: plan.ResumeAt, PendingPreamble: plan.Preamble,
		ForkedFromChatID: &srcID, ForkedFromSeq: &seq,
	}
	if _, err := s.createFreeChat(ctx, project, chat, freeChatCut{startSHA: plan.HeadBefore}); err != nil {
		s.internalError(w, "fork: create chat", err)
		return
	}
	skipped, err := s.deps.Chats.ApplyForkCheckpoint(ctx, chat.ID, chat.WorktreePath, plan)
	if err != nil {
		// The chat exists, on the right commit, without the checkpoint's
		// uncommitted state. Saying so beats deleting a row a client may
		// already have seen announced; it can be archived like any chat.
		s.internalError(w, fmt.Sprintf("fork: chat %d was created but its checkpoint was not applied", chat.ID), err)
		return
	}
	if len(skipped) > 0 {
		s.deps.Logger.Warn("chat fork kept unreadable paths", "chat", chat.ID, "paths", skipped)
	}
	fresh, err := s.deps.Store.GetChat(ctx, chat.ID)
	if err != nil {
		s.internalError(w, "fork: reread chat", err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"chat": renderChat(fresh), "skipped": nonNilStrings(skipped), "seeded": plan.Seeded,
	})
}

// forkBase is the base a fork records (decision C): the source's own, which
// for a linked chat is its task's — the chat row carries no base of its own.
func (s *Server) forkBase(ctx context.Context, src *store.Chat) (branch, sha string, err error) {
	if src.Linked() {
		task, err := s.deps.Store.GetTask(ctx, *src.LinkedTaskID)
		if err != nil {
			return "", "", err
		}
		return task.BaseBranch, task.BaseSHA, nil
	}
	return src.BaseBranch, src.BaseSHA, nil
}

// pruneChatRefs deletes a chat's checkpoint refs (task 167.8, decision 12)
// after its row is gone. Best-effort, like the transcript unlink beside it:
// the daemon's sweep removes any it misses.
func (s *Server) pruneChatRefs(ctx context.Context, projectID, chatID int64) {
	if s.deps.Chats == nil {
		return
	}
	project, err := s.deps.Store.GetProject(ctx, projectID)
	if err != nil {
		s.deps.Logger.Warn("prune chat refs: read project", "chat", chatID, "error", err)
		return
	}
	if err := s.deps.Chats.PruneCheckpointRefs(ctx, project.Path, chatID); err != nil {
		s.deps.Logger.Warn("prune chat refs", "chat", chatID, "error", err)
	}
}

func nonNilStrings(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}

func nonNilInts(v []int) []int {
	if v == nil {
		return []int{}
	}
	return v
}
