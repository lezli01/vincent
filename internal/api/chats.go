package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/lezli01/vincent/internal/agent"
	"github.com/lezli01/vincent/internal/chatrun"
	"github.com/lezli01/vincent/internal/chatstate"
	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/worktree"
)

// CodeAgentCannotResume is the typed refusal a chat on an adapter that cannot
// resume its own session gets (§9.1, §9.3, §9.7; task 063 decision 3). It is
// its own code rather than a generic validation failure because the client's
// remedy is specific and nameable: pick an adapter that can, and do not expect
// the conversation to be reconstructed some other way.
const CodeAgentCannotResume = "agent_cannot_resume"

// CodeChatCapReached is a turn refused because `max_parallel_chats` chats are
// already running (§11, decision 1). It is a 409 and never a queue.
const CodeChatCapReached = "chat_cap_reached"

// CodeRepoOperationInProgress is a handoff refused because the chat's worktree
// is partway through a git operation — a conflicted merge, a stopped rebase, a
// cherry-pick awaiting a commit (task 074 decision 4). It is its own code
// because the remedy is specific and the details name the operation: finish or
// abort it, then hand off.
//
// Ordinary uncommitted work is *not* refused. Preserving it is the feature.
const CodeRepoOperationInProgress = worktree.ReasonRepoOperationInProgress

