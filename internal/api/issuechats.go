package api

// A chat opened on an issue (task 140, spec §5.5, §5.6, §13.2): it holds the
// issue's main worktree the way a main-role task does.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/lezli01/vincent/internal/agent"
	"github.com/lezli01/vincent/internal/chatstate"
	"github.com/lezli01/vincent/internal/issuestate"
	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/worktree"
)

// CodeIssueWorktreeOccupied is an issue chat refused because a main task, or
// another chat, already holds the issue's main worktree (task 140 decision
// 2). `details` names the holder: `task_id` or `chat_id`.
const CodeIssueWorktreeOccupied = "issue_worktree_occupied"

// CodeIssueHasLiveChat is an issue chat refused because another chat is
// already open on the issue, or an issue delete refused while one is (task
// 140 decisions 2 and 10). `details.chat_id` names it.
const CodeIssueHasLiveChat = "issue_has_live_chat"

// CodeIssueChatBranch is a branch delete refused on an issue chat (task 140
// decision 4): its branch is the issue's main branch, which the chat does not
// own to delete.
const CodeIssueChatBranch = "issue_chat_branch"

// Bounds on an issue chat's opening context (task 140 decision 6), the way a
// linked chat's failure block is bounded: enough of the issue to start from,
// never a prompt the thread's length decides.
const (
	issueChatBodyLimit    = 16 << 10
	issueChatCommentLimit = 4 << 10
	issueChatComments     = 30
)

// issueChatIntro opens an issue chat's context.
const issueChatIntro = "A human opened this conversation on a vincent issue. You are working in " +
	"the issue's main worktree, on its main branch: the files are as the issue's last piece of work " +
	"left them. The issue is below.\n\n"

// createIssueChat is POST /v1/chats with issue_id (task 140 decision 1).
func (s *Server) createIssueChat(
	w http.ResponseWriter, r *http.Request, req *createChatRequest, project *store.Project, agentName, base string,
) {
	ctx := r.Context()
	iss, err := s.deps.Store.GetIssue(ctx, *req.IssueID)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, CodeNotFound, fmt.Sprintf("issue %d not found", *req.IssueID))
		return
	}
	if err != nil {
		s.internalError(w, "read issue", err)
		return
	}
	if iss.ProjectID != project.ID {
		writeError(w, http.StatusBadRequest, CodeValidationFailed,
			fmt.Sprintf("issue %d belongs to project %d, not %d", iss.ID, iss.ProjectID, project.ID))
		return
	}
	// branch_name / existing_branch may only restate the main branch, the
	// rule a main task is held to (task 134 decision 5).
	branch := strings.TrimSpace(req.BranchName)
	if branch != "" && iss.MainWorktree.Branch == "" {
		writeError(w, http.StatusBadRequest, CodeValidationFailed,
			fmt.Sprintf("issue %d has no main branch yet; a chat on it cuts one, so branch_name cannot be given", iss.ID))
		return
	}
	if req.ExistingBranch && branch == "" {
		writeError(w, http.StatusBadRequest, CodeValidationFailed,
			"existing_branch needs branch_name: there is no branch to adopt otherwise")
		return
	}
	opening, err := s.issueChatContext(ctx, iss)
	if err != nil {
		s.internalError(w, "assemble issue chat context", err)
		return
	}
	var warnings []string
	if iss.State == issuestate.Closed {
		warnings = append(warnings, fmt.Sprintf("issue %d is closed; the chat is opened on it anyway", iss.ID))
	}
	chat := &store.Chat{
		ProjectID: project.ID, Title: req.Title, State: chatstate.Idle, Agent: agentName,
		Model: req.Model, Effort: req.Effort, PermissionMode: string(agent.FullAuto),
		BaseBranch: base, Branch: branch, IssueID: &iss.ID, OpeningContext: opening,
	}
	err = s.deps.Store.CreateIssueChat(ctx, chat, func(id int64) string { return worktree.BranchName(id, req.Title) })
	if err != nil {
		writeIssueChatError(w, err)
		return
	}
	if err := s.placeIssueChat(ctx, project, chat); err != nil {
		// The chat holds the issue; a row that never got its directory would
		// hold it forever, so it goes again before the error is reported.
		if derr := s.deps.Store.DiscardUnplacedIssueChat(ctx, chat.ID); derr != nil {
			s.deps.Logger.Warn("discard unplaced issue chat", "chat", chat.ID, "error", derr)
		}
		var occupied *store.TaskLockedError
		if errors.As(err, &occupied) {
			writeErrorDetails(w, http.StatusConflict, CodeIssueWorktreeOccupied, err.Error(),
				map[string]string{"task_id": fmt.Sprint(occupied.TaskID), "chat_id": fmt.Sprint(occupied.ChatID)})
			return
		}
		writeError(w, http.StatusInternalServerError, CodeInternal, err.Error())
		return
	}
	body := renderChat(chat)
	body.Warnings = warnings
	writeJSON(w, http.StatusCreated, body)
}

