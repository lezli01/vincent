package chatrun

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/lezli01/vincent/internal/gitx"
	"github.com/lezli01/vincent/internal/store"
)

// ForkPlan is everything a new chat forked from Source's turn Seq starts
// from (task 167.6, decision 11): the commit its branch is cut from
// (HeadBefore), the checkpoint whose dirty state is laid on top, the
// source's agent and settings, and the conversation plan for its first
// turn — resume SessionID at ResumeAt (claude, which forks the session on
// that turn), or a fresh session seeded with Preamble.
type ForkPlan struct {
	Source         *store.Chat
	Seq            int
	HeadBefore     string
	CheckpointSHA  string
	Agent          string
	Model          string
	Effort         string
	PermissionMode string
	SessionID      string
	ResumeAt       string
	Preamble       string
	Seeded         bool
}

// PlanFork plans a fork of chat chatID from turn seq. It refuses what Rewind
// refuses — a live turn, an ended chat, a rewound turn, a turn without a
// checkpoint — except the chain check: a fork leaves the source's branch
// alone, so foreign commits on it are no reason to refuse. It reads only.
func (r *Runner) PlanFork(ctx context.Context, chatID int64, seq int) (ForkPlan, error) {
	var plan ForkPlan
	err := r.WhileNoTurn(ctx, chatID, func() error {
		chat, err := r.deps.Store.GetChat(ctx, chatID)
		if err != nil {
			return err
		}
		if err := liveGuard(chat); err != nil {
			return err
		}
		turns, err := r.deps.Store.ListChatTurns(ctx, chatID)
		if err != nil {
			return err
		}
		target := findTurn(turns, seq)
		if target == nil {
			return fmt.Errorf("turn %d of chat %d: %w", seq, chatID, store.ErrNotFound)
		}
		if target.RewoundAt != nil {
			return refuse(ReasonTurnRewound, "turn %d was rewound", seq)
		}
		if !hasCheckpoint(target) {
			return refuse(ReasonCheckpointUnavailable, "turn %d has no checkpoint to fork from", seq)
		}
		ok, err := r.checkpointExists(ctx, chat, target)
		if err != nil {
			return err
		}
		if !ok {
			return refuse(ReasonCheckpointUnavailable, "turn %d's checkpoint commit no longer exists", seq)
		}
		conv := r.planConversation(chat, turns, seq)
		plan = ForkPlan{
			Source: chat, Seq: seq, HeadBefore: target.HeadBefore, CheckpointSHA: target.CheckpointSHA,
			Agent: chat.Agent, Model: chat.Model, Effort: chat.Effort, PermissionMode: chat.PermissionMode,
			SessionID: conv.sessionID, ResumeAt: conv.resumeAt, Preamble: conv.preamble, Seeded: conv.seeded,
		}
		return nil
	})
	return plan, err
}

// ApplyForkCheckpoint lays plan's checkpoint onto workDir, a fresh worktree
// whose HEAD is already plan.HeadBefore: the snapshot's differences from
// that commit land as unstaged changes, as a rewind's do (decision 6). The
// worktree is new, so the state it replaces is nothing worth a rewound ref;
// skipped names the paths a pre-restore capture could not read, which the
// restore leaves alone all the same (decision D).
//
// chatID is the fork's own chat, and the restore runs under its hold
// (WhileNoTurn), as a rewind does (review F6): the chat is created and
// announced before its checkpoint is laid down, and a send in that window —
// another client, or an MCP agent that saw chat.created — would otherwise
// checkpoint and start an agent in a worktree being reset under it. A turn
// that got in first refuses the restore with ErrTurnLive.
func (r *Runner) ApplyForkCheckpoint(ctx context.Context, chatID int64, workDir string, plan ForkPlan) (skipped []string, err error) {
	err = r.WhileNoTurn(ctx, chatID, func() error {
		var err error
		skipped, err = r.applyForkCheckpoint(ctx, workDir, plan)
		return err
	})
	return skipped, err
}

func (r *Runner) applyForkCheckpoint(ctx context.Context, workDir string, plan ForkPlan) (skipped []string, err error) {
	git := gitx.New()
	gctx, cancel := context.WithTimeout(ctx, gitx.WorktreeTimeout)
	defer cancel()
	head, err := git.Run(gctx, workDir, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return nil, fmt.Errorf("fork: read HEAD: %w", err)
	}
	if head != plan.HeadBefore {
		return nil, fmt.Errorf("fork: worktree HEAD %s is not the checkpoint's %s", head, plan.HeadBefore)
	}
	pre, err := git.Snapshot(gctx, workDir, r.checkpointScratch(), "vincent: fork pre-restore")
	if err != nil {
		return nil, fmt.Errorf("fork: %w", err)
	}
	if err := git.Restore(gctx, workDir, plan.CheckpointSHA, pre.Skipped); err != nil {
		return nil, fmt.Errorf("fork: %w", err)
	}
	return pre.Skipped, nil
}

