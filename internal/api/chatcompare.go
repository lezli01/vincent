package api

// Compare (task 171 decisions 6 and 7, spec §5.5): one prompt answered by two
// agents, each in its own fork of the chat cut before the same turn, run
// side by side. It is a daemon route and never an MCP tool: it starts two
// agent processes, which no MCP tool may (§13.4).

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/lezli01/vincent/internal/agent"
	"github.com/lezli01/vincent/internal/chatrun"
	"github.com/lezli01/vincent/internal/chatstate"
	"github.com/lezli01/vincent/internal/gitx"
	"github.com/lezli01/vincent/internal/store"
)

type compareChatRequest struct {
	// Turn is the fork point: both sides start from the state before it ran.
	Turn int `json:"turn"`
	// Prompt is what both sides are sent; empty is turn Turn's own prompt,
	// which makes the compare "how would each agent have answered that".
	Prompt string `json:"prompt"`
	// Agents are the two sides, left then right, distinct.
	Agents []string `json:"agents"`
	// PermissionMode is what each side is asked to run under: restricted
	// (the default) where its agent can keep a chat restricted, or
	// full-auto for both.
	PermissionMode string `json:"permission_mode"`
}

// compareSideBody is one side as the API names it: its chat and agent, the
// mode it actually runs under, and why when that is not what was asked.
type compareSideBody struct {
	ChatID         *int64 `json:"chat_id"`
	Agent          string `json:"agent"`
	PermissionMode string `json:"permission_mode"`
	ModeNote       string `json:"mode_note,omitempty"`
}

type compareBody struct {
	ID           int64           `json:"id"`
	SourceChatID int64           `json:"source_chat_id"`
	Seq          int             `json:"seq"`
	Prompt       string          `json:"prompt"`
	Left         compareSideBody `json:"left"`
	Right        compareSideBody `json:"right"`
	KeptChatID   *int64          `json:"kept_chat_id"`
	CreatedAt    time.Time       `json:"created_at"`
}

func renderCompare(c *store.ChatCompare) compareBody {
	side := func(s store.ChatCompareSide) compareSideBody {
		return compareSideBody{ChatID: s.ChatID, Agent: s.Agent, PermissionMode: s.Mode, ModeNote: s.ModeNote}
	}
	return compareBody{
		ID: c.ID, SourceChatID: c.SourceChatID, Seq: c.Seq, Prompt: c.Prompt,
		Left: side(c.Left), Right: side(c.Right), KeptChatID: c.KeptChatID, CreatedAt: c.CreatedAt,
	}
}

// compareSide is one side being set up: its agent, the mode it will run
// under and why, and, once made, its fork.
type compareSide struct {
	agent string
	mode  string
	note  string
	fork  *forkOutcome
}