// chatBody is a chat as the API renders it (§5.5, §13.2).
type chatBody struct {
	ID        int64  `json:"id"`
	ProjectID int64  `json:"project_id"`
	Title     string `json:"title"`
	State     string `json:"state"`
	Agent     string `json:"agent"`
	Model     string `json:"model,omitempty"`
	Effort    string `json:"effort,omitempty"`
	// PermissionMode is what the chat's turns run under (§16).
	PermissionMode string `json:"permission_mode,omitempty"`
	// TurnCount is how many turns the chat has. Only the two reads carry it:
	// GET /v1/chats and GET /v1/chats/{id}.
	TurnCount *int   `json:"turn_count,omitempty"`
	Branch    string `json:"branch"`
	// AdoptedBranch says the chat runs on a branch vincent did not cut (§10,
	// task 125), exactly as a task's field does.
	AdoptedBranch bool   `json:"adopted_branch"`
	BaseBranch    string `json:"base_branch"`
	BaseSHA       string `json:"base_sha,omitempty"`
	// BaseRefresh is what the base fetch and the local base's fast-forward did
	// when the chat's worktree was created (§10, §13.2, task 099); null, not
	// omitted, when nothing was recorded.
	BaseRefresh  *baseRefreshBody `json:"base_refresh"`
	WorktreePath string           `json:"worktree_path,omitempty"`
	SessionID    string           `json:"session_id,omitempty"`
	PendingInput json.RawMessage  `json:"pending_input,omitempty"`
	// HandoffTaskID is the task this chat's worktree and branch were handed
	// to (§5.5, task 074). It is the one authoritative edge; a task's
	// `source_chat_id` is this read backwards.
	HandoffTaskID *int64 `json:"handoff_task_id,omitempty"`
	// LinkedTaskID is the task this chat was opened on (§5.5, task 119): it
	// works in that task's worktree, and while it is open the task is locked.
	LinkedTaskID *int64 `json:"linked_task_id,omitempty"`
	// IssueID is the issue this chat was opened on (task 140): it holds that
	// issue's main worktree, and its branch is the issue's main branch.
	IssueID *int64 `json:"issue_id,omitempty"`
	// EndSHA is the commit an issue chat's work ended on, once it let go of
	// the main worktree (task 140 decision 3).
	EndSHA string `json:"end_sha,omitempty"`
	// QueuedCount, QueuePaused and QueuePauseReason are the chat's queue of
	// follow-up messages (task 156, §5.5): how many wait behind the live
	// turn, and whether a non-`done` ending paused them and why.
	QueuedCount      int    `json:"queued_count"`
	QueuePaused      bool   `json:"queue_paused"`
	QueuePauseReason string `json:"queue_pause_reason,omitempty"`
	// CanSteer says whether a message can be written into the running turn
	// (task 156 decision 4), and SteerReason why not, so a client needs no
	// copy of the capability table. No adapter can yet.
	CanSteer bool `json:"can_steer"`
	// HasDraft says the chat holds an unsent draft (task 164), which is the
	// board's `✎ draft` mark. Draft and Stash are the text itself, carried
	// only by GET /v1/chats/{id}; a list never ships them.
	HasDraft bool   `json:"has_draft"`
	Draft    string `json:"draft,omitempty"`
	Stash    string `json:"stash,omitempty"`
	// DraftUpdatedAt is when the draft or stash was last written, carried
	// beside them by GET /v1/chats/{id} only: it orders a read against a
	// client's own writes, so a read taken before a save cannot pass for a
	// newer draft (review F1).
	DraftUpdatedAt *time.Time `json:"draft_updated_at,omitempty"`
	SteerReason    string     `json:"steer_reason,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
	// LastViewedAt is when a person last looked at the chat (task 165); null,
	// not omitted, when nobody ever has. One value per chat, shared by every
	// client: the installation has no per-user identity.
	LastViewedAt *time.Time `json:"last_viewed_at"`
	// Unread is the store's derivation (task 165): the latest terminal turn
	// ended after LastViewedAt. Always present, so false is an answer.
	Unread bool `json:"unread"`
	// RewoundStates are the chat's forward-rewind targets (task 167),
	// carried by GET /v1/chats/{id} only: reading them costs git.
	RewoundStates []chatRewoundStateBody `json:"rewound_states,omitempty"`
	// ForkedFrom names the chat and turn this one was forked from (task
	// 167.6); the source may since have been deleted.
	ForkedFrom *chatForkedFromBody `json:"forked_from,omitempty"`
	// ResumePending says how the next turn starts after a rewind or fork:
	// "resumed" at an anchor in the agent's session, or "seeded" from a
	// summary in a fresh one (decisions A and G).
	ResumePending string `json:"resume_pending,omitempty"`
	// PendingPreamble is the seeded context the next turn will receive —
	// turn 0 (task 171 decision 4) — present only while resume_pending is
	// "seeded". PATCH edits it until a turn starts.
	// Like draft, it is left out of list and search rows (listRow), which
	// carry resume_pending only.
	PendingPreamble string `json:"pending_preamble,omitempty"`
	// Warnings are non-fatal notes on a create — an issue chat opened on a
	// closed issue (task 140 decision 9). Never set on a read.
	Warnings []string `json:"warnings,omitempty"`
}

// chatTurnBody is one turn as the API renders it.
type chatTurnBody struct {
	ID           int64      `json:"id"`
	ChatID       int64      `json:"chat_id"`
	Seq          int        `json:"seq"`
	Prompt       string     `json:"prompt"`
	State        string     `json:"state"`
	FailReason   string     `json:"fail_reason,omitempty"`
	ErrorMessage string     `json:"error_message,omitempty"`
	ResultText   string     `json:"result_text,omitempty"`
	SessionID    string     `json:"session_id,omitempty"`
	InputTokens  int64      `json:"input_tokens"`
	OutputTokens int64      `json:"output_tokens"`
	CostUSD      *float64   `json:"cost_usd"`
	ExitCode     *int       `json:"exit_code,omitempty"`
	StartedAt    time.Time  `json:"started_at"`
	EndedAt      *time.Time `json:"ended_at,omitempty"`
	DurationMS   *int64     `json:"duration_ms,omitempty"`
	// CacheReadTokens and CacheCreationTokens are the turn's cache spend,
	// which input_tokens leaves out; ContextTokens is the prompt size of the
	// run's last model call and ContextWindow its model's window (task 157,
	// §5.5, §9.2). Each is absent when the adapter did not report it — never
	// zero for "unknown".
	CacheReadTokens     *int64 `json:"cache_read_tokens,omitempty"`
	CacheCreationTokens *int64 `json:"cache_creation_tokens,omitempty"`
	ContextTokens       *int64 `json:"context_tokens,omitempty"`
	ContextWindow       *int64 `json:"context_window,omitempty"`
	// Model, Effort and PermissionMode are what the turn ran with (task 162
	// decision 7). All three are absent on a turn from before they were
	// recorded — "not recorded", never the chat's current values.
	Model          string `json:"model,omitempty"`
	Effort         string `json:"effort,omitempty"`
	PermissionMode string `json:"permission_mode,omitempty"`
	// MentionContext is what the turn's `#issue-N`, `#task-N`, `#pr-N` and
	// `@path#Lx-y` mentions expanded into when it started (task 166
	// decision 2), absent when nothing did. Prompt stays the typed text.
	MentionContext string `json:"mention_context,omitempty"`
	// The turn's checkpoint (task 167.2): the snapshot taken before it ran,
	// and the branch head before and after it.
	CheckpointSHA string `json:"checkpoint_sha,omitempty"`
	HeadBefore    string `json:"head_before,omitempty"`
	HeadAfter     string `json:"head_after,omitempty"`
	// RewoundAt marks a turn a rewind took out of the conversation, and
	// RewindID groups the turns one rewind took (decision 9). Clients hide
	// such turns by default; they are still counted in costs.
	RewoundAt *time.Time `json:"rewound_at,omitempty"`
	RewindID  string     `json:"rewind_id,omitempty"`
	// Seeded says the turn started a fresh session from a summary of the
	// turns before it: seeded, not resumed (decision G).
	Seeded bool `json:"seeded"`
	// ResumeAnchor is the agent message the turn resumed its session at.
	ResumeAnchor string `json:"resume_anchor,omitempty"`
	// Rewind is what a turn picker may offer for this turn, carried by
	// GET /v1/chats/{id} only.
	Rewind *chatTurnRewindBody `json:"rewind,omitempty"`
}

