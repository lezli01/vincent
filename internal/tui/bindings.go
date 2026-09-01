package tui

import "github.com/lezli01/vincent/internal/apiclient"

// The binding registry is the single source the palette, the footer (T3.12)
// and the ? overlay all render from (§15 Discovery). Three hand-maintained
// lists drift within two PRs and the drift is invisible until a human
// presses a key the help promised; one registry turns "is every key
// discoverable?" into a test.

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
	ctxTasks       bindingContext = "task table"
	ctxTimeline    bindingContext = "timeline"
	ctxTaskDetails bindingContext = "task details"
	ctxOutput      bindingContext = "output"
	ctxDiff        bindingContext = "diff"
	ctxNewTask     bindingContext = "new task"
	ctxProjects    bindingContext = "projects"
	ctxWorkflows   bindingContext = "workflows"
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
	// ctxTaskPull is the task workspace's Pull Request tab (task 068). Its
	// own context rather than more rows on ctxTaskDetails because the tab
	// only exists for a task with a linked pull request, and a footer that
	// advertised `c open check` on a task with no checks would be describing
	// a screen that is not there.
	ctxTaskPull bindingContext = "task pull request"
	// ctxWorkflowStep is the step-detail modal over the graph (task 053). Its
	// own context for the reason the popups have theirs: while it is open it
	// owns the keyboard, and a row shared with the graph could only describe
	// one of the two.
	ctxWorkflowStep bindingContext = "step detail"
	// The chat surfaces (task 067). Three contexts for two views: the
	// new-chat form is a layer over the chats board with its own keyboard,
	// which is exactly what a bindingContext names.
	ctxChats   bindingContext = "chats"
	ctxChat    bindingContext = "chat"
	ctxNewChat bindingContext = "new chat"
	ctxDaemon  bindingContext = "daemon"
	ctxForm    bindingContext = "answer form"
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
)

// binding is one registry row.
type binding struct {
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
	// github marks a row that only means something when at least one
	// registered project has a usable GitHub integration (§13.2). There is no
	// stored notion of one, so the root's probe fan-out is the answer and the
	// row is withheld until it says yes — including while the probes are
	// still in flight. Mechanically this is `fold`'s precedent (task 054
	// decision 5) applied to a nav row and to two workspace keys.
	github bool
	// fold marks a row whose key only means something while the board has
	// groups. With `group_by: []` there are none, so shell.liveBindings drops
	// these and the footer never names a press that does nothing (task 054
	// decision 5).
	fold bool
	// nav marks a navigation entry; navTarget is where it goes.
	nav       bool
	navTarget viewID
	// noPalette keeps a row out of the palette: the palette's own controls
	// (: esc) and the answer form's keys, which only exist while the popup
	// owns the keyboard and are printed inside the popup itself.
	noPalette bool
}

