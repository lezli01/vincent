package tui

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/github"
	"github.com/lezli01/vincent/internal/store"
)

// TestOutcomeCardFromRealServer reads the outcome card of a finished task
// with no pull request off the screen, against the real API handlers (task
// 129.14): the result, labelled with the attempt it came from, and no PR line.
func TestOutcomeCardFromRealServer(t *testing.T) {
	h := newActionLiveHarness(t)
	done := h.createParkedTask(t, "delivered")
	h.finishTask(t, done.ID, store.TaskDone)
	got := h.overviewOf(t, done.ID, "Result  from step 1 implement · attempt 1")
	for _, want := range []string{"Outcome", "done", "$ cost", "4 diff"} {
		if !strings.Contains(got, want) {
			t.Errorf("outcome card misses %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "⇡") || strings.Contains(got, "7 PR") {
		t.Errorf("a task with no pull request shows one:\n%s", got)
	}
}

// TestOutcomeCardPullFromRealServer is the same card for a task whose pull
// request is linked, with the daemon's GitHub client pointed at cmd/fakegh:
// the PR line reads the pull row and the check rollup the workspace fetches.
func TestOutcomeCardPullFromRealServer(t *testing.T) {
	t.Setenv("FAKEGH_STATE_FILE", filepath.Join(t.TempDir(), "state.json"))
	h, _ := newGitHubLiveHarness(t, liveOptions{remote: ghLiveOrigin})
	h.p.until(10*time.Second, "the GitHub probes to answer", func() bool {
		return h.m.githubAvailable()
	})
	task := &store.Task{
		ProjectID: h.projectID, Title: "Add a thing", WorkflowName: "implement",
		BaseBranch: "main", BranchName: "vincent/1-add-a-thing", State: store.TaskDone,
	}
	if err := h.st.CreateTask(context.Background(), task, nil); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if _, err := h.st.SetTaskGitHubPull(context.Background(), task.ID, &github.PullLink{
		Repo: "octo/repo", Number: 412, Source: "human", LinkedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("SetTaskGitHubPull: %v", err)
	}
	_, cmd := h.m.Update(selectTaskMsg{id: task.ID, state: string(store.TaskDone)})
	h.p.push(cmd)
	h.p.until(15*time.Second, "the outcome card's PR line with its checks", func() bool {
		return strings.Contains(ansi.Strip(content(h.m)), "· checks ")
	})
	got := ansi.Strip(content(h.m))
	for _, want := range []string{"⇡ #412 open", "7 PR", "4 diff"} {
		if !strings.Contains(got, want) {
			t.Errorf("outcome card misses %q:\n%s", want, got)
		}
	}
}
