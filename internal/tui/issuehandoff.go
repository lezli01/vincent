package tui

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/keymap"
)

// Handoffs in the TUI (§15 views 2, 12 and 13, task 137.11). The board
// selects `pending` cards and hands them to one POST /v1/handoffs; the
// issue screens mark ready, unready and return. Every fact drawn here is the
// daemon's — the marker, its paused flag, the handoffs an issue was in — so
// the TUI derives nothing (decision 2 of 137.11's brief).

// The issues board's selection keys (task 137.11 decision 1), fixed keys
// in the task table's shape (task 011).
const (
	issueSelectKey    = "space"
	issueSelectAllKey = "V"
)

// The handoff issue actions, beside close, reopen and delete.
const (
	issueActReady   = "ready"
	issueActUnready = "unready"
	issueActReturn  = "return"
)

// The handoff marker states, as the daemon spells them (§5.7).
const (
	markerPending   = "pending"
	markerClaimed   = "claimed"
	markerWaiting   = "waiting"
	markerDelivered = "delivered"
	markerIdle      = "idle"
)

// handoffGrants are the seven capabilities of task 137 decision 6, in the
// order the form lists them. All start off: submitting is the consent.
var handoffGrants = []string{
	"issue.comment", "issue.close", "pull.create", "pull.comment", "pull.merge", "pull.close", "checks.rerun",
}

func issueMarker(iss apiclient.Issue) string {
	if iss.Handoff == nil {
		return ""
	}
	return iss.Handoff.State
}

// handoffPending is whether a card may be handed off now.
func handoffPending(iss apiclient.Issue) bool {
	return iss.Lane == "hand_off" && issueMarker(iss) == markerPending
}

// handoffClaimed is whether a live handoff item claims the issue.
func handoffClaimed(iss apiclient.Issue) bool {
	m := issueMarker(iss)
	return (m == markerClaimed || m == markerWaiting) && iss.Handoff.HandoffID != nil
}

// readyApplies is whether the ready key does something on iss: ready on an
// idle in-progress issue, unready on a pending hand-off one (decision 4).
func readyApplies(iss apiclient.Issue) bool {
	return (iss.Lane == "in_progress" && issueMarker(iss) == markerIdle) || handoffPending(iss)
}

func handoffRef(id int64) string { return "H" + strconv.FormatInt(id, 10) }

// issueHandoffCell is the marker part of a card's right cell: the delivery
// kind and the marker on a hand-off card, `idle` on an in-progress one, the
// claim wherever a live item holds the issue. "" when there is nothing to
// say. `pending` is the hand-off lane's ordinary state and adds no word.
func issueHandoffCell(iss apiclient.Issue) string {
	h := iss.Handoff
	if h == nil {
		return ""
	}
	var parts []string
	if iss.Lane == "hand_off" && iss.Delivery != nil && iss.Delivery.Kind != "" {
		parts = append(parts, iss.Delivery.Kind)
	}
	switch h.State {
	case markerClaimed:
		s := markerClaimed
		if h.HandoffID != nil {
			s += " (" + handoffRef(*h.HandoffID) + ")"
		}
		parts = append(parts, s)
	case markerWaiting, markerDelivered, markerIdle:
		parts = append(parts, h.State)
	}
	if h.Paused {
		parts = append(parts, "paused")
	}
	return strings.Join(parts, " · ")
}

// handoffReasonWords says a handoff or readiness 409 in words, "" for an
// error that carries no reason this screen knows.
func handoffReasonWords(err error) string {
	var apiErr *apiclient.Error
	if !errors.As(err, &apiErr) {
		return ""
	}
	issue := ""
	if id := apiErr.Details["issue_id"]; id != "" {
		issue = "issue #" + id + " "
	}
	in := ""
	if hid := apiErr.Details["handoff_id"]; hid != "" {
		in = " " + "H" + hid
	}
	switch apiErr.Details["reason"] {
	case apiclient.IssueReasonWorkRunning:
		return "a task on the issue is still running — wait for it to settle"
	case apiclient.IssueReasonNoFinishedWork:
		return "the issue has no finished work to hand off"
	case apiclient.IssueReasonInHandoff:
		return "the issue is in handoff" + in + " — return it first"
	case apiclient.IssueReasonNotHandOff:
		return "the issue is not in the hand-off lane"
	case "issue_not_eligible":
		if in != "" {
			return issue + "is already in handoff" + in
		}
		return issue + "is not eligible for a handoff — it is not pending in the hand-off lane"
	case "nothing_to_hand_off":
		return "no eligible issue to hand off"
	case "grant_needs_human":
		return "the grant needs a human's consent"
	}
	return ""
}

