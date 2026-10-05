package keymap

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// Op names a rebindable operation. The string is what `tui.keys` is keyed by.
type Op string

// Surface names where a key is answered. The values are the TUI registry's
// context names, so an error can say "on the chats board" in the words `?`
// already uses. Two surfaces are not contexts: Global (the root's chrome, live
// wherever no text field has the keyboard) and Actions (§6's actions, live on
// every surface ActionsLive reports).
type Surface string

// The two surfaces that are not a single registry context.
const (
	Global  Surface = "global"
	Actions Surface = "task actions"
)

// The §15 vocabulary terms.
const (
	Refresh     Op = "refresh"
	Archive     Op = "archive"
	Delete      Op = "delete"
	DraftRemove Op = "draft_remove"
	Add         Op = "add"
	Editor      Op = "editor"
	FreeText    Op = "free_text"
	Browser     Op = "browser"
	OpenRow     Op = "open_row"
	Scope       Op = "scope"
	Filter      Op = "filter"
	Lane        Op = "lane"
	Result      Op = "result"
	NextFailure Op = "next_failure"
	// AttentionFilter narrows the task table to what needs a human (task
	// 129.18). H for "needs a human".
	AttentionFilter Op = "attention_filter"
	// Comment writes a local comment on an issue in $EDITOR (task 130
	// decision 24, 130.16). W for "write": c is §6 cancel and C is fold.
	Comment Op = "comment"
)

// The §6 actions. Archive is the term above: clause 1 working, not a second
// id. Pause is one id for pause and resume, which have been one key since §15.
const (
	Pause     Op = "pause"
	Approve   Op = "approve"
	Reject    Op = "reject"
	Retry     Op = "retry"
	EditRetry Op = "edit_retry"
	Repair    Op = "repair"
	Skip      Op = "skip"
	Cancel    Op = "cancel"
	FollowUp  Op = "follow_up"
	Chat      Op = "chat"
)

// The global chrome.
const (
	Palette       Op = "palette"
	PaletteAlt    Op = "palette_alt"
	Help          Op = "help"
	HelpAlt       Op = "help_alt"
	NextAttention Op = "next_attention"
	Mouse         Op = "mouse"
	Quit          Op = "quit"
	New           Op = "new"
	// Project opens the project picker (task 132 decision 3). `@` for "at
	// which project": not ctrl+e, which the m11 gate rebinds to, and not one
	// of the ctrl keys bubbles' text fields edit with.
	Project Op = "project"
)

// Kind is which of decision 1's three families an operation belongs to.
type Kind int

// The three families.
const (
	KindTerm Kind = iota
	KindAction
	KindGlobal
)

// Info describes one operation.
type Info struct {
	Op      Op
	Default string
	Meaning string
	Kind    Kind
	// Surfaces are where the operation is answered. A term lists its
	// contexts; an action is on Actions; a global is on Global, plus any
	// context that answers it as its own row (`new` on the chats board).
	Surfaces []Surface
	// Typing marks the escape hatches that work while a text field has the
	// keyboard (task 076 decision 7, task 114 decision 1): they may only be
	// bound to a key the field would not type.
	Typing bool
}

