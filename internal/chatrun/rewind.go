package chatrun

// Rewinding a chat to a turn's checkpoint, and forking one from it (task 167
// 167.3-167.6, spec §5.5). Everything here runs git on the chat's worktree
// and writes rows; none of it starts an agent process. The conversation half
// of a rewind is a *plan* the chat's next turn carries out (decision A).

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/lezli01/vincent/internal/agent"
	"github.com/lezli01/vincent/internal/chatstate"
	"github.com/lezli01/vincent/internal/gitx"
	"github.com/lezli01/vincent/internal/store"
)

// RewindMode is what a rewind moves: the worktree, the conversation, or both
// (task 167, spec §5.5).
type RewindMode string

// The rewind modes.
const (
	RewindCode         RewindMode = "code"
	RewindConversation RewindMode = "conversation"
	RewindBoth         RewindMode = "both"
)

// The reasons a rewind or a fork is refused. Each is a 409 at the API, and
// each is the string RewindOptions shows a picker beside a disabled option.
// A live turn is not among them: it is ErrTurnLive, which the API already
// maps to chat_turn_live, and RewindOptions spells it reasonTurnLive.
const (
	// ReasonCheckpointUnavailable is a turn with no checkpoint to go back
	// to: one from before migration 0066, one whose checkpoint failed, or
	// one whose ref no longer resolves (decision E).
	ReasonCheckpointUnavailable = "chat_checkpoint_unavailable"
	// ReasonRewindForeignCommits is a code rewind over a branch something
	// other than this chat's turns moved (decision 3). There is no force.
	ReasonRewindForeignCommits = "chat_rewind_foreign_commits"
	// ReasonTurnRewound is a target an earlier rewind already took out of
	// the conversation.
	ReasonTurnRewound = "chat_turn_rewound"
	// ReasonChatTerminal is a chat that has ended (decision 8).
	ReasonChatTerminal = "chat_terminal"
	// ReasonRewindUnsavedFiles is a code rewind that would overwrite a file
	// the pre-rewind save could not read (decision D, review F2): one the
	// branch tracks, now or at the target, which the restore's reset or
	// checkout rewrites whatever its clean spares. Making the file readable
	// lets the save take it, and the rewind go ahead.
	ReasonRewindUnsavedFiles = "chat_rewind_unsaved_files"
)

// reasonTurnLive is ErrTurnLive as RewindOptions spells it: the API's code
// for the same refusal.
const reasonTurnLive = "chat_turn_live"

// RewindRefusal is a rewind or fork refused for one of the Reason* above.
type RewindRefusal struct {
	Reason  string
	Message string
}

func (e *RewindRefusal) Error() string { return e.Message }

func refuse(reason, format string, args ...any) *RewindRefusal {
	return &RewindRefusal{Reason: reason, Message: fmt.Sprintf(format, args...)}
}

// RewindRequest names a rewind's target: exactly one of Turn, a turn's seq,
// or Rewound, the k of a rewound-<k> ref to go forward to (code only). Mode
// "" is both for a turn and code for a forward rewind.
type RewindRequest struct {
	Turn    int
	Rewound int
	Mode    RewindMode
}

// RewindResult is what a rewind did. Ref is the rewound-<k> ref that saved
// the state it replaced ("" for a conversation-only rewind); Skipped are the
// paths that state could not save and the restore therefore left on disk
// (decision D); Seeded says the next turn starts a seeded session rather
// than resuming; RewoundSeqs are the turns this rewind took out of the
// conversation.
type RewindResult struct {
	Chat        *store.Chat
	RewindID    string
	Ref         string
	Skipped     []string
	Seeded      bool
	RewoundSeqs []int
}

// ErrBadRewindRequest is a request that names no target, both targets, or a
// mode a forward rewind cannot take. The API validates first; this is the
// engine's own refusal of the same thing.
var ErrBadRewindRequest = errors.New("invalid rewind request")

