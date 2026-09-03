package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/testrepo"
	"github.com/lezli01/vincent/internal/worktree"
)

// laneDiffFixture gives a task a worktree whose history is shaped like a
// finished fan-out: the parent's own commit, then two lanes cut from the same
// tip and joined with `--no-ff` in the message §7.6 fixes, then more of the
// parent's own work on top of the join, committed and uncommitted.
//
// The history is built with plain git rather than by running a fan_out
// workflow, because what is under test is the attribution — the walk over the
// merges — and a real fan-out would prove the engine instead.
func laneDiffFixture(t *testing.T, h *taskHarness, id int64) {
	t.Helper()
	stored, err := h.store.GetTask(t.Context(), id)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	created, err := h.wt.CreateAndClaim(t.Context(), h.repo, worktree.TaskOwner(id),
		stored.BranchName, stored.BaseBranch, true, nil)
	if err != nil {
		t.Fatalf("CreateAndClaim: %v", err)
	}
	if err := h.store.SetTaskProgress(t.Context(), id, nil, &created.Path, &created.BaseSHA); err != nil {
		t.Fatalf("record worktree: %v", err)
	}
	wt := created.Path

	commit := func(name, body, msg string) {
		testrepo.WriteFile(t, wt, name, body)
		testrepo.Run(t, wt, "add", ".")
		testrepo.Run(t, wt, "commit", "-q", "-m", msg)
	}

	// The parent's own setup commit, before anything fanned out.
	commit("setup.txt", "the parent's own preparation\n", "setup")
	fork := strings.TrimSpace(testrepo.Run(t, wt, "rev-parse", "HEAD"))

	// Two lanes, both cut from the fork point — which is what a fan_out does,
	// and what makes `<merge>^1` carry the earlier lane by the time the second
	// one is joined.
	for _, lane := range []struct{ branch, file, body string }{
		{"lane-alpha", "alpha.txt", "written by the alpha lane\n"},
		{"lane-beta", "beta.txt", "written by the beta lane\n"},
	} {
		testrepo.Run(t, wt, "checkout", "-q", "-b", lane.branch, fork)
		commit(lane.file, lane.body, lane.branch)
	}
	testrepo.Run(t, wt, "checkout", "-q", stored.BranchName)
	testrepo.Run(t, wt, "merge", "-q", "--no-ff", "-m",
		"Merge lane 'alpha' of task 4001", "lane-alpha")
	testrepo.Run(t, wt, "merge", "-q", "--no-ff", "-m",
		"Merge lane 'beta' of task 4002", "lane-beta")

	// The parent again, after the join: one commit and one staged file, so the
	// remainder has to cover both the tail of the history and the working
	// tree.
	commit("after.txt", "what the parent did after the join\n", "post-join")
	testrepo.WriteFile(t, wt, "staged.txt", "not committed yet\n")
	testrepo.Run(t, wt, "add", "staged.txt")
}

// laneDiff fetches ?by=lane and decodes it.
func laneDiff(t *testing.T, h *taskHarness, id int64) []diffLaneSection {
	t.Helper()
	resp, body := h.doJSON(t, http.MethodGet, fmt.Sprintf("/v1/tasks/%d/diff?by=lane", id), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("diff?by=lane: %d %s", resp.StatusCode, body)
	}
	var out diffLanesResponse
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
	return out.Sections
}

// TestDiffByLaneAttributesEachLaneToItsOwnMerge is the whole point of the
// parameter: after the join a parent's diff is one wall of merged hunks, and
// this is what says which lane wrote what.
//
// The load-bearing assertion is the negative one on the *second* lane. Its
// merge's two parents are the branch-with-alpha-already-on-it and a beta lane
// cut before alpha landed, so attributing a lane by diffing those two parents
// against each other reports alpha's file as a deletion inside beta's section.
// What the merge introduced — `<merge>^1` against the merge itself — is beta
// alone, under `needs:` as much as here.
func TestDiffByLaneAttributesEachLaneToItsOwnMerge(t *testing.T) {
	h := newActionHarness(t)
	task := queuedTask(t, h)
	laneDiffFixture(t, h, task.ID)

	sections := laneDiff(t, h, task.ID)
	if len(sections) != 3 {
		t.Fatalf("got %d sections, want two lanes and a remainder: %+v", len(sections), sections)
	}

	for i, want := range []struct {
		lane  string
		child int64
		mine  string
		// theirs is every file this section must not claim.
		theirs []string
	}{
		{"alpha", 4001, "alpha.txt", []string{"beta.txt", "setup.txt", "after.txt", "staged.txt"}},
		{"beta", 4002, "beta.txt", []string{"alpha.txt", "setup.txt", "after.txt", "staged.txt"}},
	} {
		got := sections[i]
		if got.LaneID != want.lane || got.ChildTaskID != want.child {
			t.Errorf("section %d is lane %q of task %d, want %q of %d",
				i, got.LaneID, got.ChildTaskID, want.lane, want.child)
		}
		if got.Remainder {
			t.Errorf("lane %q is marked as the remainder", want.lane)
		}
		if got.MergeCommit == "" {
			t.Errorf("lane %q names no merge commit", want.lane)
		}
		if !strings.Contains(got.Diff, want.mine) {
			t.Errorf("lane %q does not carry %s:\n%s", want.lane, want.mine, got.Diff)
		}
		for _, other := range want.theirs {
			if strings.Contains(got.Diff, other) {
				t.Errorf("lane %q claims %s, which another section wrote:\n%s",
					want.lane, other, got.Diff)
			}
		}
	}

	// The remainder is the parent's own work on both sides of the join, plus
	// what is not committed yet — the sections have to add up to the change.
	rest := sections[2]
	if !rest.Remainder || rest.LaneID != "" || rest.ChildTaskID != 0 || rest.MergeCommit != "" {
		t.Errorf("the last section is not a clean remainder: %+v", rest)
	}
	for _, want := range []string{"setup.txt", "after.txt", "staged.txt"} {
		if !strings.Contains(rest.Diff, want) {
			t.Errorf("the remainder is missing %s:\n%s", want, rest.Diff)
		}
	}
	for _, other := range []string{"alpha.txt", "beta.txt"} {
		if strings.Contains(rest.Diff, other) {
			t.Errorf("the remainder claims %s, which a lane wrote:\n%s", other, rest.Diff)
		}
	}
}