// catalog is the operation set, in the order the docs list it.
var catalog = []Info{
	{Op: Refresh, Default: "R", Meaning: "refresh / re-read", Kind: KindTerm, Surfaces: []Surface{
		"new task", "archived chats", "chats", "workflows", "workflow editor", "workflow graph",
		"step detail", "pull requests", "daemon", "triggers", "trigger ledger", "trigger form",
		"issues", "issue", "issue form",
	}},
	{Op: Archive, Default: "A", Meaning: "archive", Kind: KindTerm, Surfaces: []Surface{"chats", Actions}},
	{Op: Delete, Default: "D", Meaning: "delete a persisted record", Kind: KindTerm, Surfaces: []Surface{
		"archived tasks", "archived chats", "projects", "triggers", "issues", "issue",
	}},
	{Op: DraftRemove, Default: "d", Meaning: "remove a row from an open draft", Kind: KindTerm, Surfaces: []Surface{
		"new task fields", "workflow editor",
	}},
	{Op: Add, Default: "a", Meaning: "add / create", Kind: KindTerm, Surfaces: []Surface{
		"new task fields", "projects", "workflows", "workflow editor", "pull requests", "triggers",
		"issues", "issue",
	}},
	{Op: Editor, Default: "e", Meaning: "edit in $EDITOR", Kind: KindTerm, Surfaces: []Surface{
		"output", "new task", "workflows", "workflow graph", "step detail", "triggers",
		"repair form", "follow-up form", "open a pull request", "comment on a pull request",
		"issue form",
	}},
	{Op: FreeText, Default: "t", Meaning: "type free text instead of picking", Kind: KindTerm, Surfaces: []Surface{
		"new task", "new chat", "workflow editor", "answer form", "repair form", "follow-up form",
		"issue form",
	}},
	{Op: Browser, Default: "o", Meaning: "open in a browser", Kind: KindTerm, Surfaces: []Surface{
		"task details", "task pull request", "pull requests", "issues", "issue",
	}},
	{Op: OpenRow, Default: "enter", Meaning: "open or expand the row under the cursor", Kind: KindTerm, Surfaces: []Surface{
		"task table", "archived tasks", "archived chats", "chats", "task pull request",
		"issues", "issue",
	}},
	{Op: Scope, Default: "s", Meaning: "cycle a listing's scope", Kind: KindTerm, Surfaces: []Surface{
		"archived tasks", "archived chats", "chats", "pull requests", "issues",
	}},
	{Op: Filter, Default: "/", Meaning: "filter", Kind: KindTerm, Surfaces: []Surface{
		"task table", "archived tasks", "archived chats", "chats", "projects", "pull requests", "triggers",
		"issues",
	}},
	{Op: Lane, Default: "l", Meaning: "open a fan-out lane", Kind: KindTerm, Surfaces: []Surface{
		"task overview", "timeline", "task details", "output", "diff", "task workflow", "task step details", "task pull request",
	}},
	{Op: Result, Default: "w", Meaning: "open the output the outcome card's result came from", Kind: KindTerm, Surfaces: []Surface{
		"task overview",
	}},
	{Op: NextFailure, Default: "N", Meaning: "jump to the task's next failure", Kind: KindTerm, Surfaces: []Surface{
		"task overview", "timeline", "task details", "output", "diff", "task workflow", "task step details", "task pull request",
	}},
	{Op: AttentionFilter, Default: "H", Meaning: "show only the tasks that need a human", Kind: KindTerm, Surfaces: []Surface{
		"task table",
	}},
	{Op: Comment, Default: "W", Meaning: "comment on the issue", Kind: KindTerm, Surfaces: []Surface{
		"issue",
	}},

	{Op: Pause, Default: "p", Meaning: "pause or resume the task", Kind: KindAction, Surfaces: []Surface{Actions}},
	{Op: Approve, Default: "a", Meaning: "approve the gate", Kind: KindAction, Surfaces: []Surface{Actions}},
	{Op: Reject, Default: "x", Meaning: "reject the gate", Kind: KindAction, Surfaces: []Surface{Actions}},
	{Op: Retry, Default: "r", Meaning: "retry the blocked step", Kind: KindAction, Surfaces: []Surface{Actions}},
	{Op: EditRetry, Default: "E", Meaning: "edit the step in $EDITOR, then retry", Kind: KindAction, Surfaces: []Surface{Actions}},
	{Op: Repair, Default: "R", Meaning: "repair with an agent", Kind: KindAction, Surfaces: []Surface{Actions}},
	{Op: Skip, Default: "s", Meaning: "skip the current step", Kind: KindAction, Surfaces: []Surface{Actions}},
	{Op: Cancel, Default: "c", Meaning: "cancel the task", Kind: KindAction, Surfaces: []Surface{Actions}},
	{Op: FollowUp, Default: "F", Meaning: "follow up on a finished task", Kind: KindAction, Surfaces: []Surface{Actions}},
	{Op: Chat, Default: "T", Meaning: "chat in the task's worktree, or reopen its open chat", Kind: KindAction, Surfaces: []Surface{Actions}},

	{Op: Palette, Default: ":", Meaning: "open the command palette", Kind: KindGlobal, Surfaces: []Surface{Global}},
	{Op: PaletteAlt, Default: "ctrl+p", Meaning: "open the command palette from a text field", Kind: KindGlobal, Surfaces: []Surface{Global, "chat", "new chat"}, Typing: true},
	{Op: Help, Default: "?", Meaning: "toggle the help", Kind: KindGlobal, Surfaces: []Surface{Global}},
	{Op: HelpAlt, Default: "f1", Meaning: "toggle the help from a text field", Kind: KindGlobal, Surfaces: []Surface{Global, "chat", "new chat"}, Typing: true},
	{Op: NextAttention, Default: "!", Meaning: "jump to the next task needing a human", Kind: KindGlobal, Surfaces: []Surface{Global}},
	{Op: Mouse, Default: "M", Meaning: "toggle the mouse", Kind: KindGlobal, Surfaces: []Surface{Global}},
	{Op: Quit, Default: "q", Meaning: "quit the TUI", Kind: KindGlobal, Surfaces: []Surface{Global}},
	// `new` is §15's one deliberate two-meaning key, and the meanings are one
	// gesture — "make a new one here" — so the chats board's `n` and the
	// issue screens' (task 130.12) are this operation and move with it
	// (decision 1).
	{Op: New, Default: "n", Meaning: "make a new task, or a new chat on the chats board, or a new issue on the issue screens", Kind: KindGlobal, Surfaces: []Surface{Global, "chats", "issues", "issue"}},
	// Not Typing: a text field types `@` (the chat composer's file mention
	// among them), and ctrl+p's palette row reaches the picker from there.
	{Op: Project, Default: "@", Meaning: "switch project", Kind: KindGlobal, Surfaces: []Surface{Global}},
}