// Rewind rewinds chat chatID (task 167.3, 167.5). Every check runs before
// anything moves, so a refused rewind changes nothing and a "both" rewind
// never half-happens. It runs under the chat's hold (WhileNoTurn), so no
// turn or shell can start while it edits the worktree.
func (r *Runner) Rewind(ctx context.Context, chatID int64, req RewindRequest) (RewindResult, error) {
	mode, err := normalizeRewind(req)
	if err != nil {
		return RewindResult{}, err
	}
	var res RewindResult
	err = r.WhileNoTurn(ctx, chatID, func() error {
		var err error
		res, err = r.rewindHeld(ctx, chatID, req, mode)
		return err
	})
	return res, err
}

func normalizeRewind(req RewindRequest) (RewindMode, error) {
	switch {
	case (req.Turn > 0) == (req.Rewound > 0):
		return "", fmt.Errorf("%w: name exactly one of a turn or a rewound state", ErrBadRewindRequest)
	case req.Rewound > 0:
		if req.Mode != "" && req.Mode != RewindCode {
			return "", fmt.Errorf("%w: going forward to a rewound state moves code only", ErrBadRewindRequest)
		}
		return RewindCode, nil
	}
	switch req.Mode {
	case "":
		return RewindBoth, nil
	case RewindCode, RewindConversation, RewindBoth:
		return req.Mode, nil
	}
	return "", fmt.Errorf("%w: unknown mode %q", ErrBadRewindRequest, req.Mode)
}

// rewindHeld is Rewind with the hold taken.
func (r *Runner) rewindHeld(ctx context.Context, chatID int64, req RewindRequest, mode RewindMode) (RewindResult, error) {
	chat, err := r.deps.Store.GetChat(ctx, chatID)
	if err != nil {
		return RewindResult{}, err
	}
	if err := liveGuard(chat); err != nil {
		return RewindResult{}, err
	}
	turns, err := r.deps.Store.ListChatTurns(ctx, chatID)
	if err != nil {
		return RewindResult{}, err
	}
	git := gitx.New()
	var workDir string
	var refs refState
	if mode != RewindConversation {
		if workDir, err = r.Workspace(ctx, chat); err != nil {
			return RewindResult{}, err
		}
		if refs, err = r.readRefs(ctx, git, workDir, chatID); err != nil {
			return RewindResult{}, err
		}
	}

	// Forward: code only, back to a state an earlier rewind saved.
	if req.Rewound > 0 {
		sha, ok := refs.rewound[req.Rewound]
		if !ok {
			return RewindResult{}, fmt.Errorf("rewound state %d of chat %d: %w", req.Rewound, chatID, store.ErrNotFound)
		}
		if refs.chainFrom(turns, 0) != "" {
			return RewindResult{}, foreignRefusal()
		}
		ref, skipped, err := r.rewindCode(ctx, git, chat, workDir, refs, sha)
		if err != nil {
			return RewindResult{}, err
		}
		out, err := r.publishCodeRewind(ctx, chat)
		return RewindResult{Chat: out, Ref: ref, Skipped: skipped}, err
	}

	target := findTurn(turns, req.Turn)
	if target == nil {
		return RewindResult{}, fmt.Errorf("turn %d of chat %d: %w", req.Turn, chatID, store.ErrNotFound)
	}
	if target.RewoundAt != nil {
		return RewindResult{}, refuse(ReasonTurnRewound, "turn %d was already rewound", target.Seq)
	}
	if !hasCheckpoint(target) {
		return RewindResult{}, refuse(ReasonCheckpointUnavailable, "turn %d has no checkpoint to rewind to", target.Seq)
	}
	if mode != RewindConversation {
		if refs.turns[target.Seq] != target.CheckpointSHA {
			return RewindResult{}, refuse(ReasonCheckpointUnavailable,
				"turn %d's checkpoint ref no longer resolves to its commit", target.Seq)
		}
		if refs.chainFrom(turns, target.Seq) != "" {
			return RewindResult{}, foreignRefusal()
		}
	} else if ok, err := r.checkpointExists(ctx, chat, target); err != nil {
		return RewindResult{}, err
	} else if !ok {
		return RewindResult{}, refuse(ReasonCheckpointUnavailable,
			"turn %d's checkpoint commit no longer exists", target.Seq)
	}
	// The conversation plan is computed before the code moves, so a
	// failure to build it leaves the worktree alone too.
	var plan convPlan
	if mode != RewindCode {
		plan = r.planConversation(chat, turns, target.Seq)
	}

	res := RewindResult{}
	if mode != RewindConversation {
		res.Ref, res.Skipped, err = r.rewindCode(ctx, git, chat, workDir, refs, target.CheckpointSHA)
		if err != nil {
			return RewindResult{}, err
		}
	}
	if mode == RewindCode {
		res.Chat, err = r.publishCodeRewind(ctx, chat)
		return res, err
	}
	res.RewindID = newRewindID()
	res.Seeded = plan.seeded
	for _, t := range turns {
		if t.Seq >= target.Seq && t.RewoundAt == nil {
			res.RewoundSeqs = append(res.RewoundSeqs, t.Seq)
		}
	}
	res.Chat, err = r.deps.Store.RewindChatConversation(ctx, chat.ID, store.ConversationRewind{
		FromSeq: target.Seq, RewindID: res.RewindID,
		SessionID: plan.sessionID, ResumeAt: plan.resumeAt, Preamble: plan.preamble,
		Draft: rewoundDraft(target.Prompt, chat.Draft),
	})
	if err != nil {
		return RewindResult{}, err
	}
	return res, nil
}

