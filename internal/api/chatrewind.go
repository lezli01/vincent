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
	// Agent is the source's agent (task 171 decision 5), absent on a fork
	// made before migration 0073 recorded it.
	Agent string `json:"agent,omitempty"`
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

// pendingPreamble is the chat's turn-0 context while it is pending: only on a
// seeded plan, never beside a resume anchor (which the runner would follow
// instead).
func pendingPreamble(c *store.Chat) string {
	if resumePending(c) != "seeded" {
		return ""
	}
	return c.PendingPreamble
}

func renderForkedFrom(c *store.Chat) *chatForkedFromBody {
	if c.ForkedFromChatID == nil {
		return nil
	}
	f := &chatForkedFromBody{ChatID: *c.ForkedFromChatID, Agent: c.ForkedFromAgent}
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
	// Agent forks onto another agent (task 171 decision 2); "" or the
	// source's agent is a same-agent fork, which ignores Model and Effort.
	Agent  string `json:"agent"`
	Model  string `json:"model"`
	Effort string `json:"effort"`
	// PermissionMode overrides the mode the fork would copy from its source.
	PermissionMode string `json:"permission_mode"`
}

// handleChatFork serves POST /v1/chats/{id}/fork (task 167.6, task 171
// decision 2) through forkChat, which says what a fork is.
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
	out, fail := s.forkChat(context.WithoutCancel(r.Context()), src, req)
	if fail != nil {
		fail(w)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"chat": renderChat(out.chat), "skipped": nonNilStrings(out.skipped), "seeded": out.seeded,
	})
}

// forkOutcome is a fork made: the new chat as stored, the paths its
// checkpoint could not restore, and whether its first turn is seeded.
type forkOutcome struct {
	chat    *store.Chat
	skipped []string
	seeded  bool
}

// apiFailure writes a refusal or an error a helper decided on, so the
// handler that called it answers with it unchanged.
type apiFailure func(http.ResponseWriter)

// checkForkTarget refuses a fork target as a create would refuse its agent
// (task 171 decision 2): it must exist and resume. Model and effort belong
// to a fork onto another agent only.
func (s *Server) checkForkTarget(target chatrun.ForkTarget) apiFailure {
	if target.Agent == "" {
		if target.Model != "" || target.Effort != "" {
			return func(w http.ResponseWriter) {
				writeError(w, http.StatusBadRequest, CodeValidationFailed,
					"model and effort apply only to a fork onto another agent; a same-agent fork copies its source's")
			}
		}
		return nil
	}
	adapter, ok := s.deps.Agents.Get(target.Agent)
	if !ok {
		return func(w http.ResponseWriter) {
			writeError(w, http.StatusBadRequest, CodeValidationFailed, fmt.Sprintf("unknown agent %q", target.Agent))
		}
	}
	if !agent.CanResume(adapter) {
		return func(w http.ResponseWriter) {
			writeError(w, http.StatusBadRequest, CodeAgentCannotResume, fmt.Sprintf(
				"agent %q cannot resume its own session, so it cannot hold a conversation", target.Agent))
		}
	}
	return nil
}

// forkTargetOf is req's target, with a fork onto the source's own agent
// spelled as the same-agent fork it is.
func forkTargetOf(src *store.Chat, req forkChatRequest) chatrun.ForkTarget {
	target := chatrun.ForkTarget{Agent: strings.TrimSpace(req.Agent), Model: req.Model, Effort: req.Effort}
	if target.Agent == src.Agent {
		target.Agent = ""
	}
	return target
}

