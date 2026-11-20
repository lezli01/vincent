package tui

import (
	"slices"

	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/keymap"
)

// The binding registry is the single source the palette, the footer (T3.12)
// and the ? overlay all render from (§15 Discovery). Three hand-maintained
// lists drift within two PRs and the drift is invisible until a human
// presses a key the help promised; one registry turns "is every key
// discoverable?" into a test.

// The key vocabulary (§15 Keys, task 093). One operation, one key, across
// every surface — the registry made the help accurate long before it made the
// keys consistent, and a help that faithfully advertises four keys for
// "refresh" is describing a defect rather than hiding one.
//
//	refresh / re-read              R
//	archive                        A
//	delete a persisted record      D
//	remove a row from a draft      d
//	add / create                   a
//	edit in $EDITOR                e
//	type free text instead of      t
//	  picking from a list
//	open in a browser              o
//	open or expand the row         enter
//	  under the cursor
//	cycle a listing's scope        s
//	filter                         /
//	fold / unfold                  ←/→, C/O, space
//	open a fan-out lane            l
//	page                           </>
//
// Three clauses govern it, and `vocabulary` in bindings_test.go is the
// machine-readable half that keeps them enforced rather than aspirational —
// a comment is a wish, and this one had been the wish since task 049:
//
//  1. A key may be shared only when it means the same operation. Archive is
//     `A` whether it is a §6 action on a task or the chats board's own key.
//  2. A key may mean two different things only where the registry can prove
//     the two surfaces never co-exist. A takeover screen offers no
//     available_actions, so `a` may add on projects while `a` approves a gate;
//     the task workspace is not disjoint from the action set, which is what
//     task 025 discovered when it partitioned `R`.
//  3. A key already carrying a vocabulary term may not be given a second
//     meaning on a new surface.
//
// The §6 action letters `p a x r E R s c A F` do not move, so they decide the
// contested cases: `R` won refresh because it already held seven surfaces and
// repair only ever appears where refresh does not, and `A` won archive because
// it is the action letter. `D` destroys something persisted and `d` edits an
// unsaved draft — the inversion is what makes pressing `d` on an archive safe.

// bindingScope classifies who owns a key: global keys work wherever the
// focused surface is not capturing text, panel bindings belong to one panel
// or takeover screen, task actions are gated on the daemon's
// available_actions (§6).
type bindingScope int

const (
	scopeGlobal bindingScope = iota
	scopePanel
	scopeTaskAction
)

// bindingContext names the surface a panel-scoped binding belongs to.
type bindingContext string

const (
	ctxTasks bindingContext = "task table"
	// ctxTaskOverview is the task workspace's landing tab (task 129.12). Its
	// jump links are rows of their own, keyed on the tab digits they press,
	// so the footer, `?` and the palette list them; taskView.liveBindings
	// keeps only the ones the current frame draws.
	ctxTaskOverview bindingContext = "task overview"
	ctxTimeline     bindingContext = "timeline"
	ctxTaskDetails  bindingContext = "task details"
	ctxOutput       bindingContext = "output"
	ctxDiff         bindingContext = "diff"
	ctxNewTask      bindingContext = "new task"
	// ctxNewTaskFields is the Fields row's own editor (§5.3, task 093). Its
	// own context for the reason ctxWorkflowEditor has one: nothing the form
	// underneath offers means the same thing inside it — `e` is the
	// description's $EDITOR out there and `a`/`d` are rows of a draft map in
	// here. Registering it is also what put its keys in front of `?` and the
	// footer at all; they were handled and hinted inline for two releases and
	// the registry had never heard of them.
	ctxNewTaskFields bindingContext = "new task fields"
	ctxProjects      bindingContext = "projects"
	// ctxProjectForm is the projects overview's add/edit form (#803). Its
	// own context because while it is open it owns the keyboard, and none
	// of the table's add/remove/filter rows does anything inside it.
	ctxProjectForm bindingContext = "project form"
	// ctxProjectFormField is that form with a text field being typed into or
	// the workflow list open (review F7): the keys go to the field or the
	// list, so the form's own ↑/↓ and enter mean nothing there, and the
	// footer must not offer them.
	ctxProjectFormField bindingContext = "project form field"
	ctxWorkflows        bindingContext = "workflows"
	// ctxPullRequests is the pull-requests takeover (§15 view 7, task 052.6).
	ctxPullRequests bindingContext = "pull requests"
	// ctxWorkflowEditor is the structured editor sub-layer of the workflows
	// takeover (task 065). Its keys are its own for the same reason the
	// graph's are: nothing the list offers means the same thing inside a
	// form.
	ctxWorkflowEditor bindingContext = "workflow editor"
	// ctxWorkflowCreate is the create/fork prompt in front of it.
	ctxWorkflowCreate bindingContext = "workflow create"
	// ctxWorkflowGraph is the graph sub-layer of the workflows takeover. It
	// is its own context because its keys are entirely different from the
	// list's, and the footer and the ? overlay must say which set is live.
	ctxWorkflowGraph bindingContext = "workflow graph"
	// ctxTaskWorkflow is the task workspace's workflow-graph tab (task 051).
	// It is its own context rather than a second registration of
	// ctxWorkflowGraph because that layer's `e` (open the file in $EDITOR)
	// and `R` (re-fetch the registry entry) are meaningless against a task's
	// snapshot, and because `tab` belongs to the workspace's tab cycle here
	// rather than to the graph's source-order walk (decision 5).
	ctxTaskWorkflow bindingContext = "task workflow"
	// ctxTaskStepDetails is the task workspace's Step Details tab (issue
	// #323): what one attempt was handed, and the §8.6 resolution behind it.
	// Its own context rather than more rows on ctxTaskDetails for the reason
	// ctxTaskPull gives — the tab has its own navigation. ↑/↓ walks attempts
	// here, where on Task Details it walks the document's sections, and one
	// row could only describe one of the two.
	ctxTaskStepDetails bindingContext = "task step details"
	// ctxTaskPull is the task workspace's Pull Request tab (task 068). Its
	// own context rather than more rows on ctxTaskDetails because the tab
	// only exists for a task with a linked pull request, and a footer that
	// advertised `c open check` on a task with no checks would be describing
	// a screen that is not there. Since 068.4 it also carries the four
	// confirmed writes, whose rows the task view drops wherever the write
	// cannot apply (taskView.liveBindings).
	ctxTaskPull bindingContext = "task pull request"
	// The Pull Request tab's confirmations (task 068.4, issue #387). Three
	// contexts for the reason every popup has its own: each owns the keyboard
	// while it is up, and `y`, `n`, `enter` and `esc` mean something
	// different in each — and nothing like the tab underneath.
	ctxPullMerge   bindingContext = "merge a pull request"
	ctxPullComment bindingContext = "comment on a pull request"
	ctxPullConfirm bindingContext = "confirm a pull request write"
	// ctxWorkflowStep is the step-detail modal over the graph (task 053). Its
	// own context for the reason the popups have theirs: while it is open it
	// owns the keyboard, and a row shared with the graph could only describe
	// one of the two.
	ctxWorkflowStep bindingContext = "step detail"
	// The chat surfaces (task 067). Three contexts for two views: the
	// new-chat form is a layer over the chats board with its own keyboard,
	// which is exactly what a bindingContext names.
	//
	// The two archived boards (§15 view 10, task 092). Their own contexts
	// rather than more ctxTasks/ctxChats rows: they carry keys the live
	// boards do not (delete, the date window, the pages) and none of the §6
	// action keys, which an archived row offers none of anyway.
	ctxArchived      bindingContext = "archived tasks"
	ctxArchivedChats bindingContext = "archived chats"
	// ctxConnection is the connection screen (#793): while it hides the
	// view behind it, that view's keys do nothing, so `?` and `:` describe
	// this screen — retry and the root's own keys — rather than the hidden
	// surface (review F4).
	ctxConnection bindingContext = "connection"
	ctxChats      bindingContext = "chats"
	// ctxChatSearch is the chat search screen (task 158).
	ctxChatSearch bindingContext = "chat search"
	ctxChat       bindingContext = "chat"
	// ctxChatSkills is the chat workspace's skill list (task 124.13). Its
	// own context because while it is open it owns the keyboard, and `tab`,
	// `enter` and `esc` all mean something else the moment it closes. Not
	// ctxSkills, which is the daemon view's published-skill offer below.
	ctxChatSkills bindingContext = "chat skills"
	// ctxChatSkillsInline is the same list opened by the draft's own sigil
	// token (task 124.14). Its own context because the composer keeps the
	// keyboard here: `backspace` means "shorten the filter, and close the
	// list when it is already empty" in browse and "edit the draft" in
	// inline, and a `?` pane saying the first while the second is true is a
	// lie (decision 94).
	ctxChatSkillsInline bindingContext = "chat skills inline"
	// ctxChatFiles is the chat composer's `@` file picker (task 126.11). Its
	// own context rather than more `chat skills inline` rows for that
	// context's own reason: the two lists never draw together, and every one
	// of these four labels says something different — `tab` completes a
	// *path*, `esc` closes a list of files — so a shared row could only
	// describe one of them, and a `?` pane that described the other would be
	// a lie.
	ctxChatFiles bindingContext = "chat files"
	// ctxChatConversation is the chat's conversation holding the keyboard
	// (task 160 decision 1), and ctxChatFind its search line. Their own
	// contexts because every letter means something else in the composer:
	// `n` would be typed there, and is the next match here.
	ctxChatConversation bindingContext = "chat conversation"
	ctxChatFind         bindingContext = "chat find"
	// ctxProjectPicker is the root's project picker (task 132.4). Its own
	// context for the reason every popup has one: it owns the keyboard while
	// it is up, and its enter switches the project rather than opening a row.
	ctxProjectPicker bindingContext = "project picker"
	// ctxChatExport is the root's export form (task 159), a popup with its
	// own context for the project picker's reason: it owns the keyboard.
	ctxChatExport bindingContext = "export chat"

	ctxNewChat bindingContext = "new chat"
	ctxDaemon  bindingContext = "daemon"
	// ctxSkills is the daemon view's published-skill offer (§9.8, task 095).
	// Its own context rather than more ctxDaemon rows for the reason the
	// config editor's keys are their own: while it is open it owns the
	// keyboard, and `n` means "not now" in here and nothing at all out
	// there.
	ctxSkills bindingContext = "agent skills"
	// ctxFonts is the daemon view's Nerd Font flow (task 151), its own context
	// for the same reason.
	ctxFonts bindingContext = "nerd font"
	ctxForm  bindingContext = "answer form"
	// ctxRepairForm is the §6 repair popup (task 025). Its own context
	// rather than more ctxForm rows: the two popups share a shape and
	// nothing else — one picks from what an agent asked, the other types a
	// prompt and chooses an adapter — so a single row could only describe
	// one of them.
	ctxRepairForm bindingContext = "repair form"
	// ctxFollowUpForm is the §6 follow-up popup (task 027). Its own context
	// for the same reason the repair form has one: it is the only popup with
	// a chooser above its text, and a row shared with the others could only
	// describe one of them.
	ctxFollowUpForm bindingContext = "follow-up form"
	// ctxCreatePR is the pull-request form (task 052.6, task 069). Its own context for
	// the reason the other popups have theirs: while it is open it owns the
	// keyboard, and its two rows are nothing like the form underneath.
	ctxCreatePR bindingContext = "open a pull request"
	// ctxConfigEdit is the daemon view's config editor (task 060). Its own
	// context because while it is open it owns the keyboard, and its keys are
	// nothing like the log pane's underneath it.
	ctxConfigEdit bindingContext = "config editor"
	// ctxSettings is the Settings screen (task 152).
	ctxSettings bindingContext = "settings"
	// ctxStatusLine is the daemon view's status-line flow (task 082), its
	// own context for the reason ctxSkills has one (#803).
	ctxStatusLine bindingContext = "status line"
	// The triggers takeover (§15 view 11, task 096.6). Five contexts for one
	// view, for the reason the workflows takeover has four: the list, its
	// ledger pane, the form, the create prompt and a dry run each own the
	// keyboard in turn, and `enter` means something different in every one.
	ctxTriggers      bindingContext = "triggers"
	ctxTriggerLedger bindingContext = "trigger ledger"
	ctxTriggerForm   bindingContext = "trigger form"
	ctxTriggerCreate bindingContext = "trigger create"
	ctxTriggerDryRun bindingContext = "trigger dry run"
	// The issue screens (§15 views 12 and 13, task 130.9): the list and one
	// issue's detail, two contexts for two screens. `a` stays unbound on
	// both until #672 makes it "create a task from this issue".
	ctxIssues bindingContext = "issues"
	ctxIssue  bindingContext = "issue"
	// The issue form and the close/reopen/delete prompt (task 130.12). Their
	// own contexts for the reason every popup has one: each owns the
	// keyboard while it is up, over either issue screen, and `n` is "no" in
	// the prompt where it is "new issue" underneath.
	ctxIssueForm   bindingContext = "issue form"
	ctxIssuePrompt bindingContext = "issue prompt"
	ctxHandoffForm bindingContext = "handoff form"
)

// vocabularyTerm is the shared operation a row performs — the left column of
// the table above. A row that performs one carries it, and bindings_test.go
// then holds the three clauses: one key per term, no second meaning on a term's
// key, and a term's key may equal a §6 action letter only where the registry
// can prove no action is ever offered.
//
// Not every row has one, and that is the point: `link this pull request`,
// `fork the entry`, `group the tasks` are surface-local operations that appear
// once, and a vocabulary that named them would be a list of every key rather
// than a rule about the shared ones. Fold and page are in §15's table but not
// here for the opposite reason — each is a *set* of keys by design (←/→, C/O,
// space; </>), which is not a shape "one key per term" can describe.
type vocabularyTerm string

const (
	termRefresh   vocabularyTerm = "refresh / re-read"
	termArchive   vocabularyTerm = "archive"
	termDelete    vocabularyTerm = "delete a persisted record"
	termDraftDrop vocabularyTerm = "remove a row from an open draft"
	termAdd       vocabularyTerm = "add / create"
	termEditor    vocabularyTerm = "edit in $EDITOR"
	termFreeText  vocabularyTerm = "type free text instead of picking"
	termBrowser   vocabularyTerm = "open in a browser"
	termOpenRow   vocabularyTerm = "open or expand the row under the cursor"
	termScope     vocabularyTerm = "cycle a listing's scope"
	termFilter    vocabularyTerm = "filter"
	termLane      vocabularyTerm = "open a fan-out lane"
	termResult    vocabularyTerm = "open the output the outcome card's result came from"
	termNextFail  vocabularyTerm = "jump to the task's next failure"
	termAttention vocabularyTerm = "show only the tasks that need a human"
	termComment   vocabularyTerm = "comment on the issue"
	termReady     vocabularyTerm = "mark the issue ready to hand off, or not ready"
	termHandoff   vocabularyTerm = "hand the selected issues off"
	termReturn    vocabularyTerm = "return the issue from its handoff"
	termRename    vocabularyTerm = "rename a chat"
)

