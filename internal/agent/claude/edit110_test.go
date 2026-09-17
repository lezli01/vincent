package claude

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/lezli01/vincent/internal/agent"
)

// editFixture is trimmed from recorded claude 2.1.268 runs (task 110): one
// call line and one result line per case, in this order. Paths are rewritten
// under C:\work\repo and file bodies sanitized; the hunks, the line structure
// and the field names are verbatim.
const editFixture = "stream_edit_2.1.268.jsonl"

// editResult returns the result event at a fixture line, failing unless it is
// one with exactly one result.
func editResult(t *testing.T, events []agent.Event, line int) (agent.Event, agent.ToolResult) {
	t.Helper()
	ev := events[line]
	if ev.Type != agent.EventToolResult || len(ev.Results) != 1 {
		t.Fatalf("line %d = %+v, want one tool result", line, ev)
	}
	return ev, ev.Results[0]
}

// TestEditDelta pins the `+N −M` outcome and the unified patch of the three
// Edit shapes the corpus holds. The counts are the `+` and `-` body lines
// across every hunk: the single hunk's header says 7 → 8 lines, its body has
// nine, and only three of them changed.
func TestEditDelta(t *testing.T) {
	events, _ := fixtureEvents(t, editFixture)
	for _, tc := range []struct {
		name    string
		line    int
		callID  string
		summary string
		hunks   int
	}{
		{"single hunk", 1, "toolu_01ACQGdXtoSdabViwiM14D9q", "+2 −1", 1},
		{"two hunks summed", 3, "toolu_012nsBtBbbYXr8xFMYENumrv", "+12 −6", 2},
		{"replace_all", 5, "toolu_01CP1FG1A9iYLPpjWCjYTZby", "+2 −2", 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ev, r := editResult(t, events, tc.line)
			if r.CallID != tc.callID || r.Summary != tc.summary {
				t.Errorf("result = %+v, want call %s with summary %q", r, tc.callID, tc.summary)
			}
			// An Edit's payload carries no `type`, and none is inferred.
			if r.Verb != "" || r.IsError {
				t.Errorf("result = %+v, want no verb and no error", r)
			}
			if ev.Patch == nil {
				t.Fatal("no patch")
			}
			if ev.Patch.CallID != tc.callID || ev.Patch.Truncated {
				t.Errorf("patch = %+v, want call %s, untruncated", ev.Patch, tc.callID)
			}
			if got := strings.Count(ev.Patch.Text, "@@ -"); got != tc.hunks {
				t.Errorf("patch has %d hunk headers, want %d:\n%s", got, tc.hunks, ev.Patch.Text)
			}
		})
	}

	ev, _ := editResult(t, events, 1)
	want := strings.Join([]string{
		"@@ -590,7 +590,8 @@",
		" // bytes, decode the candidate, refuse the request without touching the disk if",
		" // it does not hold, write atomically at 0600, then apply synchronously. A GET",
		" // issued the instant a 200 lands reads the new values, with no sleep — the",
		"-// fsnotify watcher's later fire re-reads identical bytes and is a no-op.",
		"+// fsnotify watcher reads the file under the applier's lock, so its fire",
		"+// either precedes this apply or re-reads identical bytes and is a no-op.",
		" //",
		" // One mutex serializes the read-modify-write (decision 6). A hand-edit racing",
		" // a patch is last-writer-wins and undetected, which is the posture PATCH",
	}, "\n")
	if ev.Patch.Text != want {
		t.Errorf("patch text =\n%s\nwant\n%s", ev.Patch.Text, want)
	}
	ev, _ = editResult(t, events, 3)
	if !strings.Contains(ev.Patch.Text, "\n@@ -338,11 +343,12 @@\n   \"$(jq -cn") {
		t.Errorf("second hunk is not under its own header:\n%s", ev.Patch.Text)
	}
}

// TestWriteUpdateAndCreate: an overwrite is `updated` with a delta and a
// patch; a new file stays `created` with the prose summary it rendered
// before, because its structuredPatch is always empty.
func TestWriteUpdateAndCreate(t *testing.T) {
	events, _ := fixtureEvents(t, editFixture)

	ev, r := editResult(t, events, 7)
	if r.Verb != "updated" || r.Summary != "+1 −1" {
		t.Errorf("update = %+v, want verb updated and summary +1 −1", r)
	}
	wantPatch := "@@ -1,1 +1,1 @@\n" +
		"-feat(trigger): finish event triggers — daemon wiring, routes, CLI, TUI view, GitHub, reactions and HTTP ingress\n" +
		"+Finish event triggers: daemon wiring, routes, CLI, TUI view, GitHub sources, reactions and HTTP ingress"
	if ev.Patch == nil || ev.Patch.Text != wantPatch || ev.Patch.CallID != r.CallID {
		t.Errorf("update patch = %+v, want\n%s", ev.Patch, wantPatch)
	}

	ev, r = editResult(t, events, 9)
	if r.Verb != "created" || !strings.HasPrefix(r.Summary, "File created successfully at: ") {
		t.Errorf("create = %+v, want verb created and the tool's prose", r)
	}
	if ev.Patch != nil {
		t.Errorf("create produced a patch: %+v", ev.Patch)
	}
}

