package tui

import (
	"bytes"
	"strings"
	"testing"

	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
)

// Task 124.12 draws the agent.skill record in the output pane and so in the
// chat body, which is the same renderer (task 071 decision 2). These hold its
// level table (task 124 decisions 37 and 39), its pairing with the `Skill`
// call it came from, and its sanitizing.

// skillCall, skillOutcome and skillLoad are a model's load in the order
// claude writes it: the `Skill` call, its "Launching skill" outcome, then the
// load the body line became.
func skillCall(callID, parent string) apiclient.TranscriptRecord {
	return apiclient.TranscriptRecord{Type: "agent.tool_use", ParentCallID: parent, Tools: []apiclient.TranscriptTool{
		{Name: "Skill", Summary: "echo-probe", CallID: callID},
	}}
}

func skillOutcome(callID, parent string) apiclient.TranscriptRecord {
	return apiclient.TranscriptRecord{Type: "agent.tool_result", ParentCallID: parent, Results: []apiclient.TranscriptToolResult{
		{CallID: callID, Summary: "Launching skill: echo-probe"},
	}}
}

func skillLoad(callID, parent string) apiclient.TranscriptRecord {
	return apiclient.TranscriptRecord{
		Type: "agent.skill", Name: "echo-probe", Args: "zebra", By: "agent", CallID: callID, ParentCallID: parent,
	}
}

// renderPlain is the pane at a level, styling stripped, one string per line.
func renderPlain(recs []apiclient.TranscriptRecord, level outputLevel) []string {
	return plainLines(outputLines(recs, level, 80, lineOpts{expandKey: "v"}))
}

// hasLine reports whether a rendered pane holds exactly this line, trailing
// padding aside.
func hasLine(lines []string, want string) bool {
	for _, l := range lines {
		if strings.TrimRight(l, " ") == want {
			return true
		}
	}
	return false
}

func TestSkillLevelTable(t *testing.T) {
	spawn := apiclient.TranscriptRecord{Type: "agent.tool_use", Tools: []apiclient.TranscriptTool{
		{Name: "Agent", Summary: "helper", CallID: "p1"},
	}}
	for _, tc := range []struct {
		name string
		recs []apiclient.TranscriptRecord
		want string
		// shown is the levels the line appears at; absent is the rest.
		shown []outputLevel
	}{
		{
			name:  "the human's skill shows at every level",
			recs:  []apiclient.TranscriptRecord{{Type: "agent.skill", Name: "tdd", Args: "red green", By: "human"}},
			want:  "▸ skill tdd red green",
			shown: allLevels,
		},
		{
			name:  "the model's load is hidden at quiet, like a tool call",
			recs:  []apiclient.TranscriptRecord{skillCall("c1", ""), skillOutcome("c1", ""), skillLoad("c1", "")},
			want:  "▸ skill echo-probe zebra",
			shown: []outputLevel{levelCompact, levelNormal, levelVerbose},
		},
		{
			name:  "a load with no call in the window follows the same rule",
			recs:  []apiclient.TranscriptRecord{skillLoad("c1", "")},
			want:  "▸ skill echo-probe zebra",
			shown: []outputLevel{levelCompact, levelNormal, levelVerbose},
		},
		{
			name:  "a forked skill says so",
			recs:  []apiclient.TranscriptRecord{{Type: "agent.skill", Name: "fork-probe", By: "human", Forked: true}},
			want:  "▸ skill fork-probe (forked)",
			shown: allLevels,
		},
		{
			name:  "the human's failed skill shows at every level",
			recs:  []apiclient.TranscriptRecord{{Type: "agent.skill", Name: "tdd", By: "human", Error: "no such skill"}},
			want:  "▸ skill tdd failed: no such skill",
			shown: allLevels,
		},
		{
			name:  "a failure that names no skill still shows",
			recs:  []apiclient.TranscriptRecord{{Type: "agent.skill", By: "human", Error: "refused"}},
			want:  "▸ skill invocation failed: refused",
			shown: allLevels,
		},
		{
			name: "the model's failed load shows at quiet, in its call's place",
			recs: []apiclient.TranscriptRecord{
				skillCall("c1", ""), skillOutcome("c1", ""),
				{Type: "agent.skill", Name: "echo-probe", By: "agent", CallID: "c1", Error: "refused"},
			},
			want:  "▸ skill echo-probe failed: refused",
			shown: allLevels,
		},
		{
			name: "the model's failed load with no name and no call shows",
			recs: []apiclient.TranscriptRecord{
				{Type: "agent.skill", By: "agent", CallID: "c9", Error: "refused"},
			},
			want:  "▸ skill invocation failed: refused",
			shown: allLevels,
		},
		{
			name: "a subagent's load is one level quieter",
			recs: []apiclient.TranscriptRecord{
				spawn, skillCall("c2", "p1"), skillOutcome("c2", "p1"), skillLoad("c2", "p1"),
			},
			want:  "┊ ▸ skill echo-probe zebra",
			shown: []outputLevel{levelNormal, levelVerbose},
		},
		{
			name: "a subagent's human-scoped load shows from compact",
			recs: []apiclient.TranscriptRecord{
				spawn, {Type: "agent.skill", Name: "tdd", By: "human", ParentCallID: "p1"},
			},
			want:  "┊ ▸ skill tdd",
			shown: []outputLevel{levelCompact, levelNormal, levelVerbose},
		},
		{
			name: "quiet shows nothing of a subagent, a failure included",
			recs: []apiclient.TranscriptRecord{
				spawn, {Type: "agent.skill", Name: "tdd", By: "agent", CallID: "c3", ParentCallID: "p1", Error: "refused"},
			},
			want:  "┊ ▸ skill tdd failed: refused",
			shown: []outputLevel{levelCompact, levelNormal, levelVerbose},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, level := range allLevels {
				lines := renderPlain(tc.recs, level)
				got := hasLine(lines, tc.want)
				want := false
				for _, l := range tc.shown {
					want = want || l == level
				}
				if got != want {
					t.Errorf("%s: line %q shown = %v, want %v:\n%s",
						level, tc.want, got, want, strings.Join(lines, "\n"))
				}
			}
		})
	}
}