// binding is one registry row.
type binding struct {
	// wideForm marks a key only the wide new-task screen answers (task 150.11).
	wideForm bool
	// key is the press as tea reports it — the canonical one when a label
	// documents aliases ("f" for f/G). Empty for palette-only navigation.
	key   string
	label string
	scope bindingScope
	// context is the owning surface for scopePanel rows.
	context bindingContext
	// hint is the short footer form ("enter open"); rows without one stay
	// out of the footer. priority orders the segment; lower renders first.
	hint     string
	priority int
	// action is the §6 action a scopeTaskAction row is gated on.
	action string
	// op is the rebindable operation the row performs (task 118): a
	// vocabulary term, a §6 action or a piece of global chrome. key is then
	// the operation's *default*, and registry() swaps in the effective key
	// and rewrites the hint to match; a row with no op is fixed.
	op keymap.Op
	// unbound marks a row the effective keymap left without a key (task
	// 128): an operation whose default a user binding took on a lenient
	// load, or a fixed key shadowed on its surface. key and hint are empty;
	// `?` and the palette say "unbound".
	unbound bool
	// term is the §15 vocabulary operation this row performs, for the rows
	// that perform a shared one. Empty means surface-local.
	term vocabularyTerm
	// github marks a row that only means something when the selected
	// project has a usable GitHub integration (§13.2, task 132.11). There is
	// no stored notion of one, so the root's probe fan-out is the answer and
	// the row is withheld until it says yes for that project — including
	// while the probes are still in flight. Mechanically this is `fold`'s precedent (task 054
	// decision 5) applied to a nav row and to two workspace keys.
	github bool
	// remoteOnly marks a row whose key only does something on an imported
	// issue (#796): the issues list's `o browser`. footerBindings drops it
	// from the footer while the selected row has no source page; `?` and
	// the palette keep it, and the key still says there is none.
	remoteOnly bool
	// syncable marks a row that only means something while the selected
	// project has a sync pass to wake (task 142.14 decision 3): the issues
	// list's `S`. The gate is the list's own loaded sync status — not the
	// GitHub probe, which says nothing about a Jira project — so the row is
	// withheld from the footer, `?` and the palette until that status has
	// loaded, and whenever it says not_github or source_not_github (a
	// project with no source, or github pinned off github.com), where a
	// request would record nothing. A switch that is off keeps the row: `S`
	// then says import is off, and why.
	syncable bool
	// aliased marks a row another row's hint already advertises: `right`
	// behind ctxTasks' `←/→ fold`, `O` behind its `C/O fold all`, `C` behind
	// the timeline's and the diff's `O/C fold all`. The footer's `+N` counts
	// the rows it is not showing, and without this a grouped board would
	// carry a permanent `+2` with nothing actually hidden (task 094
	// decision 4). It is declared here rather than parsed out of the hint at
	// run time: splitting the string on `/` and mapping `↑↓←→` back to key
	// names is text parsing over a human-written field, and it would break
	// silently the first time a hint is worded differently. bindings_test.go
	// asserts the declaration against what the hints actually say, so a new
	// alias pair fails a test rather than inflating a count.
	aliased bool
	// fold marks a row whose key only means something while the board has
	// groups. With `group_by: []` there are none, so shell.liveBindings drops
	// these and the footer never names a press that does nothing (task 054
	// decision 5).
	fold bool
	// mainTab marks a row whose key switches the main tab (task 136.2). It
	// is only live on a screen that is one of tui.tabs while there are two
	// or more, and its hint names the tab it goes to — mainTabRows applies
	// both. panelOwnsLocalKey ignores it: the row describes the root's
	// switch, not a meaning the surface has of its own.
	mainTab bool
	// nav marks a navigation entry; navTarget is where it goes.
	nav       bool
	navTarget viewID
	// noPalette keeps a row out of the palette: the palette's own controls
	// (: esc) and the answer form's keys, which only exist while the popup
	// owns the keyboard and are printed inside the popup itself.
	noPalette bool
}

// mainTabLabel is the label every main-tab row carries.
const mainTabLabel = "switch to the next main tab, in tui.tabs order (shift+tab goes back)"

// issueChatLabel is `T` on both issue screens (task 140.9).
const issueChatLabel = "chat with an agent on the issue's main worktree — it moves the issue to in progress, and a main task waits until the chat is closed; with a chat already holding it, reopen that chat"

// issueChatWithLabel is `chat_with` on both issue screens (task 161).
const issueChatWithLabel = "chat with… — choose the agent, model and effort, and type a first message, before the chat opens on the issue's main worktree; with a chat already holding it, reopen that chat"