// TestDiffWithoutTheParameterIsUnchanged: `?by=lane` is additive. A fan-out
// parent asked the way every existing client asks still gets the flat
// text/plain diff, whole.
func TestDiffWithoutTheParameterIsUnchanged(t *testing.T) {
	h := newActionHarness(t)
	task := queuedTask(t, h)
	laneDiffFixture(t, h, task.ID)

	resp, body := h.doJSON(t, http.MethodGet, fmt.Sprintf("/v1/tasks/%d/diff", task.ID), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("diff: %d %s", resp.StatusCode, body)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("Content-Type = %q, want the text/plain diff it has always been", ct)
	}
	for _, want := range []string{"alpha.txt", "beta.txt", "setup.txt", "after.txt", "staged.txt"} {
		if !strings.Contains(string(body), want) {
			t.Errorf("the ungrouped diff is missing %s:\n%s", want, body)
		}
	}
	if strings.Contains(string(body), `"sections"`) {
		t.Errorf("the ungrouped diff came back as the grouped body:\n%s", body)
	}
}

// TestDiffByLaneWithNoLanesIsAllRemainder: a task that fanned nothing out is
// not a special case and not an error. One remainder section holds the whole
// diff, byte for byte what the unsectioned endpoint serves — which is what
// lets a client ask for the grouped form unconditionally.
func TestDiffByLaneWithNoLanesIsAllRemainder(t *testing.T) {
	h := newActionHarness(t)
	task := queuedTask(t, h)
	stored, err := h.store.GetTask(t.Context(), task.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	created, err := h.wt.CreateAndClaim(t.Context(), h.repo, worktree.TaskOwner(task.ID),
		stored.BranchName, stored.BaseBranch, true, nil)
	if err != nil {
		t.Fatalf("CreateAndClaim: %v", err)
	}
	if err := h.store.SetTaskProgress(t.Context(), task.ID, nil, &created.Path, &created.BaseSHA); err != nil {
		t.Fatalf("record worktree: %v", err)
	}
	testrepo.WriteFile(t, created.Path, "solo.txt", "no lanes anywhere\n")
	testrepo.Run(t, created.Path, "add", ".")
	testrepo.Run(t, created.Path, "commit", "-q", "-m", "solo work")

	sections := laneDiff(t, h, task.ID)
	if len(sections) != 1 || !sections[0].Remainder {
		t.Fatalf("got %+v, want a single remainder section", sections)
	}

	resp, body := h.doJSON(t, http.MethodGet, fmt.Sprintf("/v1/tasks/%d/diff", task.ID), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("diff: %d %s", resp.StatusCode, body)
	}
	if sections[0].Diff != string(body) {
		t.Errorf("the remainder is not the unsectioned diff:\ngrouped:\n%q\nflat:\n%q",
			sections[0].Diff, body)
	}
}

// TestDiffByRejectsAnyOtherGrouping: `lane` is the only grouping there is, so
// a typo is a 400 rather than a silent fall-through to the ungrouped body —
// which would hand a client the wrong shape and call it success. The request's
// shape is judged before the task's state, so the parameter is rejected even
// on a task that has no worktree to diff.
func TestDiffByRejectsAnyOtherGrouping(t *testing.T) {
	h := newActionHarness(t)
	task := queuedTask(t, h)
	laneDiffFixture(t, h, task.ID)
	noWorktree := queuedTask(t, h)

	for _, tc := range []struct {
		name, query string
		id          int64
	}{
		{"unknown grouping", "?by=file", task.ID},
		{"empty grouping", "?by=", task.ID},
		{"case matters", "?by=Lane", task.ID},
		{"before the task's state", "?by=file", noWorktree.ID},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp, body := h.doJSON(t, http.MethodGet,
				fmt.Sprintf("/v1/tasks/%d/diff%s", tc.id, tc.query), nil)
			if tc.query == "?by=" {
				// An explicitly empty value is the parameter absent, which is
				// how every other query parameter on this API reads it.
				if resp.StatusCode != http.StatusOK {
					t.Fatalf("by= : %d %s, want the ungrouped diff", resp.StatusCode, body)
				}
				return
			}
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("%s: %d %s, want 400", tc.query, resp.StatusCode, body)
			}
			var env errorBody
			if err := json.Unmarshal(body, &env); err != nil {
				t.Fatalf("decode error envelope %s: %v", body, err)
			}
			if env.Error.Code != CodeValidationFailed {
				t.Errorf("error code = %q, want %q", env.Error.Code, CodeValidationFailed)
			}
			if !strings.Contains(env.Error.Message, "lane") {
				t.Errorf("the message does not say what is accepted: %q", env.Error.Message)
			}
		})
	}
}