func unsavedRefusal(paths []string) *RewindRefusal {
	return refuse(ReasonRewindUnsavedFiles,
		"a code rewind would overwrite files vincent cannot read and so could not save: %s; "+
			"make them readable and rewind again", strings.Join(paths, ", "))
}

func foreignRefusal() *RewindRefusal {
	return refuse(ReasonRewindForeignCommits,
		"the branch has commits this chat's turns did not make; a code rewind would discard them")
}

// liveGuard is decision 8's guard beyond what WhileNoTurn already refused: a
// terminal chat, and a queue that is about to drain.
func liveGuard(chat *store.Chat) error {
	if chatstate.Terminal(chat.State) {
		return refuse(ReasonChatTerminal, "chat %d has ended (%s)", chat.ID, chat.State)
	}
	if chatstate.HoldsProcess(chat.State) || (chat.QueuedCount > 0 && !chat.QueuePaused) {
		return ErrTurnLive
	}
	return nil
}

// publishCodeRewind announces a rewind that wrote no row (task 167.3): the
// worktree changed under the chat, and a client showing its diff or status
// must hear about it. Rewriting the title it already has is the store's one
// write that publishes chat.updated and nothing else.
func (r *Runner) publishCodeRewind(ctx context.Context, chat *store.Chat) (*store.Chat, error) {
	return r.deps.Store.SetChatTitle(ctx, chat.ID, chat.Title)
}

// rewindCode saves the worktree's current state as the next rewound-<k> ref
// — its HEAD as the snapshot's parent, its dirty state as the tree, and in
// the message where it went, for the chain check — then restores to the
// snapshot commit target. The files the save could not read are kept out of
// the restore's clean (decision D) and logged.
func (r *Runner) rewindCode(
	ctx context.Context, git *gitx.Git, chat *store.Chat, workDir string, refs refState, target string,
) (ref string, skipped []string, err error) {
	gctx, cancel := context.WithTimeout(ctx, gitx.WorktreeTimeout)
	defer cancel()
	to, err := git.Run(gctx, workDir, "rev-parse", "--verify", target+"^1")
	if err != nil {
		return "", nil, fmt.Errorf("rewind: resolve target head: %w", err)
	}
	k := refs.nextRewound()
	snap, err := git.Snapshot(gctx, workDir, r.checkpointScratch(),
		rewindMessage(chat.ID, k, refs.head, to))
	if err != nil {
		return "", nil, fmt.Errorf("rewind: save current state: %w", err)
	}
	// Refused before the ref is written, so a refused rewind leaves no
	// rewound-<k> state behind (review F2).
	over, err := git.RestoreOverwrites(gctx, workDir, target, snap.Skipped)
	if err != nil {
		return "", nil, fmt.Errorf("rewind: %w", err)
	}
	if len(over) > 0 {
		return "", nil, unsavedRefusal(over)
	}
	ref = refs.names.Rewound(chat.ID, k)
	if err := git.UpdateRef(gctx, workDir, ref, snap.Commit); err != nil {
		return "", nil, fmt.Errorf("rewind: write %s: %w", ref, err)
	}
	if len(snap.Skipped) > 0 {
		r.deps.Logger.Warn("chat rewind kept files it could not save",
			"chat", chat.ID, "ref", ref, "paths", snap.Skipped)
	}
	if err := git.Restore(gctx, workDir, target, snap.Skipped); err != nil {
		return "", nil, fmt.Errorf("rewind: %w", err)
	}
	r.invalidateSkills(workDir)
	r.deps.Logger.Info("chat rewound", "chat", chat.ID, "from", refs.head, "to", to, "saved", ref)
	return ref, snap.Skipped, nil
}

