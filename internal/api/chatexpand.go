package api

// POST /v1/chats/{id}/expand (task 164 decision 6, spec §5.5, §13.2): what a
// draft's `#issue-N`, `#task-N`, `#pr-N` and `@path#Lx-y` mentions would
// expand into if it were sent now. It runs the very function a turn runs, so
// the composer's note and preview cannot disagree with what is sent.
//
// Like GET /v1/chats/{id}/files it is not a turn: it holds no slot, starts no
// agent, writes nothing, and is refused only on state — a terminal chat has
// no next turn to preview.

import (
	"context"
	"fmt"
	"net/http"

	"github.com/lezli01/vincent/internal/agent"
	"github.com/lezli01/vincent/internal/chatrun"
	"github.com/lezli01/vincent/internal/chatstate"
)

type chatExpandRequest struct {
	Message string `json:"message"`
}

type chatExpandBody struct {
	ChatID int64 `json:"chat_id"`
	// Context is the assembled mention block, "" when nothing resolved.
	Context string `json:"context"`
	// Prompt is the draft as the agent would receive it: the block, then the
	// message in `<message>` tags — agent.RunSpec.JoinedPrompt's framing. A
	// linked or issue chat's first-turn opening context is not part of it.
	Prompt     string            `json:"prompt"`
	Resolved   []chatrun.Mention `json:"resolved"`
	Unresolved []chatrun.Mention `json:"unresolved"`
	// Bytes is len(Context); TokensEstimate is Bytes ÷ 4, an estimate a
	// client always shows with `~` (task 164 decision 5).
	Bytes          int `json:"bytes"`
	TokensEstimate int `json:"tokens_estimate"`
}

func (s *Server) handleChatExpand(w http.ResponseWriter, r *http.Request) {
	if s.deps.Chats == nil {
		writeError(w, http.StatusInternalServerError, CodeInternal, "no chat runner is wired")
		return
	}
	chat, ok := s.chatFromPath(w, r)
	if !ok {
		return
	}
	if chatstate.Terminal(chat.State) {
		writeConflict(w, fmt.Sprintf("this chat is %s, so it has no next turn to expand for", chat.State),
			map[string]string{"state": string(chat.State), "action": "expand"})
		return
	}
	var req chatExpandRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	exp := s.deps.Chats.ExpandMentions(r.Context(), chat, req.Message)
	body := chatExpandBody{
		ChatID: chat.ID, Context: exp.Context,
		Prompt:     agent.RunSpec{Prompt: req.Message, Preamble: exp.Context}.JoinedPrompt(),
		Resolved:   nonNilMentions(exp.Resolved),
		Unresolved: nonNilMentions(exp.Unresolved),
		Bytes:      len(exp.Context), TokensEstimate: len(exp.Context) / 4,
	}
	writeJSON(w, http.StatusOK, body)
}

func nonNilMentions(m []chatrun.Mention) []chatrun.Mention {
	if m == nil {
		return []chatrun.Mention{}
	}
	return m
}

// MentionPull resolves a chat's `#pr-N` mention (task 164 decision 3): the
// pull request numbered n in project id's GitHub repository, through the
// same gate and client the pull request views use. Anything short of a
// pull request — no project, no GitHub binding, the integration unusable,
// GitHub unreachable, no such number — is an error, and the mention stays
// prose. The daemon hands it to internal/chatrun as a closure.
func (s *Server) MentionPull(ctx context.Context, projectID int64, n int) (chatrun.MentionPullRequest, error) {
	project, err := s.deps.Store.GetProject(ctx, projectID)
	if err != nil {
		return chatrun.MentionPullRequest{}, err
	}
	gate := s.githubGateFor(ctx, project)
	if !gate.avail.Available || s.deps.GitHub == nil {
		return chatrun.MentionPullRequest{}, chatrun.ErrNoPull
	}
	pull, err := s.deps.GitHub.GetPull(ctx, gate.repo, n)
	if err != nil {
		return chatrun.MentionPullRequest{}, err
	}
	return chatrun.MentionPullRequest{
		Number: pull.Number, Title: pull.Title, Body: pull.Body, URL: pull.URL,
		State: pull.State, Merged: pull.Merged, Draft: pull.Draft,
		HeadBranch: pull.HeadBranch, BaseBranch: pull.BaseBranch,
	}, nil
}