// handleChatCompare serves POST /v1/chats/{id}/compare. Everything that can
// be refused is checked before anything is created — the agents, each
// side's mode, the turn and two free slots under max_parallel_chats — and a
// failure after the first fork is made removes what was made, so a compare
// starts both sides or neither (decision 6).
func (s *Server) handleChatCompare(w http.ResponseWriter, r *http.Request) {
	src, ok := s.chatFromPath(w, r)
	if !ok {
		return
	}
	var req compareChatRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Turn <= 0 {
		writeError(w, http.StatusBadRequest, CodeValidationFailed, "turn (the seq of the turn to compare from) is required")
		return
	}
	if len(req.Agents) != 2 {
		writeError(w, http.StatusBadRequest, CodeValidationFailed, "agents must name exactly two agents, left then right")
		return
	}
	sides := [2]*compareSide{}
	for i, a := range req.Agents {
		sides[i] = &compareSide{agent: strings.TrimSpace(a)}
		if sides[i].agent == "" {
			writeError(w, http.StatusBadRequest, CodeValidationFailed, "agents must not be empty")
			return
		}
	}
	if sides[0].agent == sides[1].agent {
		writeError(w, http.StatusBadRequest, CodeValidationFailed,
			fmt.Sprintf("agents must be two different agents; got %q twice", sides[0].agent))
		return
	}
	want := strings.TrimSpace(req.PermissionMode)
	if want == "" {
		want = string(agent.Restricted)
	}
	if want != string(agent.Restricted) && want != string(agent.FullAuto) {
		writeError(w, http.StatusBadRequest, CodeValidationFailed,
			fmt.Sprintf("permission_mode must be %q or %q", agent.Restricted, agent.FullAuto))
		return
	}
	if s.deps.Chats == nil {
		writeError(w, http.StatusInternalServerError, CodeInternal, "no chat runner is wired")
		return
	}
	ctx := context.WithoutCancel(r.Context())
	for _, side := range sides {
		target := forkTargetOf(src, forkChatRequest{Agent: side.agent})
		if fail := s.checkForkTarget(target); fail != nil {
			fail(w)
			return
		}
		side.mode = want
		if want != string(agent.Restricted) {
			continue
		}
		// A side the agent cannot keep restricted runs full-auto, and says
		// so (decision 7): refusing the whole compare would make it
		// impossible on the very agents worth comparing, and running it
		// "restricted" for one turn only would be a promise later turns
		// break. Any other refusal of the settings refuses the compare.
		model, effort := "", ""
		if target.Agent == "" {
			model, effort = src.Model, src.Effort
		}
		if refusal := s.checkForkSettings(ctx, side.agent, model, effort, want); refusal != nil {
			if refusal.code != CodeAgentCannotRestrictResume {
				refusal.write(w)
				return
			}
			side.mode, side.note = string(agent.FullAuto), refusal.message
		}
	}
	prompt := req.Prompt
	if strings.TrimSpace(prompt) == "" {
		turns, err := s.deps.Store.ListChatTurns(ctx, src.ID)
		if err != nil {
			s.internalError(w, "compare: list turns", err)
			return
		}
		for i := range turns {
			if turns[i].Seq == req.Turn {
				prompt = turns[i].Prompt
			}
		}
		if strings.TrimSpace(prompt) == "" {
			writeError(w, http.StatusNotFound, CodeNotFound, fmt.Sprintf("turn %d of chat %d not found", req.Turn, src.ID))
			return
		}
	}
	if err := s.deps.Chats.CheckCapacity(ctx, 2); err != nil {
		s.writeCompareStartError(w, err)
		return
	}
	for i, side := range sides {
		title := cutTitle(fmt.Sprintf("%s · %s (compare turn %d)", src.Title, side.agent, req.Turn))
		out, fail := s.forkChat(ctx, src, forkChatRequest{
			Turn: req.Turn, Title: title, Agent: side.agent, PermissionMode: side.mode,
		})
		if fail != nil {
			if i == 1 {
				s.rollbackFork(r, sides[0].fork.chat)
			}
			fail(w)
			return
		}
		side.fork = out
	}
	ids := [2]int64{sides[0].fork.chat.ID, sides[1].fork.chat.ID}
	turns, err := s.deps.Chats.SubmitPair(ctx, ids, prompt)
	if err != nil {
		// Only the cap can have moved since the check, and then neither
		// side started: undo both forks rather than leave half a compare.
		if turns[0] == nil {
			s.rollbackFork(r, sides[0].fork.chat)
			s.rollbackFork(r, sides[1].fork.chat)
		}
		s.writeCompareStartError(w, err)
		return
	}
	cmp := &store.ChatCompare{
		ProjectID: src.ProjectID, SourceChatID: src.ID, Seq: req.Turn, Prompt: prompt,
		Left:  store.ChatCompareSide{ChatID: &ids[0], Agent: sides[0].agent, Mode: sides[0].mode, ModeNote: sides[0].note},
		Right: store.ChatCompareSide{ChatID: &ids[1], Agent: sides[1].agent, Mode: sides[1].mode, ModeNote: sides[1].note},
	}
	if err := s.deps.Store.CreateChatCompare(ctx, cmp); err != nil {
		s.internalError(w, fmt.Sprintf("compare: chats %d and %d are running but their pairing was not recorded", ids[0], ids[1]), err)
		return
	}
	sideOut := func(i int) map[string]any {
		f := sides[i].fork
		return map[string]any{
			"chat": renderChat(f.chat), "turn": renderChatTurn(turns[i]),
			"seeded": f.seeded, "skipped": nonNilStrings(f.skipped),
		}
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"compare": renderCompare(cmp), "left": sideOut(0), "right": sideOut(1),
	})
}

// writeCompareStartError answers a compare whose sides could not start.
func (s *Server) writeCompareStartError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, chatrun.ErrChatCapReached):
		writeError(w, http.StatusConflict, CodeChatCapReached,
			err.Error()+"; a compare starts both sides or neither")
	case errors.Is(err, chatrun.ErrTurnLive), errors.Is(err, store.ErrInvalidChatAction):
		writeError(w, http.StatusConflict, CodeChatTurnLive, err.Error())
	default:
		writeError(w, http.StatusInternalServerError, CodeInternal, err.Error())
	}
}

// rollbackFork removes a compare side that never ran: its row, worktree,
// transcripts and refs by the force-delete path, then its branch, which
// still points at the commit it was cut from — a commit its source's history
// holds — so deleting it loses nothing. Best-effort: a failure is logged,
// and what is left is an ordinary idle chat a human can archive.
func (s *Server) rollbackFork(r *http.Request, chat *store.Chat) {
	if _, err := s.forceDeleteChat(r, chat.ID); err != nil {
		s.deps.Logger.Warn("compare: fork left behind", "chat", chat.ID, "error", err)
		return
	}
	if chat.Branch == "" || chat.AdoptedBranch {
		return
	}
	project, err := s.deps.Store.GetProject(r.Context(), chat.ProjectID)
	if err != nil {
		return
	}
	if _, err := gitx.New().Run(r.Context(), project.Path, "branch", "-D", chat.Branch); err != nil {
		s.deps.Logger.Warn("compare: fork branch left behind", "chat", chat.ID, "branch", chat.Branch, "error", err)
	}
}