// rewindSubject is the subject line of a rewound-<k> snapshot: the HEAD the
// rewind moved from and the one it moved to. The chain check reads it back
// (refState.moves), so a break in the turn chain that vincent's own rewind
// made is never mistaken for a foreign commit.
const rewindSubject = "vincent: chat %d rewound-%d from %s to %s"

func rewindMessage(chatID int64, k int, from, to string) string {
	return fmt.Sprintf(rewindSubject, chatID, k, from, to)
}

// parseRewindSubject reads rewindSubject back, reporting false for any
// other subject.
func parseRewindSubject(subject string) (from, to string, ok bool) {
	f := strings.Fields(subject)
	// vincent: chat <id> rewound-<k> from <sha> to <sha>
	if len(f) != 8 || f[0] != "vincent:" || f[1] != "chat" || f[4] != "from" || f[6] != "to" {
		return "", "", false
	}
	return f[5], f[7], true
}

// refState is one read of a chat's refs and its worktree's HEAD: what every
// check below works from, gathered with one rev-parse and one for-each-ref.
type refState struct {
	names   ChatRefs
	head    string
	turns   map[int]string // seq -> turn-<seq> ref's commit
	rewound map[int]string // k -> rewound-<k> ref's commit
	// moves are the HEAD moves vincent's own code rewinds made, from -> to.
	moves map[string][]string
}

func (r *Runner) readRefs(ctx context.Context, git *gitx.Git, workDir string, chatID int64) (refState, error) {
	names, err := r.Refs(ctx)
	if err != nil {
		return refState{}, fmt.Errorf("rewind: %w", err)
	}
	qctx, cancel := context.WithTimeout(ctx, gitx.QueryTimeout)
	defer cancel()
	st := refState{names: names, turns: map[int]string{}, rewound: map[int]string{}, moves: map[string][]string{}}
	head, err := git.Run(qctx, workDir, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return refState{}, fmt.Errorf("rewind: read HEAD: %w", err)
	}
	st.head = head
	prefix := names.Prefix(chatID)
	out, err := git.Run(qctx, workDir, "for-each-ref",
		"--format=%(refname)%09%(objectname)%09%(contents:subject)", prefix)
	if err != nil {
		return refState{}, fmt.Errorf("rewind: list refs: %w", err)
	}
	for _, line := range strings.Split(out, "\n") {
		parts := strings.SplitN(strings.TrimRight(line, "\r"), "\t", 3)
		if len(parts) < 2 {
			continue
		}
		name := strings.TrimPrefix(parts[0], prefix)
		if n, ok := refNumber(name, "turn-"); ok {
			st.turns[n] = parts[1]
		}
		if n, ok := refNumber(name, "rewound-"); ok {
			st.rewound[n] = parts[1]
			if len(parts) == 3 {
				if from, to, ok := parseRewindSubject(parts[2]); ok {
					st.moves[from] = append(st.moves[from], to)
				}
			}
		}
	}
	return st, nil
}

func refNumber(name, prefix string) (int, bool) {
	rest, ok := strings.CutPrefix(name, prefix)
	if !ok {
		return 0, false
	}
	n, err := strconv.Atoi(rest)
	return n, err == nil && n > 0
}

func (st refState) nextRewound() int {
	k := 0
	for n := range st.rewound {
		k = max(k, n)
	}
	return k + 1
}

// explained reports whether HEAD going from a to b is vincent's own doing:
// equal, or reachable through the moves its code rewinds recorded.
func (st refState) explained(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	seen := map[string]bool{a: true}
	queue := []string{a}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		if cur == b {
			return true
		}
		for _, next := range st.moves[cur] {
			if !seen[next] {
				seen[next] = true
				queue = append(queue, next)
			}
		}
	}
	return false
}