// handoffErrNote is an error's note: its reason in words, else the error.
func handoffErrNote(prefix string, err error) string {
	if words := handoffReasonWords(err); words != "" {
		return prefix + ": " + words
	}
	return prefix + ": " + errString(err)
}

// newIssueReady is the ready key on iss: ready or unready, run at once —
// both are undone by the other — or a note saying why neither applies.
func newIssueReady(client *apiclient.Client, iss apiclient.Issue) (*issueAction, tea.Cmd, string) {
	ref := "issue #" + strconv.FormatInt(iss.ID, 10)
	switch {
	case handoffPending(iss):
		a := &issueAction{client: client, issue: iss, action: issueActUnready}
		return a, a.run(), ""
	case iss.Lane == "in_progress" && issueMarker(iss) == markerIdle:
		a := &issueAction{client: client, issue: iss, action: issueActReady}
		return a, a.run(), ""
	}
	return nil, nil, ref + " is neither idle in progress nor pending hand-off — ready does not apply"
}

// newIssueReturn is the return key on iss: a y/n prompt, because a return
// withdraws readiness (decision 4).
func newIssueReturn(client *apiclient.Client, iss apiclient.Issue) (*issueAction, string) {
	if !handoffClaimed(iss) {
		return nil, "issue #" + strconv.FormatInt(iss.ID, 10) + " is not in a handoff — there is nothing to return"
	}
	return &issueAction{client: client, issue: iss, action: issueActReturn, stage: iaConfirm}, ""
}

// runHandoff is run()'s branch for the three handoff actions.
func runHandoff(ctx context.Context, client *apiclient.Client, iss apiclient.Issue, action string) (apiclient.Issue, error) {
	switch action {
	case issueActReady:
		out, err := client.MarkIssueReady(ctx, iss.ID)
		if out == nil {
			return apiclient.Issue{}, err
		}
		return *out, err
	case issueActUnready:
		out, err := client.MarkIssueUnready(ctx, iss.ID)
		if out == nil {
			return apiclient.Issue{}, err
		}
		return *out, err
	}
	if iss.Handoff == nil || iss.Handoff.HandoffID == nil {
		return apiclient.Issue{}, errors.New("the issue is in no handoff")
	}
	_, err := client.ResolveHandoffItems(ctx, *iss.Handoff.HandoffID, apiclient.HandoffReturn,
		[]int64{iss.ID}, apiclient.HandoffItemRequest{})
	return apiclient.Issue{}, err
}

// returnConfirmText is the return prompt.
func returnConfirmText(iss apiclient.Issue) []string {
	in := ""
	if iss.Handoff != nil && iss.Handoff.HandoffID != nil {
		in = " from " + handoffRef(*iss.Handoff.HandoffID)
	}
	return []string{
		"Return issue #" + strconv.FormatInt(iss.ID, 10) + in + "?",
		"It goes back to in progress and is no longer ready; no follow-up task is started.",
	}
}

// handoffActedNote is actedNote's wording for the handoff actions.
func handoffActedNote(msg issueActedMsg) string {
	ref := "issue #" + strconv.FormatInt(msg.id, 10)
	if msg.err != nil {
		return handoffErrNote(msg.action+" "+ref, msg.err)
	}
	switch msg.action {
	case issueActReady:
		return ref + " is ready — it moved to hand-off"
	case issueActUnready:
		return ref + " is no longer ready — it moved back to in progress"
	}
	return "returned " + ref + " from its handoff"
}

// --- the board's handoff key and form ---

type (
	// handoffWorkflowsMsg is the project's workflow list, read before the
	// form opens so it can offer only the `role: handoff` ones.
	handoffWorkflowsMsg struct {
		projectID int64
		issueIDs  []int64
		workflows []apiclient.WorkflowEntry
		err       error
	}
	// handoffCreatedMsg is the form's create, answered.
	handoffCreatedMsg struct {
		handoff apiclient.Handoff
		err     error
	}
)

// handoffWorkflowsCmd lists the project's workflows for the form.
func handoffWorkflowsCmd(client *apiclient.Client, projectID int64, ids []int64) tea.Cmd {
	if client == nil {
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		wfs, err := client.ListWorkflows(ctx, projectID)
		return handoffWorkflowsMsg{projectID: projectID, issueIDs: ids, workflows: wfs, err: err}
	}
}

// The form's rows: the workflow, the seven grants, the title, paused.
const (
	hfWorkflow = 0
	hfTitle    = 1 + 7
	hfPaused   = hfTitle + 1
	hfRows     = hfPaused + 1
)