// Catalog returns every rebindable operation, in documentation order.
func Catalog() []Info {
	out := make([]Info, len(catalog))
	copy(out, catalog)
	return out
}

// Lookup returns one operation's catalog entry.
func Lookup(op Op) (Info, bool) {
	for _, info := range catalog {
		if info.Op == op {
			return info, true
		}
	}
	return Info{}, false
}

// actionsLive is where §6's actions are answered: the task board and every tab
// of the task workspace. A takeover screen, the archived boards and every popup
// offer none — the popups because they own the keyboard, the archived boards
// because an archived task offers no action (the TUI's tests prove that one
// against taskstate).
var actionsLive = map[Surface]bool{
	"task table": true, "task overview": true, "timeline": true, "task details": true, "output": true,
	"diff": true, "task workflow": true, "task step details": true, "task pull request": true,
}

// ActionsLive reports whether §6's action keys are answered on s.
func ActionsLive(s Surface) bool { return actionsLive[s] }

// capturesText is where a text field owns every printable key whenever the
// surface is up: the chat's composer and the new-chat form (decision 5).
var capturesText = map[Surface]bool{"chat": true, "new chat": true}

// CapturesText reports whether a text field owns the printable keys on s.
func CapturesText(s Surface) bool { return capturesText[s] }

// listLayer records the operations a capturesText surface answers from a list
// drawn *over* its rows rather than from the rows themselves.
//
// capturesText is a statement about a form's own rows: on the new-chat form
// every printable key reaches a text field (task 067 decision 8), which is why
// the clause below refuses to bind a printable key to anything offered there.
// An open picker is a layer above those rows and is navigating, not typing —
// its filter and its free-text entry are the two states that type, and both
// are entered from it. `t` is offered only while that list has the keyboard,
// so the blanket clause would refuse a row that is in fact reachable, which is
// how the new-chat form's free-text key went unregistered from issue #281
// until task 125.9.
//
// Keyed by operation and surface together: moving the operation to a surface
// whose rows really do swallow it is not covered.
var listLayer = map[string]bool{
	listLayerKey(FreeText, "new chat"): true,
}

func listLayerKey(op Op, s Surface) string { return string(op) + "\x00" + string(s) }

// Fixed is a key the TUI answers that no operation owns: a surface-local row,
// a member of a multi-key set, a popup's own key, or an unregistered alias.
type Fixed struct {
	Surface Surface
	Key     string
	Meaning string
}

// FixedKeys returns every fixed key, for the TUI's test that holds the list
// against its registry and its handlers.
func FixedKeys() []Fixed {
	out := make([]Fixed, len(fixed))
	copy(out, fixed)
	return out
}