func renderChat(c *store.Chat) chatBody {
	return chatBody{
		ID: c.ID, ProjectID: c.ProjectID, Title: c.Title, State: string(c.State),
		Agent: c.Agent, Model: c.Model, Effort: c.Effort, PermissionMode: c.PermissionMode, Branch: c.Branch,
		AdoptedBranch: c.AdoptedBranch, BaseBranch: c.BaseBranch, BaseSHA: c.BaseSHA, BaseRefresh: renderBaseRefresh(c.BaseRefresh),
		WorktreePath: c.WorktreePath, SessionID: c.SessionID, PendingInput: c.PendingInput,
		HandoffTaskID: c.HandoffTaskID, LinkedTaskID: c.LinkedTaskID, IssueID: c.IssueID, EndSHA: c.EndSHA,
		QueuedCount: c.QueuedCount, QueuePaused: c.QueuePaused, QueuePauseReason: c.QueuePauseReason,
		SteerReason: chatrun.SteerUnavailable(c.Agent), HasDraft: c.Draft != "",
		CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt, LastViewedAt: c.LastViewedAt, Unread: c.Unread,
		ForkedFrom: renderForkedFrom(c), ResumePending: resumePending(c),
		PendingPreamble: pendingPreamble(c),
	}
}

func renderChatTurn(t *store.ChatTurn) chatTurnBody {
	return chatTurnBody{
		ID: t.ID, ChatID: t.ChatID, Seq: t.Seq, Prompt: t.Prompt, State: string(t.State),
		FailReason: t.FailReason, ErrorMessage: t.ErrorMessage, ResultText: t.ResultText,
		SessionID: t.SessionID, InputTokens: t.InputTokens, OutputTokens: t.OutputTokens,
		CostUSD: t.CostUSD, ExitCode: t.ExitCode, StartedAt: t.StartedAt,
		EndedAt: t.EndedAt, DurationMS: t.DurationMS,
		CacheReadTokens: t.CacheReadTokens, CacheCreationTokens: t.CacheCreationTokens,
		ContextTokens: t.ContextTokens, ContextWindow: t.ContextWindow,
		Model: t.Model, Effort: t.Effort, PermissionMode: t.PermissionMode,
		MentionContext: t.MentionContext,
		CheckpointSHA:  t.CheckpointSHA, HeadBefore: t.HeadBefore, HeadAfter: t.HeadAfter,
		RewoundAt: t.RewoundAt, RewindID: t.RewindID, Seeded: t.Seeded, ResumeAnchor: t.ResumeAnchor,
	}
}

type createChatRequest struct {
	ProjectID  int64  `json:"project_id"`
	Title      string `json:"title"`
	Agent      string `json:"agent"`
	Model      string `json:"model"`
	Effort     string `json:"effort"`
	BaseBranch string `json:"base_branch"`
	// BranchName names the branch this chat runs on outright. It is only
	// meaningful with ExistingBranch: a chat that cuts its own branch names it
	// `vincent/{id}-{slug}` and the id does not exist until the row does.
	BranchName string `json:"branch_name"`
	// ExistingBranch selects the adopt mode (§10, task 125), exactly as it
	// does on POST /v1/tasks: BranchName must already exist, and the chat runs
	// in the project's main checkout when the branch is checked out there.
	ExistingBranch bool `json:"existing_branch"`
	// IssueID opens the chat on an issue (task 140): it holds the issue's
	// main worktree, on the issue's main branch, as a main task does.
	IssueID *int64 `json:"issue_id"`
	// Message, when set, is sent as the chat's first turn in the same
	// request (task 161 decision 1), and an empty title is derived from it.
	Message string `json:"message"`
	// PermissionMode is the chat's mode, `full-auto` when omitted (task 162
	// decision 1). It is validated by the check PATCH uses, so creating a
	// chat restricted and restricting it later refuse the same adapters.
	PermissionMode string `json:"permission_mode"`
}

