package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/testrepo"
)

// The create side of the adopt mode (§10, task 125). The first test is the
// regression guard decision 1 exists for: without the field, every existing
// refusal is exactly what it was.

func TestExistingBranchAcceptsWhatItOtherwiseRefuses(t *testing.T) {
	h := newTaskHarness(t, 0, false)
	testrepo.Run(t, h.repo, "branch", "shared/work")

	t.Run("without the field the branch is still refused", func(t *testing.T) {
		msg := h.createTaskRejected(t, map[string]any{
			"title": "clash", "branch_name": "shared/work",
		})
		if !strings.Contains(msg, "never reuses a branch") {
			t.Fatalf("message = %q, want task 001's refusal unchanged", msg)
		}
	})

	t.Run("with the field the same request is accepted", func(t *testing.T) {
		got := h.createTask(t, map[string]any{
			"title": "continue", "branch_name": "shared/work", "existing_branch": true,
		})
		if got.BranchName != "shared/work" {
			t.Fatalf("branch = %q, want the branch that was asked for", got.BranchName)
		}
		if !got.AdoptedBranch {
			t.Error("adopted_branch is false on a task created with existing_branch")
		}
	})

	t.Run("a second task on the same branch is accepted too", func(t *testing.T) {
		// Decision 2: it waits at admission rather than being refused here.
		got := h.createTask(t, map[string]any{
			"title": "and again", "branch_name": "shared/work", "existing_branch": true,
		})
		if !got.AdoptedBranch {
			t.Error("adopted_branch is false on the second task")
		}
	})

	t.Run("a branch that does not exist is a 400", func(t *testing.T) {
		msg := h.createTaskRejected(t, map[string]any{
			"title": "nope", "branch_name": "never/existed", "existing_branch": true,
		})
		if !strings.Contains(msg, "does not exist") {
			t.Fatalf("message = %q, want it to say the branch is not there", msg)
		}
	})
}

func TestExistingBranchAndGitHubPullAreRefusedTogether(t *testing.T) {
	h := newTaskHarness(t, 0, false)
	testrepo.Run(t, h.repo, "branch", "shared/pull")
	msg := h.createTaskRejected(t, map[string]any{
		"title": "both", "branch_name": "shared/pull", "existing_branch": true, "github_pull": 7,
	})
	if !strings.Contains(msg, "cannot be combined") {
		t.Fatalf("message = %q", msg)
	}
}

// A task created without the field records false, which is what keeps archive
// deleting the branches it always deleted.
func TestOrdinaryTaskIsNotAdopted(t *testing.T) {
	h := newTaskHarness(t, 0, false)
	got := h.createTask(t, map[string]any{"title": "ordinary"})
	if got.AdoptedBranch {
		t.Error("an ordinary task reports adopted_branch")
	}
}

func TestProjectBranchesListsLocalBranches(t *testing.T) {
	h := newTaskHarness(t, 0, false)
	testrepo.Run(t, h.repo, "branch", "one")
	testrepo.Run(t, h.repo, "branch", "two")

	resp, body := h.doJSON(t, http.MethodGet, "/v1/projects/"+itoa(h.projectID)+"/branches", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list branches: %d %s", resp.StatusCode, body)
	}
	var out branchListResponse
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("body: %v", err)
	}
	seen := map[string]branchBody{}
	for _, b := range out.Branches {
		seen[b.Name] = b
	}
	for _, want := range []string{"one", "two"} {
		if _, ok := seen[want]; !ok {
			t.Errorf("branch %q missing from %v", want, out.Branches)
		}
	}
	// The project's own checkout holds its current branch, and saying so is
	// what tells a user the task would run *there* (decision 3).
	var current branchBody
	for _, b := range out.Branches {
		if b.Current {
			current = b
		}
	}
	if current.Name == "" {
		t.Fatal("no branch is reported as current")
	}
	if !current.MainCheckout {
		t.Errorf("current branch %q is not reported as held by the main checkout", current.Name)
	}
}