// exception is a recorded decision that lets a key carry more than one meaning.
// It is tied to the key, not to the operations: move one of the operations and
// the exception no longer covers it, which is what makes the shipped defaults
// the only keymap these were ever decided for.
type exception struct {
	key string
	// ops may share key with each other.
	ops []Op
	// anyFixed lets ops share key with fixed meanings too.
	anyFixed bool
	why      string
}

var exceptions = []exception{
	{key: "R", ops: []Op{Refresh, Repair}, why: "task 025: repair is on the task workspace, where no screen that re-reads a registry is"},
	{key: "a", ops: []Op{Add, Approve}, why: "§6 approve, gated on available_actions and never offered where add is"},
	{key: "s", ops: []Op{Scope, Skip}, why: "§6 skip, gated on available_actions; an archived row offers none"},
	{key: "l", ops: []Op{Lane}, anyFixed: true, why: "task 052.6 link on the pull-request takeover, and l as vim-right where no lane is selected"},
	{key: "e", ops: []Op{Editor}, anyFixed: true, why: "e opens the selected row's form on the project overview, and enter or e on the daemon screen"},
	{key: "d", ops: []Op{DraftRemove}, anyFixed: true, why: "d toggles the output and diff tabs in the task workspace, where no draft is open"},
	{key: "n", ops: []Op{New}, anyFixed: true, why: "n is the popups' no, and the popups own the keyboard"},
	{key: "N", ops: []Op{NextFailure}, anyFixed: true, why: "task 129.18: N is the popups' no too, and the popups own the keyboard"},
	{key: "r", ops: []Op{Retry}, anyFixed: true, why: "r retries the connection while disconnected, when no task is on screen"},
	{key: "enter", ops: []Op{OpenRow}, anyFixed: true, why: "enter activates the focused thing on every surface"},
	{key: "T", ops: []Op{Chat}, anyFixed: true, why: "task 119: T dry-runs a trigger on the triggers takeover, which offers no available_actions"},
}

// Keymap is an effective keymap: every operation's key, defaults with
// overrides applied. The zero value is the shipped keymap.
type Keymap struct {
	keys map[Op]string
	// unbound are operations whose default a user override took on a lenient
	// load (task 128 decision 2): they answer no key until tui.keys gives them
	// one.
	unbound map[Op]bool
	// shadowed are fixed keys a user override took on a lenient load, keyed by
	// fixedKey: the fixed meaning yields on its surface (task 128 decision 3).
	shadowed map[string]bool
}

// Default returns the shipped keymap.
func Default() Keymap { return Keymap{} }

// Key returns the key op is bound to, or "" when op is unbound — a default a
// user override took on a lenient load (task 128 decision 2). Every reader
// treats "" as "no key".
func (k Keymap) Key(op Op) string {
	if key, ok := k.keys[op]; ok {
		return key
	}
	if k.unbound[op] {
		return ""
	}
	if info, ok := Lookup(op); ok {
		return info.Default
	}
	return ""
}

// Overridden reports whether op is bound to something other than its default.
func (k Keymap) Overridden(op Op) bool {
	info, ok := Lookup(op)
	return ok && k.Key(op) != info.Default
}

// Shadowed reports whether the fixed key on s yields to a user-bound
// operation (task 128 decision 3). A handler that answers a fixed key asks
// this first, and `?`, the palette and the footer show a shadowed row as
// unbound.
func (k Keymap) Shadowed(s Surface, key string) bool { return k.shadowed[fixedKey(s, key)] }

func fixedKey(s Surface, key string) string { return string(s) + "\x00" + key }

// retiredOps are operation ids a release removed, each with a note for the
// human whose tui.keys still names one (task 128 decision 1). Build drops such
// an id with a warning rather than refusing the file, so a config.yaml that was
// valid on the release before keeps the daemon starting. A retired id is never
// aliased onto another operation — an override replaces, it does not alias
// (task 118 decision 6) — so the note says what to bind instead.
var retiredOps = map[string]string{}