// TurnOptions is what a turn picker may offer for one turn: each field ""
// when that action is allowed, else the reason it is not. ConversationSeeded
// says a conversation rewind to the turn would be seeded, not resumed.
type TurnOptions struct {
	Code               string
	Conversation       string
	Fork               string
	ConversationSeeded bool
}

// RewindOptions answers TurnOptions for each of turns, keyed by seq (task
// 167.7's picker). It runs nothing but one rev-parse and one for-each-ref:
// a picker is opened often and must not cost a snapshot.
func (r *Runner) RewindOptions(ctx context.Context, chat *store.Chat, turns []store.ChatTurn) map[int]TurnOptions {
	out := make(map[int]TurnOptions, len(turns))
	all := func(reason string) map[int]TurnOptions {
		for _, t := range turns {
			out[t.Seq] = TurnOptions{Code: reason, Conversation: reason, Fork: reason}
		}
		return out
	}
	if err := liveGuard(chat); err != nil {
		if rr := (*RewindRefusal)(nil); errors.As(err, &rr) {
			return all(rr.Reason)
		}
		return all(reasonTurnLive)
	}
	if r.Running(chat.ID) || r.accepted(chat.ID) {
		return all(reasonTurnLive)
	}
	var refs refState
	refsOK := false
	if workDir, err := r.Workspace(ctx, chat); err == nil {
		if refs, err = r.readRefs(ctx, gitx.New(), workDir, chat.ID); err == nil {
			refsOK = true
		}
	}
	canResumeAt := r.resumeAtProbe(chat)
	for i := range turns {
		t := &turns[i]
		var o TurnOptions
		switch {
		case t.RewoundAt != nil:
			o = TurnOptions{Code: ReasonTurnRewound, Conversation: ReasonTurnRewound, Fork: ReasonTurnRewound}
		case !hasCheckpoint(t) || !refsOK || refs.turns[t.Seq] != t.CheckpointSHA:
			o = TurnOptions{
				Code: ReasonCheckpointUnavailable, Conversation: ReasonCheckpointUnavailable,
				Fork: ReasonCheckpointUnavailable,
			}
		default:
			o.Code = refs.chainFrom(turns, t.Seq)
			o.ConversationSeeded = r.planConversationWith(chat, turns, t.Seq, canResumeAt).seeded
		}
		out[t.Seq] = o
	}
	return out
}

// accepted reports whether the chat has an accepted turn: from Send until
// its actor goroutine returns, a draining queue included.
func (r *Runner) accepted(chatID int64) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.turns[chatID]
	return ok
}

// RewoundState is one state a code rewind saved, as a forward rewind's
// target: K and Ref name it, SHA is its snapshot commit, and Blocked is ""
// when going forward to it is allowed, else the reason it is not.
type RewoundState struct {
	K       int
	Ref     string
	SHA     string
	Blocked string
}

// RewoundStates lists chat's rewound-<k> refs, lowest k first.
func (r *Runner) RewoundStates(ctx context.Context, chat *store.Chat) ([]RewoundState, error) {
	workDir, err := r.Workspace(ctx, chat)
	if err != nil {
		return nil, err
	}
	if workDir == "" {
		return nil, nil
	}
	refs, err := r.readRefs(ctx, gitx.New(), workDir, chat.ID)
	if err != nil {
		return nil, err
	}
	blocked := ""
	if err := liveGuard(chat); err != nil {
		blocked = reasonTurnLive
		if rr := (*RewindRefusal)(nil); errors.As(err, &rr) {
			blocked = rr.Reason
		}
	} else if r.accepted(chat.ID) {
		blocked = reasonTurnLive
	}
	if blocked == "" && len(refs.rewound) > 0 {
		turns, err := r.deps.Store.ListChatTurns(ctx, chat.ID)
		if err != nil {
			return nil, err
		}
		blocked = refs.chainFrom(turns, 0)
	}
	ks := make([]int, 0, len(refs.rewound))
	for k := range refs.rewound {
		ks = append(ks, k)
	}
	slices.Sort(ks)
	out := make([]RewoundState, 0, len(ks))
	for _, k := range ks {
		out = append(out, RewoundState{K: k, Ref: refs.names.Rewound(chat.ID, k), SHA: refs.rewound[k], Blocked: blocked})
	}
	return out, nil
}