// forkChat forks a new free chat from a turn of src (task 167.6, decision
// 11). The new chat is created through createFreeChat, the path
// POST /v1/chats takes, with its branch cut at the turn's head_before and the
// checkpoint's dirty state laid on top unstaged. It inherits the source's
// agent and settings — under task 162's restricted checks, as a create's are
// — and its base (decision C), and nothing else: no link, no issue, no hold
// on a worktree. It starts no turn.
//
// agent forks onto another adapter instead (task 171 decision 2, amending
// decision 11): it is refused as a create would refuse it, its model and
// effort are the request's or the adapter's defaults, and the fork is always
// seeded. permission_mode, when given, replaces the source's. A compare
// (task 171 decision 6) makes its two sides here too.
func (s *Server) forkChat(ctx context.Context, src *store.Chat, req forkChatRequest) (*forkOutcome, apiFailure) {
	target := forkTargetOf(src, req)
	if fail := s.checkForkTarget(target); fail != nil {
		return nil, fail
	}
	plan, err := s.deps.Chats.PlanForkTo(ctx, src.ID, req.Turn, target)
	if err != nil {
		return nil, func(w http.ResponseWriter) { s.writeRewindError(w, err) }
	}
	internal := func(what string, err error) apiFailure {
		return func(w http.ResponseWriter) { s.internalError(w, what, err) }
	}
	project, err := s.deps.Store.GetProject(ctx, src.ProjectID)
	if err != nil {
		return nil, internal("fork: read project", err)
	}
	if _, ok := s.deps.Agents.Get(plan.Agent); !ok {
		return nil, func(w http.ResponseWriter) {
			writeError(w, http.StatusBadRequest, CodeValidationFailed, fmt.Sprintf("unknown agent %q", plan.Agent))
		}
	}
	mode := strings.TrimSpace(req.PermissionMode)
	if mode == "" {
		mode = plan.PermissionMode
	}
	if mode == "" {
		mode = string(agent.FullAuto)
	}
	if mode != string(agent.FullAuto) || target.Agent != "" {
		// The probe is a free chat, as the fork will be: a linked source's
		// clamp is the task's, and the fork has no task. A cross-agent fork
		// is checked even at full-auto, so a model or effort its adapter
		// does not offer is refused before anything is created.
		if refusal := s.checkForkSettings(ctx, plan.Agent, plan.Model, plan.Effort, mode); refusal != nil {
			return nil, refusal.write
		}
	}
	baseBranch, baseSHA, err := s.forkBase(ctx, src)
	if err != nil {
		return nil, internal("fork: read source base", err)
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
		ForkedFromChatID: &srcID, ForkedFromSeq: &seq, ForkedFromAgent: src.Agent,
	}
	if _, err := s.createFreeChat(ctx, project, chat, freeChatCut{startSHA: plan.HeadBefore}); err != nil {
		return nil, internal("fork: create chat", err)
	}
	skipped, err := s.deps.Chats.ApplyForkCheckpoint(ctx, chat.ID, chat.WorktreePath, plan)
	if err != nil {
		// The chat exists, on the right commit, without the checkpoint's
		// uncommitted state. Saying so beats deleting a row a client may
		// already have seen announced; it can be archived like any chat.
		return nil, internal(fmt.Sprintf("fork: chat %d was created but its checkpoint was not applied", chat.ID), err)
	}
	if len(skipped) > 0 {
		s.deps.Logger.Warn("chat fork kept unreadable paths", "chat", chat.ID, "paths", skipped)
	}
	fresh, err := s.deps.Store.GetChat(ctx, chat.ID)
	if err != nil {
		return nil, internal("fork: reread chat", err)
	}
	return &forkOutcome{chat: fresh, skipped: skipped, seeded: plan.Seeded}, nil
}

// checkForkSettings is task 162's check for the free chat a fork will be,
// run on a probe before anything is created.
func (s *Server) checkForkSettings(ctx context.Context, agentName, model, effort, mode string) *settingsRefusal {
	probe := &store.Chat{Agent: agentName, PermissionMode: string(agent.FullAuto), State: chatstate.Idle}
	_, refusal := s.checkChatSettings(ctx, probe, store.TurnSettings{Model: model, Effort: effort, PermissionMode: mode})
	return refusal
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