// TestSkillLoadDrawnAtItsCall: a model's load is drawn once, where its call
// was, with its outcome directly under it — never as both `▸ Skill` and
// `▸ skill`.
func TestSkillLoadDrawnAtItsCall(t *testing.T) {
	recs := []apiclient.TranscriptRecord{
		skillCall("c1", ""), skillOutcome("c1", ""), skillLoad("c1", ""),
		{Type: "agent.output", Text: "ECHO-PROBE zebra"},
	}
	for _, level := range []outputLevel{levelCompact, levelNormal, levelVerbose} {
		lines := renderPlain(recs, level)
		joined := strings.Join(lines, "\n")
		if n := strings.Count(joined, "▸ skill"); n != 1 {
			t.Errorf("%s: %d skill lines, want 1:\n%s", level, n, joined)
		}
		if strings.Contains(joined, "▸ Skill") {
			t.Errorf("%s: the call was drawn as well as the load:\n%s", level, joined)
		}
		for i, l := range lines {
			if strings.TrimRight(l, " ") != "▸ skill echo-probe zebra" {
				continue
			}
			if i+1 >= len(lines) || strings.TrimRight(lines[i+1], " ") != "    ✓ Launching skill: echo-probe" {
				t.Errorf("%s: the outcome is not directly under the load:\n%s", level, joined)
			}
		}
	}

	// On a live tail the call arrives first and reads as the call until its
	// load lands.
	early := renderPlain(recs[:2], levelNormal)
	if !hasLine(early, "▸ Skill echo-probe") {
		t.Errorf("before its load the call should draw as itself:\n%s", strings.Join(early, "\n"))
	}

	// In a record of several calls, only the loading call's part changes.
	multi := []apiclient.TranscriptRecord{
		{Type: "agent.tool_use", Tools: []apiclient.TranscriptTool{
			{Name: "Read", Summary: "main.go", CallID: "c0"},
			{Name: "Skill", Summary: "echo-probe", CallID: "c1"},
		}},
		skillLoad("c1", ""),
	}
	if lines := renderPlain(multi, levelNormal); !hasLine(lines, "▸ Read main.go, skill echo-probe zebra") {
		t.Errorf("multi-call record:\n%s", strings.Join(lines, "\n"))
	}
	if lines := renderPlain(multi, levelQuiet); len(lines) != 0 {
		t.Errorf("quiet drew a multi-call record with no failure:\n%s", strings.Join(lines, "\n"))
	}
	multi[1].Error = "refused"
	quiet := renderPlain(multi, levelQuiet)
	if len(quiet) != 1 || strings.TrimRight(quiet[0], " ") != "▸ skill echo-probe failed: refused" {
		t.Errorf("quiet should keep the failed load and only it:\n%s", strings.Join(quiet, "\n"))
	}

	// A load whose call is not in the window draws where it arrived.
	alone := renderPlain(recs[2:], levelNormal)
	if len(alone) == 0 || strings.TrimRight(alone[0], " ") != "▸ skill echo-probe zebra" {
		t.Errorf("a load with no call in the window:\n%s", strings.Join(alone, "\n"))
	}
}

