package tui

import (
	"fmt"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/lezli01/vincent/internal/apiclient"
)

// A project switch keeps the active view (task 132.6, spec §15): the lists
// re-scope through setProject, a detail falls back to the list it was opened
// from, and a form is re-aimed at the new project. The three interfaces below
// are how a view takes part; the root's keepViewAcrossSwitch drives them.

// switchGuard is implemented by views that can hold a draft a switch would
// discard (task 132 decision 36). draft names what would be lost — "unsent
// comment", "task draft" — and dirty is false while there is nothing to
// lose. The root asks once, for every view, rather than each form keeping a
// confirm state of its own.
type switchGuard interface {
	switchDraft() (draft string, dirty bool)
}

// switchRetargeting is implemented by views whose form is re-aimed at the new
// selection rather than left (decision 37): the new-task form, the chats
// board's new-chat form and the issues list's issue form. retarget reopens
// the form pristine for p, whatever it held, and refetches its catalogs.
type switchRetargeting interface {
	retarget(p projectSel) tea.Cmd
}

// switchLeaving is implemented by the three detail screens a switch falls
// back from. leaveForSwitch drops what belonged to the old project's object —
// the workspace's back stack and forms, a chat's stream, an issue's form —
// before the root routes to the list, so nothing of it survives to be
// reached by esc or redrawn under the new header.
type switchLeaving interface {
	leaveForSwitch()
}

// switchFallback is where a detail screen lands when the project changes
// under it: the list each one is opened from.
var switchFallback = map[viewID]viewID{
	viewTask:  viewHome,
	viewChat:  viewChats,
	viewIssue: viewIssues,
}

// pendingSwitch is a switch waiting on the human's answer because the
// active view held a draft (decision 36). open is the open that asked for the
// switch, when one did: it is routed if the switch is confirmed and dropped
// with it if not.
type pendingSwitch struct {
	project apiclient.Project
	why     string
	draft   string
	open    tea.Msg
}

// prompt is the one line the root draws for it.
func (p *pendingSwitch) prompt() string {
	return fmt.Sprintf("discard the %s and switch to `%s`? y/n", p.draft, p.project.Name)
}

// followedNotice is the line a follow raises (task 132.6): the selection moved
// because the object opened belongs to another project.
func followedNotice(name string) string { return "switched to `" + name + "`" }

// ---- the views' halves ----

func (n *newTask) switchDraft() (string, bool) {
	if !n.opened {
		return "", false
	}
	// A seeded form is a draft before a key is pressed: its seed is the
	// pull request, issue or chat it was opened from, which a re-target
	// would drop (decision 37).
	return "task draft", n.touched || n.pull != nil || n.issueID != 0 || n.handoff != nil
}

func (n *newTask) retarget(p projectSel) tea.Cmd {
	if !n.opened {
		return nil
	}
	return n.open(p.id)
}

func (v *chatsView) switchDraft() (string, bool) {
	if v.create == nil {
		return "", false
	}
	return "chat draft", v.create.dirty
}

func (v *chatsView) retarget(p projectSel) tea.Cmd {
	if v.create == nil {
		return nil
	}
	v.create = newNewChatForm(v.client, p.id)
	return v.create.init()
}

func (v *issuesView) switchDraft() (string, bool) {
	if v.w.form == nil {
		return "", false
	}
	if v.w.form.creating() {
		return "issue draft", v.w.form.dirty()
	}
	return "unsaved issue edit", v.w.form.dirty()
}

// retarget reopens a create form on the new project. An edit form belongs to
// an issue of the old one, so it closes instead, as a detail screen would.
func (v *issuesView) retarget(p projectSel) tea.Cmd {
	v.w.act = nil
	if v.w.form == nil {
		return nil
	}
	if !v.w.form.creating() {
		v.w.form = nil
		return nil
	}
	return v.w.openForm(v.client, nil, p.id)
}

func (v *issueView) switchDraft() (string, bool) {
	if v.w.form == nil {
		return "", false
	}
	return "unsaved issue edit", v.w.form.dirty()
}

func (v *issueView) leaveForSwitch() {
	v.w.form, v.w.act = nil, nil
}

