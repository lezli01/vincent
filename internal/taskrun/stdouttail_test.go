package taskrun

// A command step's `.Result` is its **stdout** tail (spec §8.4), which is what
// makes stdout a reliable channel for the structured output a `for_each:`
// (§7.6, §7.8) splits into items. Everything else a command writes — a
// progress meter, `Switched to branch …`, a deprecation notice, a human
// header a workflow author deliberately kept out of the list — goes to stderr
// and must not become an item. Issue #311.

import (
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/store"
)

// noisyEmit prints lines to stdout and one line to stderr, on either shell
// (§8.3). Which stream lands first is not determined — two goroutines read two
// pipes — and that is precisely why the separation has to happen at capture
// rather than by filtering afterwards.
func noisyEmit(stderrLine string, stdoutLines ...string) string {
	quoted := make([]string, 0, len(stdoutLines))
	for _, line := range stdoutLines {
		quoted = append(quoted, "'"+line+"'")
	}
	return script(
		"printf '%s\\n' '"+stderrLine+"' >&2\nprintf '%s\\n' "+strings.Join(quoted, " "),
		"[Console]::Error.WriteLine('"+stderrLine+"')\nWrite-Output "+strings.Join(quoted, ", "),
	)
}

// TestLoopForEachIgnoresStderr: the list a loop iterates comes from the
// producing step's stdout alone. A step that also wrote to stderr must not
// gain an iteration for the line it wrote there.
func TestLoopForEachIgnoresStderr(t *testing.T) {
	h := newEngineHarness(t)
	h.start(t)
	snapshot := "name: each\nsteps:\n" +
		commandStep("discover", noisyEmit("discovered 2 items", "alpha", "beta"), "max_retries: 0") +
		"  - id: visit\n    type: loop\n    for_each: '{{ .Steps.discover.Result }}'\n    steps:\n" +
		bodyIndent(commandStep("touch", appendCmd("items.txt", "{{ .Loop.Item }}")))
	task := h.createTask(t, snapshot)

	done := h.waitForState(t, task.ID, store.TaskDone, store.TaskBlocked)
	if done.State != store.TaskDone {
		t.Fatalf("task state = %s (block_reason %q), want done", done.State, done.BlockReason)
	}
	var items []string
	for _, r := range h.stepRuns(t, task.ID) {
		if r.StepIndex == 1 && r.LoopItem != "" {
			items = append(items, r.LoopItem)
		}
	}
	if !equalStrings(items, []string{"alpha", "beta"}) {
		t.Errorf("loop items = %v, want [alpha beta]; .Result must be stdout only (§8.4)", items)
	}
}

// TestFanOutDerivesLanesIgnoringStderr is the shape issue #311 was reported
// from: a planner writes its human header to stderr so it stays out of the
// lane list, and the fan-out blocks `fan_out_invalid` on that header as though
// it were item 1.
func TestFanOutDerivesLanesIgnoringStderr(t *testing.T) {
	h := newEngineHarness(t)
	h.start(t)

	var sb strings.Builder
	sb.WriteString("name: root\nsteps:\n")
	sb.WriteString(commandStep("plan",
		noisyEmit("graph: 2 unit(s), 2 with no dependency",
			`{"id":"api","needs":[]}`, `{"id":"db","needs":[]}`),
		"max_retries: 0"))
	sb.WriteString("  - id: build\n    type: fan_out\n")
	sb.WriteString("    for_each: '{{ (index .Steps \"plan\").Result }}'\n")
	sb.WriteString("    lane:\n      id: '{{ .Item.id }}'\n      needs: '{{ .Item.needs }}'\n")
	sb.WriteString("      fields:\n        name: '{{ .Item.id }}'\n      steps:\n")
	sb.WriteString(indent(writeFileStep("write", `{{ index .Task.Fields "name" }}.txt`, "x")))

	task := h.createTask(t, sb.String())

	done := h.waitForStateWithin(t, task.ID, fanOutBudget, store.TaskDone, store.TaskBlocked)
	if done.State != store.TaskDone {
		t.Fatalf("parent state = %s (block_reason %q), want done — the stderr header "+
			"became an item in the derived lane list (§8.4)", done.State, done.BlockReason)
	}
	kids, err := h.store.ListChildren(t.Context(), task.ID)
	if err != nil {
		t.Fatalf("ListChildren: %v", err)
	}
	ids := make([]string, 0, len(kids))
	for _, kid := range kids {
		ids = append(ids, kid.LaneID)
	}
	if !equalStrings(ids, []string{"api", "db"}) {
		t.Errorf("derived lanes = %v, want [api db]", ids)
	}
}
