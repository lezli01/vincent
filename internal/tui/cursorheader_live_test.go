package tui

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/agent"
	"github.com/lezli01/vincent/internal/agent/cursor"
	"github.com/lezli01/vincent/internal/api"
	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/worktree"
)

// TestCursorRunHeaderAndResultByLevel is task 108 in the pane, off a captured
// cursor run served by the real transcript handler: the records are what the
// daemon sends, not hand-built ones. No rendering code changed for it —
// cursor now fills records claude already filled — so what is pinned is that
// the existing level contract holds for a header with no tools and a result
// with durations and cache counts but no turns and no cost.
func TestCursorRunHeaderAndResultByLevel(t *testing.T) {
	records := cursorFixtureRecords(t)
	d := newTestDetail(t)
	d.width = 100
	d.records = records

	d.level.set(levelCompact)
	compact := plainLines(d.outputLines())
	for _, line := range compact {
		if strings.HasPrefix(line, gutterHeader) {
			t.Errorf("compact rendered the run header %q", line)
		}
	}
	if got := compact[len(compact)-1]; got != "✓ done" {
		t.Errorf("compact result = %q, want exactly what it rendered before task 108", got)
	}

	d.level.set(levelNormal)
	normal := plainLines(d.outputLines())
	if normal[0] != "# /tmp/wt" {
		t.Errorf("normal header = %q, want the directory and no tools segment", normal[0])
	}
	if got := normal[len(normal)-1]; got != "✓ done · 2.0s" {
		t.Errorf("normal result = %q, want the elapsed time added", got)
	}

	// Verbose also shows the raw lines, so the header and the result are
	// matched as whole lines rather than searched for anywhere in the pane.
	d.level.set(levelVerbose)
	verbose := strings.Join(plainLines(d.outputLines()), "\n")
	for _, want := range []string{
		"# /tmp/wt\n",
		"\n✓ done · 2.0s (2.0s api)\n",
		"\n  cache 8000 read / 0 written",
	} {
		if !strings.Contains(verbose, want) {
			t.Errorf("verbose missing %q:\n%s", want, verbose)
		}
	}
}

// cursorFixtureRecords serves the captured cursor success run through the real
// API handler with the cursor adapter registered, and returns the normalized
// records the pane renders.
func cursorFixtureRecords(t *testing.T) []apiclient.TranscriptRecord {
	t.Helper()
	return fixtureRecords(t, cursor.New(func() string { return "" }),
		filepath.Join("..", "agent", "cursor", "testdata", "success_2026.08.04.jsonl"))
}

// fixtureRecords serves a captured run through the real API handler with its
// adapter registered, and returns the normalized records the pane renders.
func fixtureRecords(t *testing.T, adapter agent.Adapter, fixture string) []apiclient.TranscriptRecord {
	t.Helper()
	const token = "fixture-token"
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	s := api.New(api.Deps{
		Token:       token,
		Config:      config.Default,
		StartedAt:   time.Now(),
		ListenAddr:  "127.0.0.1:0",
		RequestStop: func() {},
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		Store:       st,
		Agents:      agent.NewRegistry(adapter),
	})
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)

	proj := &store.Project{Name: adapter.Name(), Path: "/nowhere", DefaultBranch: "main"}
	if err := st.CreateProject(ctx, proj); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	task := &store.Task{
		ProjectID: proj.ID, Title: adapter.Name() + " run", WorkflowName: "three",
		WorkflowSnapshot: threeStepWorkflow, BaseBranch: "main", State: store.TaskQueued,
	}
	resolve := func(id int64) (string, error) { return worktree.BranchName(id, task.Title), nil }
	if err := st.CreateTask(ctx, task, resolve); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	path, err := filepath.Abs(fixture)
	if err != nil {
		t.Fatalf("fixture path: %v", err)
	}
	run := &store.StepRun{
		TaskID: task.ID, StepIndex: 0, StepID: "one", StepType: "agent",
		Attempt: 1, State: store.StepSucceeded, Agent: adapter.Name(), TranscriptPath: path,
		StartedAt: time.Now(),
	}
	if err := st.CreateStepRun(ctx, run); err != nil {
		t.Fatalf("CreateStepRun: %v", err)
	}
	records, _, err := apiclient.New(ts.URL, token).Transcript(ctx, task.ID, run.ID, apiclient.TranscriptOptions{})
	if err != nil {
		t.Fatalf("Transcript: %v", err)
	}
	return records
}