// writeIssueChatError maps CreateIssueChat's refusals onto the wire.
func writeIssueChatError(w http.ResponseWriter, err error) {
	if live, ok := store.AsIssueHasLiveChat(err); ok {
		writeErrorDetails(w, http.StatusConflict, CodeIssueHasLiveChat, live.Error(),
			map[string]string{"chat_id": fmt.Sprint(live.ChatID)})
		return
	}
	if occ, ok := store.AsIssueWorktreeOccupied(err); ok {
		details := map[string]string{"task_id": fmt.Sprint(occ.TaskID)}
		if occ.ChatID != 0 {
			details = map[string]string{"chat_id": fmt.Sprint(occ.ChatID)}
		}
		writeErrorDetails(w, http.StatusConflict, CodeIssueWorktreeOccupied, occ.Error(), details)
		return
	}
	var mismatch *store.MainBranchMismatchError
	if errors.As(err, &mismatch) {
		writeError(w, http.StatusBadRequest, CodeValidationFailed,
			fmt.Sprintf("issue %d's main branch is %q; a chat on it cannot run on %q",
				mismatch.IssueID, mismatch.MainBranch, mismatch.Branch))
		return
	}
	writeError(w, http.StatusInternalServerError, CodeInternal, err.Error())
}

// placeIssueChat gives chat the issue's main worktree (task 140 decision 1):
// by transfer from the settled main task or released chat that still names
// it, uncommitted work included; else revived from the issue's branch; else,
// for an issue with no branch yet, an ordinary cut from base.
func (s *Server) placeIssueChat(ctx context.Context, project *store.Project, chat *store.Chat) error {
	wt := s.deps.Worktrees
	inMain, err := wt.BranchInMainCheckout(ctx, project.Path, chat.Branch)
	if err != nil {
		return err
	}
	if inMain {
		return worktree.IssueBranchInMainCheckout(chat.Branch, project.Path)
	}
	holder, err := s.deps.Store.IssueMainWorktreeAnyHolder(ctx, *chat.IssueID)
	if err != nil {
		return err
	}
	if holder.Path != "" {
		op, err := wt.InProgressOp(ctx, holder.Path)
		if err != nil {
			return err
		}
		if op != "" {
			return fmt.Errorf("the issue's main worktree %s is partway through a %s; finish or abort it there first",
				holder.Path, op)
		}
		return wt.HandOverUnderClaim(ctx, project.Path, chat.Branch, func(sha string) error {
			if err := s.deps.Store.TransferIssueWorktreeToChat(ctx, holder, chat.ID, holder.Path, sha); err != nil {
				return err
			}
			chat.WorktreePath, chat.BaseSHA = holder.Path, sha
			return nil
		})
	}
	claim := func(refresh *store.BaseRefresh) func(worktree.Created) error {
		return func(c worktree.Created) error {
			_, err := s.deps.Store.ClaimChatWorktree(ctx, chat.ID, c.Path, c.BaseSHA, refresh)
			chat.WorktreePath, chat.BaseSHA, chat.BaseRefresh = c.Path, c.BaseSHA, refresh
			return err
		}
	}
	if wt.LocalBranchExists(ctx, project.Path, chat.Branch) {
		held, err := s.deps.Store.IssueMainBranchHeld(ctx, *chat.IssueID, chat.Branch)
		if err != nil {
			return err
		}
		if held {
			_, err := wt.CreateIssueMainAndClaim(ctx, project.Path, worktree.ChatOwner(chat.ID), chat.Branch, claim(nil))
			return err
		}
	}
	var created worktree.Created
	created, err = wt.CreateAndClaim(ctx, project.Path, worktree.ChatOwner(chat.ID), chat.Branch, chat.BaseBranch,
		s.deps.Config().FetchBaseBranch, func(c worktree.Created) error {
			return claim(&store.BaseRefresh{
				Fetch: store.BaseFetch(c.Fetch), FastForward: store.BaseFastForward(c.FastForward),
			})(c)
		})
	if err == nil {
		s.logBaseRefresh(chat.ID, chat.BaseBranch, created)
	}
	return err
}

// issueChatContext is an issue chat's opening context (task 140 decision 6):
// the issue's title, body and comments — mirrored ones included — bounded.
func (s *Server) issueChatContext(ctx context.Context, iss *store.Issue) (string, error) {
	comments, err := s.deps.Store.ListIssueComments(ctx, iss.ID)
	if err != nil {
		return "", err
	}
	var sb strings.Builder
	sb.WriteString(issueChatIntro)
	fmt.Fprintf(&sb, "<issue id=%q state=%q>\n", fmt.Sprint(iss.ID), iss.State)
	fmt.Fprintf(&sb, "title: %s\n\n", iss.Title)
	if iss.Body != "" {
		sb.WriteString(clip(iss.Body, issueChatBodyLimit) + "\n")
	}
	sb.WriteString("</issue>\n\n")
	if len(comments) == 0 {
		return sb.String(), nil
	}
	skipped := 0
	if len(comments) > issueChatComments {
		skipped = len(comments) - issueChatComments
		comments = comments[skipped:]
	}
	sb.WriteString("<issue-comments>\n")
	if skipped > 0 {
		fmt.Fprintf(&sb, "(%d earlier comments omitted)\n", skipped)
	}
	for _, c := range comments {
		fmt.Fprintf(&sb, "<comment author=%q at=%q>\n%s\n</comment>\n",
			c.Author, c.CreatedAt.UTC().Format("2006-01-02T15:04:05Z"), clip(c.Body, issueChatCommentLimit))
	}
	sb.WriteString("</issue-comments>\n\n")
	return sb.String(), nil
}

// clip cuts s to at most n bytes on a rune boundary, saying so.
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && (s[cut]&0xC0) == 0x80 {
		cut--
	}
	return s[:cut] + "\n…(truncated)"
}
