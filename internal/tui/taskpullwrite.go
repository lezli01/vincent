package tui

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/lezli01/vincent/internal/apiclient"
)

// The Pull Request tab's writes (task 068.4, issue #387): merge, close or
// reopen, comment, and re-run the failed jobs of an Actions run.
//
// Three rules shape everything below, and each is a recorded decision rather
// than a style:
//
//   - **Human-triggered, from this tab only** (task 068 decision 1). There is
//     no merge key on the board, on the step path or anywhere else, and none
//     of `--delete-branch`, `--auto` or `--admin` is offered here either.
//   - **Absent, not present-and-refusing.** Decision 3 said it of re-run; it
//     holds for all four. The hint line, the footer and the key handler read
//     the same predicates, so a key that cannot apply is not named and does
//     nothing — it opens no prompt and sends nothing.
//   - **Nothing is sent without a confirmation.** Close, reopen and re-run ask
//     inline, the way the pull-requests takeover's unlink does. Merge opens a
//     popup with no method chosen (decision 4). A comment's popup is its own
//     confirmation (task 069 decision 2's reasoning): the keypress and the
//     editable body in front of it are the consent.
//
// The client still never talks to GitHub. Every call here goes to the daemon,
// which owns the credential and names every refusal in its own vocabulary.

// pullWriteTimeout bounds one write. The daemon makes up to two GitHub round
// trips inside it — a merge's preflight and the merge — each bounded at
// github.RemoteTimeout, and a client that gave up between them would report a
// failure for a pull request that had in fact merged.
const pullWriteTimeout = 3 * time.Minute

// pullWrite names one of the tab's writes.
type pullWrite int

const (
	pullWriteMerge pullWrite = iota
	pullWriteClose
	pullWriteReopen
	pullWriteComment
	pullWriteRerun
	pullWriteCount
)

// verb is what a failure note says could not be done.
func (w pullWrite) verb() string {
	switch w {
	case pullWriteMerge:
		return "merge"
	case pullWriteClose:
		return "close"
	case pullWriteReopen:
		return "reopen"
	case pullWriteComment:
		return "comment on"
	default:
		return "re-run the checks of"
	}
}

// pullConfirm is the inline y/n in front of close, reopen and re-run. It pins
// what is being confirmed at the moment the question is asked: the run id a
// re-run is for is the one named in the text, even if the cursor or the
// rollup moves while the question is on screen.
type pullConfirm struct {
	write pullWrite
	runID int64
	text  string
}

// pullWriteMsg is the daemon's answer to one write.
type pullWriteMsg struct {
	taskID  int64
	write   pullWrite
	subject string // repo#number, as the human confirmed it
	method  string
	runID   int64
	url     string
	err     error
}

// livePull is the pull request the writes act on, and nil when the pull row
// carries a reason instead of one: the state is unknown then, so every write
// is absent.
func (t *taskView) livePull() *apiclient.GitHubPullRequest {
	if !t.pullTabAvailable() {
		return nil
	}
	return t.pull.Pull
}

// pullSubject is how the prompts and notes name the pull request.
func (t *taskView) pullSubject() string {
	return t.pull.Repo + "#" + strconv.Itoa(t.pull.Number)
}

// mergeHead is the commit a merge is pinned to, and whether the two places
// that report one disagree. The check rollup's Ref wins: it is the commit
// whose checks the human was looking at (task 068 decision 6). The pull row's
// HeadSHA stands in only while no rollup has loaded.
func (t *taskView) mergeHead() (head string, moved bool) {
	p := t.livePull()
	if p == nil {
		return "", false
	}
	ref := t.pullTab.checks.Ref
	if ref == "" {
		return p.HeadSHA, false
	}
	return ref, p.HeadSHA != "" && p.HeadSHA != ref
}

// canMerge is `m`: an open pull request that is not a draft, with a known
// head. The merge state (blocked, behind, failing) is deliberately not
// consulted — the daemon does not serve it on the row, and its preflight is
// the authority, answering with a named reason.
func (t *taskView) canMerge() bool {
	p := t.livePull()
	if p == nil || p.Merged || p.Draft || p.State != "open" {
		return false
	}
	head, _ := t.mergeHead()
	return head != ""
}