// handoffForm is the board's handoff form (decision 3): a workflow among
// the project's `role: handoff` ones, the grant, an optional title and
// start paused. Everything else takes the daemon's defaults; the CLI is the
// full surface.
type handoffForm struct {
	client    *apiclient.Client
	projectID int64
	issueIDs  []int64
	workflows []string
	wf        int
	grant     []bool
	title     textField
	paused    bool
	row       int
	idemKey   string
	busy      bool
	discard   bool
	note      string
}

func newHandoffForm(client *apiclient.Client, projectID int64, ids []int64, workflows []string) *handoffForm {
	t := newTextField()
	t.SetPlaceholder("optional — the daemon names it from the issues")
	return &handoffForm{
		client: client, projectID: projectID, issueIDs: ids, workflows: workflows,
		grant: make([]bool, len(handoffGrants)), title: t, idemKey: newIdempotencyKey(),
	}
}

func (f *handoffForm) dirty() bool {
	return f.wf != 0 || f.paused || strings.TrimSpace(f.title.Value()) != "" || slices.Contains(f.grant, true)
}

// request is the create the form would send.
func (f *handoffForm) request() apiclient.CreateHandoffRequest {
	req := apiclient.CreateHandoffRequest{
		ProjectID: f.projectID, Workflow: f.workflows[f.wf], IssueIDs: slices.Clone(f.issueIDs),
		Title: strings.TrimSpace(f.title.Value()), Paused: f.paused,
	}
	for i, on := range f.grant {
		if on {
			req.Grant = append(req.Grant, handoffGrants[i])
		}
	}
	return req
}

func (f *handoffForm) submit() tea.Cmd {
	if f.client == nil {
		f.note = "not connected"
		return nil
	}
	f.busy, f.note = true, ""
	client, req, key := f.client, f.request(), f.idemKey
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), actionTimeout)
		defer cancel()
		h, err := client.CreateHandoff(ctx, req, key)
		return handoffCreatedMsg{handoff: h, err: err}
	}
}

func (f *handoffForm) move(d int) {
	f.row = (f.row + d + hfRows) % hfRows
	if f.row == hfTitle {
		f.title.Focus()
	} else {
		f.title.Blur()
	}
}

// updateKey handles one key; closed is true when the form is done with.
func (f *handoffForm) updateKey(msg tea.KeyPressMsg) (cmd tea.Cmd, closed bool) {
	if f.busy {
		return nil, false
	}
	if f.discard {
		f.discard = false
		if s := msg.String(); s == "y" || s == "Y" {
			return nil, true
		}
		return nil, false
	}
	switch msg.String() {
	case "esc":
		if f.dirty() {
			f.discard = true
			return nil, false
		}
		return nil, true
	case "ctrl+s":
		return f.submit(), false
	case "up", "shift+tab":
		f.move(-1)
		return nil, false
	case "down", "tab":
		f.move(1)
		return nil, false
	}
	if f.row == hfTitle {
		var c tea.Cmd
		f.title, c = f.title.Update(msg)
		return c, false
	}
	switch msg.String() {
	case "left", "h":
		if f.row == hfWorkflow {
			f.wf = (f.wf - 1 + len(f.workflows)) % len(f.workflows)
		}
	case "right", "l":
		if f.row == hfWorkflow {
			f.wf = (f.wf + 1) % len(f.workflows)
		}
	case "space", "enter", "x":
		switch {
		case f.row == hfPaused:
			f.paused = !f.paused
		case f.row > hfWorkflow && f.row < hfTitle:
			f.grant[f.row-1] = !f.grant[f.row-1]
		case f.row == hfWorkflow:
			f.wf = (f.wf + 1) % len(f.workflows)
		}
	}
	return nil, false
}

func (f *handoffForm) paste(text string) tea.Cmd {
	if f.row != hfTitle {
		return nil
	}
	var cmd tea.Cmd
	f.title, cmd = f.title.Update(tea.PasteMsg{Content: text})
	return cmd
}

