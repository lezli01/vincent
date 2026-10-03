package tui

import (
	"context"
	"slices"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
)

// The issue state and delete prompts (§15 views 12 and 13, task 130.12):
// `X` closes or reopens, `D` deletes. One prompt for both views, drawn under
// whichever screen raised it.
//
// `X` offers exactly the daemon's available_actions. A list row carries none
// (the list DTO leaves them zero), so on the list `X` reads the issue first
// rather than inferring what is allowed from its state. Delete is not a state
// action and never appears among them (decision 6, the spec §3 row 30
// precedent): it is allowed in any state and always asks.

// The two issue keys that carry no vocabulary term: `i` edits in the form,
// on the trigger list's precedent, and `X` changes the state.
const (
	issueEditKey  = "i"
	issueStateKey = "X"
)

// The issue actions, as available_actions spells them.
const (
	issueActClose  = "close"
	issueActReopen = "reopen"
	issueActDelete = "delete"
)

// The close reasons, as the daemon spells them.
var issueCloseReasons = []pickerOption{
	{value: "completed", label: "completed"},
	{value: "not_planned", label: "not planned"},
	{value: "duplicate", label: "duplicate", note: "of another issue in this project"},
}

// issueLocalOnly is what a state change on an imported issue says before it
// is made (decision 17). The write-back outbox (130.10) has not landed, so
// the change is vincent's alone; 130.10 replaces this with its own
// confirmation.
const issueLocalOnly = "this changes vincent's copy only — it is not written to GitHub yet, and the next sync may overwrite it"

// Issue-prompt messages.
type (
	// issueActionTargetMsg is the full issue `X` on the list read, so the
	// prompt can offer its available_actions.
	issueActionTargetMsg struct {
		issue apiclient.Issue
		err   error
	}
	issueDuplicatesMsg struct {
		id     int64
		issues []apiclient.Issue
		err    error
	}
	// issueActedMsg is a finished close, reopen or delete. issue is the
	// issue the write returned; a delete returns none.
	issueActedMsg struct {
		id     int64
		action string
		issue  apiclient.Issue
		err    error
	}
)

// iaStage is where the prompt is.
type iaStage int

const (
	iaChoose iaStage = iota
	iaReason
	iaDuplicate
	iaConfirm
	iaBusy
)

// issueAction is one open prompt.
type issueAction struct {
	client *apiclient.Client
	issue  apiclient.Issue
	stage  iaStage
	action string
	reason string
	dupOf  *int64
	pick   *picker
	note   string
}

// issueActionFor reads the issue `X` acts on. The detail already holds it in
// full; the list holds a row, which carries no available_actions.
func issueActionFor(client *apiclient.Client, id int64) tea.Cmd {
	if client == nil {
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		iss, err := client.GetIssue(ctx, id, "")
		return issueActionTargetMsg{issue: iss, err: err}
	}
}

// newIssueStateAction opens `X`'s prompt over iss's available_actions. It
// returns a note instead when there is none to offer.
func newIssueStateAction(client *apiclient.Client, iss apiclient.Issue) (*issueAction, tea.Cmd, string) {
	actions := slices.DeleteFunc(slices.Clone(iss.AvailableActions), func(a string) bool {
		return a != issueActClose && a != issueActReopen
	})
	switch len(actions) {
	case 0:
		return nil, nil, "issue #" + strconv.FormatInt(iss.ID, 10) + " offers no state change"
	case 1:
		a := &issueAction{client: client, issue: iss}
		return a, a.begin(actions[0]), ""
	}
	opts := make([]pickerOption, 0, len(actions))
	for _, act := range actions {
		opts = append(opts, pickerOption{value: act, label: act})
	}
	a := &issueAction{client: client, issue: iss, stage: iaChoose}
	a.pick = newPicker(0, "action", opts, false, "")
	return a, nil, ""
}

// newIssueDelete opens `D`'s confirmation. The list row is enough: whether
// the issue is imported is on it.
func newIssueDelete(client *apiclient.Client, iss apiclient.Issue) *issueAction {
	return &issueAction{client: client, issue: iss, action: issueActDelete, stage: iaConfirm}
}

func (a *issueAction) imported() bool { return a.issue.Source != nil }