// closeOrReopen is `X`: close on an open pull request, drafts included,
// reopen on a closed one that did not merge, and nothing on a merged one.
func (t *taskView) closeOrReopen() (pullWrite, bool) {
	p := t.livePull()
	switch {
	case p == nil || p.Merged:
		return 0, false
	case p.State == "open":
		return pullWriteClose, true
	case p.State == "closed":
		return pullWriteReopen, true
	default:
		return 0, false
	}
}

// canComment is `i`: any fetched pull request. GitHub takes comments on
// closed and merged ones too.
func (t *taskView) canComment() bool { return t.livePull() != nil }

// rerunTarget is the row `ctrl+r` acts on: the selected check, only while it
// is both Actions-backed and failed. The daemon refuses a passing Actions
// row, so offering the key there would be present-and-refusing.
func (t *taskView) rerunTarget() *apiclient.GitHubCheckRun {
	if t.livePull() == nil {
		return nil
	}
	run := t.selectedCheck()
	if run == nil || !run.Actions() || !run.Failed() {
		return nil
	}
	return run
}

// pullWriteAvailable answers for one registry key, so the hint line, the
// footer and the palette read the handler's own predicate. ok=false means the
// key is not one of the writes.
func (t *taskView) pullWriteAvailable(key string) (hint string, available, ok bool) {
	switch key {
	case "m":
		return "m merge", t.canMerge(), true
	case "X":
		w, can := t.closeOrReopen()
		if w == pullWriteReopen {
			return "X reopen", can, true
		}
		return "X close", can, true
	case "i":
		return "i comment", t.canComment(), true
	case "ctrl+r":
		return "ctrl+r re-run failed jobs", t.rerunTarget() != nil, true
	}
	return "", false, false
}

// liveBindings drops the write rows that cannot apply right now and gives `X`
// the label its state earns — the footer's and the palette's half of "absent,
// not refusing". Rows on any other surface pass through untouched.
func (t *taskView) liveBindings(rows []binding) []binding {
	out := make([]binding, 0, len(rows))
	for _, b := range rows {
		if b.context == ctxTaskPull {
			if hint, available, ok := t.pullWriteAvailable(b.key); ok {
				if !available {
					continue
				}
				b.hint = hint
			}
		}
		out = append(out, b)
	}
	return out
}

// updatePullWriteKey is the four keys. handled=false means the key is not a
// write, and the tab's other keys and the task actions get it.
func (t *taskView) updatePullWriteKey(msg tea.KeyPressMsg) (cmd tea.Cmd, handled bool) {
	switch msg.String() {
	case "m":
		return t.openPullMerge(), true
	case "X":
		t.askCloseOrReopen()
		return nil, true
	case "i":
		t.openPullComment()
		return nil, true
	case "ctrl+r":
		t.askRerun()
		return nil, true
	}
	return nil, false
}

// refuseInFlight reports, on the note line, that a write of this kind has not
// answered yet. A second merge or comment sent behind the first is the one
// outcome nobody asked for — comments carry no idempotency key.
func (t *taskView) refuseInFlight(writes ...pullWrite) bool {
	for _, w := range writes {
		if t.pullTab.inflight[w] {
			t.pullTab.note, t.pullTab.noteBad = "still waiting for the daemon to answer the last "+
				strings.TrimSuffix(w.verb(), " on")+" — nothing was sent", true
			return true
		}
	}
	return false
}

func (t *taskView) openPullMerge() tea.Cmd {
	if !t.canMerge() || t.refuseInFlight(pullWriteMerge) {
		return nil
	}
	form := newPullMergeForm(t.detail.taskID)
	form.submit = t.mergePullCmd
	t.pullMerge = form
	t.refreshPullMerge()
	t.popup = true
	if _, moved := t.mergeHead(); moved {
		// The two reports disagree, so one of them is stale. `y` stays inert
		// until the refetch lands and they agree again.
		return tea.Batch(t.pullCmd(), t.checksCmd())
	}
	return nil
}