// handleChatCreate creates a chat, allocating its worktree and
// `vincent/{id}-{slug}` branch exactly as a task's is (§10).
//
// The adapter is checked for resume support *before* anything is written: a
// chat on an adapter that cannot resume would be a conversation that silently
// forgets itself, and refusing it here is the same shape as §9.4's restricted
// refusal (decision 3).
func (s *Server) handleChatCreate(w http.ResponseWriter, r *http.Request) {
	var req createChatRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	// The create outlives its caller. The row is written before the worktree
	// exists, so a client that gives up mid-checkout — a timeout, an Esc —
	// must not cancel the git work under it and strand a chat with no
	// worktree: the creation runs to completion under git's own budgets, and
	// the chat.created event tells every client it exists.
	r = r.WithContext(context.WithoutCancel(r.Context()))
	req.Title = DeriveChatTitle(req.Title, req.Message)
	if req.Title == "" {
		writeError(w, http.StatusBadRequest, CodeValidationFailed, "a title or a message is required")
		return
	}
	project, err := s.deps.Store.GetProject(r.Context(), req.ProjectID)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, CodeNotFound, fmt.Sprintf("project %d not found", req.ProjectID))
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, CodeInternal, err.Error())
		return
	}
	// An omitted agent resolves to the first registered adapter that can
	// resume, rather than to a configured default: there is no
	// `defaults.agent` key — a task's agent comes from its workflow — and a
	// chat's whole premise is continuity, so "any adapter that can hold a
	// conversation" is the only default that is not a trap.
	name := req.Agent
	if name == "" {
		for _, a := range s.deps.Agents.All() {
			if agent.CanResume(a) {
				name = a.Name()
				break
			}
		}
	}
	adapter, ok := s.deps.Agents.Get(name)
	if !ok {
		writeError(w, http.StatusBadRequest, CodeValidationFailed, fmt.Sprintf("unknown agent %q", name))
		return
	}
	if !agent.CanResume(adapter) {
		writeError(w, http.StatusBadRequest, CodeAgentCannotResume, fmt.Sprintf(
			"agent %q cannot resume its own session, so it cannot hold a conversation; "+
				"vincent refuses this rather than replaying the log as prompt context", name))
		return
	}
	mode := strings.TrimSpace(req.PermissionMode)
	if mode == "" {
		mode = string(agent.FullAuto)
	}
	if mode != string(agent.FullAuto) {
		probe := &store.Chat{Agent: name, PermissionMode: string(agent.FullAuto), State: chatstate.Idle}
		if _, refusal := s.checkChatSettings(r.Context(), probe,
			store.TurnSettings{Model: req.Model, Effort: req.Effort, PermissionMode: mode}); refusal != nil {
			refusal.write(w)
			return
		}
	}
	req.PermissionMode = mode
	base := req.BaseBranch
	if base == "" {
		base = project.DefaultBranch
	}
	if req.IssueID != nil {
		s.createIssueChat(w, r, &req, project, name, base)
		return
	}
	branch := strings.TrimSpace(req.BranchName)
	if req.ExistingBranch {
		if branch == "" {
			writeError(w, http.StatusBadRequest, CodeValidationFailed,
				"existing_branch needs branch_name: there is no branch to adopt otherwise")
			return
		}
		if msg := s.checkBranchPresent(r.Context(), project.Path, branch); msg != "" {
			writeError(w, http.StatusBadRequest, CodeValidationFailed, msg)
			return
		}
		// The working-directory claim (task 125 decision 2). A task whose
		// directory is taken waits in the queue; a chat is created
		// synchronously and has nowhere to wait, so it is refused — with a
		// 409, because nothing about the request is wrong and the same request
		// will succeed once the claimant is archived.
		claimant, err := s.deps.Store.WorkingDirClaim(r.Context(), project.ID, branch, 0)
		if err != nil {
			s.internalError(w, "check working directory claim", err)
			return
		}
		if claimant != "" {
			writeConflict(w,
				fmt.Sprintf("branch %q is already being worked on by %s; one working directory has at most one owner",
					branch, claimant),
				map[string]string{"branch": branch, "claimed_by": claimant})
			return
		}
	} else if branch != "" {
		writeError(w, http.StatusBadRequest, CodeValidationFailed,
			"branch_name needs existing_branch: a chat that cuts its own branch is named from its id")
		return
	}
	chat := &store.Chat{
		ProjectID: project.ID, Title: req.Title, State: chatstate.Idle, Agent: name,
		Model: req.Model, Effort: req.Effort, PermissionMode: req.PermissionMode,
		BaseBranch: base, AdoptedBranch: req.ExistingBranch,
	}
	cut := freeChatCut{fetch: s.deps.Config().FetchBaseBranch}
	if req.ExistingBranch {
		cut.adopt = branch
	}
	created, err := s.createFreeChat(r.Context(), project, chat, cut)
	if err != nil {
		writeError(w, http.StatusInternalServerError, CodeInternal, err.Error())
		return
	}
	s.logBaseRefresh(chat.ID, base, created)
	s.writeChatCreated(r.Context(), w, renderChat(chat), req.Message)
}