// TestSkillLoadDoesNotSplitAnUnrecognizedRun: a load drawn at its call has no
// line of its own, so two unrecognized lines either side of it are one count
// (compare the input echo, 47de6732).
func TestSkillLoadDoesNotSplitAnUnrecognizedRun(t *testing.T) {
	recs := []apiclient.TranscriptRecord{
		skillCall("c1", ""), skillOutcome("c1", ""),
		{Type: "agent.raw", Line: `{"type":"one"}`},
		skillLoad("c1", ""),
		{Type: "agent.raw", Line: `{"type":"two"}`},
	}
	for _, level := range []outputLevel{levelCompact, levelNormal} {
		joined := strings.Join(renderPlain(recs, level), "\n")
		if n := strings.Count(joined, "unrecognized line(s)"); n != 1 || !strings.Contains(joined, "… 2 unrecognized line(s)") {
			t.Errorf("%s: want one count of 2:\n%s", level, joined)
		}
	}
}

// TestSkillLiveAndRefetchedAgree is chatverbosity_test.go's equivalence for
// the new record: the chunk and the refetched record render identically at
// every level, the pairing included.
func TestSkillLiveAndRefetchedAgree(t *testing.T) {
	fetched := []apiclient.TranscriptRecord{
		{Type: "agent.skill", Name: "fork-probe", Args: "zebra", By: "human", Forked: true},
		skillCall("c1", ""), skillOutcome("c1", ""), skillLoad("c1", ""),
		{Type: "agent.skill", Name: "tdd", By: "human", Error: "refused"},
		{Type: "agent.output", Text: "done"},
	}
	live := []apiclient.OutputNote{
		{Type: "agent.skill", Payload: []byte(
			`{"chat_id":1,"turn_id":9,"offset":1,"name":"fork-probe","args":"zebra","by":"human","forked":true}`)},
		{Type: "agent.tool_use", Payload: []byte(
			`{"chat_id":1,"turn_id":9,"offset":2,"tools":[{"name":"Skill","summary":"echo-probe","call_id":"c1"}],"raw":"{}"}`)},
		{Type: "agent.tool_result", Payload: []byte(
			`{"chat_id":1,"turn_id":9,"offset":3,"results":[{"call_id":"c1","summary":"Launching skill: echo-probe"}],"raw":"{}"}`)},
		{Type: "agent.skill", Payload: []byte(
			`{"chat_id":1,"turn_id":9,"offset":4,"call_id":"c1","name":"echo-probe","args":"zebra","by":"agent"}`)},
		{Type: "agent.skill", Payload: []byte(
			`{"chat_id":1,"turn_id":9,"offset":5,"name":"tdd","by":"human","error":"refused"}`)},
		{Type: "agent.output", Payload: []byte(`{"chat_id":1,"turn_id":9,"offset":6,"text":"done","raw":"{}"}`)},
	}
	streamed := make([]apiclient.TranscriptRecord, 0, len(live))
	for _, note := range live {
		streamed = append(streamed, recordFromChunk(note))
	}
	for _, level := range allLevels {
		opts := lineOpts{expandKey: chatExpandKey}
		want := strings.Join(plainLines(outputLines(fetched, level, 80, opts)), "\n")
		got := strings.Join(plainLines(outputLines(streamed, level, 80, opts)), "\n")
		if want != got {
			t.Errorf("%s: the stream and the refetch disagree\nrefetched:\n%s\nlive:\n%s", level, want, got)
		}
		if !strings.Contains(got, "▸ skill fork-probe zebra (forked)") {
			t.Errorf("%s: the human's forked skill is missing:\n%s", level, got)
		}
	}
}

