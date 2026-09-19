package cli

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/chatstate"
	"github.com/lezli01/vincent/internal/store"
)

// Task 124.12 prints the agent.skill record, which both transcript commands
// used to drop in renderTranscriptRecord's default arm. A model's load prints
// on its call's line whenever the two are in the same fetch (task 124
// decision 38).

func skillCallRec(callID, parent string) apiclient.TranscriptRecord {
	return apiclient.TranscriptRecord{Type: "agent.tool_use", ParentCallID: parent, Tools: []apiclient.TranscriptTool{
		{Name: "Skill", Summary: "echo-probe", CallID: callID},
	}}
}

func skillOutcomeRec(callID, parent string) apiclient.TranscriptRecord {
	return apiclient.TranscriptRecord{Type: "agent.tool_result", ParentCallID: parent, Results: []apiclient.TranscriptToolResult{
		{CallID: callID, Summary: "Launching skill: echo-probe"},
	}}
}

func skillLoadRec(callID, parent string) apiclient.TranscriptRecord {
	return apiclient.TranscriptRecord{
		Type: "agent.skill", Name: "echo-probe", Args: "zebra", By: "agent", CallID: callID, ParentCallID: parent,
	}
}

func TestRenderTranscriptSkillForms(t *testing.T) {
	paired := map[string]apiclient.TranscriptRecord{"c1": skillLoadRec("c1", "")}
	failed := map[string]apiclient.TranscriptRecord{"c1": {
		Type: "agent.skill", Name: "echo-probe", By: "agent", CallID: "c1", Error: "refused",
	}}
	for _, tc := range []struct {
		name   string
		rec    apiclient.TranscriptRecord
		skills map[string]apiclient.TranscriptRecord
		want   string
	}{
		{
			"the human's skill",
			apiclient.TranscriptRecord{Type: "agent.skill", Name: "tdd", Args: "red green", By: "human"},
			nil,
			"> skill tdd red green",
		},
		{
			"a skill with no arguments",
			apiclient.TranscriptRecord{Type: "agent.skill", Name: "tdd", By: "human"},
			nil,
			"> skill tdd",
		},
		{
			"a forked skill",
			apiclient.TranscriptRecord{Type: "agent.skill", Name: "fork-probe", By: "human", Forked: true},
			nil,
			"> skill fork-probe (forked)",
		},
		{"a model's load on its own", skillLoadRec("c1", ""), nil, "> skill echo-probe zebra"},
		{
			"a failure",
			apiclient.TranscriptRecord{Type: "agent.skill", Name: "tdd", By: "human", Error: "no such skill"},
			nil,
			"! skill tdd failed: no such skill",
		},
		{
			"a failure naming no skill",
			apiclient.TranscriptRecord{Type: "agent.skill", By: "agent", CallID: "c9", Error: "refused"},
			nil,
			"! skill invocation failed: refused",
		},
		{"a call that loaded a skill", skillCallRec("c1", ""), paired, "> skill echo-probe zebra"},
		{"a call whose load failed", skillCallRec("c1", ""), failed, "! skill echo-probe failed: refused"},
		{"a call with no load in the fetch", skillCallRec("c1", ""), nil, "> Skill echo-probe"},
		{"one call of several", apiclient.TranscriptRecord{Type: "agent.tool_use", Tools: []apiclient.TranscriptTool{
			{Name: "Read", Summary: "main.go", CallID: "c0"},
			{Name: "Skill", Summary: "echo-probe", CallID: "c1"},
		}}, paired, "> Read main.go, skill echo-probe zebra"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := renderTranscriptRecord(tc.rec, false, tc.skills)
			if !ok || got != tc.want {
				t.Errorf("rendered %q (%v), want %q", got, ok, tc.want)
			}
		})
	}
}

// batchSource hands a printer one fetched range per offset, which is what a
// `-f` poll is: each call to normalized is the next fetch.
type batchSource struct {
	batches [][]apiclient.TranscriptRecord
}

func (s batchSource) normalized(
	_ context.Context, opts apiclient.TranscriptOptions,
) ([]apiclient.TranscriptRecord, int64, error) {
	if int(opts.Offset) >= len(s.batches) {
		return nil, opts.Offset, nil
	}
	return s.batches[opts.Offset], opts.Offset + 1, nil
}

func (batchSource) raw(context.Context, apiclient.TranscriptOptions) ([]byte, int64, error) {
	return nil, 0, nil
}

func (batchSource) state(context.Context) (bool, string, error) { return false, "done", nil }