// freeChatCut is how createFreeChat makes a free chat's branch: adopt names
// an existing branch to run on (task 125); startSHA cuts a new one at that
// commit with no refresh (a fork, task 167 decision 11); otherwise the new
// branch is cut from the chat's base, refreshed first when fetch is set.
type freeChatCut struct {
	adopt    string
	startSHA string
	fetch    bool
}

// createFreeChat writes a free chat's row, names its branch, and creates and
// claims its worktree: the one path POST /v1/chats and a fork both take, so a
// forked chat is named, cut and claimed exactly as any free chat is. chat is
// updated in place with its id, branch, worktree, base SHA and refresh. An
// adopted chat and a fork record no base refresh (decision 7 of task 125;
// a fork's base is inherited, not refreshed). A fork's BaseSHA is the one
// the caller set, kept rather than overwritten by the cut's empty one.
func (s *Server) createFreeChat(ctx context.Context, project *store.Project, chat *store.Chat, cut freeChatCut) (worktree.Created, error) {
	if err := s.deps.Store.CreateChat(ctx, chat); err != nil {
		return worktree.Created{}, err
	}
	// The branch name needs the id, and the id needs the row: the chat is
	// written first and named second, exactly as a task is. An adopted branch
	// is the one name that does not need the id — the user typed it.
	chat.Branch = worktree.BranchName(chat.ID, chat.Title)
	if cut.adopt != "" {
		chat.Branch = cut.adopt
	}
	if err := s.deps.Store.SetChatBranch(ctx, chat.ID, chat.Branch); err != nil {
		return worktree.Created{}, err
	}
	// A chat's base is refreshed exactly as a task's is (§10, task 099): the
	// `fetch_base_branch` key is read per request, so a hot-reloaded `false`
	// holds for the next chat, and the outcome is recorded on the row in the
	// same write that claims the worktree. With the key off no fetch runs, no
	// base_sha is recorded, and the record says disabled / not_attempted.
	var refresh *store.BaseRefresh
	baseSHA := chat.BaseSHA
	claim := func(c worktree.Created) error {
		if cut.adopt == "" && cut.startSHA == "" {
			refresh = &store.BaseRefresh{
				Fetch:       store.BaseFetch(c.Fetch),
				FastForward: store.BaseFastForward(c.FastForward),
			}
		}
		if cut.startSHA == "" {
			baseSHA = c.BaseSHA
		}
		_, err := s.deps.Store.ClaimChatWorktree(ctx, chat.ID, c.Path, baseSHA, refresh)
		return err
	}
	var (
		created worktree.Created
		err     error
	)
	owner := worktree.ChatOwner(chat.ID)
	switch {
	case cut.adopt != "":
		created, err = s.deps.Worktrees.CreateAdoptAndClaim(ctx, project.Path, owner, chat.Branch, claim)
	case cut.startSHA != "":
		created, err = s.deps.Worktrees.CreateAtAndClaim(ctx, project.Path, owner, chat.Branch, cut.startSHA, claim)
	default:
		created, err = s.deps.Worktrees.CreateAndClaim(
			ctx, project.Path, owner, chat.Branch, chat.BaseBranch, cut.fetch, claim)
	}
	if err != nil {
		return created, err
	}
	chat.WorktreePath, chat.BaseSHA, chat.BaseRefresh = created.Path, baseSHA, refresh
	return created, nil
}

// logBaseRefresh reports a chat's base refresh that did not do what was asked.
// A failed fetch and a fast-forward that git itself refused are Warn: the chat
// started from a possibly stale base, or the human's branch was left behind,
// for a reason worth reading. Every other outcome — no upstream, a dirty or
// diverged local base — is an ordinary answer already recorded on the row, and
// logging it would cry wolf on every chat.
func (s *Server) logBaseRefresh(chatID int64, base string, c worktree.Created) {
	if c.Fetch.Degraded() {
		s.deps.Logger.Warn("chat base fetch failed; starting from the local base",
			"chat", chatID, "base", base, "remote", c.Fetch.Remote, "error", c.Fetch.Error)
	}
	if c.FastForward.Skipped() && c.FastForward.Reason == worktree.SkipError {
		s.deps.Logger.Warn("chat base fast-forward failed; local base left where it was",
			"chat", chatID, "base", base, "error", c.FastForward.Error)
	}
}