// refreshPullMerge re-reads what the open merge popup shows from the pull row
// and the rollup, so a refetch that lands while it is up is what it confirms.
func (t *taskView) refreshPullMerge() {
	f := t.pullMerge
	if f == nil {
		return
	}
	facts := pullMergeFacts{subject: t.pullSubject(), mergeable: t.canMerge()}
	if p := t.pull.Pull; p != nil {
		facts.title, facts.headBranch, facts.baseBranch = p.Title, p.HeadBranch, p.BaseBranch
	}
	facts.head, facts.moved = t.mergeHead()
	facts.checks = t.pullTab.checks.State
	f.facts = facts
}

func (t *taskView) askCloseOrReopen() {
	w, ok := t.closeOrReopen()
	if !ok || t.refuseInFlight(pullWriteClose, pullWriteReopen) {
		return
	}
	text := "close " + t.pullSubject() + " without merging? (y/n)"
	if w == pullWriteReopen {
		text = "reopen " + t.pullSubject() + "? (y/n)"
	}
	t.pullTab.note, t.pullTab.noteBad = "", false
	t.pullTab.confirm = &pullConfirm{write: w, text: text}
}

// askRerun names every failed row the selected run backs, because the
// daemon's API takes one run id and re-runs all of that run's failed jobs —
// the prompt is about the run, not the row the cursor happens to be on.
func (t *taskView) askRerun() {
	run := t.rerunTarget()
	if run == nil || t.refuseInFlight(pullWriteRerun) {
		return
	}
	var names []string
	for _, r := range t.pullTab.checks.Runs {
		if r.RunID == run.RunID && r.Failed() {
			names = append(names, r.Name)
		}
	}
	t.pullTab.note, t.pullTab.noteBad = "", false
	t.pullTab.confirm = &pullConfirm{
		write: pullWriteRerun,
		runID: run.RunID,
		text: "re-run the failed jobs of Actions run " + strconv.FormatInt(run.RunID, 10) +
			" (" + strings.Join(names, ", ") + ")? (y/n)",
	}
}

// updatePullConfirmKey answers the inline prompt. `y` sends; every other key
// declines and is spent on declining, as the takeover's unlink prompt does —
// a stray `q` or digit must neither send nor do what it does elsewhere.
func (t *taskView) updatePullConfirmKey(msg tea.KeyPressMsg) tea.Cmd {
	c := t.pullTab.confirm
	t.pullTab.confirm = nil
	if msg.String() != "y" {
		return nil
	}
	switch c.write {
	case pullWriteClose, pullWriteReopen:
		return t.stateWriteCmd(c.write)
	case pullWriteRerun:
		return t.rerunPullCmd(c.runID)
	}
	return nil
}

// pullWriteCmd marks a write in flight and runs call against the daemon.
func (t *taskView) pullWriteCmd(w pullWrite, call func(ctx context.Context, client *apiclient.Client, msg *pullWriteMsg) error) tea.Cmd {
	client, id := t.detail.client, t.detail.taskID
	if client == nil || id == 0 {
		t.pullTab.note, t.pullTab.noteBad = "not connected — nothing was sent", true
		return nil
	}
	t.pullTab.inflight[w] = true
	subject := t.pullSubject()
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), pullWriteTimeout)
		defer cancel()
		msg := pullWriteMsg{taskID: id, write: w, subject: subject}
		msg.err = call(ctx, client, &msg)
		return msg
	}
}

// mergePullCmd is the merge popup's `y`: exactly the method picked and the
// head shown.
func (t *taskView) mergePullCmd(method, head string) tea.Cmd {
	if t.refuseInFlight(pullWriteMerge) {
		return nil
	}
	return t.pullWriteCmd(pullWriteMerge, func(ctx context.Context, c *apiclient.Client, msg *pullWriteMsg) error {
		msg.method = method
		_, err := c.MergeGitHubPull(ctx, msg.taskID, apiclient.GitHubPullMergeRequest{Method: method, HeadSHA: head})
		return err
	})
}

func (t *taskView) stateWriteCmd(w pullWrite) tea.Cmd {
	return t.pullWriteCmd(w, func(ctx context.Context, c *apiclient.Client, msg *pullWriteMsg) error {
		var err error
		if w == pullWriteReopen {
			_, err = c.ReopenGitHubPull(ctx, msg.taskID)
		} else {
			_, err = c.CloseGitHubPull(ctx, msg.taskID)
		}
		return err
	})
}