// printBatches prints each batch as its own fetch through one printer.
func printBatches(t *testing.T, batches ...[]apiclient.TranscriptRecord) []string {
	t.Helper()
	var out bytes.Buffer
	p := &transcriptPrinter{out: &out, errOut: &out}
	src := batchSource{batches: batches}
	var offset int64
	for range batches {
		next, err := p.printFrom(t.Context(), src, apiclient.TranscriptOptions{Offset: offset})
		if err != nil {
			t.Fatalf("printFrom: %v", err)
		}
		offset = next
	}
	return strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
}

func TestTranscriptPrintsASkillLoadOnce(t *testing.T) {
	reply := apiclient.TranscriptRecord{Type: "agent.output", Text: "ECHO-PROBE zebra"}
	for _, tc := range []struct {
		name    string
		batches [][]apiclient.TranscriptRecord
		want    []string
	}{
		{
			name: "one fetch: the call's line is the load's",
			batches: [][]apiclient.TranscriptRecord{
				{skillCallRec("c1", ""), skillOutcomeRec("c1", ""), skillLoadRec("c1", ""), reply},
			},
			want: []string{"> skill echo-probe zebra", "< Launching skill: echo-probe", "ECHO-PROBE zebra"},
		},
		{
			name: "a -f poll split the call from its load",
			batches: [][]apiclient.TranscriptRecord{
				{skillCallRec("c1", ""), skillOutcomeRec("c1", "")},
				{skillLoadRec("c1", ""), reply},
			},
			want: []string{"> Skill echo-probe", "< Launching skill: echo-probe", "ECHO-PROBE zebra"},
		},
		{
			name:    "a load whose call is not in range prints where it is",
			batches: [][]apiclient.TranscriptRecord{{skillLoadRec("c1", ""), reply}},
			want:    []string{"> skill echo-probe zebra", "ECHO-PROBE zebra"},
		},
		{
			name: "the human's skill prints where it is",
			batches: [][]apiclient.TranscriptRecord{{
				{Type: "agent.skill", Name: "fork-probe", Args: "zebra", By: "human", Forked: true}, reply,
			}},
			want: []string{"> skill fork-probe zebra (forked)", "ECHO-PROBE zebra"},
		},
		{
			name: "a subagent's load prints on the rail",
			batches: [][]apiclient.TranscriptRecord{{
				{Type: "agent.tool_use", Tools: []apiclient.TranscriptTool{{Name: "Agent", Summary: "helper", CallID: "p1"}}},
				skillCallRec("c2", "p1"), skillOutcomeRec("c2", "p1"), skillLoadRec("c2", "p1"),
				{Type: "agent.skill", Name: "tdd", By: "human", ParentCallID: "p1"},
			}},
			want: []string{
				"> Agent helper", "| -> helper",
				"| > skill echo-probe zebra", "| < Launching skill: echo-probe", "| > skill tdd",
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := printBatches(t, tc.batches...)
			if strings.Join(got, "\n") != strings.Join(tc.want, "\n") {
				t.Errorf("printed\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(tc.want, "\n"))
			}
		})
	}
}

// TestTranscriptSkillCaptures runs the real claude captures through the real
// handlers and both commands: the load is printed, the call is not printed a
// second time, the skill's body never appears (task 124 decision 39), and a
// chat turn reads exactly as a task attempt does.
func TestTranscriptSkillCaptures(t *testing.T) {
	for _, tc := range []struct {
		fixture string
		want    string
	}{
		{"stream_skill_model_2.1.277.jsonl", "> skill echo-probe zebra"},
		{"stream_skill_fork_2.1.277.jsonl", "> skill fork-probe (forked)"},
	} {
		t.Run(tc.fixture, func(t *testing.T) {
			h := newLiveHarness(t)
			lines := fixtureLines(t, filepath.Join("..", "agent", "claude", "testdata", tc.fixture))
			run := h.addRunAs(t, "claude", h.taskID, "implement", store.StepSucceeded, lines...)
			printed := renderedTranscript(t, h, run)
			all := strings.Join(printed, "\n")
			if n := strings.Count("\n"+all+"\n", "\n"+tc.want+"\n"); n != 1 {
				t.Errorf("%q printed %d times, want once:\n%s", tc.want, n, all)
			}
			for _, gone := range []string{"> Skill", "Base directory for this skill"} {
				if strings.Contains(all, gone) {
					t.Errorf("transcript shows %q:\n%s", gone, all)
				}
			}

			chat := h.addChat(t)
			h.addTurn(t, chat, chatstate.TurnDone, "", lines...)
			out, errOut, code := runCLI(t, chatArgs(chat)...)
			if code != 0 {
				t.Fatalf("exit = %d, want 0 (%s)", code, errOut)
			}
			if got := strings.TrimRight(strings.ReplaceAll(out, "\r\n", "\n"), "\n"); got != all {
				t.Errorf("chat turn printed\n%s\nwant what the task attempt prints:\n%s", got, all)
			}
		})
	}
}