// bindings is the registry. Order within a scope+context is display order.
var bindings = []binding{
	// Global chrome.
	{op: keymap.Palette, key: ":", label: "open the command palette", scope: scopeGlobal, noPalette: true},
	// The same palette, reachable where `:` is not: a chat's composer owns
	// every printable key, so the palette had been unreachable there since
	// task 067 (task 076 decision 7). noPalette for the reason `:` is —
	// a row teaching the way to open the thing you already opened.
	{op: keymap.PaletteAlt, key: paletteAltKey, label: "open the command palette (works while a text field has the keyboard)", scope: scopeGlobal, noPalette: true},
	{op: keymap.Help, key: "?", label: "toggle this help", scope: scopeGlobal},
	// The same help, reachable where `?` is not (task 114 decision 1): a text
	// field takes `?` as a character. noPalette for the reason the ctrl+p row
	// is — `?`'s own row already runs help from the palette.
	{op: keymap.HelpAlt, key: helpAltKey, label: "toggle this help (works while a text field has the keyboard)", scope: scopeGlobal, noPalette: true},
	{op: keymap.NextAttention, key: "!", label: "jump to the next task that needs you — awaiting input, awaiting approval or blocked; then the next project's", scope: scopeGlobal},
	{op: keymap.Mouse, key: "M", label: "toggle the mouse (native text selection needs it off — or shift-drag)", scope: scopeGlobal},
	// The project switch (task 132 decision 3). A global row, so the palette
	// lists it and running it from a chat opens the picker rather than typing
	// `@` into the draft.
	{op: keymap.Project, key: "@", label: "switch project — pick the project the TUI is looking at", scope: scopeGlobal},
	// Task 149: the global pause. It asks y/n on the status line before it
	// acts, since it changes what every project is doing.
	{op: keymap.PauseAll, key: "Z", label: "pause all work, or continue it — nothing new starts and running tasks park at their next step; asks first", scope: scopeGlobal},
	// Paste is normally the terminal's own (Cmd+V, Ctrl+Shift+V, middle
	// click): it arrives as bracketed paste and lands in the focused field
	// with no key involved. ctrl+v is the fallback for terminals that pass
	// the key through instead, and it is documented rather than palette-run —
	// running it from the palette would paste into a field the palette just
	// closed.
	{key: "ctrl+v", label: "paste into the focused field (Cmd+V and the terminal's own paste work too)", scope: scopeGlobal, noPalette: true},
	{key: "esc", label: "close one layer: popup → the task you came from → screen → selection → filter — never quits", scope: scopeGlobal, noPalette: true},
	{op: keymap.Quit, key: "q", label: "quit the TUI (the daemon keeps running)", scope: scopeGlobal},
	{key: "ctrl+c", label: "quit the TUI", scope: scopeGlobal, noPalette: true},

	// Navigation: the five takeover screens (§15 views 3–7). Only new task
	// keeps a direct key; the rest live here, in the palette — retiring
	// 1..6 without substituting new memorized keys is the point.
	{op: keymap.New, key: "n", label: "new task — for the project you are looking at", scope: scopeGlobal, nav: true, navTarget: viewNewTask},
	// The board gets a screen row now that it is one main tab among others
	// (task 136.2): with issues first, the board is a place you go to.
	{label: "tasks — the task board", scope: scopeGlobal, nav: true, navTarget: viewHome},
	{label: "project overview — every project's figures, what needs you, add, edit, remove", scope: scopeGlobal, nav: true, navTarget: viewProjects},
	{label: "workflows — registry with scopes and validity", scope: scopeGlobal, nav: true, navTarget: viewWorkflows},
	{label: "daemon — identity, adapters, log", scope: scopeGlobal, nav: true, navTarget: viewDaemon},
	// Settings get a palette row and no key, the pattern every takeover but
	// new task follows, and no main tab (task 152).
	{label: "settings — everything in config.yaml, explained and editable", scope: scopeGlobal, nav: true, navTarget: viewSettings},
	{label: "pull requests — what is open in this project on GitHub", scope: scopeGlobal, nav: true, navTarget: viewPullRequests, github: true},
	// Chats get a palette row and no direct key, the pattern every takeover
	// but new task follows. `n` is not shared: on the chats board it makes a
	// chat, everywhere else it still makes a task.
	{label: "chats — conversations with an agent, each in its own worktree", scope: scopeGlobal, nav: true, navTarget: viewChats},
	{label: "Search all chats — find text in any chat's title, prompts or answers, live or archived", scope: scopeGlobal, nav: true, navTarget: viewChatSearch},
	// The two archived boards get palette rows and no keys, for the reason
	// task 049 retired 1..6 and task 067 gave chats a row: retiring memorized
	// keys without substituting new ones is the point. `s` is skip and `A` is
	// archive, and neither was ever going to be free.
	{label: "archived tasks — history, with a permanent delete", scope: scopeGlobal, nav: true, navTarget: viewArchived},
	{label: "archived chats — ended conversations, with a permanent delete", scope: scopeGlobal, nav: true, navTarget: viewArchivedChats},
	// Triggers get a palette row and no key (task 096 decision 14), the
	// pattern every takeover but new task follows.
	{label: "triggers — what starts work on its own, and what each event became", scope: scopeGlobal, nav: true, navTarget: viewTriggers},
	// Issues get a palette row and no key, the same pattern. Not github:
	// issues are vincent's own, and a project with no GitHub integration has
	// them too (task 130.9). Scoped to the selected project (task 132.11).
	{label: "issues — this project's issues, local and imported", scope: scopeGlobal, nav: true, navTarget: viewIssues},

	// Task actions, gated on available_actions. `p` appears twice because
	// pause and resume are distinct actions behind one key; the palette
	// shows whichever the daemon offers.
	{op: keymap.Pause, key: "p", label: "pause the running task", scope: scopeTaskAction, action: apiclient.ActionPause, priority: 3},
	{op: keymap.Pause, key: "p", label: "resume the paused task", scope: scopeTaskAction, action: apiclient.ActionResume, priority: 3},
	{op: keymap.Approve, key: "a", label: "approve the gate", scope: scopeTaskAction, action: apiclient.ActionApprove, priority: 1},
	{op: keymap.Reject, key: "x", label: "reject the gate", scope: scopeTaskAction, action: apiclient.ActionReject, priority: 2},
	{op: keymap.Retry, key: "r", label: "retry the blocked step", scope: scopeTaskAction, action: apiclient.ActionRetry, priority: 4},
	{op: keymap.EditRetry, key: "E", label: "edit the step's prompt or command in $EDITOR, then retry", scope: scopeTaskAction, action: apiclient.ActionRetry, priority: 5},
	{op: keymap.Repair, key: "R", label: "repair with an agent — a one-off run in this task's worktree; the task stays blocked afterwards", scope: scopeTaskAction, action: apiclient.ActionRepair, priority: 5},
	{op: keymap.Skip, key: "s", label: "skip the current step", scope: scopeTaskAction, action: apiclient.ActionSkip, priority: 6},
	{op: keymap.Cancel, key: "c", label: "cancel the task (asks first — a running step is killed)", scope: scopeTaskAction, action: apiclient.ActionCancel, priority: 7},
	{op: keymap.Archive, key: "A", label: "archive the task (asks first — the worktree is removed)", scope: scopeTaskAction, action: apiclient.ActionArchive, priority: 8, term: termArchive},
	{op: keymap.FollowUp, key: "F", label: "follow up — run an agent prompt, a shell command or a workflow in this finished task's worktree; it returns to the state it came from", scope: scopeTaskAction, action: apiclient.ActionFollowUp, priority: 9},
	// `T` for talk (task 119). Every letter the word "chat" offers is spoken
	// for where a task's actions are: `c` is cancel, `C` folds, `h` walks the
	// Output tab's attempts, and `t` is the vocabulary's free text. `T` is
	// the triggers takeover's dry run, which clause 2 allows — that screen
	// offers no available_actions — and keymap records as an exception on the
	// default key alone. One row covers both halves of the key:
	// with `open_chat_id` set the daemon offers no `chat`, and the key opens
	// that chat instead (taskActions.offersChat), so the row is gated there
	// rather than on the action alone.
	{op: keymap.Chat, key: "T", label: "chat with an agent in this task's worktree — the task stays locked until the chat is closed; with a chat already open, reopen it", scope: scopeTaskAction, action: apiclient.ActionChat, priority: 10},
	// Task 161 decision 7: `T` with a choice first. Offered exactly where
	// `chat` is, so it carries the same action.
	{op: keymap.ChatWith, key: "alt+t", label: "chat with… — choose the agent, model and effort, and type a first message, before the chat opens; with a chat already open, reopen it", scope: scopeTaskAction, action: apiclient.ActionChat, priority: 11},

	// Task table.
	// tab switches the main tab (task 136.2). The hint names the tab it goes
	// to, and the root rewrites it from tui.tabs (mainTabRows); noPalette
	// because replaying tab from the palette would switch from a screen the
	// palette already covered, and the screen rows are the palette's way.
	{key: "tab", label: mainTabLabel, scope: scopePanel, context: ctxTasks, hint: "tab next tab", priority: 0, mainTab: true, noPalette: true},
	{key: "down", label: "move up and down the column (↑/↓)", scope: scopePanel, context: ctxTasks, hint: "↑↓ card", priority: 3},
	{op: keymap.OpenRow, key: "enter", label: "open the selected task in its full-screen workspace", scope: scopePanel, context: ctxTasks, hint: "enter open", priority: 1, term: termOpenRow},
	{op: keymap.Filter, key: "/", label: "filter by id, title, state or issue", scope: scopePanel, context: ctxTasks, hint: "/ filter", priority: 2, term: termFilter},
	{key: "left", label: "move to the column on the left", scope: scopePanel, context: ctxTasks, hint: "←/→ lane", priority: 4},
	{key: "right", label: "move to the column on the right", scope: scopePanel, context: ctxTasks, priority: 4, aliased: true},
	{key: "f", label: "fold or open the issue group under the cursor", scope: scopePanel, context: ctxTasks, hint: "f fold", priority: 8},
	// Lanes under a fan-out parent's card (boardcardlanes.go, issue #316).
	{key: "L", label: "show or hide a fan-out's lanes under its card — on a lane, its own lanes, or back to its parent", scope: scopePanel, context: ctxTasks, hint: "L lanes", priority: 8},
	{key: "space", label: "select this task for a bulk action — the action keys then act on every selected task (space again deselects, esc clears)", scope: scopePanel, context: ctxTasks, hint: "space select", priority: 5},
	{op: keymap.AttentionFilter, key: "H", label: "show only the tasks that need a human — awaiting input, awaiting approval, blocked, or a fan-out whose lane is (H again shows every task)", scope: scopePanel, context: ctxTasks, hint: "H needs you", priority: 6, term: termAttention},
	{key: "V", label: "select every task the filter is showing, or clear that selection", scope: scopePanel, context: ctxTasks, priority: 6},
	// Folding the issue groups (task 150.6). C/O are the diff pane's two
	// letters in the same meaning.
	{key: "C", label: "fold every issue group", scope: scopePanel, context: ctxTasks, hint: "C/O fold all", priority: 10},
	{key: "O", label: "open every issue group", scope: scopePanel, context: ctxTasks, priority: 11, aliased: true},

	// Overview (task 129.12, task 164.7). The frame's one link is on enter;
	// the strip has no digits to key a row of links with any more.
	{key: "tab", label: "move between the task's tabs in the order the strip draws them (shift+tab goes back)", scope: scopePanel, context: ctxTaskOverview, hint: "tab tabs", priority: 2},
	{key: "]", label: "move to the next tab ([ goes back)", scope: scopePanel, context: ctxTaskOverview, priority: 3},
	{key: "enter", label: "open what the frame is about: the output of the running attempt or the one it stopped on, or the diff of a finished task — or, with the cursor on a Linked row, that row", scope: scopePanel, context: ctxTaskOverview, hint: "enter output", priority: 4},
	{key: "down", label: "move along the Linked card's rows that open somewhere (↑/↓): the issue, the pull request, the chat", scope: scopePanel, context: ctxTaskOverview, hint: "↑/↓ linked", priority: 5},
	// The Task Details document is the Overview's lower half (task 164
	// decision 3), so its pull-request keys came with it.
	{op: keymap.Browser, key: "o", label: "open this task's pull request in a browser", scope: scopePanel, context: ctxTaskOverview, priority: 9, github: true, term: termBrowser},
	{key: "P", label: "push this task's branch to origin and open its pull request — the title, body and draft flag are editable first", scope: scopePanel, context: ctxTaskOverview, hint: "P open a PR", priority: 9, github: true},
	{op: keymap.Lane, key: "l", label: "open the fan-out lane the failure card blames, or the first lane that needs you on a parent waiting on its lanes", scope: scopePanel, context: ctxTaskOverview, hint: "l open the lane", priority: 6, term: termLane},
	{op: keymap.NextFailure, key: "N", label: "jump to the next failure: the newest failed attempt of each step in execution order, on the Output tab at the line it failed on — a blocked lane opens, esc comes back", scope: scopePanel, context: ctxTaskOverview, hint: "N next failure", priority: 6, term: termNextFail},
	{op: keymap.Result, key: "w", label: "open the output of the attempt the outcome card's result came from, at its end", scope: scopePanel, context: ctxTaskOverview, hint: "w result output", priority: 6, term: termResult},
	{key: "d", label: "open the Diff tab", scope: scopePanel, context: ctxTaskOverview, priority: 8},
	{key: openIssueKey, label: "open the task's linked issue — esc comes back to this tab", scope: scopePanel, context: ctxTaskOverview, hint: "i issue", priority: 8},
	{key: "U", label: "open this lane's parent task", scope: scopePanel, context: ctxTaskOverview, priority: 9},
	{key: "pgdown", label: "scroll the overview a page (pgup/pgdown, home/end, wheel)", scope: scopePanel, context: ctxTaskOverview, priority: 10},

	// Timeline.
	{key: "tab", label: "move between the task's tabs in the order the strip draws them (shift+tab goes back)", scope: scopePanel, context: ctxTimeline, hint: "tab tabs", priority: 1},
	{key: "]", label: "move to the next tab ([ goes back)", scope: scopePanel, context: ctxTimeline, priority: 2},
	{key: "down", label: "select an attempt or a fan-out lane (↑/↓); scrollback is per attempt", scope: scopePanel, context: ctxTimeline, hint: "↑/↓ attempts", priority: 3},
	{key: "enter", label: "open the selected attempt in the Output tab, open the folded iteration or round the cursor is on, or open the lane the cursor is on", scope: scopePanel, context: ctxTimeline, hint: "enter output", priority: 3},
	// Folding a loop's iterations and a `fan_out`'s rounds (issue #317). The
	// Diff tab's vocabulary verbatim, because it is the same gesture on the
	// same screen. ↑/↓ stop once on a folded tier — its first row — so every
	// selection they reach is drawn.
	{key: "space", label: "open or close the iteration or round the cursor is in, or the own lanes of the lane it is on (enter and →/← too)", scope: scopePanel, context: ctxTimeline, hint: "space fold", priority: 4, fold: true},
	{key: "right", label: "open the folded iteration or round the cursor is in, or unfold the own lanes of the lane it is on", scope: scopePanel, context: ctxTimeline, priority: 5, fold: true},
	{key: "left", label: "close the iteration or round the cursor is in, or fold the own lanes of the lane it is on", scope: scopePanel, context: ctxTimeline, priority: 6, fold: true},
	{key: "O", label: "open every iteration and round of this task", scope: scopePanel, context: ctxTimeline, hint: "O/C fold all", priority: 7, fold: true},
	{key: "C", label: "close every one — the timeline opens with the latest iteration showing", scope: scopePanel, context: ctxTimeline, priority: 8, fold: true, aliased: true},
	{op: keymap.Lane, key: "l", label: "open the fan-out lane the cursor is on, or the first lane of the selected fan_out step", scope: scopePanel, context: ctxTimeline, priority: 9, term: termLane},
	{op: keymap.NextFailure, key: "N", label: "jump to the next failure: the newest failed attempt of each step in execution order, on the Output tab at the line it failed on — a blocked lane opens, esc comes back", scope: scopePanel, context: ctxTimeline, priority: 9, term: termNextFail},
	{key: openIssueKey, label: "open the task's linked issue — esc comes back to this tab", scope: scopePanel, context: ctxTimeline, hint: "i issue", priority: 8},
	{key: "U", label: "open this lane's parent task", scope: scopePanel, context: ctxTimeline, priority: 10},
	{key: "d", label: "open the Diff tab", scope: scopePanel, context: ctxTimeline, priority: 12},
	{key: "pgdown", label: "page the attempts (pgup/pgdown)", scope: scopePanel, context: ctxTimeline, priority: 13},
	{key: "g", label: "jump to the first attempt (g/home; G/end to the last)", scope: scopePanel, context: ctxTimeline, priority: 14},

	// Task details.
	{key: "tab", label: "move between the task's tabs (shift+tab goes back)", scope: scopePanel, context: ctxTaskDetails, hint: "tab tabs", priority: 1},
	{key: "]", label: "move to the next tab ([ goes back)", scope: scopePanel, context: ctxTaskDetails, priority: 2},
	{key: "down", label: "select a task-detail section (↑/↓); pgup/pgdn scrolls that section", scope: scopePanel, context: ctxTaskDetails, hint: "↑/↓ sections", priority: 3},
	// The pull-request section's two keys (task 052.6, decision 2). Both only
	// reach a browser: neither writes anything in vincent, which is what
	// keeps §15's "read-only inspector" true in the sense it was written.
	{op: keymap.Browser, key: "o", label: "open this task's pull request in a browser", scope: scopePanel, context: ctxTaskDetails, hint: "o pull request", priority: 4, github: true, term: termBrowser},
	{key: "P", label: "push this task's branch to origin and open its pull request — the title, body and draft flag are editable first", scope: scopePanel, context: ctxTaskDetails, hint: "P open a PR", priority: 5, github: true},
	// Walking a fan-out (issue #316). `l` and `U` are registered on every tab
	// that can name a lane, because a reader standing anywhere in a parent's
	// workspace means the same thing by them; the tabs differ only in which
	// lane `l` resolves to (taskView.laneJump).
	{op: keymap.Lane, key: "l", label: "open the selected fan-out lane's workspace", scope: scopePanel, context: ctxTaskDetails, priority: 6, term: termLane},
	{op: keymap.NextFailure, key: "N", label: "jump to the next failure: the newest failed attempt of each step in execution order, on the Output tab at the line it failed on — a blocked lane opens, esc comes back", scope: scopePanel, context: ctxTaskDetails, priority: 6, term: termNextFail},
	{key: openIssueKey, label: "open the task's linked issue — esc comes back to this tab", scope: scopePanel, context: ctxTaskDetails, hint: "i issue", priority: 8},
	{key: "U", label: "open this lane's parent task", scope: scopePanel, context: ctxTaskDetails, priority: 7},
	{key: "d", label: "open the Diff tab", scope: scopePanel, context: ctxTaskDetails, priority: 9},

	// Output pane.
	{key: "tab", label: "move between the task's tabs in the order the strip draws them (shift+tab goes back)", scope: scopePanel, context: ctxOutput, hint: "tab tabs", priority: 1},
	{key: "]", label: "move to the next tab ([ goes back)", scope: scopePanel, context: ctxOutput, priority: 2},
	{key: "f", label: "follow the live output again (f/G)", scope: scopePanel, context: ctxOutput, hint: "f follow", priority: 2},
	{key: "v", label: "show more or less: quiet → compact → normal → verbose (tool lines, then reasoning, then unrecognized lines)", scope: scopePanel, context: ctxOutput, hint: "v detail", priority: 3},
	{key: rawToggleKey, label: "show the assistant's original Markdown instead of the rendered view", scope: scopePanel, context: ctxOutput, hint: "ctrl+o raw", priority: 6},
	{key: copyPickKey, label: "copy an assistant message, its plain text, or one of its code blocks", scope: scopePanel, context: ctxOutput, hint: "ctrl+y copy", priority: 7},
	// No vocabulary term, for copyPickKey's reason: the key raises a picker,
	// and the picker's enter is what opens (task 112 decision 1).
	{key: linkPickKey, label: "list the links in the assistant's messages — open one in a browser or copy it", scope: scopePanel, context: ctxOutput, hint: "ctrl+l links", priority: 8},
	{op: keymap.Editor, key: "e", label: "open this attempt's whole transcript in $EDITOR (the pane holds only the end of it)", scope: scopePanel, context: ctxOutput, hint: "e transcript", priority: 5, term: termEditor},
	{key: "down", label: "scroll (↑/↓; scrolling up pauses follow)", scope: scopePanel, context: ctxOutput, hint: "↑/↓ scroll", priority: 4},
	{key: "right", label: "select which attempt's output to show (←/→ or h/l)", scope: scopePanel, context: ctxOutput, hint: "←/→ attempts", priority: 4},
	{key: "enter", label: "unfold or fold the newest tool call in view — its output, under its one line (a click does it for the call under the pointer)", scope: scopePanel, context: ctxOutput, hint: "enter unfold", priority: 9},
	{key: "<", label: "previous fan-out lane in the Output pane", scope: scopePanel, context: ctxOutput, priority: 9},
	{key: ">", label: "next fan-out lane in the Output pane", scope: scopePanel, context: ctxOutput, priority: 10},
	{op: keymap.Lane, key: "l", label: "open the selected fan-out lane's workspace", scope: scopePanel, context: ctxOutput, priority: 11, term: termLane},
	{op: keymap.NextFailure, key: "N", label: "jump to the next failure: the newest failed attempt of each step in execution order, on the Output tab at the line it failed on — a blocked lane opens, esc comes back", scope: scopePanel, context: ctxOutput, priority: 11, term: termNextFail},
	{key: openIssueKey, label: "open the task's linked issue — esc comes back to this tab", scope: scopePanel, context: ctxOutput, hint: "i issue", priority: 8},
	{key: "U", label: "open this lane's parent task", scope: scopePanel, context: ctxOutput, priority: 12},
	{key: "d", label: "open the Diff tab", scope: scopePanel, context: ctxOutput, priority: 14},
	{key: "g", label: "jump to the top of the output (g/home; G/end follows)", scope: scopePanel, context: ctxOutput, priority: 15},

	// Diff tab. Its own context rather than more ctxOutput rows: the diff is a
	// list of files and the output is a stream of lines, so ↑/↓ mean different
	// things on the two tabs and a single row could only describe one of them.
	// `]` is repeated here because the way back must stay on screen.
	{key: "tab", label: "move between the task's tabs in the order the strip draws them (shift+tab goes back)", scope: scopePanel, context: ctxDiff, hint: "tab tabs", priority: 1},
	{key: "]", label: "move to the next tab ([ goes back)", scope: scopePanel, context: ctxDiff, priority: 2},
	{key: "down", label: "move between the files (↑/↓); the pane scrolls to keep the file in view", scope: scopePanel, context: ctxDiff, hint: "↑/↓ files", priority: 2},
	{key: "enter", label: "expand or collapse the file under the cursor (space and →/← too)", scope: scopePanel, context: ctxDiff, hint: "enter fold", priority: 3},
	{key: "O", label: "expand every file", scope: scopePanel, context: ctxDiff, hint: "O/C fold all", priority: 4},
	{key: "C", label: "collapse every file — which is how the tab opens", scope: scopePanel, context: ctxDiff, priority: 5, aliased: true},
	{op: keymap.Lane, key: "l", label: "open the selected fan-out lane's workspace", scope: scopePanel, context: ctxDiff, priority: 6, term: termLane},
	{op: keymap.NextFailure, key: "N", label: "jump to the next failure: the newest failed attempt of each step in execution order, on the Output tab at the line it failed on — a blocked lane opens, esc comes back", scope: scopePanel, context: ctxDiff, priority: 6, term: termNextFail},
	{key: openIssueKey, label: "open the task's linked issue — esc comes back to this tab", scope: scopePanel, context: ctxDiff, hint: "i issue", priority: 8},
	{key: "U", label: "open this lane's parent task", scope: scopePanel, context: ctxDiff, priority: 7},
	{key: "d", label: "switch between the whole task's diff and what the attempt selected on the rail changed", scope: scopePanel, context: ctxDiff, hint: "d by attempt", priority: 9},
	{key: "pgdown", label: "page the diff (pgup/pgdown, ctrl+d/ctrl+u)", scope: scopePanel, context: ctxDiff, priority: 10},
	{key: "shift+right", label: "pan long lines (shift+←/→)", scope: scopePanel, context: ctxDiff, priority: 11},

	// New task.
	{key: "enter", label: "open the focused field's editor or picker", scope: scopePanel, context: ctxNewTask, hint: "enter edit field", priority: 2},
	{op: keymap.Editor, key: "e", label: "edit the description in $EDITOR", scope: scopePanel, context: ctxNewTask, hint: "e $EDITOR", priority: 3, term: termEditor},
	{key: "+", label: "nudge the priority (+/-; higher runs first)", scope: scopePanel, context: ctxNewTask, hint: "+/- priority", priority: 4},
	{op: keymap.Refresh, key: "R", label: "re-probe the adapters (the list is otherwise cache-served)", scope: scopePanel, context: ctxNewTask, hint: "R re-probe", priority: 5, term: termRefresh},
	{key: "ctrl+s", label: "create the task — from any field or open list, which is kept first", scope: scopePanel, context: ctxNewTask, hint: "ctrl+s create", priority: 1},
	{key: "left", label: "on the wide form: move to the workflow list (→ or enter comes back)", scope: scopePanel, context: ctxNewTask, hint: "← workflows", priority: 2, wideForm: true},
	{op: keymap.Filter, key: "/", label: "on the wide form's workflow list: filter the workflows", scope: scopePanel, context: ctxNewTask, priority: 7, term: termFilter, wideForm: true},
	// The free-text row of an open picker (task 093). Registered on each of
	// the four contexts a picker that offers one can be open in: a model or an
	// effort newer than the catalog is the case it exists for (§9.6), and the
	// picker printed the offer without the registry ever naming a key for it.
	{op: keymap.FreeText, key: "t", label: "in an open list: type a value it does not offer", scope: scopePanel, context: ctxNewTask, priority: 6, term: termFreeText},

	// The new-task form's Fields editor (§5.3, task 093).
	{key: "down", label: "move between the fields (↑/↓ or j/k)", scope: scopePanel, context: ctxNewTaskFields, hint: "↑/↓ fields", priority: 1},
	{key: "enter", label: "edit the row under the cursor, toggle a declared boolean, or open a declared enum's list", scope: scopePanel, context: ctxNewTaskFields, hint: "enter edit", priority: 2},
	{key: "left", label: "step a declared enum or boolean through its values in place (←/→)", scope: scopePanel, context: ctxNewTaskFields, hint: "←/→ choose", priority: 3},
	// `a` and `d` are the vocabulary's add and draft-remove: nothing here is
	// persisted until the task is created, which is why the removal wears the
	// lower case that `D` was freed from on projects.
	{op: keymap.Add, key: "a", label: "add a custom field", scope: scopePanel, context: ctxNewTaskFields, hint: "a add", priority: 4, term: termAdd},
	{op: keymap.DraftRemove, key: "d", label: "remove the custom field under the cursor (a workflow-declared one cannot be removed)", scope: scopePanel, context: ctxNewTaskFields, hint: "d remove", priority: 5, term: termDraftDrop},
	{key: "esc", label: "close the editor and keep the fields", scope: scopePanel, context: ctxNewTaskFields, hint: "esc done", priority: 6},
	// ctrl+s submits from in here too (§15, task 020; #797): the editor's
	// rows are kept first, exactly as esc keeps them.
	{key: "ctrl+s", label: "keep the fields and create the task", scope: scopePanel, context: ctxNewTaskFields, hint: "ctrl+s create", priority: 7},

	// Archived tasks (§15 view 10, task 092). The board's own keys — ↑/↓,
	// enter, /, g, space, V and the fold keys — work here unchanged and are
	// listed under ctxTasks; these are the three the mode adds. There is no
	// §6 action key: an archived task offers no `available_actions`, which is
	// what makes the workspace `enter` opens read-only for free.
	{key: "tab", label: "switch to archived chats", scope: scopePanel, context: ctxArchived, hint: "tab chats", priority: 0, noPalette: true},
	{op: keymap.OpenRow, key: "enter", label: "open the archived task's workspace — read-only, because an archived task offers no actions", scope: scopePanel, context: ctxArchived, hint: "enter open", priority: 1, term: termOpenRow},
	{op: keymap.Delete, key: "D", label: "delete permanently (asks first — the row, its step attempts and its transcripts go; optionally its branch, unless the branch has commits)", scope: scopePanel, context: ctxArchived, hint: "D delete", priority: 2, term: termDelete},
	// `s` rather than `d` (task 093): cycling the date window is the same
	// gesture the chats board and the pull-request list already spell `s`, and
	// `d` had to stop meaning "change what is listed" on the one screen whose
	// other letter deletes for good. `s` is skip as a §6 action, and the two
	// are provably disjoint — Archived is never a `from` state in taskstate's
	// transition table, so an archived row offers no action at all.
	{op: keymap.Scope, key: "s", label: "cycle the window: last 7 days → last 30 days → all time", scope: scopePanel, context: ctxArchived, hint: "s window", priority: 3, term: termScope},
	{key: ">", label: "next page of the archive (< goes back)", scope: scopePanel, context: ctxArchived, hint: "</> page", priority: 4},
	{op: keymap.Filter, key: "/", label: "filter by id, title, state or issue", scope: scopePanel, context: ctxArchived, priority: 5, term: termFilter},
	{key: "space", label: "select this task for the bulk delete (V selects every row the filter is showing)", scope: scopePanel, context: ctxArchived, priority: 6},
	{op: keymap.Refresh, key: "R", label: "reload the board", scope: scopePanel, context: ctxArchived, priority: 7, term: termRefresh},

	// The connection screen. `r` is retry-connecting, a fixed key (task 118
	// decision 8); the footer draws it from its retry segment, so the row is
	// for `?` and `:`.
	{key: "r", label: "retry the connection now (while reconnecting, restart the daemon if it stays down)", scope: scopePanel, context: ctxConnection, priority: 1},

	// Archived chats. The same three keys, on the same reasoning: a chat that
	// ended is history, and this is the only place it can be discarded.
	{key: "tab", label: "switch to archived tasks", scope: scopePanel, context: ctxArchivedChats, hint: "tab tasks", priority: 0, noPalette: true},
	{op: keymap.OpenRow, key: "enter", label: "open the archived chat's workspace — read-only, the conversation as it ended", scope: scopePanel, context: ctxArchivedChats, hint: "enter open", priority: 1, term: termOpenRow},
	{op: keymap.Delete, key: "D", label: "delete permanently (asks first — the row, its turns and its transcripts go; optionally its branch, unless the branch has commits)", scope: scopePanel, context: ctxArchivedChats, hint: "D delete", priority: 2, term: termDelete},
	{op: keymap.Scope, key: "s", label: "cycle the window: last 7 days → last 30 days → all time", scope: scopePanel, context: ctxArchivedChats, hint: "s window", priority: 3, term: termScope},
	{key: ">", label: "next page of the archive (< goes back)", scope: scopePanel, context: ctxArchivedChats, hint: "</> page", priority: 4},
	{op: keymap.Filter, key: "/", label: "filter by title, agent or branch", scope: scopePanel, context: ctxArchivedChats, priority: 5, term: termFilter},
	{op: keymap.Refresh, key: "R", label: "reload the board", scope: scopePanel, context: ctxArchivedChats, priority: 6, term: termRefresh},
	{op: keymap.ExportChat, key: "alt+e", label: chatExportLabel, scope: scopePanel, context: ctxArchivedChats, priority: 7},
	{op: keymap.Rename, key: "r", label: "rename the chat (the branch keeps its name)", scope: scopePanel, context: ctxArchivedChats, priority: 8, term: termRename},

	// Chats board. Its tab row exists only while chats is one of tui.tabs:
	// mainTabRows drops it otherwise.
	{key: "tab", label: mainTabLabel, scope: scopePanel, context: ctxChats, hint: "tab next tab", priority: 0, mainTab: true, noPalette: true},
	{op: keymap.OpenRow, key: "enter", label: "open the chat's workspace", scope: scopePanel, context: ctxChats, hint: "enter open", priority: 1, term: termOpenRow},
	{op: keymap.New, key: "n", label: "start a chat in the project you are looking at", scope: scopePanel, context: ctxChats, hint: "n new", priority: 2},
	// `A` rather than `a` (task 093): archiving is archiving, and the tasks
	// board next door has spelled it `A` since §6 named the action.
	{op: keymap.Archive, key: "A", label: "archive the chat (asks first — the worktree is removed)", scope: scopePanel, context: ctxChats, hint: "A archive", priority: 3, term: termArchive},
	{op: keymap.Filter, key: "/", label: "filter by title, agent or branch", scope: scopePanel, context: ctxChats, hint: "/ filter", priority: 4, term: termFilter},
	// `s` is the pull-request board's key for the same idea (task 064
	// decision 9): terminal chats are hidden by default (issue #298), and
	// this is the way back to them.
	{op: keymap.Scope, key: "s", label: "cycle the listing between live, ended (archived, handed-off or closed), and all", scope: scopePanel, context: ctxChats, hint: "s listing", priority: 5, term: termScope},
	{op: keymap.Refresh, key: "R", label: "reload the board", scope: scopePanel, context: ctxChats, hint: "R reload", priority: 6, term: termRefresh},
	// `S` rather than `/` (task 158 decision 4): `/` filters the list in
	// view, and this searches every chat's words, archived ones included.
	{op: keymap.ChatSearch, key: "S", label: "search every chat's title, prompts and answers, live and archived", scope: scopePanel, context: ctxChats, hint: "S search", priority: 7},

	// Chat search (task 158).
	{key: "enter", label: "open the chat — on a turn's match, scrolled to that turn", scope: scopePanel, context: ctxChatSearch, hint: "enter open", priority: 1},
	{key: "down", label: "move between results (↑/↓, pgup/pgdown)", scope: scopePanel, context: ctxChatSearch, hint: "↑/↓ move", priority: 2},
	{op: keymap.ExportChat, key: "alt+e", label: chatExportLabel, scope: scopePanel, context: ctxChats, priority: 7},
	// Task 161 decision 6: the prompt is prefilled with the title, and the
	// branch keeps the slug it was cut with.
	{op: keymap.Rename, key: "r", label: "rename the chat (the branch keeps its name)", scope: scopePanel, context: ctxChats, hint: "r rename", priority: 8, term: termRename},

	// Chat workspace.
	{key: "enter", label: "send the message — while a turn runs it is queued, and runs after that turn ends done (task 156)", scope: scopePanel, context: ctxChat, hint: "enter send", priority: 1},
	{op: keymap.Steer, key: "ctrl+s", label: "steer the running turn — unavailable on every agent for now; the composer says why", scope: scopePanel, context: ctxChat, hint: "ctrl+s steer", priority: 15},
	{key: chatQueueEditKey, label: "on an empty composer, edit the newest queued message (enter saves, esc stops editing)", scope: scopePanel, context: ctxChat, priority: 16},
	{key: chatQueueDeleteKey, label: "delete the queued message being edited", scope: scopePanel, context: ctxChat, priority: 17},
	{key: chatQueueResumeKey, label: "resume a paused queue — on an idle chat the next message starts now, paused or not", scope: scopePanel, context: ctxChat, priority: 18},
	{key: chatQueueClearKey, label: "clear the queue: delete every queued message", scope: scopePanel, context: ctxChat, priority: 19},
	// ctrl+j is the key the row names (issue #500) because a legacy terminal
	// can always send it: there shift+enter arrives as a bare CR, which is
	// enter, which sends. All three are the textarea's own newline binding,
	// set in newChatView; keymap.fixed records the other two as aliases.
	{key: "ctrl+j", label: "insert a newline in the message (shift+enter and alt+enter too, where the terminal sends them)", scope: scopePanel, context: ctxChat, hint: "ctrl+j newline", priority: 11},
	{key: "ctrl+x", label: "stop the running turn (its process tree is killed)", scope: scopePanel, context: ctxChat, hint: "ctrl+x stop", priority: 2},
	// ctrl+r rather than `v` (task 071 decision 4): the composer owns every
	// printable key, so a letter would be typed into the message.
	{key: "ctrl+r", label: "how much of the conversation to show (quiet → compact → normal → verbose)", scope: scopePanel, context: ctxChat, hint: "ctrl+r detail", priority: 4},
	// Ahead of ctrl+r (issue #801 F5): with enter or ctrl+x it is what an
	// 80-column footer still has room for, and a reader who cannot find the
	// way back through the conversation cannot read it.
	{key: "pgup", label: "scroll the conversation back (pgdown goes forward)", scope: scopePanel, context: ctxChat, hint: "pgup/pgdown scroll", priority: 3},
	// alt+shift+g since task 163 decision 1, beside alt+g's jump to the start:
	// ctrl+g is the composer's editor now.
	{key: "alt+shift+g", label: "jump to the live end and follow it again", scope: scopePanel, context: ctxChat, hint: "alt+shift+g live", priority: 5},
	// The two $EDITOR operations (task 163). Combinations for the reason
	// ctrl+r is one: the composer owns every printable key.
	{op: keymap.ComposeEditor, key: "ctrl+g", label: "write the message in $EDITOR — what you save comes back into the composer, unsent", scope: scopePanel, context: ctxChat, hint: "ctrl+g editor", priority: 12},
	{op: keymap.ChatMarkdown, key: "alt+v", label: "open the whole conversation as Markdown in $EDITOR, read-only", scope: scopePanel, context: ctxChat, hint: "alt+v markdown", priority: 20},
	// ctrl+t rather than `h`, for the reason ctrl+r is a combination: the
	// composer owns every printable key (task 074).
	{key: "ctrl+t", label: "hand the worktree and branch to a new task (the chat ends)", scope: scopePanel, context: ctxChat, hint: "ctrl+t hand off", priority: 6},
	// The linked chat's way out (task 119), and the free chat's hand-off
	// never shows beside it: chatView.liveBindings keeps exactly one of the
	// two, because a chat opened on a task can be closed and nothing else,
	// and a free chat cannot be closed at all. ctrl+q for the reason ctrl+t
	// is a combination — the composer owns every printable key — and because
	// the textarea binds ctrl+w, ctrl+d and ctrl+k to editing.
	{key: chatCloseKey, label: "close this chat — a chat opened on a task, whose worktree and branch stay and which unlocks, or on an issue, whose main worktree is freed for the next main task (asks first)", scope: scopePanel, context: ctxChat, hint: "ctrl+q close", priority: 6},
	{key: "esc", label: "back to where the chat was opened", scope: scopePanel, context: ctxChat, hint: "esc back", priority: 7},
	{key: rawToggleKey, label: "show the assistant's original Markdown instead of the rendered view", scope: scopePanel, context: ctxChat, hint: "ctrl+o raw", priority: 8},
	{key: copyPickKey, label: "copy an assistant message, its plain text, or one of its code blocks", scope: scopePanel, context: ctxChat, hint: "ctrl+y copy", priority: 9},
	{key: linkPickKey, label: "list the links in the assistant's messages — open one in a browser or copy it", scope: scopePanel, context: ctxChat, hint: "ctrl+l links", priority: 10},
	// tab is free in the composer — bubbles' textarea binds no tab, and this
	// view matched none until now — so it is the taught key, the way every
	// terminal spells completion (task 124 decision 69). f2 rides in the
	// label as the alias for a terminal that swallows tab, the way ctrl+j's
	// row already names shift+enter and alt+enter; keymap.fixed records it.
	{key: "tab", label: "the skills this chat's agent can run — type to filter, enter or tab inserts one (f2 too, where the terminal swallows tab)", scope: scopePanel, context: ctxChat, hint: "tab skills", priority: 12},
	// The line step and the jump to the start (issue #801 F8). alt rather
	// than shift or ctrl: the composer owns ↑/↓, shift+↑/↓ select in it,
	// ctrl+home/ctrl+end move to its ends, and ctrl+↑ is a desktop shortcut
	// on macOS — alt+↑/↓ and alt+g are the ones the textarea leaves free.
	{key: "alt+up", label: "scroll the conversation back one line (alt+down goes forward)", scope: scopePanel, context: ctxChat, hint: "alt+↑/↓ line", priority: 13},
	{key: "alt+g", label: "jump to the start of the conversation (alt+shift+g goes back to the live end)", scope: scopePanel, context: ctxChat, hint: "alt+g top", priority: 14},
	// The way back to a question esc put away (issue #801 F7). Offered only
	// while one is put away: chatView.liveBindings drops it otherwise.
	{key: "alt+a", label: "reopen the agent's question after esc closed it (what you picked is kept)", scope: scopePanel, context: ctxChat, hint: "alt+a answer", priority: 1},
	// The context gauge's way out (task 157 decision 4). Offered only for an
	// agent with a compact command (`/compact` on claude), and only while
	// the chat is idle: chatView.liveBindings drops it otherwise.
	{op: keymap.Compact, key: "alt+k", label: "compact the agent's context — sends its compact command (/compact on claude) as the next message (asks first)", scope: scopePanel, context: ctxChat, hint: "alt+k compact", priority: 15},
	// alt rather than a letter for ctrl+r's reason — the composer owns every
	// printable key — and alt+e because the textarea binds no alt+e.
	{op: keymap.ExportChat, key: "alt+e", label: chatExportLabel, scope: scopePanel, context: ctxChat, priority: 15},
	{op: keymap.ChatModel, key: "alt+p", label: "pick the model and effort the chat's next turns run on", scope: scopePanel, context: ctxChat, hint: "alt+p model", priority: 15},
	{op: keymap.ChatMode, key: "shift+tab", label: "toggle the permission mode between restricted and full-auto (asks before full-auto)", scope: scopePanel, context: ctxChat, hint: "shift+tab mode", priority: 15},

	// Task 160: ctrl+f hands the keyboard to the conversation with the
	// search line open. A combination, because the composer owns every
	// printable key (071 decision 4, amended by 160 decision 1).
	{key: chatSearchKey, label: "search the conversation — the conversation takes the keyboard, esc gives it back", scope: scopePanel, context: ctxChat, hint: "ctrl+f search", priority: 20},

	// The conversation with the keyboard (task 160 decision 1). Single
	// letters here, because nothing is typed: the draft waits untouched.
	{key: "down", label: "move the line cursor (j/k and ↑/↓)", scope: scopePanel, context: ctxChatConversation, hint: "j/k move", priority: 1},
	{key: "enter", label: "expand or fold the tool call under the cursor — it draws as at verbose, the level stays", scope: scopePanel, context: ctxChatConversation, hint: "enter expand", priority: 2},
	{op: keymap.Filter, key: "/", label: "search the conversation again", scope: scopePanel, context: ctxChatConversation, hint: "/ search", priority: 3, term: termFilter},
	{key: "n", label: "the next match (N the previous); both wrap", scope: scopePanel, context: ctxChatConversation, hint: "n/N match", priority: 4},
	{key: "}", label: "jump to the next prompt you sent ({ the previous)", scope: scopePanel, context: ctxChatConversation, hint: "{/} prompt", priority: 5},
	{key: "g", label: "jump to the start of the conversation (G to the live end)", scope: scopePanel, context: ctxChatConversation, priority: 6},
	{key: "pgup", label: "scroll the conversation back (pgdown goes forward; alt+↑/↓ a line)", scope: scopePanel, context: ctxChatConversation, priority: 7},
	// alt+shift+g, the composer's own follow key since task 163 decision 1.
	{key: "alt+shift+g", label: "jump to the live end and follow it again", scope: scopePanel, context: ctxChatConversation, priority: 8},
	{key: "i", label: "give the keyboard back to the composer (esc too)", scope: scopePanel, context: ctxChatConversation, hint: "i/esc compose", priority: 9},
	{key: "esc", label: "give the keyboard back to the composer — it never leaves the chat from here", scope: scopePanel, context: ctxChatConversation, priority: 10, aliased: true},

	// The search line. It owns every key: a printable one types.
	{key: "enter", label: "search every turn, the ones not loaded yet too", scope: scopePanel, context: ctxChatFind, hint: "enter search", noPalette: true},
	{key: "esc", label: "close the search line; the conversation keeps the keyboard", scope: scopePanel, context: ctxChatFind, hint: "esc close", noPalette: true},

	// The chat workspace's skill list, while it is open. noPalette for the
	// reason the daemon view's skill offer's rows are: a key that only
	// exists inside a popup cannot be replayed into one that is shut.
	{key: "down", label: "move the highlight (↑/↓); nothing is highlighted until the first press", scope: scopePanel, context: ctxChatSkills, noPalette: true},
	{key: "tab", label: "insert the highlighted skill, or the top match when none is highlighted (f2 too)", scope: scopePanel, context: ctxChatSkills, noPalette: true},
	{key: "enter", label: "insert the highlighted skill; with none highlighted, send the message as typed", scope: scopePanel, context: ctxChatSkills, noPalette: true},
	{key: "backspace", label: "shorten the filter, and close the list when it is already empty", scope: scopePanel, context: ctxChatSkills, noPalette: true},
	{key: "esc", label: "close the list and keep the draft exactly as it was", scope: scopePanel, context: ctxChatSkills, noPalette: true},

	// The same list, opened inline by the draft's own sigil token (task
	// 124.14). No backspace row: here it edits the draft and the list
	// recomputes from it, which is the whole of decision 94. noPalette for
	// the reason the browse rows are.
	{key: "down", label: "move the highlight (↑/↓) through the matches; nothing is highlighted until the first press", scope: scopePanel, context: ctxChatSkillsInline, noPalette: true},
	{key: "tab", label: "complete the token you are typing with the highlighted skill, or the top match (f2 too)", scope: scopePanel, context: ctxChatSkillsInline, noPalette: true},
	{key: "enter", label: "accept the highlighted skill; with none highlighted, send the message as typed", scope: scopePanel, context: ctxChatSkillsInline, noPalette: true},
	{key: "esc", label: "close the list, and give ↑/↓ back to editing the draft", scope: scopePanel, context: ctxChatSkillsInline, noPalette: true},

	// The `@` file picker, opened by the draft's own token (task 126.11).
	// The same five presses as the inline skills list and none of its words:
	// what is completed is a path, and the text inserted is the daemon's.
	// noPalette for the reason those rows are.
	{key: "down", label: "move the highlight (↑/↓) through the matching files; nothing is highlighted until the first press", scope: scopePanel, context: ctxChatFiles, noPalette: true},
	{key: "tab", label: "complete the @ token with the highlighted file, or the top match (f2 too)", scope: scopePanel, context: ctxChatFiles, noPalette: true},
	{key: "enter", label: "insert the highlighted file's mention; with none highlighted, send the message as typed", scope: scopePanel, context: ctxChatFiles, noPalette: true},
	{key: "esc", label: "close the file list, and give ↑/↓ back to editing the draft", scope: scopePanel, context: ctxChatFiles, noPalette: true},

	// The project picker (task 132.4): every printable key types into its
	// filter, so its own keys are the ones a filter does not type.
	{key: "down", label: "move the highlight (↑/↓)", scope: scopePanel, context: ctxProjectPicker, noPalette: true},

	// The export form (task 159). noPalette for the project picker's reason:
	// a key that only exists inside a popup cannot be replayed into one that
	// is shut.
	{key: "down", label: "move between format, diff and path (↑/↓, tab/shift+tab); once written, between the copy choices", scope: scopePanel, context: ctxChatExport, noPalette: true},
	{key: "space", label: "on format, cycle md → html → json; on include diff, toggle it (←/→ too)", scope: scopePanel, context: ctxChatExport, noPalette: true},
	{key: "enter", label: "export and write the file; once written, copy the highlighted choice — the path, or the Markdown", scope: scopePanel, context: ctxChatExport, noPalette: true},
	{key: "esc", label: "close the form", scope: scopePanel, context: ctxChatExport, noPalette: true},
	{key: "enter", label: "switch to the highlighted project — or, on the last row, open the project overview", scope: scopePanel, context: ctxProjectPicker, noPalette: true},
	{key: "esc", label: "close the picker without switching", scope: scopePanel, context: ctxProjectPicker, noPalette: true},
	{key: "pgdown", label: "page the list (pgup/pgdown, home/end)", scope: scopePanel, context: ctxProjectPicker, noPalette: true},

	// New chat.
	{key: "ctrl+s", label: "create the chat and open it (with the first message, when one is typed)", scope: scopePanel, context: ctxNewChat, hint: "ctrl+s create", priority: 1},
	// The form opens on the message row, where enter creates the chat and
	// sends the message in one request (task 161 decision 5); on every
	// other row it opens the list or moves on, as before.
	{key: "enter", label: "on the message: create the chat and send it; elsewhere: open the focused field's list, or move on from a text field", scope: scopePanel, context: ctxNewChat, hint: "enter send", priority: 2},
	{key: "tab", label: "next field (shift+tab goes back)", scope: scopePanel, context: ctxNewChat, hint: "tab next", priority: 3},
	{key: "left", label: "step the agent, or a short model or effort list, in place (← →); enter opens its list", scope: scopePanel, context: ctxNewChat, hint: "← →  step", priority: 4},
	{key: "esc", label: "close an open list, else discard the draft", scope: scopePanel, context: ctxNewChat, hint: "esc cancel", priority: 5},
	{op: keymap.FreeText, key: "t", label: "in an open list: type a value it does not offer", scope: scopePanel, context: ctxNewChat, priority: 6, term: termFreeText},
	{key: "ctrl+j", label: "insert a newline in the first message (shift+enter and alt+enter too, where the terminal sends them)", scope: scopePanel, context: ctxNewChat, priority: 7},

	// Projects.
	{op: keymap.Add, key: "a", label: "register a repository", scope: scopePanel, context: ctxProjects, hint: "a add", priority: 1, term: termAdd},
	// Enter selects rather than edits (task 132 decision 42): the overview
	// is how a project is reached as well as managed, so `e` alone edits.
	// `e` keeps no row of its own: the vocabulary gives `e` to $EDITOR, so
	// it is named here and recorded in keymap.fixed, as the daemon view's is.
	{key: "enter", label: "select the highlighted project and go back to the view you came from — on a needs-you row, open that task (e edits the project)", scope: scopePanel, context: ctxProjects, hint: "enter select", priority: 2},
	{key: "tab", label: "move between the project table and the needs-you list", scope: scopePanel, context: ctxProjects, hint: "tab needs you", priority: 3},
	// `D` rather than `d` (task 093): this destroys a persisted record after a
	// confirmation, which is what `D` means on both archived boards. `d` is
	// left meaning "drop a row from an unsaved draft" and nothing else.
	{op: keymap.Delete, key: "D", label: "remove the project (asks first; its task rows go with it)", scope: scopePanel, context: ctxProjects, hint: "D remove", priority: 4, term: termDelete},
	{op: keymap.Filter, key: "/", label: "filter by name or path", scope: scopePanel, context: ctxProjects, hint: "/ filter", priority: 5, term: termFilter},

	// The project form (#803): it owns the keyboard while it is open.
	{key: "down", label: "move between the fields (↑/↓ or tab)", scope: scopePanel, context: ctxProjectForm, hint: "↑/↓ fields", priority: 1},
	{key: "enter", label: "edit the field, or open its list", scope: scopePanel, context: ctxProjectForm, hint: "enter edit", priority: 2},
	{key: "ctrl+s", label: "save the project", scope: scopePanel, context: ctxProjectForm, hint: "ctrl+s save", priority: 3},
	{key: "esc", label: "close the form without saving", scope: scopePanel, context: ctxProjectForm, hint: "esc cancel", priority: 4},
	{key: "enter", label: "finish the field, or pick the highlighted workflow", scope: scopePanel, context: ctxProjectFormField, hint: "enter done", priority: 1},
	{key: "ctrl+s", label: "finish the field, or close the list, and save the project", scope: scopePanel, context: ctxProjectFormField, hint: "ctrl+s save", priority: 2},
	{key: "esc", label: "finish the field, or close the list without picking", scope: scopePanel, context: ctxProjectFormField, hint: "esc done", priority: 3},

	// Workflows.
	{key: "enter", label: "show the entry's steps, or explore its graph where the steps are already on screen", scope: scopePanel, context: ctxWorkflows, hint: "enter open", priority: 1},
	{op: keymap.Editor, key: "e", label: "open the workflow file in $EDITOR (the view updates when you save)", scope: scopePanel, context: ctxWorkflows, hint: "e edit", priority: 2, term: termEditor},
	{op: keymap.Refresh, key: "R", label: "re-read the registry", scope: scopePanel, context: ctxWorkflows, hint: "R reload", priority: 3, term: termRefresh},
	{key: "g", label: "draw the workflow as a control-flow graph", scope: scopePanel, context: ctxWorkflows, hint: "g graph", priority: 4},
	// The structured editor (task 065). `e` keeps meaning $EDITOR — it means
	// that in every other context too, and one key with two meanings
	// depending on the view is the confusion decision 6 refused.
	{key: "i", label: "edit the entry in a structured form", scope: scopePanel, context: ctxWorkflows, hint: "i form", priority: 5},
	{op: keymap.Add, key: "a", label: "create a workflow in a chosen scope", scope: scopePanel, context: ctxWorkflows, hint: "a new", priority: 6, term: termAdd},
	{key: "f", label: "fork the entry into another scope, where it shadows the original", scope: scopePanel, context: ctxWorkflows, hint: "f fork", priority: 7},

	// The structured editor sub-layer.
	{key: "up/down", label: "move between rows", scope: scopePanel, context: ctxWorkflowEditor, hint: "↑↓ rows", priority: 1},
	{key: "enter", label: "edit the row, cycle its values, or descend into a nested body", scope: scopePanel, context: ctxWorkflowEditor, hint: "enter edit", priority: 2},
	{op: keymap.Refresh, key: "R", label: "re-read the file (what a stale-write 409 offers)", scope: scopePanel, context: ctxWorkflowEditor, hint: "R reload", priority: 3, term: termRefresh},
	{key: "esc", label: "leave the nested body, then the editor", scope: scopePanel, context: ctxWorkflowEditor, hint: "esc back", priority: 4},
	// The structural keys (issue #320). They sit behind the takeover, so they
	// never reach the workflows list's own `a`/`i`/`f`, and an insert carries
	// the new entry's required fields: the daemon re-parses the whole file on
	// every PATCH, so a step the form itself created must be valid on arrival.
	{op: keymap.Add, key: "a", label: "add a step, lane or declared field after the one under the cursor", scope: scopePanel, context: ctxWorkflowEditor, hint: "a add", priority: 5, term: termAdd},
	{op: keymap.DraftRemove, key: "d", label: "remove the item under the cursor (asks first)", scope: scopePanel, context: ctxWorkflowEditor, hint: "d remove", priority: 6, term: termDraftDrop},
	{key: "K/J", label: "move the item up (K) or down (J); k and j still move the cursor", scope: scopePanel, context: ctxWorkflowEditor, hint: "K/J move", priority: 7},
	{key: "ctrl+s", label: "in the multi-line pane: save (enter inserts a newline there)", scope: scopePanel, context: ctxWorkflowEditor, hint: "ctrl+s save", priority: 8},
	{op: keymap.FreeText, key: "t", label: "in an open list: type a value it does not offer", scope: scopePanel, context: ctxWorkflowEditor, priority: 9, term: termFreeText},

	// The create/fork prompt. noPalette for the reason the step-detail
	// modal's rows are: a form holds the keyboard on a text field, so `:`
	// types a colon rather than opening the palette. The prompt prints its
	// own keys instead.
	{key: "tab", label: "move between the scope and the file name", scope: scopePanel, context: ctxWorkflowCreate, hint: "tab row", priority: 1, noPalette: true},
	{key: "left/right", label: "choose the scope", scope: scopePanel, context: ctxWorkflowCreate, hint: "←→ scope", priority: 2, noPalette: true},
	{key: "enter", label: "write the file and open it in the editor", scope: scopePanel, context: ctxWorkflowCreate, hint: "enter create", priority: 3, noPalette: true},
	{key: "esc", label: "close the prompt", scope: scopePanel, context: ctxWorkflowCreate, hint: "esc cancel", priority: 4, noPalette: true},

	// The graph sub-layer (task 017). Arrows move the selection and the
	// viewport follows it; panning is deliberately a separate, shifted key,
	// so an arrow can never scroll the canvas out from under the cursor.
	{key: "down", label: "move the selection (↑/↓/←/→ or hjkl); the view follows it", scope: scopePanel, context: ctxWorkflowGraph, hint: "↑↓←→ select", priority: 1},
	{key: "shift+down", label: "pan the canvas (shift+↑/↓/←/→); pgup/pgdn page it", scope: scopePanel, context: ctxWorkflowGraph, hint: "⇧ pan", priority: 2},
	{key: "tab", label: "walk the nodes in source order (shift+tab goes back)", scope: scopePanel, context: ctxWorkflowGraph, hint: "tab next", priority: 3},
	{op: keymap.Editor, key: "e", label: "open the workflow file in $EDITOR (the graph redraws when you save)", scope: scopePanel, context: ctxWorkflowGraph, hint: "e edit", priority: 4, term: termEditor},
	{op: keymap.Refresh, key: "R", label: "re-fetch this workflow's definition", scope: scopePanel, context: ctxWorkflowGraph, hint: "R reload", priority: 5, term: termRefresh},
	{key: "enter", label: "open the selected node in full — every field it carries, including the prompt or run body", scope: scopePanel, context: ctxWorkflowGraph, hint: "enter detail", priority: 2},

	// The step-detail modal (task 053): these exist only while the popup owns
	// the keyboard, and the popup prints them itself — they are here so ?
	// stays complete. `e` and `R` carry one layer further for the reason they
	// carried into the graph: editing is the path from wherever you are
	// reading, and R is the layer's only recovery.
	{key: "down", label: "scroll the detail (↑/↓; pgup/pgdn page it)", scope: scopePanel, context: ctxWorkflowStep, noPalette: true},
	{op: keymap.Editor, key: "e", label: "open the workflow file in $EDITOR (the detail redraws when you save)", scope: scopePanel, context: ctxWorkflowStep, noPalette: true, term: termEditor},
	{op: keymap.Refresh, key: "R", label: "re-fetch this workflow's definition", scope: scopePanel, context: ctxWorkflowStep, noPalette: true, term: termRefresh},
	{key: "esc", label: "close the detail, back to the graph with the same node selected", scope: scopePanel, context: ctxWorkflowStep, noPalette: true},

	// The task workspace's workflow tab (task 051). The graph is this task's
	// own snapshot with its run state on it; `tab` cycles the workspace's
	// tabs here, so the source-order node walk is deliberately absent.
	{key: "down", label: "move the selection (↑/↓/←/→ or hjkl); the view follows it", scope: scopePanel, context: ctxTaskWorkflow, hint: "↑↓←→ select", priority: 1},
	{key: "shift+down", label: "pan the canvas (shift+↑/↓/←/→); pgup/pgdn page it", scope: scopePanel, context: ctxTaskWorkflow, hint: "⇧ pan", priority: 2},
	// Here the lane is the one under the graph cursor, which is the only tab
	// that can point at a lane the failure does not blame.
	{key: openIssueKey, label: "open the task's linked issue — esc comes back to this tab", scope: scopePanel, context: ctxTaskWorkflow, hint: "i issue", priority: 8},
	{op: keymap.Lane, key: "l", label: "open the selected fan-out lane's workspace", scope: scopePanel, context: ctxTaskWorkflow, priority: 4, term: termLane},
	{op: keymap.NextFailure, key: "N", label: "jump to the next failure: the newest failed attempt of each step in execution order, on the Output tab at the line it failed on — a blocked lane opens, esc comes back", scope: scopePanel, context: ctxTaskWorkflow, priority: 4, term: termNextFail},
	{key: "tab", label: "move between the task's tabs in the order the strip draws them (shift+tab goes back)", scope: scopePanel, context: ctxTaskWorkflow, hint: "tab tabs", priority: 5},
	{key: "U", label: "open this lane's parent task", scope: scopePanel, context: ctxTaskWorkflow, priority: 6},
	{key: "d", label: "open the Diff tab", scope: scopePanel, context: ctxTaskWorkflow, priority: 8},

	// The task workspace's Step Details tab (issue #323). Unconditional, so
	// `6` always means it — which is what lets the pull-request tab keep
	// costing nothing when it is absent even though it no longer owns `6`.
	{key: "down", label: "select an attempt (↑/↓ or ←/→, which move it everywhere else too); pgup/pgdn scrolls the facts", scope: scopePanel, context: ctxTaskStepDetails, hint: "↑/↓ attempts", priority: 2},
	{key: "tab", label: "move between the task's tabs in the order the strip draws them (shift+tab goes back)", scope: scopePanel, context: ctxTaskStepDetails, hint: "tab tabs", priority: 3},
	{key: openIssueKey, label: "open the task's linked issue — esc comes back to this tab", scope: scopePanel, context: ctxTaskStepDetails, hint: "i issue", priority: 8},
	{op: keymap.Lane, key: "l", label: "open the selected fan-out lane's workspace", scope: scopePanel, context: ctxTaskStepDetails, priority: 4, term: termLane},
	{op: keymap.NextFailure, key: "N", label: "jump to the next failure: the newest failed attempt of each step in execution order, on the Output tab at the line it failed on — a blocked lane opens, esc comes back", scope: scopePanel, context: ctxTaskStepDetails, priority: 4, term: termNextFail},
	{key: "U", label: "open this lane's parent task", scope: scopePanel, context: ctxTaskStepDetails, priority: 5},
	{key: "d", label: "open the Diff tab", scope: scopePanel, context: ctxTaskStepDetails, priority: 7},

	// The task workspace's Pull Request tab (task 068). The tab is on the
	// strip only when this task has a live link and the integration is
	// usable, which is why `7` is registered here and nowhere else: on a task
	// with no pull request the key does nothing, and a row promising
	// otherwise would be the lie the availability filter exists to prevent.
	{key: "down", label: "select a check (↑/↓ or j/k)", scope: scopePanel, context: ctxTaskPull, hint: "↑/↓ checks", priority: 2, github: true},
	// `enter` rather than `c` (task 093). The cursor is on a check and `enter`
	// opens what the cursor is on; `c` intercepted here meant cancel was
	// unreachable from a tab whose own footer advertised it. There is no
	// refresh key either, for the same reason and the other half of the same
	// bug: `R` is repair on every tab of this workspace and does not move, and
	// the tab already re-reads on its own timer — which is what the row below
	// used to say in its parenthesis while the key shadowed retry.
	{op: keymap.OpenRow, key: "enter", label: "open the selected check's own page in a browser", scope: scopePanel, context: ctxTaskPull, hint: "enter open check", priority: 3, github: true, term: termOpenRow},
	{op: keymap.Browser, key: "o", label: "open the pull request in a browser", scope: scopePanel, context: ctxTaskPull, hint: "o open PR", priority: 4, github: true, term: termBrowser},
	{key: "u", label: "unlink this pull request from the task — the refusal sticks, and the reconciler will not link it again", scope: scopePanel, context: ctxTaskPull, hint: "u unlink", priority: 6, github: true},
	// Not github-gated: `l` is a jump between two tasks vincent owns, and it
	// means the same thing on this tab whether or not a lane has a pull
	// request of its own.
	{op: keymap.Lane, key: "l", label: "open the selected fan-out lane's workspace", scope: scopePanel, context: ctxTaskPull, priority: 7, term: termLane},
	{op: keymap.NextFailure, key: "N", label: "jump to the next failure: the newest failed attempt of each step in execution order, on the Output tab at the line it failed on — a blocked lane opens, esc comes back", scope: scopePanel, context: ctxTaskPull, priority: 7, term: termNextFail},
	{key: "tab", label: "move between the task's tabs in the order the strip draws them (shift+tab goes back)", scope: scopePanel, context: ctxTaskPull, hint: "tab tabs", priority: 12},
	{key: "U", label: "open this lane's parent task", scope: scopePanel, context: ctxTaskPull, priority: 13},
	{key: "d", label: "open the Diff tab", scope: scopePanel, context: ctxTaskPull, priority: 15},
	// The confirmed writes (task 068 decisions 1, 3 and 4; issue #387). All
	// four are surface-local, so none carries a term, and none moves a §6
	// letter (task 093 decision 1): `X` is one key for close and reopen the
	// way `p` is for pause and resume, and re-run is ctrl+r because `r` is
	// retry. `X`, `i` and ctrl+r mean other things on the triggers takeover,
	// the workflows list, the daemon view and a chat — none of which can be
	// open beside a task workspace. Each row is absent wherever its write
	// cannot apply, not present and refusing.
	{key: "m", label: "merge this pull request — choose merge, squash or rebase in the confirmation (none is preselected); pinned to the head commit whose checks are shown", scope: scopePanel, context: ctxTaskPull, hint: "m merge", priority: 8, github: true},
	{key: "X", label: "close this pull request without merging, or reopen a closed one (asks first)", scope: scopePanel, context: ctxTaskPull, hint: "X close", priority: 9, github: true},
	{key: "i", label: "comment on this pull request — the popup is the confirmation, ctrl+s posts", scope: scopePanel, context: ctxTaskPull, hint: "i comment", priority: 10, github: true},
	{key: "ctrl+r", label: "re-run the failed jobs of the selected check's GitHub Actions run (asks first; only on a failed Actions row)", scope: scopePanel, context: ctxTaskPull, hint: "ctrl+r re-run failed jobs", priority: 11, github: true},

	// Pull requests (task 052.6). Link and unlink live only here: they are the
	// two actions that write vincent's own column, and they belong on the one
	// screen that can see a pull request no task claims — the case they exist
	// for.
	{key: "enter", label: "open the workspace of the task that claims this pull request", scope: scopePanel, context: ctxPullRequests, hint: "enter task", priority: 1},
	{op: keymap.Browser, key: "o", label: "open the selected pull request in a browser", scope: scopePanel, context: ctxPullRequests, hint: "o browser", priority: 2, term: termBrowser},
	// `a` rather than `c` (task 093): creating a task from a pull request is
	// creating, which is what `a` means on projects, workflows and the editor.
	// It leaves `c` meaning cancel and nothing else, registry-wide.
	{op: keymap.Add, key: "a", label: "create a task from this pull request — it runs on the pull request's head branch, and the form is editable first", scope: scopePanel, context: ctxPullRequests, hint: "a new task", priority: 3, term: termAdd},
	{key: "l", label: "link this pull request to a task in the same project", scope: scopePanel, context: ctxPullRequests, hint: "l link", priority: 4},
	// The takeover's half of task 069. This screen has no task rows — its
	// question is "what is open in this project" — so the offer is a
	// picker of tasks that have a branch and no pull request, and choosing
	// one opens that task's workspace with the form up.
	{key: "P", label: "open a pull request for a task that has none — pick the task, then push its branch and create it", scope: scopePanel, context: ctxPullRequests, hint: "P open a PR", priority: 5, github: true},
	{key: "u", label: "unlink it (asks first — the refusal sticks, and the reconciler will not link it again)", scope: scopePanel, context: ctxPullRequests, hint: "u unlink", priority: 5},
	{op: keymap.Scope, key: "s", label: "cycle the listing between open, closed and all", scope: scopePanel, context: ctxPullRequests, hint: "s state", priority: 6, term: termScope},
	{op: keymap.Refresh, key: "R", label: "re-list this project", scope: scopePanel, context: ctxPullRequests, hint: "R refresh", priority: 7, term: termRefresh},
	{key: "down", label: "move the selection (↑/↓)", scope: scopePanel, context: ctxPullRequests, priority: 8},
	{op: keymap.Filter, key: "/", label: "filter by number, title or branch", scope: scopePanel, context: ctxPullRequests, priority: 9, term: termFilter},

	// Daemon.
	{op: keymap.Refresh, key: "R", label: "re-read the daemon info, the config and the log", scope: scopePanel, context: ctxDaemon, hint: "R refresh", priority: 1, term: termRefresh},
	{key: "f", label: "follow the end of the log again (f/G)", scope: scopePanel, context: ctxDaemon, hint: "f follow", priority: 2},
	{key: "down", label: "scroll the log (↑/↓)", scope: scopePanel, context: ctxDaemon, hint: "↑/↓ scroll", priority: 3},
	// The config block became editable in task 060. tab is what says whether
	// ↑/↓ mean the config list or the log pane — the view has two scrollable
	// things now and one pair of arrows.
	{key: "enter", label: "open Settings, where every configuration key is changed", scope: scopePanel, context: ctxDaemon, hint: "enter settings", priority: 4},
	// Task 082. The only key in vincent that leads to a write outside its own
	// data dir, which is why the label says whose file it is: what it opens
	// shows the exact JSON before anything happens, and the same key takes it
	// back out again.
	{key: "i", label: "make vincent claude's status line, or remove it — the exact JSON is shown first", scope: scopePanel, context: ctxDaemon, hint: "i status line", priority: 6},
	// Task 095. `S` and not `i`: both keys lead to a write outside vincent's
	// own directories, but they are two different operations on two
	// different targets, and clause 1 of the vocabulary lets a key be shared
	// only when it means the same one. `s` was unavailable — it is the
	// vocabulary's "cycle a listing's scope" — and `S` carries no term.
	{key: "S", label: "install the agent skills vincent publishes — the exact command is shown first", scope: scopePanel, context: ctxDaemon, hint: "S skills", priority: 7},
	// Task 151. Another write outside vincent's own directories, so another
	// key that says what it will do before it does it.
	{key: "U", label: "set up Nerd Font icons — what gets downloaded is shown first", scope: scopePanel, context: ctxDaemon, hint: "U icons", priority: 8},

	// The Nerd Font flow owns the keyboard and prints its own key line.
	{key: "enter", label: "install the font, or turn the icons on once you can see them", scope: scopePanel, context: ctxFonts, noPalette: true},
	{key: "y", label: "turn the icons on", scope: scopePanel, context: ctxFonts, noPalette: true},
	{key: "n", label: "keep Unicode icons", scope: scopePanel, context: ctxFonts, noPalette: true},
	{key: "u", label: "remove the font vincent installed and go back to Unicode icons", scope: scopePanel, context: ctxFonts, noPalette: true},
	{key: "esc", label: "close without changing anything", scope: scopePanel, context: ctxFonts, noPalette: true},

	// The §9.8 offer: it owns the keyboard while it is open and prints its
	// own key line, so these are here to keep ? complete.
	{key: "enter", label: "run the install (one npx per skill; the first needs network)", scope: scopePanel, context: ctxSkills, noPalette: true},
	{key: "n", label: "not now — remembered, and S brings it back", scope: scopePanel, context: ctxSkills, noPalette: true},
	{key: "esc", label: "close without installing anything", scope: scopePanel, context: ctxSkills, noPalette: true},

	// The status-line flow (task 082): the same, for `i`.
	{key: "enter", label: "write the change shown (y also confirms)", scope: scopePanel, context: ctxStatusLine, noPalette: true},
	{key: "n", label: "not now — remembered, and i brings it back", scope: scopePanel, context: ctxStatusLine, noPalette: true},
	{key: "esc", label: "close without writing anything", scope: scopePanel, context: ctxStatusLine, noPalette: true},

	// The Settings screen (task 152).
	{key: "down", label: "move between the settings (↑/↓ or j/k)", scope: scopePanel, context: ctxSettings, hint: "↑/↓ setting", priority: 1},
	{key: "left", label: "change the selected setting (←/→)", scope: scopePanel, context: ctxSettings, hint: "←/→ change", priority: 2},
	{key: "enter", label: "edit the selected setting", scope: scopePanel, context: ctxSettings, hint: "enter edit", priority: 3},
	{key: "tab", label: "next category (shift+tab goes back)", scope: scopePanel, context: ctxSettings, hint: "tab category", priority: 4},
	{op: keymap.Filter, key: "/", label: "search every setting", scope: scopePanel, context: ctxSettings, hint: "/ search", priority: 5, term: termFilter},
	{key: "backspace", label: "reset the selected setting to its default", scope: scopePanel, context: ctxSettings, hint: "⌫ reset", priority: 6},
	{op: keymap.Editor, key: "e", label: "open config.yaml in $EDITOR", scope: scopePanel, context: ctxSettings, priority: 7, term: termEditor},
	{op: keymap.Refresh, key: "R", label: "re-read the configuration", scope: scopePanel, context: ctxSettings, priority: 8, term: termRefresh},
	{key: "esc", label: "clear the search, else leave Settings", scope: scopePanel, context: ctxSettings, priority: 9},

	// The config editor: it owns the keyboard while it is open and prints its
	// own key line, so these are here to keep ? complete.
	{key: "left", label: "choose a value, for a key with a fixed vocabulary (←/→)", scope: scopePanel, context: ctxConfigEdit, noPalette: true},
	{key: "enter", label: "apply the change; the daemon validates and writes config.yaml", scope: scopePanel, context: ctxConfigEdit, hint: "enter apply", priority: 1, noPalette: true},
	{key: "y", label: "confirm a key that decides what the daemon executes or exposes", scope: scopePanel, context: ctxConfigEdit, noPalette: true},
	{key: "esc", label: "close the editor without saving (the confirmation returns to the field)", scope: scopePanel, context: ctxConfigEdit, hint: "esc cancel", priority: 2, noPalette: true},

	// Triggers (§15 view 11, task 096 decision 25). `space` flips `enabled`
	// and asks before it turns one on (decision 19); `T` and `X` are the two
	// dry runs, which fire nothing; `B` opens the global switch in the daemon
	// view's editor rather than flipping it from here.
	// The issues list (§15 view 12, task 130.9). The footer's order is what
	// a narrow pane keeps (#796): moving, filtering and filing come before
	// the browser, which only an imported row can use — remoteOnly keeps it
	// out of the footer elsewhere, and `?` and the palette still list it.
	{key: "tab", label: mainTabLabel, scope: scopePanel, context: ctxIssues, hint: "tab next tab", priority: 0, mainTab: true, noPalette: true},
	{op: keymap.OpenRow, key: "enter", label: "open the selected issue", scope: scopePanel, context: ctxIssues, hint: "enter open", priority: 1, term: termOpenRow},
	{key: "down", label: "move up and down the column (↑/↓)", scope: scopePanel, context: ctxIssues, hint: "↑↓ card", priority: 2},
	{op: keymap.Filter, key: "/", label: "filter by id, title, label or kind", scope: scopePanel, context: ctxIssues, hint: "/ filter", priority: 3, term: termFilter},
	// The writes (task 130.12). `n` is §15's "make a new one here", the
	// chats board's precedent; `i` is the trigger list's "edit in a form";
	// `X` offers what the daemon's available_actions allow and nothing else.
	{op: keymap.New, key: "n", label: "file a new issue in this project", scope: scopePanel, context: ctxIssues, hint: "n new", priority: 4},
	{op: keymap.Browser, key: "o", label: "open an imported issue's page in a browser", scope: scopePanel, context: ctxIssues, hint: "o browser", priority: 5, term: termBrowser, remoteOnly: true},
	{op: keymap.Scope, key: "s", label: "show or hide the done lane", scope: scopePanel, context: ctxIssues, hint: "s done", priority: 6, term: termScope},
	{op: keymap.Refresh, key: "R", label: "re-read the issues — never a sync with a remote tracker", scope: scopePanel, context: ctxIssues, hint: "R refresh", priority: 7, term: termRefresh},
	{op: keymap.Add, key: "a", label: "create a task from the selected issue — the form is prefilled from it and editable first, and offers a separate worktree once the issue has a main branch", scope: scopePanel, context: ctxIssues, hint: "a new task", priority: 8, term: termAdd},
	{key: issueEditKey, label: "edit the selected issue in a form", scope: scopePanel, context: ctxIssues, hint: "i edit", priority: 9},
	{key: issueStateKey, label: "close or reopen the selected issue — only what the daemon offers", scope: scopePanel, context: ctxIssues, hint: "X close/reopen", priority: 10},
	// Moving a card (task 145): the lane is still derived, so `m` runs the
	// action the picked lane is derived from, and lists what it cannot do.
	{key: issueMoveKey, label: issueMoveLabel, scope: scopePanel, context: ctxIssues, hint: "m move", priority: 10},
	{op: keymap.Delete, key: "D", label: "delete the issue permanently (asks first; never deletes at the issue's source)", scope: scopePanel, context: ctxIssues, priority: 11, term: termDelete},
	// `R` above only re-reads (task 130 decision 2); `S` is the explicit
	// request to the project's issue source, `vincent issue sync` from the
	// list, offered only where there is a sync pass to wake (task 142.14).
	{key: issueSyncKey, label: "sync this project's issues from its issue source now — sweeping out the ones deleted there", scope: scopePanel, context: ctxIssues, hint: "S sync", priority: 11, syncable: true},
	// Crossing the columns (task 150.2), a main key of the board.
	{key: "left", label: "move to the next column with a card (←/→)", scope: scopePanel, context: ctxIssues, hint: "←/→ lane", priority: 3},
	{key: "right", label: "move to the next column with a card (←/→)", scope: scopePanel, context: ctxIssues, priority: 13, aliased: true},
	// Paging (#796), the issue detail's and the timeline's keys.
	{key: "pgdown", label: "page the column (pgup/pgdown, home/g, end/G)", scope: scopePanel, context: ctxIssues, priority: 16},
	// Handoffs (task 137.11). The selection is the task table's shape (task
	// 011) over the pending hand-off cards only; liveBindings withholds
	// ready, handoff and return where they do not apply to the row.
	{key: issueSelectKey, label: "select this pending hand-off issue for a handoff (any other card is refused, with the reason)", scope: scopePanel, context: ctxIssues, hint: "space select", priority: 17},
	{key: issueSelectAllKey, label: "select every pending hand-off issue the filter is showing, or clear the selection", scope: scopePanel, context: ctxIssues, priority: 18},
	{op: keymap.Handoff, key: "I", label: "hand the selected issues off — or the cursor's pending one — in a form: workflow, grant, title, paused", scope: scopePanel, context: ctxIssues, hint: "I hand off", priority: 8, term: termHandoff},
	{op: keymap.Ready, key: ".", label: "mark an idle in-progress issue ready, or a pending hand-off issue not ready", scope: scopePanel, context: ctxIssues, hint: ". ready", priority: 8, term: termReady},
	{op: keymap.Return, key: ",", label: "return a claimed or waiting issue from its handoff (asks first)", scope: scopePanel, context: ctxIssues, hint: ", return", priority: 8, term: termReturn},
	// A chat on the issue (task 140.9): the `chat` operation, `T` as on a
	// task, because the gesture is the same — talk it through in its
	// worktree, or reopen the chat already doing so.
	{op: keymap.Chat, key: "T", label: issueChatLabel, scope: scopePanel, context: ctxIssues, hint: "T chat", priority: 8},
	{op: keymap.ChatWith, key: "alt+t", label: issueChatWithLabel, scope: scopePanel, context: ctxIssues, priority: 8},
	// The issue detail (§15 view 13).
	{op: keymap.OpenRow, key: "enter", label: "open the selected row — a task's workspace or a chat, and esc comes back here; on the Issue tab's tasks-and-chats line, switch to the Work tab", scope: scopePanel, context: ctxIssue, hint: "enter open", priority: 1, term: termOpenRow},
	{op: keymap.Browser, key: "o", label: "open an imported issue's page in a browser", scope: scopePanel, context: ctxIssue, hint: "o browser", priority: 2, term: termBrowser},
	{key: rawToggleKey, label: "show the description's and comments' original Markdown instead of the rendered view", scope: scopePanel, context: ctxIssue, hint: "ctrl+o raw", priority: 3},
	{key: linkPickKey, label: "list the links in the description and comments, and the source page — open one in a browser or copy it", scope: scopePanel, context: ctxIssue, hint: "ctrl+l links", priority: 4},
	{key: copyPickKey, label: "copy the title, the description, a comment or the source URL", scope: scopePanel, context: ctxIssue, hint: "ctrl+y copy", priority: 4},
	{op: keymap.Refresh, key: "R", label: "re-read the issue — never a sync with a remote tracker", scope: scopePanel, context: ctxIssue, hint: "R refresh", priority: 5, term: termRefresh},
	// The page is scroll-first (#792): the arrows scroll it, moving the
	// selection only among the Main worktree and task rows on screen.
	{key: "down", label: "scroll a line; on screen, move the selection between the Main worktree section and the linked tasks first (↑/↓)", scope: scopePanel, context: ctxIssue, hint: "↑↓ scroll", priority: 6},
	{key: "pgdown", label: "scroll a page (pgup/pgdown, space)", scope: scopePanel, context: ctxIssue, priority: 7},
	{key: "ctrl+d", label: "scroll half a page (ctrl+u/ctrl+d)", scope: scopePanel, context: ctxIssue, priority: 7},
	{key: "end", label: "jump to the end of the page (home/end, g/G)", scope: scopePanel, context: ctxIssue, priority: 7},
	// The Issue | Work strip (task 144): tab/] forward, shift+tab/[ back,
	// wrapping; no digit keys (decision 2).
	{key: "tab", label: "move between the Issue and Work tabs (shift+tab goes back)", scope: scopePanel, context: ctxIssue, hint: "tab work", priority: 6},
	{key: "]", label: "move to the next tab ([ goes back)", scope: scopePanel, context: ctxIssue, priority: 7},
	{op: keymap.New, key: "n", label: "file a new issue in this issue's project", scope: scopePanel, context: ctxIssue, hint: "n new", priority: 8},
	{op: keymap.Add, key: "a", label: "create a task from this issue — the form is prefilled from it and editable first, and offers a separate worktree once the issue has a main branch", scope: scopePanel, context: ctxIssue, hint: "a new task", priority: 8, term: termAdd},
	{key: issueEditKey, label: "edit the issue in a form", scope: scopePanel, context: ctxIssue, hint: "i edit", priority: 9},
	{key: issueStateKey, label: "close or reopen the issue — only what the daemon offers", scope: scopePanel, context: ctxIssue, hint: "X close/reopen", priority: 10},
	{key: issueMoveKey, label: issueMoveLabel, scope: scopePanel, context: ctxIssue, hint: "m move", priority: 10},
	// The thread (task 130 decision 24, 130.16): a local comment, written in
	// $EDITOR. Withheld on an issue whose remote is live, which takes
	// no comment here — nothing is ever posted to its remote.
	{op: keymap.Comment, key: "W", label: "write a comment in $EDITOR — saved empty, nothing is posted; never on an issue mirrored from a remote tracker", scope: scopePanel, context: ctxIssue, hint: "W comment", priority: 10, term: termComment},
	{op: keymap.Delete, key: "D", label: "delete the issue permanently (asks first; never deletes at the issue's source)", scope: scopePanel, context: ctxIssue, priority: 11, term: termDelete},
	{op: keymap.Ready, key: ".", label: "mark an idle in-progress issue ready, or a pending hand-off issue not ready", scope: scopePanel, context: ctxIssue, hint: ". ready", priority: 8, term: termReady},
	{op: keymap.Return, key: ",", label: "return a claimed or waiting issue from its handoff (asks first)", scope: scopePanel, context: ctxIssue, hint: ", return", priority: 8, term: termReturn},
	{op: keymap.Chat, key: "T", label: issueChatLabel, scope: scopePanel, context: ctxIssue, hint: "T chat", priority: 8},
	{op: keymap.ChatWith, key: "alt+t", label: issueChatWithLabel, scope: scopePanel, context: ctxIssue, priority: 8},

	// The issue form and prompt own the keyboard and print their own key
	// lines, so these are here to keep ? complete.
	{key: "down", label: "move between the issue's fields (↑/↓, tab)", scope: scopePanel, context: ctxIssueForm, noPalette: true},
	{key: "enter", label: "edit the field, or open its list; a field mirrored from a remote tracker says so instead", scope: scopePanel, context: ctxIssueForm, noPalette: true},
	{op: keymap.Editor, key: "e", label: "on the description: open it in $EDITOR", scope: scopePanel, context: ctxIssueForm, noPalette: true, term: termEditor},
	{op: keymap.FreeText, key: "t", label: "in an open list: type a label or kind it does not offer", scope: scopePanel, context: ctxIssueForm, noPalette: true, term: termFreeText},
	{op: keymap.Refresh, key: "R", label: "re-read the issue after a conflict, keeping your edits where they still apply", scope: scopePanel, context: ctxIssueForm, noPalette: true, term: termRefresh},
	{key: "ctrl+s", label: "save — a create, or an edit of only the fields that changed", scope: scopePanel, context: ctxIssueForm, noPalette: true},
	{key: "esc", label: "close the form (asks first when something is unsaved)", scope: scopePanel, context: ctxIssueForm, noPalette: true},
	{key: "enter", label: "pick the action, lane, close reason or duplicate target", scope: scopePanel, context: ctxIssuePrompt, noPalette: true},
	{key: "y", label: "confirm the close, reopen, delete, return or cancel", scope: scopePanel, context: ctxIssuePrompt, noPalette: true},
	{key: "esc", label: "cancel — nothing is written", scope: scopePanel, context: ctxIssuePrompt, noPalette: true},
	// The handoff form (task 137.11) owns the keyboard and prints its own
	// key line, like the issue form.
	{key: "down", label: "move between the handoff's fields (↑/↓, tab)", scope: scopePanel, context: ctxHandoffForm, noPalette: true},
	{key: "space", label: "toggle a grant or start paused; on the workflow, the next one (←/→ cycle it)", scope: scopePanel, context: ctxHandoffForm, noPalette: true},
	{key: "ctrl+s", label: "start the handoff — submitting is the consent to the grant", scope: scopePanel, context: ctxHandoffForm, noPalette: true},
	{key: "esc", label: "close the form (asks first when something is set)", scope: scopePanel, context: ctxHandoffForm, noPalette: true},

	{key: "down", label: "move the selection (↑/↓)", scope: scopePanel, context: ctxTriggers, priority: 1},
	{key: "enter", label: "open the selected trigger in the form (i also opens it)", scope: scopePanel, context: ctxTriggers, hint: "enter edit", priority: 2},
	{key: "space", label: "enable or disable the selected trigger — enabling asks first", scope: scopePanel, context: ctxTriggers, hint: "space on/off", priority: 3},
	{op: keymap.Add, key: "a", label: "create a trigger from a starter", scope: scopePanel, context: ctxTriggers, hint: "a add", priority: 4, term: termAdd},
	{op: keymap.Editor, key: "e", label: "open the trigger's file in $EDITOR", scope: scopePanel, context: ctxTriggers, priority: 5, term: termEditor},
	{op: keymap.Delete, key: "D", label: "delete the trigger's file — its ledger is kept (asks first)", scope: scopePanel, context: ctxTriggers, priority: 6, term: termDelete},
	{key: "T", label: "dry-run a sample event through the trigger's filter; fires nothing", scope: scopePanel, context: ctxTriggers, hint: "T test", priority: 7},
	{key: "X", label: "poll the source once and judge what it returns; fires nothing", scope: scopePanel, context: ctxTriggers, hint: "X poll", priority: 8},
	{key: "tab", label: "move between the trigger list and its delivery ledger", scope: scopePanel, context: ctxTriggers, hint: "tab ledger", priority: 9},
	{key: "B", label: "open triggers.enabled, the global switch, in the daemon view's editor", scope: scopePanel, context: ctxTriggers, priority: 10},
	{op: keymap.Refresh, key: "R", label: "re-read the triggers and the ledger", scope: scopePanel, context: ctxTriggers, hint: "R refresh", priority: 11, term: termRefresh},
	{op: keymap.Filter, key: "/", label: "filter by id, source or action", scope: scopePanel, context: ctxTriggers, priority: 12, term: termFilter},

	{key: "down", label: "move through the deliveries (↑/↓)", scope: scopePanel, context: ctxTriggerLedger, priority: 1},
	{key: "enter", label: "open the task the delivery created or acted on", scope: scopePanel, context: ctxTriggerLedger, hint: "enter open task", priority: 2},
	{key: "tab", label: "back to the trigger list", scope: scopePanel, context: ctxTriggerLedger, hint: "tab list", priority: 3},
	{op: keymap.Refresh, key: "R", label: "re-read the triggers and the ledger", scope: scopePanel, context: ctxTriggerLedger, hint: "R refresh", priority: 4, term: termRefresh},

	// The form, the create prompt and the dry runs own the keyboard and print
	// their own key lines, so these are here to keep ? complete.
	{key: "down", label: "move between the trigger's fields (↑/↓)", scope: scopePanel, context: ctxTriggerForm, noPalette: true},
	{key: "enter", label: "edit the field; the daemon validates and writes the file", scope: scopePanel, context: ctxTriggerForm, noPalette: true},
	{op: keymap.Refresh, key: "R", label: "re-read the trigger from disk", scope: scopePanel, context: ctxTriggerForm, noPalette: true, term: termRefresh},
	{key: "esc", label: "close the form", scope: scopePanel, context: ctxTriggerForm, noPalette: true},
	{key: "tab", label: "move between the starter's inputs", scope: scopePanel, context: ctxTriggerCreate, noPalette: true},
	{key: "enter", label: "write the new trigger — it is created disabled", scope: scopePanel, context: ctxTriggerCreate, noPalette: true},
	{key: "esc", label: "close the prompt", scope: scopePanel, context: ctxTriggerCreate, noPalette: true},
	{key: "ctrl+s", label: "run the dry run on the sample event", scope: scopePanel, context: ctxTriggerDryRun, noPalette: true},
	{key: "esc", label: "close the dry run", scope: scopePanel, context: ctxTriggerDryRun, noPalette: true},

	// Answer form: these exist only while the popup owns the keyboard, and
	// the popup prints them itself — they are here so ? stays complete.
	{key: "space", label: "pick an option (toggles, for a multi-select question)", scope: scopePanel, context: ctxForm, noPalette: true},
	// `t` rather than `e` (task 093). `e` means $EDITOR everywhere else,
	// including on the two popups that sit beside this one, so the free-text
	// meaning is the one that moved: it is the same gesture the four pickers
	// offer, and it now has the same key on all five.
	{op: keymap.FreeText, key: "t", label: "type your own answer — options are suggestions, never a list", scope: scopePanel, context: ctxForm, noPalette: true, term: termFreeText},
	{key: "enter", label: "submit the answer; the attempt resumes where it stopped", scope: scopePanel, context: ctxForm, noPalette: true},
	{key: "ctrl+t", label: "read this task's details without leaving the form — the picks and the typed answer are kept (ctrl+t again, or esc, comes back)", scope: scopePanel, context: ctxForm, noPalette: true},
	{key: "esc", label: "close the popup without answering (what you picked is kept)", scope: scopePanel, context: ctxForm, noPalette: true},

	// Repair form: as with the answer form, these exist only while the popup
	// owns the keyboard and it prints them itself.
	{key: "enter", label: "edit the row under the cursor — the prompt, or the agent/model/effort list", scope: scopePanel, context: ctxRepairForm, noPalette: true},
	{op: keymap.Editor, key: "e", label: "write the repair prompt in $EDITOR", scope: scopePanel, context: ctxRepairForm, noPalette: true, term: termEditor},
	{op: keymap.FreeText, key: "t", label: "in the agent/model/effort list: type a value it does not offer", scope: scopePanel, context: ctxRepairForm, noPalette: true, term: termFreeText},
	{key: "ctrl+s", label: "start the repair agent", scope: scopePanel, context: ctxRepairForm, noPalette: true},
	{key: "ctrl+t", label: "read this task's details without leaving the form — the draft is kept (ctrl+t again, or esc, comes back)", scope: scopePanel, context: ctxRepairForm, noPalette: true},
	{key: "esc", label: "close the popup without repairing (the draft is discarded)", scope: scopePanel, context: ctxRepairForm, noPalette: true},

	// Follow-up form: same again — the popup owns the keyboard while it is
	// open and prints its own key line.
	{key: "enter", label: "edit the row under the cursor — the kind of follow-up, what to run, or the agent/model/effort list", scope: scopePanel, context: ctxFollowUpForm, noPalette: true},
	{op: keymap.Editor, key: "e", label: "write the prompt or command in $EDITOR", scope: scopePanel, context: ctxFollowUpForm, noPalette: true, term: termEditor},
	{op: keymap.FreeText, key: "t", label: "in the workflow/agent/model/effort list: type a value it does not offer", scope: scopePanel, context: ctxFollowUpForm, noPalette: true, term: termFreeText},
	{key: "ctrl+s", label: "start the follow-up run", scope: scopePanel, context: ctxFollowUpForm, noPalette: true},
	{key: "ctrl+t", label: "read this task's details without leaving the form — the draft is kept (ctrl+t again, or esc, comes back)", scope: scopePanel, context: ctxFollowUpForm, noPalette: true},
	{key: "esc", label: "close the popup without running anything (the draft is discarded)", scope: scopePanel, context: ctxFollowUpForm, noPalette: true},

	// The pull-request form (task 052.6, task 069). Same again: the popup owns the
	// keyboard while it is open and prints its own key line.
	{key: "enter", label: "edit the row under the cursor — the pull request's title or its body, or toggle the draft row", scope: scopePanel, context: ctxCreatePR, noPalette: true},
	{key: "space", label: "toggle draft / ready for review, on the draft row", scope: scopePanel, context: ctxCreatePR, noPalette: true},
	{op: keymap.Editor, key: "e", label: "write the body in $EDITOR", scope: scopePanel, context: ctxCreatePR, noPalette: true, term: termEditor},
	{key: "ctrl+s", label: "push the branch to origin and open the pull request", scope: scopePanel, context: ctxCreatePR, noPalette: true},
	{key: "ctrl+o", label: "open GitHub's own new-pull-request page with this prefill instead", scope: scopePanel, context: ctxCreatePR, noPalette: true},
	{key: "esc", label: "close the popup without sending anything (the draft is discarded)", scope: scopePanel, context: ctxCreatePR, noPalette: true},

	// The Pull Request tab's confirmations (task 068.4). Same again: each
	// owns the keyboard while it is up and prints its own key line, so these
	// are here to keep ? complete.
	{key: "left", label: "choose the merge method — merge, squash or rebase (←/→); none is chosen until you pick one", scope: scopePanel, context: ctxPullMerge, noPalette: true},
	{key: "y", label: "merge with the chosen method at the head commit shown — does nothing until a method is chosen, or while the head has moved", scope: scopePanel, context: ctxPullMerge, noPalette: true},
	{key: "n", label: "close without merging (esc too; enter never confirms)", scope: scopePanel, context: ctxPullMerge, noPalette: true},
	{key: "esc", label: "close without merging", scope: scopePanel, context: ctxPullMerge, noPalette: true},
	{key: "enter", label: "type the comment in the popup", scope: scopePanel, context: ctxPullComment, noPalette: true},
	{op: keymap.Editor, key: "e", label: "write the comment in $EDITOR", scope: scopePanel, context: ctxPullComment, noPalette: true, term: termEditor},
	{key: "ctrl+s", label: "post the comment — the popup is the confirmation; a blank one is not sent", scope: scopePanel, context: ctxPullComment, noPalette: true},
	{key: "esc", label: "stop typing; pressed again, close without posting (the comment is discarded)", scope: scopePanel, context: ctxPullComment, noPalette: true},
	{key: "y", label: "yes — send the close, reopen or re-run the prompt names", scope: scopePanel, context: ctxPullConfirm, hint: "y yes", priority: 1, noPalette: true},
	{key: "n", label: "no — nothing is sent (any other key declines too)", scope: scopePanel, context: ctxPullConfirm, hint: "n no", priority: 2, noPalette: true},
}