// handleChatList lists chats. They are here and nowhere else: chats never
// appear in GET /v1/tasks, which is the whole point of the separate entity.
func (s *Server) handleChatList(w http.ResponseWriter, r *http.Request) {
	var f store.ChatFilter
	if v := r.URL.Query().Get("project_id"); v != "" {
		id, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			writeError(w, http.StatusBadRequest, CodeValidationFailed, "project_id must be an integer")
			return
		}
		f.ProjectID = &id
	}
	if v := r.URL.Query().Get("task_id"); v != "" {
		id, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			writeError(w, http.StatusBadRequest, CodeValidationFailed, "task_id must be an integer")
			return
		}
		f.TaskID = &id
	}
	// The conversations that concern one issue (#931): opened on it, linked
	// to any of its tasks or lanes, or handed off into one. Parsed the way
	// GET /v1/tasks parses its issue_id; an unknown issue lists nothing
	// rather than 404ing, as task_id does.
	if v := r.URL.Query().Get("issue_id"); v != "" {
		id, err := strconv.ParseInt(v, 10, 64)
		if err != nil || id < 1 {
			writeError(w, http.StatusBadRequest, CodeValidationFailed, "issue_id must be a positive integer")
			return
		}
		f.IssueID = id
	}
	for _, st := range r.URL.Query()["state"] {
		if !chatstate.Valid(chatstate.State(st)) {
			writeError(w, http.StatusBadRequest, CodeValidationFailed, fmt.Sprintf("unknown chat state %q", st))
			return
		}
		f.States = append(f.States, chatstate.State(st))
	}
	// `archived` is GET /v1/tasks' parameter, spelled the same and defaulting
	// the same way (§13.2, amended 2026-09-01): terminal chats are excluded
	// unless asked for. It hides `handed_off` as well as `archived` — both are
	// terminal (§5.5, task 074 decision 5) — which the name alone does not
	// say. An explicit `state=` wins over it.
	switch v := r.URL.Query().Get("archived"); v {
	case "", "false":
		f.Archived = store.ArchivedExclude
	case "true":
		f.Archived = store.ArchivedOnly
	case "all":
		f.Archived = store.ArchivedAll
	default:
		writeError(w, http.StatusBadRequest, CodeValidationFailed,
			"archived must be one of: false, true, all")
		return
	}
	// The same paging and the same date bounds GET /v1/tasks takes, spelled
	// the same way (§13.2, task 092): one vocabulary covers both entities.
	// The chat bounds are measured over `updated_at`, which is when a
	// terminal chat ended (task 074 decision 6, task 079 decision 2).
	for name, dst := range map[string]*int{"limit": &f.Limit, "offset": &f.Offset} {
		if v := r.URL.Query().Get(name); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil || n < 0 {
				writeError(w, http.StatusBadRequest, CodeValidationFailed,
					fmt.Sprintf("%s must be a non-negative integer", name))
				return
			}
			*dst = n
		}
	}
	before, since, ok := parseArchivedBounds(w, r)
	if !ok {
		return
	}
	f.ArchivedBefore, f.ArchivedSince = before, since
	chats, err := s.deps.Store.ListChats(r.Context(), f)
	if err != nil {
		writeError(w, http.StatusInternalServerError, CodeInternal, err.Error())
		return
	}
	ids := make([]int64, len(chats))
	for i := range chats {
		ids[i] = chats[i].ID
	}
	counts, err := s.deps.Store.ChatTurnCounts(r.Context(), ids)
	if err != nil {
		writeError(w, http.StatusInternalServerError, CodeInternal, err.Error())
		return
	}
	out := make([]chatBody, 0, len(chats))
	for i := range chats {
		body := renderChat(&chats[i])
		n := counts[chats[i].ID]
		body.TurnCount = &n
		listRow(&body)
		out = append(out, body)
	}
	writeJSON(w, http.StatusOK, map[string]any{"chats": out})
}

// listRow trims a chat body to what a list or search row carries (review
// F3). The seeded context can be up to store.MaxPendingPreambleBytes and
// lives on until the next turn — after an archive too — so only the
// single-chat reads and the responses of the actions that change it carry
// pending_preamble; a row says resume_pending "seeded", as it says
// has_draft rather than the draft.
func listRow(b *chatBody) { b.PendingPreamble = "" }

// handleChatGet returns one chat with its turns — the whole conversation, in
// order, which is what a pane renders.
func (s *Server) handleChatGet(w http.ResponseWriter, r *http.Request) {
	chat, ok := s.chatFromPath(w, r)
	if !ok {
		return
	}
	turns, err := s.deps.Store.ListChatTurns(r.Context(), chat.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, CodeInternal, err.Error())
		return
	}
	body := make([]chatTurnBody, 0, len(turns))
	for i := range turns {
		body = append(body, renderChatTurn(&turns[i]))
	}
	rendered := renderChat(chat)
	rendered.Draft, rendered.Stash, rendered.DraftUpdatedAt = chat.Draft, chat.Stash, chat.DraftUpdatedAt
	n := len(turns)
	rendered.TurnCount = &n
	s.addRewindOptions(r.Context(), chat, turns, &rendered, body)
	writeJSON(w, http.StatusOK, map[string]any{"chat": rendered, "turns": body})
}