// fixedNames are names a reader might reasonably try in tui.keys for keys that
// are deliberately not rebindable. Refusing them by name says why, rather than
// "unknown operation" (decision 1).
var fixedNames = map[string]string{
	"group":     "g is a surface-local row of the task table",
	"lanes":     "L is a surface-local row of the task table",
	"merge":     "m is a surface-local row of the Pull Request tab",
	"fold":      "fold is a set of keys (←/→, C/O, space), not one",
	"fold_all":  "fold is a set of keys (←/→, C/O, space), not one",
	"page":      "page is a pair of keys (</>), not one",
	"select":    "selection moves on ↑/↓ everywhere",
	"views":     "the task tabs move on tab and [/] everywhere",
	"back":      "esc is the layer stack, and every layer is closed by it",
	"esc":       "esc is the layer stack, and every layer is closed by it",
	"close":     "esc is the layer stack, and every layer is closed by it",
	"interrupt": "ctrl+c must always kill the TUI",
	"paste":     "ctrl+v is the terminal's paste fallback",
	"tab":       "tab and shift+tab move focus everywhere",
	"confirm":   "a popup's y/n answer the question the popup prints",
	"decline":   "a popup's y/n answer the question the popup prints",
	"yes":       "a popup's y/n answer the question the popup prints",
	"no":        "a popup's y/n answer the question the popup prints",
	"resume":    "resume is the pause operation: one key for both",
	"follow":    "f is a surface-local row of the output and daemon panes",
}

// Build applies overrides — `tui.keys` as written — to the defaults and checks
// the result strictly. Every problem is reported, sorted, so one edit can fix
// them all. It is the write path's check (PATCH /v1/config): an override that
// lands on a key another operation's default or a fixed key holds is refused
// (task 118 decision 3). A retired id is dropped rather than refused.
func Build(overrides map[string]string) (Keymap, error) {
	km, _, err := build(overrides, false)
	return km, err
}

// BuildLenient is Build for the load paths — daemon start, hot reload and the
// TUI applying a fetched config (task 128 decision 2). An override that lands
// on a key a non-overridden operation's default or a fixed key holds wins: the
// default becomes unbound, the fixed key is shadowed, and a warning says so.
// That is the clash an upgrade introduces when it adds a default the user had
// already bound. A retired id is dropped with a warning. Everything else is
// still an error: an unknown id, bad key syntax, two overrides on one key, and
// the text-field rules (task 118 decision 5).
func BuildLenient(overrides map[string]string) (Keymap, []string, error) {
	return build(overrides, true)
}

func build(overrides map[string]string, lenient bool) (Keymap, []string, error) {
	var errs, warnings []string
	keys := make(map[Op]string, len(overrides))
	for name, key := range overrides {
		op := Op(name)
		info, ok := Lookup(op)
		if !ok {
			if note, retired := retiredOps[name]; retired {
				warnings = append(warnings, fmt.Sprintf("%s: retired operation, override ignored — %s", name, note))
			} else if why, fixedName := fixedNames[name]; fixedName {
				errs = append(errs, fmt.Sprintf("%s: not rebindable — %s", name, why))
			} else {
				errs = append(errs, fmt.Sprintf("%s: unknown operation; want one of %s", name, strings.Join(opNames(), ", ")))
			}
			continue
		}
		norm, err := ParseKey(key)
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", name, err))
			continue
		}
		if norm != info.Default {
			keys[op] = norm
		}
	}
	if len(errs) > 0 {
		sort.Strings(errs)
		return Keymap{}, nil, errors.New(strings.Join(errs, "; "))
	}
	km := Keymap{keys: keys}
	if lenient {
		warnings = append(warnings, km.yield()...)
	}
	if err := Check(km); err != nil {
		return Keymap{}, nil, err
	}
	sort.Strings(warnings)
	return km, dedupe(warnings), nil
}

// yield lets every override win over what an upgrade may have put on its key:
// a non-overridden operation's default becomes unbound and a fixed key is
// shadowed. What two overrides do to each other is left to Check, which still
// refuses it — that clash is the user's own.
func (k *Keymap) yield() []string {
	var warnings []string
	for _, info := range catalog {
		key, ok := k.keys[info.Op]
		if !ok {
			continue
		}
		for _, other := range catalog {
			if _, moved := k.keys[other.Op]; moved || other.Op == info.Op || other.Default != key {
				continue
			}
			if excepted(key, info.Op, use{op: other.Op}) {
				continue
			}
			if k.unbound == nil {
				k.unbound = map[Op]bool{}
			}
			k.unbound[other.Op] = true
			warnings = append(warnings, fmt.Sprintf(
				"%s: %q is %s's default (%s); %s is unbound until tui.keys gives it a key",
				info.Op, key, other.Op, other.Meaning, other.Op))
		}
		for _, f := range fixed {
			// The root's own fixed keys — tab, esc, ctrl+c, ctrl+v — never
			// yield: the layer stack and the way out of the TUI must work on
			// every surface, so a clash with one stays a hard error.
			if f.Key != key || f.Surface == Global || excepted(key, info.Op, use{surface: f.Surface}) {
				continue
			}
			if k.shadowed == nil {
				k.shadowed = map[string]bool{}
			}
			k.shadowed[fixedKey(f.Surface, f.Key)] = true
			warnings = append(warnings, fmt.Sprintf(
				"%s: %q no longer does %q on %s", info.Op, key, f.Meaning, f.Surface))
		}
	}
	return warnings
}