// isHomeContext reports whether a context belongs to the board/task daily loop
// — the surfaces a task's actions and forms belong to, as opposed to a
// management takeover.
func isHomeContext(ctx bindingContext) bool {
	switch ctx {
	case ctxTasks, ctxTaskOverview, ctxTimeline, ctxTaskDetails, ctxOutput, ctxDiff,
		ctxTaskWorkflow, ctxTaskStepDetails, ctxTaskPull:
		return true
	default:
		return false
	}
}

// bindingsFor returns the panel rows owned by one context, in registry order.
func bindingsFor(ctx bindingContext) []binding {
	out := make([]binding, 0, 8)
	for _, b := range registry() {
		if b.scope == scopePanel && b.context == ctx {
			out = append(out, b)
		}
	}
	return out
}

// withoutUnsyncable drops the syncable rows — the issues list's `S` — while
// the project has no sync pass to wake (task 142.14 decision 3). The issues
// view applies it to its own live rows; the root applies it to the `?`
// sheet, which does not consult a view's live rows.
func withoutUnsyncable(rows []binding, syncable bool) []binding {
	if syncable {
		return rows
	}
	return slices.DeleteFunc(slices.Clone(rows), func(b binding) bool { return b.syncable })
}

// withoutGitHub drops the rows that only mean something while a project's
// GitHub integration is usable. It is applied at the root, where the probe
// results live: shell.liveBindings is the wrong seam because it is
// shell-scoped and the nav row is global.
func withoutGitHub(rows []binding, available bool) []binding {
	if available {
		return rows
	}
	out := make([]binding, 0, len(rows))
	for _, b := range rows {
		if !b.github {
			out = append(out, b)
		}
	}
	return out
}