type sendChatRequest struct {
	Message string `json:"message"`
	// The per-turn overrides (task 162 decision 6): this turn — or this
	// queued message, when it drains — runs with them, and the chat keeps
	// its own settings.
	chatSettingsRequest
}

// handleChatSend starts a turn on an idle chat, or queues the message behind
// a live one (task 156). An idle chat over the cap is refused with 409 and
// never queued: a chat turn is a foreground reply and must not wait behind
// batch work (task 063 decision 1). A queued message answers 202 with
// `{"queued": true, "queued_message": …}`; a started turn answers 202 with
// the turn, as it always has.
func (s *Server) handleChatSend(w http.ResponseWriter, r *http.Request) {
	chat, ok := s.chatFromPath(w, r)
	if !ok {
		return
	}
	var req sendChatRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Message == "" {
		writeError(w, http.StatusBadRequest, CodeValidationFailed, "message is required")
		return
	}
	if s.deps.Chats == nil {
		writeError(w, http.StatusInternalServerError, CodeInternal, "no chat runner is wired")
		return
	}
	ov := req.turnSettings()
	if !s.checkTurnOverrides(w, r, chat, ov) {
		return
	}
	if !s.checkChatImages(w, r, chat, req.Message) {
		return
	}
	turn, queued, err := s.deps.Chats.SubmitWith(r.Context(), chat.ID, req.Message, ov)
	switch {
	case errors.Is(err, chatrun.ErrChatCapReached):
		writeJSON(w, http.StatusConflict, errorBody{Error: errorDetail{
			Code: CodeChatCapReached, Message: err.Error(),
			Details: map[string]string{"state": string(chat.State)},
		}})
		return
	case errors.Is(err, store.ErrInvalidChatAction):
		writeConflict(w, err.Error(), map[string]string{"state": string(chat.State), "action": "send"})
		return
	case err != nil:
		writeError(w, http.StatusInternalServerError, CodeInternal, err.Error())
		return
	}
	if queued != nil {
		writeJSON(w, http.StatusAccepted, map[string]any{
			"queued": true, "queued_message": renderChatQueued(queued),
		})
		return
	}
	writeJSON(w, http.StatusAccepted, renderChatTurn(turn))
}