func (f *handoffForm) render(width, height int) string {
	ids := make([]string, len(f.issueIDs))
	for i, id := range f.issueIDs {
		ids[i] = "#" + strconv.FormatInt(id, 10)
	}
	lines := []string{
		" " + styleTitle.Render("hand off") + styleDim.Render("  ·  "+strings.Join(ids, " ")),
		"",
	}
	mark := func(row int) string {
		if row == f.row {
			return styleFocus.Render("› ")
		}
		return "  "
	}
	box := func(on bool) string {
		if on {
			return "[x]"
		}
		return "[ ]"
	}
	lines = append(lines, mark(hfWorkflow)+"workflow  "+styleKey.Render("‹ "+f.workflows[f.wf]+" ›"),
		"", "  "+styleDim.Render("grant — what the handoff's agent may write to the forge; all off is none"))
	for i, c := range handoffGrants {
		lines = append(lines, mark(1+i)+box(f.grant[i])+" "+c)
	}
	f.title.SetWidth(max(width-12, 10))
	lines = append(lines, "", mark(hfTitle)+"title     "+f.title.View(),
		mark(hfPaused)+box(f.paused)+" start paused", "")
	switch {
	case f.busy:
		lines = append(lines, "  "+styleDim.Render("starting the handoff…"))
	case f.discard:
		lines = append(lines, "  "+styleWarn.Render("Discard this handoff? y discards · any other key keeps it"))
	default:
		lines = append(lines, "  "+styleDim.Render("↑/↓ move · space toggles · ←/→ workflow · ctrl+s hands off · esc closes"))
	}
	if f.note != "" {
		lines = append(lines, styled(wrapText(f.note, width, hanging("  ")), styleBad)...)
	}
	if len(lines) > height {
		lines = lines[:height]
	}
	for i, l := range lines {
		lines[i] = ansi.Truncate(l, width, "…")
	}
	return strings.Join(lines, "\n")
}

// handoffWorkflowNames is the role: handoff entries' names.
func handoffWorkflowNames(wfs []apiclient.WorkflowEntry) []string {
	var out []string
	for _, w := range wfs {
		if w.IsHandoff() {
			out = append(out, w.Name)
		}
	}
	return out
}

// --- the issues board's half ---

// togglePick is space: select or unselect the cursor's card. Only a
// pending hand-off card may be selected; any other says why.
func (v *issuesView) togglePick() {
	row, ok := v.current()
	if !ok {
		return
	}
	iss := row.issue
	if v.picked[iss.ID] {
		delete(v.picked, iss.ID)
		return
	}
	if !handoffPending(iss) {
		v.setNote(notPendingNote(iss), true)
		return
	}
	if v.picked == nil {
		v.picked = map[int64]bool{}
	}
	v.picked[iss.ID] = true
}

// notPendingNote says why a card cannot be handed off.
func notPendingNote(iss apiclient.Issue) string {
	ref := "issue #" + strconv.FormatInt(iss.ID, 10)
	switch m := issueMarker(iss); {
	case handoffClaimed(iss):
		return ref + " is already in handoff " + handoffRef(*iss.Handoff.HandoffID)
	case m == markerDelivered:
		return ref + "'s delivery was already handed off — a new delivery makes it pending again"
	case iss.Lane != "hand_off":
		return ref + " is not in the hand-off lane — only a pending hand-off issue can be handed off"
	}
	return ref + " is not pending hand-off"
}

// pickAll is V: select every pending card the filter shows, or clear the
// selection when there is one.
func (v *issuesView) pickAll() {
	if len(v.picked) > 0 {
		v.picked = nil
		return
	}
	for _, row := range v.lines() {
		if row.header || row.none || !handoffPending(row.issue) {
			continue
		}
		if v.picked == nil {
			v.picked = map[int64]bool{}
		}
		v.picked[row.issue.ID] = true
	}
	if len(v.picked) == 0 {
		v.setNote("no pending hand-off issue is showing", true)
	}
}

// prunePicked drops selected ids a re-list no longer shows as pending.
func (v *issuesView) prunePicked() {
	for id := range v.picked {
		i := slices.IndexFunc(v.issues, func(is apiclient.Issue) bool { return is.ID == id })
		if i < 0 || !handoffPending(v.issues[i]) {
			delete(v.picked, id)
		}
	}
}

// handoffTargets is what the handoff key sends: the selection, else the
// cursor's card when it is pending, else a note saying why nothing.
func (v *issuesView) handoffTargets() ([]int64, string) {
	if len(v.picked) > 0 {
		ids := make([]int64, 0, len(v.picked))
		for id := range v.picked {
			ids = append(ids, id)
		}
		slices.Sort(ids)
		return ids, ""
	}
	row, ok := v.current()
	if !ok {
		return nil, "select a pending hand-off issue first"
	}
	if !handoffPending(row.issue) {
		return nil, notPendingNote(row.issue)
	}
	return []int64{row.issue.ID}, ""
}

// startHandoff is the handoff key: it reads the project's workflows, and
// the form opens on the answer.
func (v *issuesView) startHandoff() tea.Cmd {
	ids, note := v.handoffTargets()
	if note != "" {
		v.setNote(note, true)
		return nil
	}
	return handoffWorkflowsCmd(v.client, v.project.id, ids)
}