func (t *taskView) switchDraft() (string, bool) {
	switch {
	case t.createPR != nil && t.createPR.dirty:
		return "pull request draft", true
	case t.pullComment != nil && t.pullComment.dirty:
		return "unsent comment", true
	case t.detail.followUp != nil && t.detail.followUp.dirty:
		return "follow-up", true
	case t.detail.repair != nil && t.detail.repair.dirty:
		return "repair", true
	case t.detail.form != nil && t.detail.form.dirty:
		return "unsent answer", true
	}
	return "", false
}

// leaveForSwitch empties the back stack — esc after a switch goes to the new
// project's board, never to a task of the old one — and drops every form the
// workspace held. The stream is stopped by the deactivation the root's
// switchTo delivers next.
func (t *taskView) leaveForSwitch() {
	t.stack, t.leftTab, t.crumb = nil, nil, nil
	t.stackPush, t.stackKeep = 0, false
	t.back = viewHome
	t.pendingFailure = 0
	t.popup = false
	t.createPR, t.pullComment, t.pullMerge = nil, nil, nil
	t.detail.followUp, t.detail.repair, t.detail.form = nil, nil, nil
}

// leaveForSwitch stops the chat's stream and forgets the chat, so a switch
// leaves no subscription behind for a screen nobody is on.
func (v *chatView) leaveForSwitch() {
	if v.streamStop != nil {
		v.streamStop()
		v.streamStop = nil
	}
	v.chatID = 0
	v.chat, v.turns = nil, nil
	v.resetRecords()
	v.composer.SetValue("")
}

// ---- the forms' dirty flags ----
//
// Each form records an edit by comparing its draft before and after an input.
// The open editor's text counts only while an edit stays open: opening the
// editor on a filled row, or closing it unchanged, is not an edit.

func editorEdited(wasEditing, editing bool, before, after string) bool {
	return wasEditing && editing && before != after
}

func (f *followUpForm) draftKey() string {
	return strings.Join([]string{
		f.form, f.prompt, f.run, f.workflow, f.agent, f.model, f.effort,
		strconv.FormatBool(f.paused),
	}, "\x00")
}

func (f *followUpForm) watch() func() {
	key, editing, ed := f.draftKey(), f.editing, f.editor.Value()
	return func() {
		if f.draftKey() != key || editorEdited(editing, f.editing, ed, f.editor.Value()) {
			f.dirty = true
		}
	}
}

func (f *repairForm) draftKey() string {
	return strings.Join([]string{f.prompt, f.agent, f.model, f.effort}, "\x00")
}

func (f *repairForm) watch() func() {
	key, editing, ed := f.draftKey(), f.editing, f.editor.Value()
	return func() {
		if f.draftKey() != key || editorEdited(editing, f.editing, ed, f.editor.Value()) {
			f.dirty = true
		}
	}
}

func (f *answerForm) draftKey() string {
	allow := ""
	if f.allow != nil {
		allow = strconv.FormatBool(*f.allow)
	}
	// fmt prints a map in key order, so equal answers print equal.
	return fmt.Sprint(f.answers) + "\x00" + allow
}

func (f *answerForm) watch() func() {
	key, editing, ed := f.draftKey(), f.editing, f.editor.Value()
	return func() {
		if f.draftKey() != key || editorEdited(editing, f.editing, ed, f.editor.Value()) {
			f.dirty = true
		}
	}
}

func (f *createPRForm) draftKey() string {
	return strings.Join([]string{f.title, f.body, strconv.FormatBool(f.draft)}, "\x00")
}

func (f *createPRForm) watch() func() {
	key, editing, ed := f.draftKey(), f.editing, f.editor.Value()
	return func() {
		if f.draftKey() != key || editorEdited(editing, f.editing, ed, f.editor.Value()) {
			f.dirty = true
		}
	}
}

func (f *pullCommentForm) watch() func() {
	body, editing, ed := f.body, f.editing, f.editor.Value()
	return func() {
		if f.body != body || editorEdited(editing, f.editing, ed, f.editor.Value()) {
			f.dirty = true
		}
	}
}

func (f *newChatForm) draftKey() string {
	return strings.Join([]string{
		strconv.FormatInt(f.projectID, 10), strconv.Itoa(f.agentIdx),
		f.model, f.effort, f.title.Value(), f.base.Value(), f.branch.Value(),
	}, "\x00")
}

func (f *newChatForm) watch() func() {
	key := f.draftKey()
	return func() {
		if f.draftKey() != key {
			f.dirty = true
		}
	}
}
