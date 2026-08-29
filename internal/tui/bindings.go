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
	ctxDaemon       bindingContext = "daemon"
	ctxForm         bindingContext = "answer form"
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

	// Navigation: the four takeover screens (§15 views 3–6). Only new task
	// keeps a direct key; the rest live here, in the palette — retiring
	// 1..6 without substituting new memorized keys is the point.
	{key: "n", label: "new task — for the project you are looking at", scope: scopeGlobal, nav: true, navTarget: viewNewTask},
	{label: "projects — list, add, edit, remove", scope: scopeGlobal, nav: true, navTarget: viewProjects},
	{label: "workflows — registry with scopes and validity", scope: scopeGlobal, nav: true, navTarget: viewWorkflows},
	{label: "daemon — identity, config, adapters, log", scope: scopeGlobal, nav: true, navTarget: viewDaemon},

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

	// Timeline.
	{key: "tab", label: "move between Steps & Attempts, Task Details, Output and Diff (shift+tab goes back; 1–4 jump directly)", scope: scopePanel, context: ctxTimeline, hint: "tab views", priority: 1},
	{key: "]", label: "move to the next task view ([ goes back)", scope: scopePanel, context: ctxTimeline, hint: "[/] views", priority: 2},
	{key: "down", label: "select an attempt (↑/↓); scrollback is per attempt", scope: scopePanel, context: ctxTimeline, hint: "↑/↓ attempts", priority: 3},
	{key: "enter", label: "open the selected attempt in the Output tab", scope: scopePanel, context: ctxTimeline, hint: "enter output", priority: 3},

	// Task details.
	{key: "tab", label: "move between Steps & Attempts, Task Details, Output and Diff (shift+tab goes back; 1–4 jump directly)", scope: scopePanel, context: ctxTaskDetails, hint: "tab views", priority: 1},
	{key: "]", label: "move to the next task view ([ goes back)", scope: scopePanel, context: ctxTaskDetails, hint: "[/] views", priority: 2},
	{key: "down", label: "select a task-detail section (↑/↓); pgup/pgdn scrolls that section", scope: scopePanel, context: ctxTaskDetails, hint: "↑/↓ sections", priority: 3},

	// Output pane.
	{key: "tab", label: "move between Steps & Attempts, Task Details, Output and Diff (shift+tab goes back; 1–4 jump directly)", scope: scopePanel, context: ctxOutput, hint: "tab views", priority: 1},
	{key: "]", label: "move to the next task view ([ goes back)", scope: scopePanel, context: ctxOutput, hint: "[/] views", priority: 2},
	{key: "f", label: "follow the live output again (f/G)", scope: scopePanel, context: ctxOutput, hint: "f follow", priority: 2},
	{key: "v", label: "show more or less: compact → normal → verbose (reasoning, then unrecognized lines)", scope: scopePanel, context: ctxOutput, hint: "v detail", priority: 3},
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

	// The graph sub-layer (task 017). Arrows move the selection and the
	// viewport follows it; panning is deliberately a separate, shifted key,
	// so an arrow can never scroll the canvas out from under the cursor.
	{key: "down", label: "move the selection (↑/↓/←/→ or hjkl); the view follows it", scope: scopePanel, context: ctxWorkflowGraph, hint: "↑↓←→ select", priority: 1},
	{key: "shift+down", label: "pan the canvas (shift+↑/↓/←/→); pgup/pgdn page it", scope: scopePanel, context: ctxWorkflowGraph, hint: "⇧ pan", priority: 2},
	{key: "tab", label: "walk the nodes in source order (shift+tab goes back)", scope: scopePanel, context: ctxWorkflowGraph, hint: "tab next", priority: 3},
	{key: "e", label: "open the workflow file in $EDITOR (the graph redraws when you save)", scope: scopePanel, context: ctxWorkflowGraph, hint: "e edit", priority: 4},
	{key: "R", label: "re-fetch this workflow's definition", scope: scopePanel, context: ctxWorkflowGraph, hint: "R reload", priority: 5},

	// The task workspace's workflow tab (task 051). The graph is this task's
	// own snapshot with its run state on it; `tab` cycles the workspace's
	// tabs here, so the source-order node walk is deliberately absent.
	{key: "down", label: "move the selection (↑/↓/←/→ or hjkl); the view follows it", scope: scopePanel, context: ctxTaskWorkflow, hint: "↑↓←→ select", priority: 1},
	{key: "shift+down", label: "pan the canvas (shift+↑/↓/←/→); pgup/pgdn page it", scope: scopePanel, context: ctxTaskWorkflow, hint: "⇧ pan", priority: 2},
	{key: "5", label: "the workflow this task ran, with what each step did on it", scope: scopePanel, context: ctxTaskWorkflow, hint: "5 workflow", priority: 3},

	// Daemon.
	{key: "R", label: "re-read the daemon info, the config and the log", scope: scopePanel, context: ctxDaemon, hint: "R refresh", priority: 1},
	{key: "f", label: "follow the end of the log again (f/G)", scope: scopePanel, context: ctxDaemon, hint: "f follow", priority: 2},
	{key: "down", label: "scroll the log (↑/↓)", scope: scopePanel, context: ctxDaemon, hint: "↑/↓ scroll", priority: 3},

	// Answer form: these exist only while the popup owns the keyboard, and
	// the popup prints them itself — they are here so ? stays complete.
	{key: "space", label: "pick an option (toggles, for a multi-select question)", scope: scopePanel, context: ctxForm, noPalette: true},
	{key: "e", label: "type your own answer — options are suggestions, never a list", scope: scopePanel, context: ctxForm, noPalette: true},
	{key: "enter", label: "submit the answer; the run resumes where it stopped", scope: scopePanel, context: ctxForm, noPalette: true},
	{key: "esc", label: "close the popup without answering (what you picked is kept)", scope: scopePanel, context: ctxForm, noPalette: true},

	// Repair form: as with the answer form, these exist only while the popup
	// owns the keyboard and it prints them itself.
	{key: "enter", label: "edit the row under the cursor — the prompt, or the agent/model/effort list", scope: scopePanel, context: ctxRepairForm, noPalette: true},
	{key: "e", label: "write the repair prompt in $EDITOR", scope: scopePanel, context: ctxRepairForm, noPalette: true},
	{key: "ctrl+s", label: "start the repair agent", scope: scopePanel, context: ctxRepairForm, noPalette: true},
	{key: "esc", label: "close the popup without repairing (the draft is discarded)", scope: scopePanel, context: ctxRepairForm, noPalette: true},

	// Follow-up form: same again — the popup owns the keyboard while it is
	// open and prints its own key line.
	{key: "enter", label: "edit the row under the cursor — the run form, what to run, or the agent/model/effort list", scope: scopePanel, context: ctxFollowUpForm, noPalette: true},
	{key: "e", label: "write the prompt or command in $EDITOR", scope: scopePanel, context: ctxFollowUpForm, noPalette: true},
	{key: "ctrl+s", label: "start the follow-up run", scope: scopePanel, context: ctxFollowUpForm, noPalette: true},
	{key: "esc", label: "close the popup without running anything (the draft is discarded)", scope: scopePanel, context: ctxFollowUpForm, noPalette: true},
}

// isHomeContext reports whether a context belongs to the board/task daily loop
// — the surfaces a task's actions and forms belong to, as opposed to a
// management takeover.
func isHomeContext(ctx bindingContext) bool {
	switch ctx {
	case ctxTasks, ctxTimeline, ctxTaskDetails, ctxOutput, ctxDiff, ctxTaskWorkflow:
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