// chainFrom is decision 3's check for a code rewind to turn seq, or, with
// seq 0, for a forward rewind: over the turns from seq on that ran on a
// checkpoint, each turn's head_before must follow the previous one's
// head_after, and the worktree's HEAD the last one's — a link vincent's own
// rewind broke counting as followed. A forward rewind checks only the last
// link, against what vincent last left. It returns "" when the chain holds,
// else ReasonRewindForeignCommits.
//
// Rewound turns are in the chain (review F1): a rewind takes a turn out of
// the conversation, not out of the branch's history. A conversation-only
// rewind leaves the commits those turns made where they are, and only the
// rewound turns' own head_before/head_after explain them; skipping the turns
// would read this chat's own commits as foreign. Only the *target* must not
// be rewound, which Rewind checks before it gets here.
func (st refState) chainFrom(turns []store.ChatTurn, seq int) string {
	var chain []store.ChatTurn
	for _, t := range turns {
		if t.HeadBefore != "" {
			chain = append(chain, t)
		}
	}
	if seq == 0 {
		if len(chain) == 0 {
			return ""
		}
		chain = chain[len(chain)-1:]
	} else {
		i := slices.IndexFunc(chain, func(t store.ChatTurn) bool { return t.Seq >= seq })
		if i < 0 {
			return ReasonRewindForeignCommits
		}
		chain = chain[i:]
	}
	for i := range chain {
		if chain[i].HeadAfter == "" {
			// Nothing recorded where the turn left HEAD: cannot prove.
			return ReasonRewindForeignCommits
		}
		next := st.head
		if i+1 < len(chain) {
			next = chain[i+1].HeadBefore
		}
		if !st.explained(chain[i].HeadAfter, next) {
			return ReasonRewindForeignCommits
		}
	}
	return ""
}

// hasCheckpoint is decision E's row half: the turn recorded a checkpoint and
// it did not fail.
func hasCheckpoint(t *store.ChatTurn) bool {
	return t.CheckpointSHA != "" && t.HeadBefore != "" && t.FailReason != ReasonCheckpointFailed
}

// checkpointExists is decision E's git half for a rewind that does not move
// code, and a fork: the checkpoint commit is still in the project's object
// store. It reads through the project repository, which a free chat's
// archived worktree no longer offers.
func (r *Runner) checkpointExists(ctx context.Context, chat *store.Chat, t *store.ChatTurn) (bool, error) {
	project, err := r.deps.Store.GetProject(ctx, chat.ProjectID)
	if err != nil {
		return false, err
	}
	names, err := r.Refs(ctx)
	if err != nil {
		return false, err
	}
	qctx, cancel := context.WithTimeout(ctx, gitx.QueryTimeout)
	defer cancel()
	sha, err := gitx.New().Run(qctx, project.Path, "rev-parse", "--verify", "-q", names.Turn(chat.ID, t.Seq))
	return err == nil && sha == t.CheckpointSHA, nil
}

func findTurn(turns []store.ChatTurn, seq int) *store.ChatTurn {
	for i := range turns {
		if turns[i].Seq == seq {
			return &turns[i]
		}
	}
	return nil
}

// rewoundDraft is decision F's draft: the rewound prompt back in the
// composer, ahead of whatever the human had already typed. A combination
// over the draft bound keeps the prompt alone rather than failing the
// rewind; the typed text is the human's to retype, the prompt is not.
func rewoundDraft(prompt, draft string) string {
	if draft == "" {
		return prompt
	}
	joined := prompt + "\n\n" + draft
	if len(joined) > store.MaxChatDraftBytes {
		return prompt
	}
	return joined
}

func newRewindID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// convPlan is the plan a chat's next turn runs on after a conversation
// rewind or a fork (decisions 1, A, G): resume sessionID at resumeAt, or
// start fresh with preamble, which is seeded context when seeded is true.
type convPlan struct {
	sessionID, resumeAt, preamble string
	seeded                        bool
}