// use is one meaning a key carries somewhere.
type use struct {
	op      Op // empty for a fixed key
	surface Surface
	meaning string
}

func (u use) String() string {
	if u.op != "" {
		return fmt.Sprintf("%s (%s)", u.op, u.meaning)
	}
	return fmt.Sprintf("%q on %s", u.meaning, u.surface)
}

// Check holds a keymap to §15's three clauses and to decision 5's text-field
// rule. It is the function the TUI's registry tests run over the defaults and
// config runs over an override, so it reports a problem in the words a user
// can act on: the operation, the key, and what the key already means.
func Check(k Keymap) error {
	byKey := map[string][]use{}
	for _, info := range catalog {
		key := k.Key(info.Op)
		if key == "" {
			continue
		}
		byKey[key] = append(byKey[key], use{op: info.Op, meaning: info.Meaning})
	}
	for _, f := range fixed {
		if k.Shadowed(f.Surface, f.Key) {
			continue
		}
		byKey[f.Key] = append(byKey[f.Key], use{surface: f.Surface, meaning: f.Meaning})
	}

	var errs []string
	for _, info := range catalog {
		key := k.Key(info.Op)
		if key == "" {
			// Unbound on a lenient load (task 128): it carries no key to check.
			continue
		}
		printable, err := printableKey(key)
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", info.Op, err))
			continue
		}
		if printable {
			if info.Typing {
				errs = append(errs, fmt.Sprintf(
					"%s: %q is a key a text field types; this operation exists to work while one has the keyboard, so bind it to a ctrl, alt or function key",
					info.Op, key))
			}
			for _, s := range info.Surfaces {
				if CapturesText(s) && !info.Typing && !listLayer[listLayerKey(info.Op, s)] {
					errs = append(errs, fmt.Sprintf(
						"%s: %q would be typed into the text field on %s; bind it to a ctrl, alt or function key",
						info.Op, key, s))
				}
			}
		}
		for _, other := range byKey[key] {
			if other.op == info.Op {
				continue
			}
			if excepted(key, info.Op, other) {
				continue
			}
			// Report an op-vs-op clash once, from the side listed first.
			if other.op != "" && opIndex(other.op) < opIndex(info.Op) {
				continue
			}
			errs = append(errs, fmt.Sprintf("%s: %q already means %s — one key, one meaning (§15)",
				info.Op, key, other))
		}
	}
	if len(errs) == 0 {
		return nil
	}
	sort.Strings(errs)
	return errors.New(strings.Join(dedupe(errs), "; "))
}

// excepted reports whether a recorded exception lets op share key with other.
func excepted(key string, op Op, other use) bool {
	for _, e := range exceptions {
		if e.key != key || !hasOp(e.ops, op) {
			continue
		}
		if other.op == "" && e.anyFixed {
			return true
		}
		if other.op != "" && hasOp(e.ops, other.op) {
			return true
		}
	}
	return false
}

func hasOp(ops []Op, op Op) bool {
	for _, o := range ops {
		if o == op {
			return true
		}
	}
	return false
}

func opIndex(op Op) int {
	for i, info := range catalog {
		if info.Op == op {
			return i
		}
	}
	return len(catalog)
}

func opNames() []string {
	out := make([]string, 0, len(catalog))
	for _, info := range catalog {
		out = append(out, string(info.Op))
	}
	return out
}

func dedupe(sorted []string) []string {
	out := sorted[:0]
	for i, s := range sorted {
		if i == 0 || s != sorted[i-1] {
			out = append(out, s)
		}
	}
	return out
}