// bindings is the registry. Order within a scope+context is display order.
var bindings = []binding{
	// Global chrome.
	{key: ":", label: "open the command palette", scope: scopeGlobal, noPalette: true},
	// The same palette, reachable where `:` is not: a chat's composer owns
	// every printable key, so the palette had been unreachable there since
	// task 067 (task 076 decision 7). noPalette for the reason `:` is —
	// a row teaching the way to open the thing you already opened.
	{key: paletteAltKey, label: "open the command palette (works while a text field has the keyboard)", scope: scopeGlobal, noPalette: true},
	{key: "?", label: "toggle this help", scope: scopeGlobal},
	{key: "tab", label: "move to the next task tab (shift+tab goes back)", scope: scopeGlobal},
	{key: "!", label: "jump to the next task needing a human", scope: scopeGlobal},
	{key: "M", label: "toggle the mouse (native text selection needs it off — or shift-drag)", scope: scopeGlobal},
	// Paste is normally the terminal's own (Cmd+V, Ctrl+Shift+V, middle
	// click): it arrives as bracketed paste and lands in the focused field
	// with no key involved. ctrl+v is the fallback for terminals that pass
	// the key through instead, and it is documented rather than palette-run —
	// running it from the palette would paste into a field the palette just
	// closed.
	{key: "ctrl+v", label: "paste into the focused field (Cmd+V and the terminal's own paste work too)", scope: scopeGlobal, noPalette: true},
	{key: "esc", label: "close one layer: popup → screen → selection → filter — never quits", scope: scopeGlobal, noPalette: true},
	{key: "q", label: "quit the TUI (the daemon keeps running)", scope: scopeGlobal},
	{key: "ctrl+c", label: "quit the TUI", scope: scopeGlobal, noPalette: true},

	// Navigation: the five takeover screens (§15 views 3–7). Only new task
	// keeps a direct key; the rest live here, in the palette — retiring
	// 1..6 without substituting new memorized keys is the point.
	{key: "n", label: "new task — for the project you are looking at", scope: scopeGlobal, nav: true, navTarget: viewNewTask},
	{label: "projects — list, add, edit, remove", scope: scopeGlobal, nav: true, navTarget: viewProjects},
	{label: "workflows — registry with scopes and validity", scope: scopeGlobal, nav: true, navTarget: viewWorkflows},
	{label: "daemon — identity, config, adapters, log", scope: scopeGlobal, nav: true, navTarget: viewDaemon},
	{label: "pull requests — what is open across every GitHub project", scope: scopeGlobal, nav: true, navTarget: viewPullRequests, github: true},
	// Chats get a palette row and no direct key, the pattern every takeover
	// but new task follows. `n` is not shared: on the chats board it makes a
	// chat, everywhere else it still makes a task.
	{label: "chats — conversations with an agent, each in its own worktree", scope: scopeGlobal, nav: true, navTarget: viewChats},

	// Task actions, gated on available_actions. `p` appears twice because
	// pause and resume are distinct actions behind one key; the palette
	// shows whichever the daemon offers.
	{key: "p", label: "pause the running task", scope: scopeTaskAction, action: apiclient.ActionPause, priority: 3},
	{key: "p", label: "resume the paused task", scope: scopeTaskAction, action: apiclient.ActionResume, priority: 3},
	{key: "a", label: "approve the gate", scope: scopeTaskAction, action: apiclient.ActionApprove, priority: 1},
	{key: "x", label: "reject the gate", scope: scopeTaskAction, action: apiclient.ActionReject, priority: 2},
	{key: "r", label: "retry the blocked step", scope: scopeTaskAction, action: apiclient.ActionRetry, priority: 4},
	{key: "E", label: "edit the step's prompt or command in $EDITOR, then retry", scope: scopeTaskAction, action: apiclient.ActionRetry, priority: 5},
	{key: "R", label: "repair with an agent — a one-off run in this task's worktree; the task stays blocked afterwards", scope: scopeTaskAction, action: apiclient.ActionRepair, priority: 5},
	{key: "s", label: "skip the current step", scope: scopeTaskAction, action: apiclient.ActionSkip, priority: 6},
	{key: "c", label: "cancel the task (asks first — a running step is killed)", scope: scopeTaskAction, action: apiclient.ActionCancel, priority: 7},
	{key: "A", label: "archive the task (asks first — the worktree is removed)", scope: scopeTaskAction, action: apiclient.ActionArchive, priority: 8},
	{key: "F", label: "follow up — run an agent prompt, a shell command or a workflow in this finished task's worktree; it returns to the state it came from", scope: scopeTaskAction, action: apiclient.ActionFollowUp, priority: 9},

	// Task table.
	{key: "down", label: "move the selection (↑/↓ — the panels follow the cursor)", scope: scopePanel, context: ctxTasks, hint: "↑/↓ select", priority: 3},
	{key: "enter", label: "open the selected task in its full-screen workspace", scope: scopePanel, context: ctxTasks, hint: "enter open", priority: 1},
	{key: "/", label: "filter by id, title, project or state", scope: scopePanel, context: ctxTasks, hint: "/ filter", priority: 2},
	{key: "g", label: "group the tasks: project › workflow → project → workflow → flat (config.yaml sets the one you start on)", scope: scopePanel, context: ctxTasks, hint: "g group", priority: 4},
	{key: "space", label: "select this task for a bulk action — the action keys then act on every selected task (space again deselects, esc clears)", scope: scopePanel, context: ctxTasks, hint: "space select", priority: 5},
	{key: "V", label: "select every task the filter is showing, or clear that selection", scope: scopePanel, context: ctxTasks, priority: 6},
	{key: "L", label: "drill into the selected fan-out's lanes, or back out (lanes are hidden from the board otherwise)", scope: scopePanel, context: ctxTasks, hint: "L lanes", priority: 7},
	// Folding (task 054). ← and → walk the group tree a level at a time; the
	// cursor rests on a collapsed header, which is how every level stays
	// addressable. C/O are the diff pane's two letters in the same meaning.
	{key: "left", label: "collapse the group you are in (← again folds the group around it; the header keeps the count and the ! badge)", scope: scopePanel, context: ctxTasks, hint: "←/→ fold", priority: 8, fold: true},
	{key: "right", label: "expand the collapsed group under the cursor, one level", scope: scopePanel, context: ctxTasks, priority: 9, fold: true},
	{key: "C", label: "collapse every group", scope: scopePanel, context: ctxTasks, hint: "C/O fold all", priority: 10, fold: true},
	{key: "O", label: "expand every group", scope: scopePanel, context: ctxTasks, priority: 11, fold: true},

	// Timeline.
	{key: "tab", label: "move between Steps & Attempts, Task Details, Output and Diff (shift+tab goes back; 1–4 jump directly)", scope: scopePanel, context: ctxTimeline, hint: "tab views", priority: 1},
	{key: "]", label: "move to the next task view ([ goes back)", scope: scopePanel, context: ctxTimeline, hint: "[/] views", priority: 2},
	{key: "down", label: "select an attempt (↑/↓); scrollback is per attempt", scope: scopePanel, context: ctxTimeline, hint: "↑/↓ attempts", priority: 3},
	{key: "enter", label: "open the selected attempt in the Output tab", scope: scopePanel, context: ctxTimeline, hint: "enter output", priority: 3},

	// Task details.
	{key: "tab", label: "move between Steps & Attempts, Task Details, Output and Diff (shift+tab goes back; 1–4 jump directly)", scope: scopePanel, context: ctxTaskDetails, hint: "tab views", priority: 1},
	{key: "]", label: "move to the next task view ([ goes back)", scope: scopePanel, context: ctxTaskDetails, hint: "[/] views", priority: 2},
	{key: "down", label: "select a task-detail section (↑/↓); pgup/pgdn scrolls that section", scope: scopePanel, context: ctxTaskDetails, hint: "↑/↓ sections", priority: 3},
	// The pull-request section's two keys (task 052.6, decision 2). Both only
	// reach a browser: neither writes anything in vincent, which is what
	// keeps §15's "read-only inspector" true in the sense it was written.
	{key: "o", label: "open this task's pull request in a browser", scope: scopePanel, context: ctxTaskDetails, hint: "o pull request", priority: 4, github: true},
	{key: "P", label: "push this task's branch to origin and open its pull request — the title, body and draft flag are editable first", scope: scopePanel, context: ctxTaskDetails, hint: "P open a PR", priority: 5, github: true},

	// Output pane.
	{key: "tab", label: "move between Steps & Attempts, Task Details, Output and Diff (shift+tab goes back; 1–4 jump directly)", scope: scopePanel, context: ctxOutput, hint: "tab views", priority: 1},
	{key: "]", label: "move to the next task view ([ goes back)", scope: scopePanel, context: ctxOutput, hint: "[/] views", priority: 2},
	{key: "f", label: "follow the live output again (f/G)", scope: scopePanel, context: ctxOutput, hint: "f follow", priority: 2},
	{key: "v", label: "show more or less: compact → normal → verbose (reasoning, then unrecognized lines)", scope: scopePanel, context: ctxOutput, hint: "v detail", priority: 3},
	{key: rawToggleKey, label: "show the assistant's original Markdown instead of the rendered view", scope: scopePanel, context: ctxOutput, hint: "ctrl+o raw", priority: 6},
	{key: copyPickKey, label: "copy an assistant message, its plain text, or one of its code blocks", scope: scopePanel, context: ctxOutput, hint: "ctrl+y copy", priority: 7},
	{key: "e", label: "open this attempt's whole transcript in $EDITOR (the pane holds only the end of it)", scope: scopePanel, context: ctxOutput, hint: "e transcript", priority: 5},
	{key: "down", label: "scroll (↑/↓; scrolling up pauses follow)", scope: scopePanel, context: ctxOutput, hint: "↑/↓ scroll", priority: 4},
	{key: "right", label: "select which attempt's output to show (←/→ or h/l)", scope: scopePanel, context: ctxOutput},

	// Diff tab. Its own context rather than more ctxOutput rows: the diff is a
	// list of files and the output is a stream of lines, so ↑/↓ mean different
	// things on the two tabs and a single row could only describe one of them.
	// `]` is repeated here because the way back must stay on screen.
	{key: "tab", label: "move between Steps & Attempts, Task Details, Output and Diff (shift+tab goes back; 1–4 jump directly)", scope: scopePanel, context: ctxDiff, hint: "tab views", priority: 1},
	{key: "]", label: "move to the next task view ([ goes back)", scope: scopePanel, context: ctxDiff, hint: "[/] views", priority: 2},
	{key: "down", label: "move between the files (↑/↓); the pane scrolls to keep the file in view", scope: scopePanel, context: ctxDiff, hint: "↑/↓ files", priority: 2},
	{key: "enter", label: "expand or collapse the file under the cursor (space and →/← too)", scope: scopePanel, context: ctxDiff, hint: "enter fold", priority: 3},
	{key: "O", label: "expand every file", scope: scopePanel, context: ctxDiff, hint: "O/C fold all", priority: 4},
	{key: "C", label: "collapse every file — which is how the tab opens", scope: scopePanel, context: ctxDiff, priority: 5},

	// New task.
	{key: "enter", label: "open the focused field's editor or picker", scope: scopePanel, context: ctxNewTask, hint: "enter edit field", priority: 2},
	{key: "e", label: "edit the description in $EDITOR", scope: scopePanel, context: ctxNewTask, hint: "e $EDITOR", priority: 3},
	{key: "+", label: "nudge the priority (+/-; higher runs first)", scope: scopePanel, context: ctxNewTask, hint: "+/- priority", priority: 4},
	{key: "R", label: "re-probe the adapters (the list is otherwise cache-served)", scope: scopePanel, context: ctxNewTask, hint: "R re-probe", priority: 5},
	{key: "ctrl+s", label: "create the task", scope: scopePanel, context: ctxNewTask, hint: "ctrl+s create", priority: 1},

	// Chats board.
	{key: "enter", label: "open the chat's workspace", scope: scopePanel, context: ctxChats, hint: "enter open", priority: 1},
	{key: "n", label: "start a chat in the project you are looking at", scope: scopePanel, context: ctxChats, hint: "n new", priority: 2},
	{key: "a", label: "archive the chat (asks first — the worktree is removed)", scope: scopePanel, context: ctxChats, hint: "a archive", priority: 3},
	{key: "/", label: "filter by title, agent or branch", scope: scopePanel, context: ctxChats, hint: "/ filter", priority: 4},
	{key: "left", label: "collapse the project group", scope: scopePanel, context: ctxChats, hint: "← fold", priority: 5},
	{key: "right", label: "expand the project group", scope: scopePanel, context: ctxChats, hint: "→ unfold", priority: 6},
	{key: "r", label: "reload the board", scope: scopePanel, context: ctxChats, hint: "r reload", priority: 7},

	// Chat workspace.
	{key: "enter", label: "send the message", scope: scopePanel, context: ctxChat, hint: "enter send", priority: 1},
	{key: "ctrl+x", label: "stop the running turn (its process tree is killed)", scope: scopePanel, context: ctxChat, hint: "ctrl+x stop", priority: 2},
	// ctrl+r rather than `v` (task 071 decision 4): the composer owns every
	// printable key, so a letter would be typed into the message.
	{key: "ctrl+r", label: "how much of the conversation to show (compact → normal → verbose)", scope: scopePanel, context: ctxChat, hint: "ctrl+r detail", priority: 3},
	{key: "pgup", label: "scroll the conversation back (pgdown goes forward)", scope: scopePanel, context: ctxChat, hint: "pgup/pgdown scroll", priority: 4},
	{key: "ctrl+g", label: "jump to the live end and follow it again", scope: scopePanel, context: ctxChat, hint: "ctrl+g live", priority: 5},
	// ctrl+t rather than `h`, for the reason ctrl+r is a combination: the
	// composer owns every printable key (task 074).
	{key: "ctrl+t", label: "hand the worktree and branch to a new task (the chat ends)", scope: scopePanel, context: ctxChat, hint: "ctrl+t hand off", priority: 6},
	{key: "esc", label: "back to the chats board", scope: scopePanel, context: ctxChat, hint: "esc back", priority: 7},
	{key: rawToggleKey, label: "show the assistant's original Markdown instead of the rendered view", scope: scopePanel, context: ctxChat, hint: "ctrl+o raw", priority: 8},
	{key: copyPickKey, label: "copy an assistant message, its plain text, or one of its code blocks", scope: scopePanel, context: ctxChat, hint: "ctrl+y copy", priority: 9},

	// New chat.
	{key: "ctrl+s", label: "create the chat and open it", scope: scopePanel, context: ctxNewChat, hint: "ctrl+s create", priority: 1},
	{key: "enter", label: "open the focused field's list, or move on from a text field", scope: scopePanel, context: ctxNewChat, hint: "enter list", priority: 2},
	{key: "tab", label: "next field (shift+tab goes back)", scope: scopePanel, context: ctxNewChat, hint: "tab next", priority: 3},
	{key: "left", label: "step the project and agent fields in place (← →); enter opens their list", scope: scopePanel, context: ctxNewChat, hint: "← →  step", priority: 4},
	{key: "esc", label: "close an open list, else discard the draft", scope: scopePanel, context: ctxNewChat, hint: "esc cancel", priority: 5},

	// Projects.
	{key: "a", label: "register a repository", scope: scopePanel, context: ctxProjects, hint: "a add", priority: 1},
	{key: "enter", label: "edit the selected project (enter/e)", scope: scopePanel, context: ctxProjects, hint: "enter edit", priority: 2},
	{key: "d", label: "remove the project (asks first; its task rows go with it)", scope: scopePanel, context: ctxProjects, hint: "d remove", priority: 3},
	{key: "/", label: "filter by name or path", scope: scopePanel, context: ctxProjects, hint: "/ filter", priority: 4},
	{key: "ctrl+s", label: "in the form: save", scope: scopePanel, context: ctxProjects, hint: "ctrl+s save", priority: 5},

	// Workflows.
	{key: "enter", label: "show the entry's steps", scope: scopePanel, context: ctxWorkflows, hint: "enter steps", priority: 1},
	{key: "e", label: "open the workflow file in $EDITOR (the view updates when you save)", scope: scopePanel, context: ctxWorkflows, hint: "e edit", priority: 2},
	{key: "R", label: "re-read the registry", scope: scopePanel, context: ctxWorkflows, hint: "R reload", priority: 3},
	{key: "g", label: "draw the workflow as a control-flow graph", scope: scopePanel, context: ctxWorkflows, hint: "g graph", priority: 4},
	// The structured editor (task 065). `e` keeps meaning $EDITOR — it means
	// that in every other context too, and one key with two meanings
	// depending on the view is the confusion decision 6 refused.
	{key: "i", label: "edit the entry in a structured form", scope: scopePanel, context: ctxWorkflows, hint: "i form", priority: 5},
	{key: "a", label: "create a workflow in a chosen scope", scope: scopePanel, context: ctxWorkflows, hint: "a new", priority: 6},
	{key: "f", label: "fork the entry into another scope, where it shadows the original", scope: scopePanel, context: ctxWorkflows, hint: "f fork", priority: 7},

	// The structured editor sub-layer.
	{key: "up/down", label: "move between rows", scope: scopePanel, context: ctxWorkflowEditor, hint: "↑↓ rows", priority: 1},
	{key: "enter", label: "edit the row, cycle its values, or descend into a nested body", scope: scopePanel, context: ctxWorkflowEditor, hint: "enter edit", priority: 2},
	{key: "R", label: "re-read the file (what a stale-write 409 offers)", scope: scopePanel, context: ctxWorkflowEditor, hint: "R reload", priority: 3},
	{key: "esc", label: "leave the nested body, then the editor", scope: scopePanel, context: ctxWorkflowEditor, hint: "esc back", priority: 4},

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
	{key: "e", label: "open the workflow file in $EDITOR (the graph redraws when you save)", scope: scopePanel, context: ctxWorkflowGraph, hint: "e edit", priority: 4},
	{key: "R", label: "re-fetch this workflow's definition", scope: scopePanel, context: ctxWorkflowGraph, hint: "R reload", priority: 5},
	{key: "enter", label: "open the selected node in full — every field it carries, including the prompt or run body", scope: scopePanel, context: ctxWorkflowGraph, hint: "enter detail", priority: 2},

	// The step-detail modal (task 053): these exist only while the popup owns
	// the keyboard, and the popup prints them itself — they are here so ?
	// stays complete. `e` and `R` carry one layer further for the reason they
	// carried into the graph: editing is the path from wherever you are
	// reading, and R is the layer's only recovery.
	{key: "down", label: "scroll the detail (↑/↓; pgup/pgdn page it)", scope: scopePanel, context: ctxWorkflowStep, noPalette: true},
	{key: "e", label: "open the workflow file in $EDITOR (the detail redraws when you save)", scope: scopePanel, context: ctxWorkflowStep, noPalette: true},
	{key: "R", label: "re-fetch this workflow's definition", scope: scopePanel, context: ctxWorkflowStep, noPalette: true},
	{key: "esc", label: "close the detail, back to the graph with the same node selected", scope: scopePanel, context: ctxWorkflowStep, noPalette: true},

	// The task workspace's workflow tab (task 051). The graph is this task's
	// own snapshot with its run state on it; `tab` cycles the workspace's
	// tabs here, so the source-order node walk is deliberately absent.
	{key: "down", label: "move the selection (↑/↓/←/→ or hjkl); the view follows it", scope: scopePanel, context: ctxTaskWorkflow, hint: "↑↓←→ select", priority: 1},
	{key: "shift+down", label: "pan the canvas (shift+↑/↓/←/→); pgup/pgdn page it", scope: scopePanel, context: ctxTaskWorkflow, hint: "⇧ pan", priority: 2},
	{key: "5", label: "the workflow this task ran, with what each step did on it", scope: scopePanel, context: ctxTaskWorkflow, hint: "5 workflow", priority: 3},

	// The task workspace's Pull Request tab (task 068). The tab is on the
	// strip only when this task has a live link and the integration is
	// usable, which is why `6` is registered here and nowhere else: on a task
	// with no pull request the key does nothing, and a row promising
	// otherwise would be the lie the availability filter exists to prevent.
	{key: "6", label: "this task's pull request, with a live row per check on its head commit", scope: scopePanel, context: ctxTaskPull, hint: "6 pull request", priority: 1, github: true},
	{key: "down", label: "select a check (↑/↓ or j/k)", scope: scopePanel, context: ctxTaskPull, hint: "↑/↓ checks", priority: 2, github: true},
	{key: "c", label: "open the selected check's own page in a browser", scope: scopePanel, context: ctxTaskPull, hint: "c open check", priority: 3, github: true},
	{key: "o", label: "open the pull request in a browser", scope: scopePanel, context: ctxTaskPull, hint: "o open PR", priority: 4, github: true},
	{key: "r", label: "re-read the pull request and its checks now (they also refresh on their own while the tab is open)", scope: scopePanel, context: ctxTaskPull, hint: "r refresh", priority: 5, github: true},
	{key: "u", label: "unlink this pull request from the task — the refusal sticks, and the reconciler will not link it again", scope: scopePanel, context: ctxTaskPull, hint: "u unlink", priority: 6, github: true},

	// Pull requests (task 052.6). Link and unlink live only here: they are the
	// two actions that write vincent's own column, and they belong on the one
	// screen that can see a pull request no task claims — the case they exist
	// for.
	{key: "enter", label: "open the workspace of the task that claims this pull request", scope: scopePanel, context: ctxPullRequests, hint: "enter task", priority: 1},
	{key: "o", label: "open the selected pull request in a browser", scope: scopePanel, context: ctxPullRequests, hint: "o browser", priority: 2},
	{key: "c", label: "create a task from this pull request — it runs on the pull request's head branch, and the form is editable first", scope: scopePanel, context: ctxPullRequests, hint: "c new task", priority: 3},
	{key: "l", label: "link this pull request to a task in the same project", scope: scopePanel, context: ctxPullRequests, hint: "l link", priority: 4},
	// The takeover's half of task 069. This screen has no task rows — its
	// question is "what is open across everything I run" — so the offer is a
	// picker of tasks that have a branch and no pull request, and choosing
	// one opens that task's workspace with the form up.
	{key: "P", label: "open a pull request for a task that has none — pick the task, then push its branch and create it", scope: scopePanel, context: ctxPullRequests, hint: "P open a PR", priority: 5, github: true},
	{key: "u", label: "unlink it (asks first — the refusal sticks, and the reconciler will not link it again)", scope: scopePanel, context: ctxPullRequests, hint: "u unlink", priority: 5},
	{key: "s", label: "cycle the listing between open, closed and all", scope: scopePanel, context: ctxPullRequests, hint: "s state", priority: 6},
	{key: "R", label: "re-list every project", scope: scopePanel, context: ctxPullRequests, hint: "R refresh", priority: 7},
	{key: "down", label: "move the selection (↑/↓)", scope: scopePanel, context: ctxPullRequests, priority: 8},
	{key: "/", label: "filter by number, title, branch or project", scope: scopePanel, context: ctxPullRequests, priority: 9},

	// Daemon.
	{key: "R", label: "re-read the daemon info, the config and the log", scope: scopePanel, context: ctxDaemon, hint: "R refresh", priority: 1},
	{key: "f", label: "follow the end of the log again (f/G)", scope: scopePanel, context: ctxDaemon, hint: "f follow", priority: 2},
	{key: "down", label: "scroll the log (↑/↓)", scope: scopePanel, context: ctxDaemon, hint: "↑/↓ scroll", priority: 3},
	// The config block became editable in task 060. tab is what says whether
	// ↑/↓ mean the config list or the log pane — the view has two scrollable
	// things now and one pair of arrows.
	{key: "tab", label: "move between the config list and the log pane", scope: scopePanel, context: ctxDaemon, hint: "tab config", priority: 4},
	{key: "enter", label: "edit the selected configuration key (e also opens it)", scope: scopePanel, context: ctxDaemon, hint: "enter edit", priority: 5},

	// The config editor: it owns the keyboard while it is open and prints its
	// own key line, so these are here to keep ? complete.
	{key: "left", label: "choose a value, for a key with a fixed vocabulary (←/→)", scope: scopePanel, context: ctxConfigEdit, noPalette: true},
	{key: "enter", label: "apply the change; the daemon validates and writes config.yaml", scope: scopePanel, context: ctxConfigEdit, noPalette: true},
	{key: "y", label: "confirm a key that decides what the daemon executes or exposes", scope: scopePanel, context: ctxConfigEdit, noPalette: true},
	{key: "esc", label: "close the editor without saving (the confirmation returns to the field)", scope: scopePanel, context: ctxConfigEdit, noPalette: true},

	// Answer form: these exist only while the popup owns the keyboard, and
	// the popup prints them itself — they are here so ? stays complete.
	{key: "space", label: "pick an option (toggles, for a multi-select question)", scope: scopePanel, context: ctxForm, noPalette: true},
	{key: "e", label: "type your own answer — options are suggestions, never a list", scope: scopePanel, context: ctxForm, noPalette: true},
	{key: "enter", label: "submit the answer; the run resumes where it stopped", scope: scopePanel, context: ctxForm, noPalette: true},
	{key: "ctrl+t", label: "read this task's details without leaving the form — the picks and the typed answer are kept (ctrl+t again, or esc, comes back)", scope: scopePanel, context: ctxForm, noPalette: true},
	{key: "esc", label: "close the popup without answering (what you picked is kept)", scope: scopePanel, context: ctxForm, noPalette: true},

	// Repair form: as with the answer form, these exist only while the popup
	// owns the keyboard and it prints them itself.
	{key: "enter", label: "edit the row under the cursor — the prompt, or the agent/model/effort list", scope: scopePanel, context: ctxRepairForm, noPalette: true},
	{key: "e", label: "write the repair prompt in $EDITOR", scope: scopePanel, context: ctxRepairForm, noPalette: true},
	{key: "ctrl+s", label: "start the repair agent", scope: scopePanel, context: ctxRepairForm, noPalette: true},
	{key: "ctrl+t", label: "read this task's details without leaving the form — the draft is kept (ctrl+t again, or esc, comes back)", scope: scopePanel, context: ctxRepairForm, noPalette: true},
	{key: "esc", label: "close the popup without repairing (the draft is discarded)", scope: scopePanel, context: ctxRepairForm, noPalette: true},

	// Follow-up form: same again — the popup owns the keyboard while it is
	// open and prints its own key line.
	{key: "enter", label: "edit the row under the cursor — the run form, what to run, or the agent/model/effort list", scope: scopePanel, context: ctxFollowUpForm, noPalette: true},
	{key: "e", label: "write the prompt or command in $EDITOR", scope: scopePanel, context: ctxFollowUpForm, noPalette: true},
	{key: "ctrl+s", label: "start the follow-up run", scope: scopePanel, context: ctxFollowUpForm, noPalette: true},
	{key: "ctrl+t", label: "read this task's details without leaving the form — the draft is kept (ctrl+t again, or esc, comes back)", scope: scopePanel, context: ctxFollowUpForm, noPalette: true},
	{key: "esc", label: "close the popup without running anything (the draft is discarded)", scope: scopePanel, context: ctxFollowUpForm, noPalette: true},

	// The pull-request form (task 052.6, task 069). Same again: the popup owns the
	// keyboard while it is open and prints its own key line.
	{key: "enter", label: "edit the row under the cursor — the pull request's title or its body, or toggle the draft row", scope: scopePanel, context: ctxCreatePR, noPalette: true},
	{key: "space", label: "toggle draft / ready for review, on the draft row", scope: scopePanel, context: ctxCreatePR, noPalette: true},
	{key: "e", label: "write the body in $EDITOR", scope: scopePanel, context: ctxCreatePR, noPalette: true},
	{key: "ctrl+s", label: "push the branch to origin and open the pull request", scope: scopePanel, context: ctxCreatePR, noPalette: true},
	{key: "ctrl+o", label: "open GitHub's own new-pull-request page with this prefill instead", scope: scopePanel, context: ctxCreatePR, noPalette: true},
	{key: "esc", label: "close the popup without sending anything (the draft is discarded)", scope: scopePanel, context: ctxCreatePR, noPalette: true},
}