// handleChatCompareGet serves GET /v1/chats/{id}/compare: the compare the
// chat is a side of, which is how a client finds its other side.
func (s *Server) handleChatCompareGet(w http.ResponseWriter, r *http.Request) {
	chat, ok := s.chatFromPath(w, r)
	if !ok {
		return
	}
	cmp, err := s.deps.Store.GetChatCompareForChat(r.Context(), chat.ID)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, CodeNotFound, fmt.Sprintf("chat %d is not a side of a compare", chat.ID))
		return
	}
	if err != nil {
		s.internalError(w, "compare: read", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"compare": renderCompare(cmp)})
}

// CodeCompareKept refuses keeping a side of a compare that already has a
// kept side (task 171 decision 8): the other one is archived, its branch gone.
const CodeCompareKept = "compare_kept"

// handleChatCompareKeep serves POST /v1/chats/{id}/compare/keep: {id} is the
// side kept, and the other side is archived — its worktree removed even when
// dirty, since discarding that answer is the point — with its branch and
// checkpoint refs deleted (decision 8). Refused while either side has a live
// turn: an answer still being written is not one to choose between.
func (s *Server) handleChatCompareKeep(w http.ResponseWriter, r *http.Request) {
	kept, ok := s.chatFromPath(w, r)
	if !ok {
		return
	}
	ctx := context.WithoutCancel(r.Context())
	cmp, err := s.deps.Store.GetChatCompareForChat(ctx, kept.ID)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, CodeNotFound, fmt.Sprintf("chat %d is not a side of a compare", kept.ID))
		return
	}
	if err != nil {
		s.internalError(w, "compare keep: read", err)
		return
	}
	if cmp.KeptChatID != nil {
		writeError(w, http.StatusConflict, CodeCompareKept,
			fmt.Sprintf("compare %d already kept chat %d", cmp.ID, *cmp.KeptChatID))
		return
	}
	otherID := cmp.Right.ChatID
	if cmp.Side(kept.ID) == "right" {
		otherID = cmp.Left.ChatID
	}
	var other *store.Chat
	if otherID != nil {
		if other, err = s.deps.Store.GetChat(ctx, *otherID); err != nil && !errors.Is(err, store.ErrNotFound) {
			s.internalError(w, "compare keep: read the other side", err)
			return
		}
	}
	for _, c := range []*store.Chat{kept, other} {
		if c == nil {
			continue
		}
		if chatstate.HoldsProcess(c.State) || (s.deps.Chats != nil && s.deps.Chats.Running(c.ID)) {
			writeError(w, http.StatusConflict, CodeChatTurnLive, fmt.Sprintf(
				"chat %d still has a live turn; wait for both answers, or cancel it, before keeping one", c.ID))
			return
		}
	}
	out := map[string]any{"archived": nil, "branch_deleted": false}
	if other != nil {
		archived := other
		if !chatstate.Terminal(other.State) {
			var fail apiFailure
			if archived, fail = s.archiveChat(ctx, other, true); fail != nil {
				fail(w)
				return
			}
		}
		out["archived"] = renderChat(archived)
		out["branch_deleted"] = s.deleteCompareBranch(ctx, archived)
		s.pruneChatRefs(ctx, archived.ProjectID, archived.ID)
	}
	if err := s.deps.Store.SetChatCompareKept(ctx, cmp.ID, kept.ID); err != nil {
		s.internalError(w, "compare keep: record", err)
		return
	}
	if cmp, err = s.deps.Store.GetChatCompare(ctx, cmp.ID); err != nil {
		s.internalError(w, "compare keep: reread", err)
		return
	}
	out["compare"], out["kept"] = renderCompare(cmp), renderChat(kept)
	writeJSON(w, http.StatusOK, out)
}

// deleteCompareBranch deletes the discarded side's branch, and reports
// whether it did. Only a branch vincent cut for the fork goes — never one a
// human named (adopted), a task's or an issue's, which a fork never has but
// which this does not assume. Its commits are the discarded answer; the
// kept side and the source are on other branches.
func (s *Server) deleteCompareBranch(ctx context.Context, chat *store.Chat) bool {
	if chat.Branch == "" || chat.AdoptedBranch || chat.Linked() || chat.OnIssue() {
		return false
	}
	project, err := s.deps.Store.GetProject(ctx, chat.ProjectID)
	if err != nil {
		return false
	}
	if _, err := gitx.New().Run(ctx, project.Path, "branch", "-D", chat.Branch); err != nil {
		s.deps.Logger.Warn("compare keep: branch kept", "chat", chat.ID, "branch", chat.Branch, "error", err)
		return false
	}
	return true
}
