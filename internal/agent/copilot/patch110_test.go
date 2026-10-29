package copilot

import (
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/agent"
)

// TestEditPatch: an edit tool's completion carries a `diff --git` in
// result.detailedContent, which rides on its tool_result as the hunks alone
// (task 110). `view` writes a diff too — the file it read as context lines —
// and produces no patch: only an edit's diff is a change.
func TestEditPatch(t *testing.T) {
	var patches []*agent.Patch
	for _, ev := range parseFixture(t, "edit_1.0.105.jsonl") {
		if ev.Type == agent.EventToolResult && ev.Results[0].Name == "view" && ev.Patch != nil {
			t.Errorf("view produced a patch: %+v", ev.Patch)
		}
		if ev.Patch != nil {
			patches = append(patches, ev.Patch)
		}
	}
	want := agent.Patch{
		CallID: "call_RhhljI3744KJKVC1KyO45z4b", Name: "edit",
		Text: "@@ -1,2 +1,2 @@\n-hello\n+goodbye\n \n",
	}
	if len(patches) != 1 || *patches[0] != want {
		t.Fatalf("patches = %+v, want %+v", patches, want)
	}
}

// TestApplyPatchPatch: sigterm_1.0.105.jsonl's `apply_patch` created a file
// before the run was stopped, and its "create file" diff is a patch of
// additions from a `@@ -1,0` hunk.
func TestApplyPatchPatch(t *testing.T) {
	var patches []*agent.Patch
	for _, ev := range parseFixture(t, "sigterm_1.0.105.jsonl") {
		if ev.Patch != nil {
			patches = append(patches, ev.Patch)
		}
	}
	if len(patches) != 1 {
		t.Fatalf("patches = %d, want apply_patch's one", len(patches))
	}
	p := patches[0]
	if p.CallID != "custom_call_SXFwcf5vMyxUdaE6ChZ7XBnF" || p.Name != "apply_patch" || p.Truncated ||
		!strings.HasPrefix(p.Text, "@@ -1,0 +1,10 @@\n+# The History of Version Control\n") {
		t.Errorf("patch = %+v, want apply_patch's hunk", p)
	}
}

// TestPatchOnlyFromAnEdit: over every capture, only an edit tool produces a
// patch — restricted_1.0.105.jsonl's directory `view` writes a "create file"
// diff too, and it is not one.
func TestPatchOnlyFromAnEdit(t *testing.T) {
	allFixtures(t, func(name string, i int, ev agent.Event) {
		if ev.Patch != nil && !editTools[ev.Results[0].Name] {
			t.Errorf("%s line %d: produced a patch from %+v", name, i, ev.Results)
		}
	})
}

// TestPatchShapes: apply_patch is an edit tool too; a failed edit, or a diff
// with no hunk, carries no patch; a long one is capped.
func TestPatchShapes(t *testing.T) {
	long := make([]byte, agent.PatchMax+10)
	for i := range long {
		long[i] = '+'
	}
	tests := []struct {
		tool, complete string
		want           string
		truncated      bool
	}{
		{
			tool:     "apply_patch",
			complete: `"success":true,"result":{"content":"ok","detailedContent":"\ndiff --git a/x b/x\n--- a/x\n+++ b/x\n@@ -1 +1 @@\n-a\n+b\n"}`,
			want:     "@@ -1 +1 @@\n-a\n+b\n",
		},
		{tool: "edit", complete: `"success":false,"error":{"message":"no match"}`},
		{tool: "edit", complete: `"success":true,"result":{"content":"ok","detailedContent":"\ndiff --git a/x b/x\n"}`},
		{
			tool:      "edit",
			complete:  `"success":true,"result":{"content":"ok","detailedContent":"@@ -1 +1 @@\n` + string(long) + `"}`,
			want:      ("@@ -1 +1 @@\n" + string(long))[:agent.PatchMax],
			truncated: true,
		},
	}
	for _, tt := range tests {
		parse := New(nil).NewLineParser()
		parse([]byte(`{"type":"tool.execution_start","data":{"toolCallId":"c","toolName":"` + tt.tool + `","arguments":{}}}`))
		ev := parse([]byte(`{"type":"tool.execution_complete","data":{"toolCallId":"c",` + tt.complete + `}}`))
		switch {
		case tt.want == "":
			if ev.Patch != nil {
				t.Errorf("%s %.40s → %+v, want no patch", tt.tool, tt.complete, ev.Patch)
			}
		case ev.Patch == nil || ev.Patch.Text != tt.want || ev.Patch.Truncated != tt.truncated || ev.Patch.Name != tt.tool:
			t.Errorf("%s %.40s → %+v, want %.40q truncated=%v", tt.tool, tt.complete, ev.Patch, tt.want, tt.truncated)
		}
	}
}
