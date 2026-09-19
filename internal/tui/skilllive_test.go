package tui

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/lezli01/vincent/internal/agent"
	"github.com/lezli01/vincent/internal/agent/agenttest"
	"github.com/lezli01/vincent/internal/agent/claude"
	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/events"
	"github.com/lezli01/vincent/internal/store"
)

// skillBodyMarker opens every rendered SKILL.md claude writes into the
// conversation, the fake's included. No level may draw it (task 124 decision
// 39).
const skillBodyMarker = "Base directory for this skill"

// openSkillRun puts cold on disk as a step run of a fresh task, opens that
// task's output, and returns the run and the pane's detail once the fetched
// window is on screen.
func (h *boardLiveHarness) openSkillRun(
	t *testing.T, cold []string, state store.StepRunState, ready string,
) (*store.StepRun, *detail) {
	t.Helper()
	ctx := context.Background()
	task := h.createTask(t, "skill task")
	path := filepath.Join(t.TempDir(), "0-1.jsonl")
	for _, line := range cold {
		appendRawLine(t, path, line)
	}
	if _, _, err := h.st.TransitionTask(ctx, task.ID,
		store.TaskQueued, store.TaskRunning, store.TaskChange{}); err != nil {
		t.Fatalf("transition to running: %v", err)
	}
	run := &store.StepRun{
		TaskID: task.ID, StepIndex: 0, StepID: "one", StepType: "agent", Agent: "claude",
		Attempt: 1, State: state, TranscriptPath: path, StartedAt: time.Now(),
	}
	if err := h.st.CreateStepRun(ctx, run); err != nil {
		t.Fatalf("CreateStepRun: %v", err)
	}
	_, cmd := h.m.Update(selectTaskMsg{id: task.ID})
	h.p.push(cmd)
	_, cmd = h.m.Update(tea.KeyPressMsg{Code: '3', Text: "3"})
	h.p.push(cmd)
	h.p.until(20*time.Second, "the fetched transcript to render", func() bool {
		return strings.Contains(plainContent(h.m), ready)
	})
	return run, h.m.views[viewHome].(*shell).detail
}

// refetch is the displayed attempt's transcript, fetched whole through the
// real handler: what a reader who opened the task afterwards would get.
func refetch(t *testing.T, d *detail) []apiclient.TranscriptRecord {
	t.Helper()
	msg, ok := d.transcriptCmd(d.displayRun, apiclient.TranscriptOptions{})().(detailTranscriptMsg)
	if !ok || msg.err != nil {
		t.Fatalf("refetch: %+v", msg)
	}
	return msg.records
}