func (t *taskView) rerunPullCmd(runID int64) tea.Cmd {
	return t.pullWriteCmd(pullWriteRerun, func(ctx context.Context, c *apiclient.Client, msg *pullWriteMsg) error {
		msg.runID = runID
		_, err := c.RerunGitHubPullChecks(ctx, msg.taskID, runID)
		return err
	})
}

// commentPullCmd is the comment popup's ctrl+s: the body, verbatim.
func (t *taskView) commentPullCmd(body string) tea.Cmd {
	if t.refuseInFlight(pullWriteComment) {
		return nil
	}
	return t.pullWriteCmd(pullWriteComment, func(ctx context.Context, c *apiclient.Client, msg *pullWriteMsg) error {
		out, err := c.CommentGitHubPull(ctx, msg.taskID, body)
		msg.url = out.URL
		return err
	})
}

// applyPullWrite reports what the daemon said. The daemon publishes no event
// for these writes, so after one lands the tab refetches what it changed
// itself: the pull row and the checks after a merge, close or reopen, the
// checks after a re-run. A failure is the daemon's own message — GitHub's
// text never reaches a client.
func (t *taskView) applyPullWrite(msg pullWriteMsg) tea.Cmd {
	if msg.taskID != t.detail.taskID {
		return nil
	}
	t.pullTab.inflight[msg.write] = false
	if msg.err != nil {
		t.pullTab.note, t.pullTab.noteBad = pullWriteFailure(msg), true
		if msg.write == pullWriteMerge {
			t.pullMerge = nil
		}
		if f := t.pullComment; msg.write == pullWriteComment && f != nil {
			// The body stays: a comment refused for a missing scope is a
			// thing a human fixes and sends again, not retypes.
			f.failed(githubReasonMessage(msg.err))
		}
		t.dropEmptyPopup()
		return nil
	}
	switch msg.write {
	case pullWriteMerge:
		t.pullMerge = nil
		t.pullTab.note = "merged " + msg.subject + " (" + msg.method + ")"
	case pullWriteClose:
		t.pullTab.note = "closed " + msg.subject + " without merging"
	case pullWriteReopen:
		t.pullTab.note = "reopened " + msg.subject
	case pullWriteComment:
		t.pullComment = nil
		t.pullTab.note = "commented on " + msg.subject
		if msg.url != "" {
			t.pullTab.note += " — " + msg.url
		}
	case pullWriteRerun:
		t.pullTab.note = "re-run requested for Actions run " + strconv.FormatInt(msg.runID, 10)
	}
	t.pullTab.noteBad = false
	t.dropEmptyPopup()
	switch msg.write {
	case pullWriteMerge, pullWriteClose, pullWriteReopen:
		return tea.Batch(t.pullCmd(), t.checksCmd())
	case pullWriteRerun:
		return t.checksCmd()
	}
	return nil
}

// pullWriteFailure is the note for a refused write. The daemon's own message
// already names the pull request and the operation ("could not merge
// octo/repo#412: …"), so it is shown verbatim; only a failure that never
// reached the daemon gets that framing added here.
func pullWriteFailure(msg pullWriteMsg) string {
	var apiErr *apiclient.Error
	if errors.As(msg.err, &apiErr) && apiErr.Message != "" {
		return apiErr.Message
	}
	return "could not " + msg.write.verb() + " " + msg.subject + ": " + githubReasonMessage(msg.err)
}

// dropEmptyPopup lowers the popup flag once nothing is left to show in it.
func (t *taskView) dropEmptyPopup() {
	if t.popup && t.detail.form == nil && t.detail.repair == nil && t.detail.followUp == nil &&
		t.createPR == nil && t.pullMerge == nil && t.pullComment == nil {
		t.popup = false
	}
}

func (t *taskView) openPullComment() {
	if !t.canComment() || t.refuseInFlight(pullWriteComment) {
		return
	}
	form := newPullCommentForm(t.detail.taskID, t.pullSubject())
	form.submit = t.commentPullCmd
	form.openEditor = t.detail.editPullComment
	t.pullComment = form
	t.popup = true
}
