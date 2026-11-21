package chatrun

import (
	"context"

	"github.com/lezli01/vincent/internal/agent"
	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/transcript"
)

// turnPlan is how turn continues the chat's conversation (task 167.4). A
// chat with no rewind or fork behind it resumes its session as it always
// has. One a rewind or fork left a plan on runs it: a pending preamble
// starts a fresh session seeded with it; a resume anchor resumes the session
// at that message, which claude forks (decision A) — or, on a CLI that
// cannot, the turn is seeded instead and says so. It is never faked by
// resuming the whole session.
func (r *Runner) turnPlan(
	ctx context.Context, chat *store.Chat, turn *store.ChatTurn, adapter agent.Adapter, tr *transcript.Writer,
) convPlan {
	switch {
	case chat.PendingPreamble != "":
		r.markSeeded(ctx, turn, tr)
		return convPlan{preamble: chat.PendingPreamble, seeded: true}
	case chat.ResumeAt != "" && chat.SessionID != "":
		if !agent.CanResumeAtPoint(adapter) {
			return r.seedInstead(ctx, chat, turn, tr)
		}
		tr.Note("resume_at", map[string]any{"session_id": chat.SessionID, "resume_at": chat.ResumeAt})
		return convPlan{sessionID: chat.SessionID, resumeAt: chat.ResumeAt}
	}
	return convPlan{sessionID: chat.SessionID}
}

// seedInstead is the seeded plan for a turn whose resume anchor the CLI
// cannot honour: a fresh session with the turns the rewind kept (decision 1).
// A failure to read them seeds nothing rather than resuming the whole
// session — the turn still never sees what the rewind removed.
func (r *Runner) seedInstead(ctx context.Context, chat *store.Chat, turn *store.ChatTurn, tr *transcript.Writer) convPlan {
	turns, err := r.deps.Store.ListChatTurns(ctx, chat.ID)
	if err != nil {
		r.deps.Logger.Warn("seed chat turn: list turns", "chat", chat.ID, "error", err)
	}
	var kept []store.ChatTurn
	for _, t := range turns {
		if t.Seq < turn.Seq && t.RewoundAt == nil {
			kept = append(kept, t)
		}
	}
	r.markSeeded(ctx, turn, tr)
	if len(kept) == 0 {
		return convPlan{seeded: true}
	}
	return convPlan{preamble: r.seededPreamble(chat, kept, ""), seeded: true}
}

// markSeeded records on the row and in the transcript that turn starts a
// seeded session, so every surface can say "seeded, not resumed".
func (r *Runner) markSeeded(ctx context.Context, turn *store.ChatTurn, tr *transcript.Writer) {
	turn.Seeded = true
	tr.Note("seeded", map[string]any{"resumed": false})
	if err := r.deps.Store.UpdateChatTurn(ctx, turn); err != nil {
		r.deps.Logger.Warn("chat turn seeded flag not recorded", "chat", turn.ChatID, "turn", turn.ID, "error", err)
	}
}

// openingPreamble is a linked or issue chat's opening context when turn is
// the first its session sees: turn 1, as always (task 119), or the first
// turn after a rewind to turn 1 cleared the session — a fresh session that
// would otherwise never be told what it was opened on. A seeded plan carries
// the opening context itself, so this adds it only where no earlier turn is
// kept.
func (r *Runner) openingPreamble(ctx context.Context, chat *store.Chat, turn *store.ChatTurn) string {
	if opening := turnPreamble(chat, turn); opening != "" || chat.OpeningContext == "" {
		return opening
	}
	if chat.SessionID != "" || chat.ResumeAt != "" || chat.PendingPreamble != "" {
		return ""
	}
	turns, err := r.deps.Store.ListChatTurns(ctx, chat.ID)
	if err != nil {
		return ""
	}
	for _, t := range turns {
		if t.Seq < turn.Seq && t.RewoundAt == nil {
			return ""
		}
	}
	return chat.OpeningContext
}

// consumePlan clears a rewind's or a fork's plan once a turn has reported
// the session it ran — the forked or seeded one, which is the chat's session
// from now on (decision 10). A turn that fails before reporting one leaves
// the plan for the next attempt.
func (r *Runner) consumePlan(chat *store.Chat, sessionID string) {
	if chat.ResumeAt == "" && chat.PendingPreamble == "" {
		return
	}
	if _, err := r.deps.Store.SetChatResumePlan(r.persist, chat.ID, sessionID, "", ""); err != nil {
		r.deps.Logger.Error("clear chat resume plan", "chat", chat.ID, "error", err)
		return
	}
	chat.SessionID, chat.ResumeAt, chat.PendingPreamble = sessionID, "", ""
}