// TestLiveSkillLoadMatchesItsRefetch is task 124.12 end to end: fakeagent's
// skill-model run, published live through the runner's own mapping, redraws
// its `Skill` call as the load when the load lands, and the frame the stream
// built is the frame a refetch builds, at every level.
func TestLiveSkillLoadMatchesItsRefetch(t *testing.T) {
	cmd := exec.Command(agenttest.BuildFakeAgent(t), "-p", "--output-format", "stream-json", "--verbose")
	cmd.Stdin = strings.NewReader("use the skill")
	cmd.Env = append(os.Environ(), "FAKEAGENT_SCENARIO=skill-model")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("run fakeagent: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(strings.ReplaceAll(string(out), "\r\n", "\n")), "\n")
	if len(lines) < 4 {
		t.Fatalf("fakeagent wrote %d lines:\n%s", len(lines), out)
	}

	h := newBoardLiveHarness(t)
	// The init line and the call are on disk when the pane opens; the
	// outcome, the load, the reply and the result arrive live.
	cold, hot := lines[:2], lines[2:]
	run, d := h.openSkillRun(t, cold, store.StepRunning, "▸ Skill echo-probe")
	h.p.until(10*time.Second, "the per-task subscription to attach", func() bool {
		return h.broker.OutputSubscribers(run.TaskID) > 0
	})

	parse := claude.New(func() string { return "" }).NewLineParser()
	for _, line := range cold {
		parse([]byte(line))
	}
	for _, line := range hot {
		offset := appendRawLine(t, run.TranscriptPath, line)
		for _, c := range agent.LiveChunks(parse([]byte(line))) {
			payload := map[string]any{"run_id": run.ID, "offset": offset}
			for k, v := range c.Payload {
				payload[k] = v
			}
			h.broker.PublishOutput(run.TaskID, events.Chunk{Type: c.Type, Payload: payload})
		}
	}
	h.p.until(20*time.Second, "the load to redraw its call", func() bool {
		got := plainContent(h.m)
		return strings.Contains(got, "▸ skill echo-probe zebra") && strings.Contains(got, "ECHO-PROBE zebra")
	})
	if got := plainContent(h.m); strings.Contains(got, "▸ Skill") || strings.Contains(got, skillBodyMarker) {
		t.Errorf("the frame draws the call twice or the skill's body:\n%s", got)
	}

	streamed := d.records
	// agent.result has no live chunk: the pane refetches when a run ends
	// (agent.LiveChunks), so the two doors are compared over what both carry.
	var fetched []apiclient.TranscriptRecord
	for _, rec := range refetch(t, d) {
		if rec.Type != "agent.result" {
			fetched = append(fetched, rec)
		}
	}
	for _, level := range allLevels {
		opts := lineOpts{expandKey: "v"}
		want := strings.Join(plainLines(outputLines(fetched, level, 100, opts)), "\n")
		got := strings.Join(plainLines(outputLines(streamed, level, 100, opts)), "\n")
		if want != got {
			t.Errorf("%s: the stream and the refetch disagree\nrefetched:\n%s\nlive:\n%s", level, want, got)
		}
		if strings.Contains(got, skillBodyMarker) {
			t.Errorf("%s: the skill's body is on screen:\n%s", level, got)
		}
	}
}

// TestLiveSkillCapturesNeverShowTheBody runs the two real claude captures
// through the transcript route's normalization and holds decision 39: no level
// draws the skill's body and the copy picker never offers it, while the load
// itself is drawn.
func TestLiveSkillCapturesNeverShowTheBody(t *testing.T) {
	for _, tc := range []struct {
		fixture string
		// line is the load's line at every level it shows at.
		line  string
		quiet bool
	}{
		{"stream_skill_model_2.1.277.jsonl", "▸ skill echo-probe zebra", false},
		{"stream_skill_fork_2.1.277.jsonl", "▸ skill fork-probe (forked)", true},
	} {
		t.Run(tc.fixture, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join("..", "agent", "claude", "testdata", tc.fixture))
			if err != nil {
				t.Fatalf("read fixture: %v", err)
			}
			lines := strings.Split(strings.TrimSpace(strings.ReplaceAll(string(data), "\r\n", "\n")), "\n")
			h := newBoardLiveHarness(t)
			_, d := h.openSkillRun(t, lines, store.StepSucceeded, tc.line)
			records := refetch(t, d)

			for _, level := range allLevels {
				got := renderPlain(records, level)
				joined := strings.Join(got, "\n")
				if strings.Contains(joined, skillBodyMarker) {
					t.Errorf("%s: the skill's body is on screen:\n%s", level, joined)
				}
				if want := level != levelQuiet || tc.quiet; hasLine(got, tc.line) != want {
					t.Errorf("%s: %q shown = %v, want %v:\n%s", level, tc.line, !want, want, joined)
				}
				if strings.Contains(joined, "▸ Skill") {
					t.Errorf("%s: the call is drawn as well as the load:\n%s", level, joined)
				}
			}
			items := copyDocs(copyDocsFromRecords(records, nil))
			v := chatViewFixture()
			v.turns = []apiclient.ChatTurn{{ID: 9, Seq: 1, State: "done", Prompt: "/fork-probe zebra"}}
			v.turnRecords[1] = records
			items = append(items, copyDocs(v.copyDocs())...)
			for _, item := range items {
				if strings.Contains(item.text, skillBodyMarker) || strings.Contains(item.snippet, skillBodyMarker) {
					t.Errorf("the copy picker offers the skill's body: %+v", item)
				}
			}
		})
	}
}