// begin moves to what action needs next.
func (a *issueAction) begin(action string) tea.Cmd {
	a.action = action
	if action == issueActClose {
		a.stage = iaReason
		a.pick = newPicker(0, "close as", slices.Clone(issueCloseReasons), false, "completed")
		return nil
	}
	return a.confirmOrRun()
}

// confirmOrRun asks first only where there is something to say: on an
// imported issue, that the change is local.
func (a *issueAction) confirmOrRun() tea.Cmd {
	a.pick = nil
	if a.imported() {
		a.stage = iaConfirm
		return nil
	}
	return a.run()
}

// update handles one key; done is true when the prompt is finished with,
// whether or not it ran anything.
func (a *issueAction) updateKey(msg tea.KeyPressMsg) (cmd tea.Cmd, done bool) {
	switch a.stage {
	case iaBusy:
		return nil, false
	case iaConfirm:
		if s := msg.String(); s == "y" || s == "Y" {
			return a.run(), false
		}
		return nil, true
	case iaDuplicate:
		if a.pick == nil {
			// Still listing; esc gives up.
			return nil, msg.String() == "esc"
		}
	case iaChoose, iaReason:
	}
	res := a.pick.update(msg)
	if res.closed && !res.chosen {
		return nil, true
	}
	if !res.chosen {
		return res.cmd, false
	}
	switch a.stage {
	case iaChoose:
		return a.begin(res.value), false
	case iaReason:
		a.reason = res.value
		if a.reason == "duplicate" {
			a.stage, a.pick = iaDuplicate, nil
			return a.duplicatesCmd(), false
		}
		return a.confirmOrRun(), false
	case iaDuplicate:
		if id, err := strconv.ParseInt(res.value, 10, 64); err == nil {
			a.dupOf = &id
		}
		return a.confirmOrRun(), false
	case iaConfirm, iaBusy:
	}
	return nil, false
}

// duplicatesCmd lists the issues a duplicate may point at: the same
// project's, in any state (decision 14.3).
func (a *issueAction) duplicatesCmd() tea.Cmd {
	client, iss := a.client, a.issue
	if client == nil {
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		list, err := client.ListIssues(ctx, apiclient.IssueListOptions{ProjectID: iss.ProjectID})
		return issueDuplicatesMsg{id: iss.ID, issues: list, err: err}
	}
}

// applyDuplicates fills the target picker. "No target" stays first: a
// duplicate whose original is not in vincent is still a duplicate.
func (a *issueAction) applyDuplicates(msg issueDuplicatesMsg) {
	if msg.id != a.issue.ID || a.stage != iaDuplicate {
		return
	}
	if msg.err != nil {
		a.note = "could not list this project's issues: " + errString(msg.err)
	}
	opts := []pickerOption{{value: "", label: "(no target)"}}
	for _, iss := range msg.issues {
		if iss.ID == a.issue.ID {
			continue
		}
		opts = append(opts, pickerOption{
			value: strconv.FormatInt(iss.ID, 10),
			label: "#" + strconv.FormatInt(iss.ID, 10) + " " + iss.Title,
			note:  iss.State,
		})
	}
	a.pick = newPicker(0, "duplicate of", opts, false, "")
}

func (a *issueAction) run() tea.Cmd {
	client, id, action := a.client, a.issue.ID, a.action
	if client == nil {
		a.note = "not connected"
		return nil
	}
	a.stage = iaBusy
	req := apiclient.CloseIssueRequest{Reason: a.reason, DuplicateOf: a.dupOf}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), actionTimeout)
		defer cancel()
		out := issueActedMsg{id: id, action: action}
		switch action {
		case issueActClose:
			out.issue, out.err = client.CloseIssue(ctx, id, req)
		case issueActReopen:
			out.issue, out.err = client.ReopenIssue(ctx, id)
		case issueActDelete:
			out.err = client.DeleteIssue(ctx, id)
		}
		return out
	}
}