// planConversation is the plan for a conversation that keeps chat's turns
// before seq. claude with the capability resumes the last kept turn's
// session at that turn's anchor, natively; a conversation that keeps no
// turn starts fresh; everything else is seeded with the kept turns.
func (r *Runner) planConversation(chat *store.Chat, turns []store.ChatTurn, seq int) convPlan {
	return r.planConversationWith(chat, turns, seq, r.resumeAtProbe(chat))
}

// resumeAtProbe is chat's agent's resume-at-point capability, asked at most
// once and only when called (review F7). claude answers it with a probe that
// may take up to its help timeout when the CLI hangs, and does not cache a
// probe that timed out; a caller planning many turns — RewindOptions, once
// per turn — shares one answer instead of paying that per turn.
func (r *Runner) resumeAtProbe(chat *store.Chat) func() bool {
	var done, ok bool
	return func() bool {
		if !done {
			done = true
			a, found := r.deps.Agents.Get(chat.Agent)
			ok = found && agent.CanResumeAtPoint(a)
		}
		return ok
	}
}

// planConversationWith is planConversation with the capability supplied, so
// a caller can share one probe across turns. The capability is asked only
// for a kept turn that has an anchor to resume at: without one the answer
// cannot change the plan.
func (r *Runner) planConversationWith(
	chat *store.Chat, turns []store.ChatTurn, seq int, canResumeAt func() bool,
) convPlan {
	var kept []store.ChatTurn
	for _, t := range turns {
		if t.Seq < seq && t.RewoundAt == nil {
			kept = append(kept, t)
		}
	}
	if len(kept) == 0 {
		return convPlan{}
	}
	prev := kept[len(kept)-1]
	if prev.ResumeAnchor != "" && prev.SessionID != "" && canResumeAt() {
		return convPlan{sessionID: prev.SessionID, resumeAt: prev.ResumeAnchor}
	}
	return convPlan{preamble: seededPreamble(chat, kept), seeded: true}
}

// The seeded context's bound (decision G): a linked chat's opening context
// bounds what it inlines from a transcript by internal/taskrun's
// repairTranscriptLines and repairTranscriptTailBytes, and the seeded
// history is held to the same two numbers rather than a budget of its own.
// They are repeated here because chatrun never imports taskrun; a change to
// one is a change to both.
const (
	seededLines = 200
	seededBytes = 512 << 10
)

// seededIntro opens a seeded session's context and says what it is: a
// replay of earlier turns, not a resumed session (decision 1).
const seededIntro = "This conversation was rewound and continues in a new session. " +
	"It was seeded, not resumed: below are the earlier turns it keeps, each the human's " +
	"message and your final reply. Tool calls and intermediate output from those turns " +
	"are not included.\n\n"

// seededPreamble is the seeded context for a fresh session that keeps
// turns: the chat's own opening context first, if it has one (a fresh
// session has never seen it), then the turns, each prompt and result. The
// history keeps its most recent seededLines lines within seededBytes.
func seededPreamble(chat *store.Chat, kept []store.ChatTurn) string {
	var hist strings.Builder
	for _, t := range kept {
		fmt.Fprintf(&hist, "<turn seq=%q>\n<human>\n%s\n</human>\n<reply>\n%s\n</reply>\n</turn>\n",
			strconv.Itoa(t.Seq), strings.TrimRight(t.Prompt, "\n"), strings.TrimRight(t.ResultText, "\n"))
	}
	body := hist.String()
	if tail, cut := tailLinesOf(body, seededLines, seededBytes); cut {
		body = fmt.Sprintf("(earlier turns omitted; the last %d lines follow)\n", seededLines) + tail
	}
	return joinPreamble(chat.OpeningContext, "<earlier-turns>\n"+seededIntro+body+"</earlier-turns>\n")
}

// tailLinesOf is s's last n lines within at most maxBytes, cut on a line
// boundary, and whether anything was cut.
func tailLinesOf(s string, n, maxBytes int) (string, bool) {
	cut := false
	if len(s) > maxBytes {
		s = s[len(s)-maxBytes:]
		if i := strings.IndexByte(s, '\n'); i >= 0 {
			s = s[i+1:]
		}
		cut = true
	}
	lines := strings.SplitAfter(s, "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
		cut = true
	}
	return strings.Join(lines, ""), cut
}
