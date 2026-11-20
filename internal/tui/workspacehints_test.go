package tui

import (
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/apiclient"
)

// workspaceTabContexts is every task-workspace tab's context, with the digit
// that jumps to that tab (issue #595).
var workspaceTabContexts = []struct {
	ctx   bindingContext
	digit string
}{
	{ctxTaskOverview, "0"},
	{ctxTimeline, "1"},
	{ctxTaskDetails, "2"},
	{ctxOutput, "3"},
	{ctxDiff, "4"},
	{ctxTaskWorkflow, "5"},
	{ctxTaskStepDetails, "6"},
	{ctxTaskPull, "7"},
}

// TestWorkspaceFooterHasNoDuplicateOrSelfHints is issue #595's first
// acceptance criterion: the workspace footer never shows two hints for one
// operation (`tab tabs` beside `[/] tabs`), and never a hint for the tab
// already open (`5 workflow` on the Workflow tab).
func TestWorkspaceFooterHasNoDuplicateOrSelfHints(t *testing.T) {
	for _, w := range workspaceTabContexts {
		rows := bindingsFor(w.ctx)
		var tabHinted, bracketHinted bool
		for _, b := range rows {
			switch {
			case b.key == "tab" && b.hint != "":
				tabHinted = true
			case b.key == "]" && b.hint != "" && !b.aliased:
				bracketHinted = true
			case b.key == w.digit && b.hint != "":
				t.Errorf("%s: the footer advertises the tab already open: %q", w.ctx, b.hint)
			}
		}
		if tabHinted && bracketHinted {
			t.Errorf("%s: `tab` and `]` both carry a footer hint for one operation, and `]` is not marked aliased", w.ctx)
		}
	}
}

// TestWorkspaceHelpListsEveryWorkspaceKey is issue #595's second acceptance
// criterion: `?` on every workspace tab lists `tab`, `d`, `l` and `U` —
// every one of which taskView.Update answers from any tab — and, since task
// 164 took the digits away, `i` for the linked issue on every tab but Pull
// request, whose `i` is its comment.
func TestWorkspaceHelpListsEveryWorkspaceKey(t *testing.T) {
	for _, w := range workspaceTabContexts {
		keys := map[string]bool{}
		issue := false
		for _, b := range bindingsFor(w.ctx) {
			keys[b.key] = true
			if b.key == openIssueKey && strings.Contains(b.label, "linked issue") {
				issue = true
			}
		}
		for _, want := range []string{"tab", "d", "l", "U"} {
			if !keys[want] {
				t.Errorf("%s: help does not list %q, which the workspace handles on this tab", w.ctx, want)
			}
		}
		if !issue && w.ctx != ctxTaskPull {
			t.Errorf("%s: help lists no row for opening the linked issue", w.ctx)
		}
	}
}

// TestBlockedFooterKeepsRepairAndEditRetryAt80 is issue #595's third
// acceptance criterion: on a blocked task at 80 columns, `R repair` and
// `E edit & retry` are on the footer, not behind `+N` — they are the
// recovery actions a blocked task exists to offer.
func TestBlockedFooterKeepsRepairAndEditRetryAt80(t *testing.T) {
	blocked := taskActions{id: 12, state: stateBlocked, actions: []string{
		apiclient.ActionRetry, apiclient.ActionRepair, apiclient.ActionSkip,
		apiclient.ActionCancel, apiclient.ActionArchive, apiclient.ActionChat,
	}}
	for _, ctx := range []bindingContext{ctxTaskOverview, ctxTimeline, ctxOutput} {
		_, hits := buildFooter(80, bindingsFor(ctx), &actionBar{}, blocked, attentionTally{here: 1}, false, false)
		shown := map[string]bool{}
		for _, h := range hits {
			shown[h.key] = true
		}
		for _, want := range []string{"R", "E"} {
			if !shown[want] {
				t.Errorf("%s at 80 columns: a blocked task's footer does not show %q", ctx, want)
			}
		}
	}
}