// isHomeContext reports whether a context belongs to the board/task daily loop
// — the surfaces a task's actions and forms belong to, as opposed to a
// management takeover.
func isHomeContext(ctx bindingContext) bool {
	switch ctx {
	case ctxTasks, ctxTimeline, ctxTaskDetails, ctxOutput, ctxDiff, ctxTaskWorkflow, ctxTaskPull:
		return true
	default:
		return false
	}
}

// bindingsFor returns the panel rows owned by one context, in registry order.
func bindingsFor(ctx bindingContext) []binding {
	out := make([]binding, 0, 8)
	for _, b := range bindings {
		if b.scope == scopePanel && b.context == ctx {
			out = append(out, b)
		}
	}
	return out
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

// The reader-action keys (task 076 decision 7). Both are ctrl-modified in
// both contexts rather than a letter in the output pane and a ctrl twin in
// the chat — the `v`/`ctrl+r` split task 071 chose is a cost, not a model,
// and one action should have one name in the help overlay and the palette.
// A bare letter cannot work in a chat at all: the composer owns every
// printable key.
const (
	rawToggleKey = "ctrl+o"
	copyPickKey  = "ctrl+y"
	// paletteAltKey reaches the palette from a surface that is capturing
	// text, where `:` types a colon into the draft. The root hoists it above
	// the input-capture gate the way it already hoists ctrl+v.
	paletteAltKey = "ctrl+p"
)