// handleChatAnswer answers the chat's pending §7.4 request. It is the same
// flow a task's answer takes, down to the adapter's Respond (decision 8).
func (s *Server) handleChatAnswer(w http.ResponseWriter, r *http.Request) {
	chat, ok := s.chatFromPath(w, r)
	if !ok {
		return
	}
	if chat.State != chatstate.AwaitingInput {
		writeConflict(w, "this chat is not waiting for an answer", map[string]string{"state": string(chat.State), "action": "answer"})
		return
	}
	var resp agent.InputResponse
	if !decodeJSON(w, r, &resp) {
		return
	}
	if s.deps.Chats == nil {
		writeError(w, http.StatusInternalServerError, CodeInternal, "no chat runner is wired")
		return
	}
	if err := s.deps.Chats.Answer(r.Context(), chat.ID, resp); err != nil {
		writeError(w, http.StatusInternalServerError, CodeInternal, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleChatCancel stops a live turn.
func (s *Server) handleChatCancel(w http.ResponseWriter, r *http.Request) {
	chat, ok := s.chatFromPath(w, r)
	if !ok {
		return
	}
	// A chat with no live turn but a shell command running still has
	// something to stop (task 170): the chat's cancel reaches it.
	shell := s.deps.Chats != nil && s.deps.Chats.ShellRunning(chat.ID)
	if !chatstate.Allowed(chat.State, chatstate.Cancel) && (!shell || chatstate.Terminal(chat.State)) {
		writeConflict(w, "this chat has no live turn", map[string]string{"state": string(chat.State), "action": "cancel"})
		return
	}
	if s.deps.Chats == nil {
		writeError(w, http.StatusInternalServerError, CodeInternal, "no chat runner is wired")
		return
	}
	if err := s.deps.Chats.Cancel(r.Context(), chat.ID); err != nil {
		writeError(w, http.StatusInternalServerError, CodeInternal, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// archiveRefusal says which state actually blocked an archive. Archiving is
// legal from `idle` alone (§5.5), so every other state reaches this — and the
// one sentence that used to serve all of them, "a chat with a live turn cannot
// be archived", was true of `running` and `awaiting_input` and false of both
// terminal states, which hold no process at all (§11, issue #298). The
// message now agrees with the `state` the details already carried.
func archiveRefusal(st chatstate.State) string {
	switch st {
	case chatstate.Archived:
		return "this chat is already archived"
	case chatstate.HandedOff:
		return "this chat was handed off to a task, which owns its worktree now"
	case chatstate.Closed:
		return "this chat is already closed"
	default:
		return "a chat with a live turn cannot be archived"
	}
}

// handleChatArchive archives a chat: the worktree goes and the branch may go
// with it, exactly as task 008 archives a task (§10). A dirty worktree is
// refused unless `force` is set — the same refusal, and the same way out.
func (s *Server) handleChatArchive(w http.ResponseWriter, r *http.Request) {
	chat, ok := s.chatFromPath(w, r)
	if !ok {
		return
	}
	if chat.Linked() && !chatstate.Terminal(chat.State) {
		// Not in the linked table at all (task 119 decision 2): the worktree
		// archive would remove is the task's.
		writeChatLinked(w, chat, string(chatstate.Archive))
		return
	}
	if !chatstate.AllowedFor(chat.Kind(), chat.State, chatstate.Archive) {
		writeConflict(w, archiveRefusal(chat.State), map[string]string{"state": string(chat.State), "action": "archive"})
		return
	}
	// A shell command the human left running is stopped first (task 170):
	// its directory is about to change hands or go.
	// It stays held until the archive is done, so none starts meanwhile.
	if s.deps.Chats != nil {
		defer s.deps.Chats.HoldShell(r.Context(), chat.ID)()
	}
	force := r.URL.Query().Get("force") == "true"
	project, err := s.deps.Store.GetProject(r.Context(), chat.ProjectID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, CodeInternal, err.Error())
		return
	}
	keepDir := false
	if chat.OnIssue() {
		// An issue chat's branch is the issue's main branch and is never
		// deleted (task 140 decision 4); its tip is the chat's end_sha. The
		// directory stays for a main task already waiting for it, which
		// receives it by transfer, uncommitted work included; otherwise it
		// goes, and the next holder revives it from the branch. end_sha is
		// stamped below, once the chat has actually let go (review F8).
		if keepDir, err = s.deps.Store.IssueHasWaitingMainTask(r.Context(), *chat.IssueID); err != nil {
			writeError(w, http.StatusInternalServerError, CodeInternal, err.Error())
			return
		}
	}
	if chat.WorktreePath != "" && !keepDir {
		err = s.deps.Worktrees.RemoveAndRelease(r.Context(), project.Path, chat.WorktreePath, force,
			func() error {
				_, err := s.deps.Store.SetChatWorktree(r.Context(), chat.ID, "", "")
				return err
			})
		var wtErr *worktree.Error
		if errors.As(err, &wtErr) {
			// `reason` is the worktree's own vocabulary, the discriminator a
			// task's archive refusal carries too: `worktree_dirty` is the
			// one a client re-sends with force, any other one a force
			// delete is the way out of (task 153).
			writeConflict(w, wtErr.Error(), map[string]string{
				"state": string(chat.State), "action": "archive", "reason": wtErr.Reason,
			})
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, CodeInternal, err.Error())
			return
		}
	}
	if chat.OnIssue() {
		s.stampChatEndSHA(r.Context(), project, chat)
	}
	// The checkpoint refs stay (task 167 decision 12 prunes them on an
	// archive that removes the branch): a chat's archive never deletes its
	// branch, so the refs go with the row, at permanent delete.
	updated, err := s.deps.Store.SetChatState(r.Context(), chat.ID, chatstate.Archived)
	if err != nil {
		writeError(w, http.StatusInternalServerError, CodeInternal, err.Error())
		return
	}
	if updated.OnIssue() && s.deps.WakeRunner != nil {
		// A main task queued behind the chat may be admitted now.
		s.deps.WakeRunner()
	}
	writeJSON(w, http.StatusOK, renderChat(updated))
}

// stampChatEndSHA records an issue chat's branch tip as its end_sha at
// release (task 140 decision 3). A failed read is logged, never fatal: the
// branch still has the work, and end_sha is history.
func (s *Server) stampChatEndSHA(ctx context.Context, project *store.Project, chat *store.Chat) {
	err := s.deps.Worktrees.HandOverUnderClaim(ctx, project.Path, chat.Branch, func(sha string) error {
		chat.EndSHA = sha
		return s.deps.Store.SetChatEndSHA(ctx, chat.ID, sha)
	})
	if err != nil {
		s.deps.Logger.Warn("stamp issue chat end_sha", "chat", chat.ID, "error", err)
	}
}

// chatFromPath resolves {id}, answering 400 or 404 itself.
func (s *Server) chatFromPath(w http.ResponseWriter, r *http.Request) (*store.Chat, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, CodeValidationFailed, "chat id must be an integer")
		return nil, false
	}
	chat, err := s.deps.Store.GetChat(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, CodeNotFound, fmt.Sprintf("chat %d not found", id))
		return nil, false
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, CodeInternal, err.Error())
		return nil, false
	}
	return chat, true
}