// openHandoff opens the form, or refuses when the project has no
// `role: handoff` workflow to run.
func (v *issuesView) openHandoff(msg handoffWorkflowsMsg) {
	if msg.projectID != v.project.id {
		return
	}
	if msg.err != nil {
		v.setNote("could not list the workflows: "+errString(msg.err), true)
		return
	}
	names := handoffWorkflowNames(msg.workflows)
	if len(names) == 0 {
		v.setNote(v.project.name+" has no `role: handoff` workflow — add one to hand issues off", true)
		return
	}
	v.handoff = newHandoffForm(v.client, msg.projectID, msg.issueIDs, names)
}

// handoffCreated closes the form on success and names the handoff and its
// task; a refusal stays on the form, worded.
func (v *issuesView) handoffCreated(msg handoffCreatedMsg) tea.Cmd {
	if v.handoff == nil {
		return nil
	}
	if msg.err != nil {
		v.handoff.busy = false
		v.handoff.note = handoffErrNote("could not hand off", msg.err)
		return nil
	}
	n := len(v.handoff.issueIDs)
	v.handoff, v.picked = nil, nil
	h := msg.handoff
	v.setNote("started handoff "+handoffRef(h.ID)+" as task #"+strconv.FormatInt(h.TaskID, 10)+
		" with "+plural(n, "issue", "issues"), false)
	return v.loadCmd()
}

// liveBindings withholds ready, handoff and return where they do not apply
// to the selected row (decision 4), and the selection keys while no card
// is pending.
func (v *issuesView) liveBindings(rows []binding) []binding {
	row, ok := v.current()
	return slices.DeleteFunc(slices.Clone(rows), func(b binding) bool {
		switch b.op {
		case "":
			return false
		case keymap.Ready:
			return !ok || !readyApplies(row.issue)
		case keymap.Return:
			return !ok || !handoffClaimed(row.issue)
		case keymap.Handoff:
			return len(v.picked) == 0 && (!ok || !handoffPending(row.issue))
		}
		return false
	})
}

// inHandoffNote is the new-task refusal on a claimed issue (task 137.11).
func inHandoffNote(iss apiclient.Issue) string {
	in := "a handoff"
	if iss.Handoff != nil && iss.Handoff.HandoffID != nil {
		in = "handoff " + handoffRef(*iss.Handoff.HandoffID)
	}
	return "issue #" + strconv.FormatInt(iss.ID, 10) + " is in " + in + " — return it first"
}

// handoffEventNames reports whether a handoff event's payload names issue
// id: an item change's issue_id, or a create's issue_ids.
func handoffEventNames(ev apiclient.Event, id int64) bool {
	var p struct {
		IssueID  int64   `json:"issue_id"`
		IssueIDs []int64 `json:"issue_ids"`
	}
	if json.Unmarshal(ev.Payload, &p) != nil {
		return false
	}
	return p.IssueID == id || slices.Contains(p.IssueIDs, id)
}

// inHandoffTask reports whether ev is about a handoff the issue was in: a
// settle names only the handoff and its task.
func (v *issueView) inHandoffTask(ev apiclient.Event) bool {
	if ev.TaskID == nil {
		return false
	}
	return slices.ContainsFunc(v.issue.Handoffs, func(h apiclient.IssueHandoffEntry) bool { return h.TaskID == *ev.TaskID })
}

// handoffLines is the issue detail's Handoff section (task 137.11): every
// handoff that took the issue, newest first, with its state, its task and
// this issue's outcome — when and by whom. enter on a row opens the task.
func (v *issueView) handoffLines(rows []issueDetailRow, width, base int) []string {
	if len(v.issue.Handoffs) == 0 {
		return nil
	}
	lines := []string{"", " " + styleTitle.Render("Handoff")}
	for i, row := range rows {
		h := row.handoff
		if h == nil {
			continue
		}
		marker := "  "
		if i == v.cursor {
			marker = styleFocus.Render("› ")
		}
		v.rowLines = append(v.rowLines, base+len(lines))
		outcome := "unresolved"
		if h.Outcome != "" {
			outcome = h.Outcome
			if h.OutcomeAt != nil {
				outcome += " " + h.OutcomeAt.Local().Format("2006-01-02 15:04")
			}
			if h.OutcomeBy != "" {
				outcome += " by " + h.OutcomeBy
			}
		}
		line := marker + styleKey.Render(padRight(handoffRef(h.HandoffID), 6)) + " " +
			padRight(h.State, 8) + " " + styleKey.Render("#"+strconv.FormatInt(h.TaskID, 10)) + "  " +
			styleDim.Render(outcome)
		lines = append(lines, ansi.Truncate(line, width, "…"))
	}
	return lines
}