// The reader-action keys (task 076 decision 7; the link picker is task 112's
// third). All are ctrl-modified in both contexts rather than a letter in the
// output pane and a ctrl twin in the chat — the `v`/`ctrl+r` split task 071
// chose is a cost, not a model, and one action should have one name in the
// help overlay and the palette. A bare letter cannot work in a chat at all:
// the composer owns every printable key.
const (
	rawToggleKey = "ctrl+o"
	copyPickKey  = "ctrl+y"
	linkPickKey  = "ctrl+l"
	// paletteAltKey reaches the palette from a surface that is capturing
	// text, where `:` types a colon into the draft. The root hoists it above
	// the input-capture gate the way it already hoists ctrl+v.
	paletteAltKey = "ctrl+p"
	// helpAltKey is `?` for the same surfaces (task 114). A function key
	// rather than a ctrl chord because it reads the same on every platform
	// and keyboard layout; ctrl+/ arrives as ctrl+_ from legacy terminals.
	// The costs are recorded rather than worked around: MacBook keyboards
	// need fn, and VS Code's integrated terminal takes F1 unless its
	// settings hand it over.
	helpAltKey = "f1"
)

// chatExportLabel is the export operation's row on every surface it is on
// (task 159): the same gesture, so the same words.
const chatExportLabel = "export the chat as Markdown, HTML or JSON — a form picks the format, the diff and the path"