// confirmText is what the confirmation says, line by line.
func (a *issueAction) confirmText() []string {
	ref := "issue #" + strconv.FormatInt(a.issue.ID, 10)
	switch a.action {
	case issueActDelete:
		out := []string{"Delete " + ref + " permanently? It cannot be undone."}
		if a.imported() {
			out = append(out,
				"This never deletes the issue on GitHub.",
				"vincent remembers the deletion, so a sync will not import it again.")
		}
		return out
	case issueActClose:
		what := "Close " + ref + " as " + strings.ReplaceAll(firstNonEmpty(a.reason, "completed"), "_", " ")
		if a.dupOf != nil {
			what += " of #" + strconv.FormatInt(*a.dupOf, 10)
		}
		return []string{what + "?", issueLocalOnly}
	}
	return []string{"Reopen " + ref + "?", issueLocalOnly}
}

// lines draws the prompt for the bottom of the owning screen.
func (a *issueAction) lines(width int) []string {
	out := []string{""}
	switch a.stage {
	case iaConfirm:
		for _, l := range a.confirmText() {
			out = append(out, styleWarn.Render("  "+l))
		}
		out = append(out, styleDim.Render("  y confirms · any other key cancels"))
	case iaBusy:
		out = append(out, styleDim.Render("  "+a.action+"…"))
	case iaDuplicate:
		if a.pick == nil {
			out = append(out, styleDim.Render("  listing this project's issues…"))
			break
		}
		fallthrough
	case iaChoose, iaReason:
		out = append(out, " "+styleTitle.Render(a.pick.heading)+
			styleDim.Render("  issue #"+strconv.FormatInt(a.issue.ID, 10)))
		a.pick.setWidth(width - 4)
		out = append(out, a.pick.renderBody()...)
		out = append(out, styleDim.Render("  enter picks · esc cancels"))
	}
	if a.note != "" {
		out = append(out, styleBad.Render("  "+a.note))
	}
	for i, l := range out {
		out[i] = ansi.Truncate(l, width, "…")
	}
	return out
}

// issueEditTargetMsg is the full issue `i` on the list read: a list row
// carries no body, version-current `editable` list or description to edit.
type issueEditTargetMsg struct {
	issue apiclient.Issue
	err   error
}

func issueEditFor(client *apiclient.Client, id int64) tea.Cmd {
	if client == nil {
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		iss, err := client.GetIssue(ctx, id, "")
		return issueEditTargetMsg{issue: iss, err: err}
	}
}

// issueWrites is the write layer both issue screens carry: the form and the
// prompt, at most one of them open, each owning the keyboard while it is.
type issueWrites struct {
	exec execFunc
	form *issueForm
	act  *issueAction
}

func newIssueWrites() issueWrites { return issueWrites{exec: tea.ExecProcess} }

func (w *issueWrites) open() bool { return w.form != nil || w.act != nil }

// context names the layer for the binding registry, base when none is open.
func (w *issueWrites) context(base bindingContext) bindingContext {
	switch {
	case w.form != nil:
		return ctxIssueForm
	case w.act != nil:
		return ctxIssuePrompt
	}
	return base
}

func (w *issueWrites) paste(text string) tea.Cmd {
	if w.form != nil {
		return w.form.paste(text)
	}
	return nil
}

// openForm opens the form, creating when iss is nil.
func (w *issueWrites) openForm(client *apiclient.Client, iss *apiclient.Issue, projectID int64) tea.Cmd {
	w.act = nil
	w.form = newIssueForm(client, w.exec, iss, projectID)
	return w.form.init()
}

// update gives an open layer the messages that are its own, keys first. ok
// is false for anything the owning screen must handle itself.
func (w *issueWrites) update(msg tea.Msg) (cmd tea.Cmd, ok bool) {
	if w.form != nil {
		if cmd, ok := w.form.update(msg); ok {
			return cmd, true
		}
	}
	if w.act == nil {
		return nil, false
	}
	switch msg := msg.(type) {
	case issueDuplicatesMsg:
		w.act.applyDuplicates(msg)
		return nil, true
	case tea.KeyPressMsg:
		cmd, done := w.act.updateKey(msg)
		if done {
			w.act = nil
		}
		return cmd, true
	}
	return nil, false
}

// actedNote is what a finished write says on the screen it was made from.
func actedNote(msg issueActedMsg) string {
	ref := "issue #" + strconv.FormatInt(msg.id, 10)
	if msg.err != nil {
		return msg.action + " " + ref + ": " + errString(msg.err)
	}
	switch msg.action {
	case issueActDelete:
		return "deleted " + ref
	case issueActClose:
		return "closed " + ref
	}
	return "reopened " + ref
}