// TestFailedAndNestedEditsKeepProse: a failed Edit reports a string
// tool_use_result and a subagent's Edit reports none at all, so neither has a
// delta or a patch to give — which is the dialect, stated rather than worked
// around.
func TestFailedAndNestedEditsKeepProse(t *testing.T) {
	events, _ := fixtureEvents(t, editFixture)

	ev, r := editResult(t, events, 11)
	if !r.IsError || !strings.HasPrefix(r.Summary, "<tool_use_error>String to replace not found") {
		t.Errorf("failed edit = %+v, want an error with the tool's prose", r)
	}
	if ev.Patch != nil {
		t.Errorf("failed edit produced a patch: %+v", ev.Patch)
	}

	ev, r = editResult(t, events, 13)
	if ev.ParentCallID == "" {
		t.Fatalf("line 13 is not a subagent's: %+v", ev)
	}
	if !strings.HasPrefix(r.Summary, `The file C:\work\repo\internal\workflow\condition.go has been updated`) {
		t.Errorf("nested edit summary = %q, want the tool's prose", r.Summary)
	}
	if ev.Patch != nil {
		t.Errorf("nested edit produced a patch: %+v", ev.Patch)
	}
}

// TestOverCapPatch: a patch past agent.PatchMax is cut on a rune boundary and
// says so, and its delta is still counted from the whole patch.
func TestOverCapPatch(t *testing.T) {
	lines := make([]string, 0, 3001)
	for range 1500 {
		lines = append(lines, "+ŝŝŝ", "-ŝŝŝ")
	}
	lines = append(lines, " ŝŝŝ")
	res, err := json.Marshal(map[string]any{
		"filePath": `C:\work\repo\big.go`,
		"structuredPatch": []map[string]any{{
			"oldStart": 1, "oldLines": 1501, "newStart": 1, "newLines": 1501, "lines": lines,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	line := `{"type":"user","message":{"content":[{"type":"tool_result",` +
		`"tool_use_id":"t1","content":"The file has been updated successfully."}]},` +
		`"tool_use_result":` + string(res) + `}`
	ev := parseLine([]byte(line))
	if len(ev.Results) != 1 || ev.Results[0].Summary != "+1500 −1500" {
		t.Errorf("results = %+v, want the whole patch's delta", ev.Results)
	}
	if ev.Patch == nil || !ev.Patch.Truncated {
		t.Fatalf("patch = %+v, want truncated", ev.Patch)
	}
	if n := utf8.RuneCountInString(ev.Patch.Text); n != agent.PatchMax || !utf8.ValidString(ev.Patch.Text) {
		t.Errorf("patch is %d runes (valid UTF-8: %v), want %d", n, utf8.ValidString(ev.Patch.Text), agent.PatchMax)
	}
}

// TestEmptyPatchInventsNoDelta: a result whose structuredPatch is empty keeps
// the summary it had, rather than reporting `+0 −0`.
func TestEmptyPatchInventsNoDelta(t *testing.T) {
	line := `{"type":"user","message":{"content":[{"type":"tool_result",` +
		`"tool_use_id":"t1","content":"ok"}]},` +
		`"tool_use_result":{"filePath":"C:\\work\\repo\\a.go","structuredPatch":[]}}`
	ev := parseLine([]byte(line))
	if len(ev.Results) != 1 || ev.Results[0].Summary != "ok" || ev.Patch != nil {
		t.Errorf("event = %+v, want summary ok and no patch", ev)
	}
}

// TestNoPatchInOlderFixtures: the three 2.1.226 captures hold a Write of type
// create and no edit, and normalize exactly as they did before task 110 — no
// patch, and no verb but `created`.
func TestNoPatchInOlderFixtures(t *testing.T) {
	for _, name := range []string{
		"stream_permission_allow_2.1.226.jsonl",
		"stream_permission_deny_2.1.226.jsonl",
		"stream_question_2.1.226.jsonl",
	} {
		events, _ := fixtureEvents(t, name)
		for i, ev := range events {
			if ev.Patch != nil {
				t.Errorf("%s line %d: produced a patch", name, i)
			}
			for _, r := range ev.Results {
				if r.Verb != "" && r.Verb != "created" {
					t.Errorf("%s line %d: verb %q", name, i, r.Verb)
				}
				if strings.HasPrefix(r.Summary, "+") {
					t.Errorf("%s line %d: summary %q reads as a delta", name, i, r.Summary)
				}
			}
		}
	}
}