// TestChatBodyDrawsTheHumansSkill: the chat body is the pane, so a `/tdd` the
// human sent reads as a skill line under the prompt even at quiet.
func TestChatBodyDrawsTheHumansSkill(t *testing.T) {
	v := chatViewFixture()
	v.level.set(levelQuiet)
	v.turns = []apiclient.ChatTurn{{ID: 9, Seq: 1, State: "done", Prompt: "/tdd red"}}
	v.turnRecords[1] = []apiclient.TranscriptRecord{
		{Type: "agent.skill", Name: "tdd", Args: "red", By: "human"},
		{Type: "agent.output", Text: "on it"},
	}
	if body := plainLines(v.bodyLines(80)); !hasLine(body, "▸ skill tdd red") {
		t.Errorf("the chat body has no skill line at quiet:\n%s", strings.Join(body, "\n"))
	}
	if items := copyDocs(v.copyDocs()); len(items) == 0 || strings.Contains(items[0].text, "skill") {
		t.Errorf("the copy picker offers %+v, want the prose alone", items)
	}
}

// TestSkillLineWithoutColour is §15's colour contract for the new line: under
// NO_COLOR the words carry the meaning, and an escape sequence in anything
// the record carries — it comes from repository content — never reaches the
// frame (task 073 decision 7).
func TestSkillLineWithoutColour(t *testing.T) {
	recs := []apiclient.TranscriptRecord{
		{Type: "agent.skill", Name: "fork-probe", By: "human", Forked: true},
		skillCall("c1", ""), skillOutcome("c1", ""), skillLoad("c1", ""),
		{Type: "agent.skill", Name: "tdd", By: "human", Error: "refused"},
	}
	frame := strings.Join(outputLines(recs, levelNormal, 80, lineOpts{expandKey: "v"}), "\n")
	var buf bytes.Buffer
	w := &colorprofile.Writer{Forward: &buf, Profile: colorprofile.ASCII}
	if _, err := w.Write([]byte(frame)); err != nil {
		t.Fatalf("downgrade: %v", err)
	}
	plain := ansi.Strip(buf.String())
	for _, want := range []string{"▸ skill fork-probe (forked)", "▸ skill echo-probe zebra", "▸ skill tdd failed: refused"} {
		if !strings.Contains(plain, want) {
			t.Errorf("without colour the frame lost %q:\n%s", want, plain)
		}
	}

	hostile := []apiclient.TranscriptRecord{
		{Type: "agent.skill", Name: "evil\x1b[2Jname", Args: "a\x1b]0;pwned\x07b", By: "human"},
		{Type: "agent.skill", Name: "x\x9b31m", By: "human", Error: "bad\x1b[5mblink"},
		skillCall("c1", ""),
		{Type: "agent.skill", Name: "echo\x1b[H", Args: "z\x1b[2K", By: "agent", CallID: "c1"},
	}
	for _, level := range allLevels {
		raw := strings.Join(outputLines(hostile, level, 80, lineOpts{expandKey: "v"}), "\n")
		for _, bad := range []string{"\x1b[2J", "\x1b]0;", "\x07", "\x9b", "\x1b[5m", "\x1b[H", "\x1b[2K"} {
			if strings.Contains(raw, bad) {
				t.Errorf("%s: %q reached the frame:\n%q", level, bad, raw)
			}
		}
		stripped := strings.Join(plainLines(strings.Split(raw, "\n")), "\n")
		if strings.ContainsRune(stripped, '\x1b') {
			t.Errorf("%s: an escape outside the pane's own styling reached the frame:\n%q", level, stripped)
		}
		if !strings.Contains(stripped, "skill evilname ab") {
			t.Errorf("%s: sanitizing lost the text around the escape:\n%s", level, stripped)
		}
	}
}
