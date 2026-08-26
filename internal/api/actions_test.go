package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/testrepo"
)

// actionHarness is the task harness with no runner: tasks stay queued, so a
// test can put one in any state it likes and drive the endpoints against it.
func newActionHarness(t *testing.T) *taskHarness {
	t.Helper()
	return newTaskHarness(t, 0, false)
}

// queuedTask creates a task and leaves it queued.
func queuedTask(t *testing.T, h *taskHarness) taskResponse {
	t.Helper()
	return h.createTask(t, map[string]any{"project_id": h.projectID, "title": "action test"})
}

// setState moves a task directly, for states the endpoints are meant to be
// exercised from.
func setState(t *testing.T, h *taskHarness, id int64, to store.TaskState) {
	t.Helper()
	task, err := h.store.GetTask(t.Context(), id)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if _, _, err := h.store.TransitionTask(t.Context(), id, task.State, to, store.TaskChange{}); err != nil {
		t.Fatalf("set state %s: %v", to, err)
	}
}

// decodeTask reads a task response body.
func decodeTask(t *testing.T, body []byte) taskResponse {
	t.Helper()
	var out taskResponse
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode task: %v (%s)", err, body)
	}
	return out
}

// decodeError reads the §13.1 error envelope.
func decodeError(t *testing.T, body []byte) errorDetail {
	t.Helper()
	var out errorBody
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode error: %v (%s)", err, body)
	}
	return out.Error
}

func TestCancelQueuedTask(t *testing.T) {
	h := newActionHarness(t)
	task := queuedTask(t, h)

	resp, body := h.doJSON(t, http.MethodPost, fmt.Sprintf("/v1/tasks/%d/cancel", task.ID), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("cancel: %d %s", resp.StatusCode, body)
	}
	got := decodeTask(t, body)
	if got.State != string(store.TaskAborted) {
		t.Errorf("state = %s, want aborted", got.State)
	}
}

// TestInvalidActionReportsStateInDetails is the §13.1 promise that made the
// envelope grow a details object: a client must be able to branch on the
// state it did not expect, not parse it out of a sentence.
func TestInvalidActionReportsStateInDetails(t *testing.T) {
	h := newActionHarness(t)
	task := queuedTask(t, h)

	resp, body := h.doJSON(t, http.MethodPost, fmt.Sprintf("/v1/tasks/%d/approve", task.ID), nil)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("approve on a queued task: %d %s, want 409", resp.StatusCode, body)
	}
	e := decodeError(t, body)
	if e.Code != CodeInvalidState {
		t.Errorf("code = %s, want %s", e.Code, CodeInvalidState)
	}
	if e.Details["state"] != string(store.TaskQueued) {
		t.Errorf("details.state = %q, want %q", e.Details["state"], store.TaskQueued)
	}
}

// TestEveryActionRejectsAWrongState walks the endpoints, not the FSM: each
// one must answer 409 rather than 404 or 500 when the state is wrong.
func TestEveryActionRejectsAWrongState(t *testing.T) {
	// Each action paired with a state it is not valid from.
	cases := []struct {
		action string
		state  store.TaskState
	}{
		{"cancel", store.TaskDone},
		{"pause", store.TaskBlocked},
		{"resume", store.TaskQueued},
		{"retry", store.TaskQueued},
		{"skip", store.TaskQueued},
		{"approve", store.TaskQueued},
		{"reject", store.TaskQueued},
		{"archive", store.TaskQueued},
	}
	for _, c := range cases {
		t.Run(c.action, func(t *testing.T) {
			h := newActionHarness(t)
			task := queuedTask(t, h)
			if c.state != store.TaskQueued {
				setState(t, h, task.ID, c.state)
			}
			path := fmt.Sprintf("/v1/tasks/%d/%s", task.ID, c.action)
			resp, body := h.doJSON(t, http.MethodPost, path, nil)
			if resp.StatusCode != http.StatusConflict {
				t.Fatalf("%s from %s: %d %s, want 409", c.action, c.state, resp.StatusCode, body)
			}
			if got := decodeError(t, body).Details["state"]; got != string(c.state) {
				t.Errorf("details.state = %q, want %q", got, c.state)
			}
		})
	}
}

// TestPauseRunningTaskDefers: the response reports the deferral rather than
// claiming a state the task has not reached (§6).
func TestPauseRunningTaskDefers(t *testing.T) {
	h := newActionHarness(t)
	task := queuedTask(t, h)
	setState(t, h, task.ID, store.TaskRunning)

	resp, body := h.doJSON(t, http.MethodPost, fmt.Sprintf("/v1/tasks/%d/pause", task.ID), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("pause: %d %s", resp.StatusCode, body)
	}
	got := decodeTask(t, body)
	if got.State != string(store.TaskRunning) {
		t.Errorf("state = %s, want running (the step finishes first)", got.State)
	}
	if !got.PauseRequested {
		t.Error("pause_requested = false; a client cannot tell the pause was accepted")
	}
}

// TestAvailableActionsTracksState asserts the field the M3 TUI will render
// its action bar from actually follows §6.
func TestAvailableActionsTracksState(t *testing.T) {
	h := newActionHarness(t)
	task := queuedTask(t, h)
	if !hasAction(task.AvailableActions, "cancel") || !hasAction(task.AvailableActions, "pause") {
		t.Errorf("queued actions = %v, want cancel and pause", task.AvailableActions)
	}

	setState(t, h, task.ID, store.TaskAwaitingGate)
	resp, body := h.doJSON(t, http.MethodGet, fmt.Sprintf("/v1/tasks/%d", task.ID), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get task: %d %s", resp.StatusCode, body)
	}
	got := decodeTask(t, body)
	for _, want := range []string{"approve", "reject", "skip", "cancel"} {
		if !hasAction(got.AvailableActions, want) {
			t.Errorf("awaiting_gate actions = %v, want %s", got.AvailableActions, want)
		}
	}
	if hasAction(got.AvailableActions, "resume") {
		t.Errorf("awaiting_gate actions = %v, must not offer resume", got.AvailableActions)
	}
}

func TestPatchPriority(t *testing.T) {
	h := newActionHarness(t)
	task := queuedTask(t, h)

	resp, body := h.doJSON(t, http.MethodPatch, fmt.Sprintf("/v1/tasks/%d", task.ID),
		map[string]any{"priority": 9})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("patch priority: %d %s", resp.StatusCode, body)
	}
	if got := decodeTask(t, body); got.Priority != 9 {
		t.Errorf("priority = %d, want 9", got.Priority)
	}

	setState(t, h, task.ID, store.TaskRunning)
	resp, body = h.doJSON(t, http.MethodPatch, fmt.Sprintf("/v1/tasks/%d", task.ID),
		map[string]any{"priority": 1})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("patch priority while running: %d %s, want 409", resp.StatusCode, body)
	}
	if got := decodeError(t, body).Details["state"]; got != string(store.TaskRunning) {
		t.Errorf("details.state = %q, want running", got)
	}
}

func TestPatchTaskRejectsUnknownField(t *testing.T) {
	h := newActionHarness(t)
	task := queuedTask(t, h)

	resp, body := h.doJSON(t, http.MethodPatch, fmt.Sprintf("/v1/tasks/%d", task.ID),
		map[string]any{"title": "renamed"})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("patch title: %d %s, want 400", resp.StatusCode, body)
	}
}

// TestRetryOverrideValidatedAgainstStepType: sending the wrong override kind
// is a client error, not a silently dropped field.
func TestRetryOverrideValidatedAgainstStepType(t *testing.T) {
	h := newActionHarness(t)
	task := queuedTask(t, h)
	setState(t, h, task.ID, store.TaskBlocked)

	resp, body := h.doJSON(t, http.MethodPost, fmt.Sprintf("/v1/tasks/%d/retry", task.ID),
		map[string]any{"run_override": "echo hi"})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("run_override on an agent step: %d %s, want 400", resp.StatusCode, body)
	}
	if got := decodeError(t, body).Code; got != CodeValidationFailed {
		t.Errorf("code = %s, want %s", got, CodeValidationFailed)
	}
}

func TestRetryWithoutBodySucceeds(t *testing.T) {
	h := newActionHarness(t)
	task := queuedTask(t, h)
	setState(t, h, task.ID, store.TaskBlocked)

	resp, body := h.doJSON(t, http.MethodPost, fmt.Sprintf("/v1/tasks/%d/retry", task.ID), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("retry: %d %s", resp.StatusCode, body)
	}
	if got := decodeTask(t, body); got.State != string(store.TaskQueued) {
		t.Errorf("state = %s, want queued", got.State)
	}
}

// TestArchiveDirtyWorktree covers the §13.2 addition: removal happens before
// the transition, so a refusal leaves the task exactly as it was.
func TestArchiveDirtyWorktree(t *testing.T) {
	h := newActionHarness(t)
	task := queuedTask(t, h)
	setState(t, h, task.ID, store.TaskDone)

	stored, err := h.store.GetTask(t.Context(), task.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	path, err := h.wt.Create(t.Context(), h.repo, task.ID, stored.BranchName, stored.BaseBranch)
	if err != nil {
		t.Fatalf("create worktree: %v", err)
	}
	if err := h.store.SetTaskProgress(t.Context(), task.ID, nil, &path); err != nil {
		t.Fatalf("record worktree: %v", err)
	}
	if err := os.WriteFile(filepath.Join(path, "wip.txt"), []byte("x"), 0o600); err != nil {
		t.Fatalf("dirty worktree: %v", err)
	}

	resp, body := h.doJSON(t, http.MethodPost, fmt.Sprintf("/v1/tasks/%d/archive", task.ID), nil)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("archive dirty: %d %s, want 409", resp.StatusCode, body)
	}
	if got := decodeError(t, body).Details["reason"]; got != CodeWorktreeDirty {
		t.Errorf("details.reason = %q, want %q", got, CodeWorktreeDirty)
	}
	if got, _ := h.store.GetTask(t.Context(), task.ID); got.State != store.TaskDone {
		t.Errorf("state = %s after a refused archive, want done", got.State)
	}

	// force is accepted as a query parameter, matching DELETE /v1/projects.
	resp, body = h.doJSON(t, http.MethodPost,
		fmt.Sprintf("/v1/tasks/%d/archive?force", task.ID), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("archive force: %d %s", resp.StatusCode, body)
	}
	if got := decodeTask(t, body); got.State != string(store.TaskArchived) {
		t.Errorf("state = %s, want archived", got.State)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("worktree %s survived the archive", path)
	}
}

// decodeArchive reads an archive response: a task with a branch object beside
// it (§13.2, task 008).
func decodeArchive(t *testing.T, body []byte) archiveResponse {
	t.Helper()
	var out archiveResponse
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode archive: %v (%s)", err, body)
	}
	return out
}

// archivableTask leaves a done task holding a real worktree cut from main.
func archivableTask(t *testing.T, h *taskHarness) *store.Task {
	t.Helper()
	task := queuedTask(t, h)
	setState(t, h, task.ID, store.TaskDone)
	stored, err := h.store.GetTask(t.Context(), task.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	path, err := h.wt.Create(t.Context(), h.repo, task.ID, stored.BranchName, stored.BaseBranch)
	if err != nil {
		t.Fatalf("create worktree: %v", err)
	}
	if err := h.store.SetTaskProgress(t.Context(), task.ID, nil, &path); err != nil {
		t.Fatalf("record worktree: %v", err)
	}
	stored.WorktreePath = path
	return stored
}

// TestArchiveReportsTheBranchOutcome walks the three answers the endpoint can
// give (§13.2, task 008). The branch is the one consequence of an archive that
// is invisible afterwards — the row stops naming a worktree to go look in — so
// the response is where it has to be said.
func TestArchiveReportsTheBranchOutcome(t *testing.T) {
	cases := []struct {
		name string
		// setup runs after the worktree exists and before the archive.
		setup      func(t *testing.T, h *taskHarness, task *store.Task)
		wantResult string
		wantErrMsg bool
		wantBranch bool // does the branch still exist afterwards?
	}{
		{
			name:       "deleted",
			setup:      func(*testing.T, *taskHarness, *store.Task) {},
			wantResult: "deleted",
		},
		{
			name: "kept because it has commits",
			setup: func(t *testing.T, _ *taskHarness, task *store.Task) {
				testrepo.WriteFile(t, task.WorktreePath, "work.txt", "real work\n")
				testrepo.Run(t, task.WorktreePath, "add", ".")
				testrepo.Run(t, task.WorktreePath, "commit", "-q", "-m", "the work")
			},
			wantResult: "has_commits",
			wantBranch: true,
		},
		{
			name: "kept because git cannot judge it",
			setup: func(t *testing.T, h *taskHarness, _ *store.Task) {
				testrepo.Run(t, h.repo, "branch", "-m", "main", "trunk")
			},
			wantResult: "unknown",
			wantErrMsg: true,
			wantBranch: true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newActionHarness(t)
			task := archivableTask(t, h)
			c.setup(t, h, task)

			resp, body := h.doJSON(t, http.MethodPost,
				fmt.Sprintf("/v1/tasks/%d/archive", task.ID), nil)
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("archive: %d %s — a branch problem must never fail it", resp.StatusCode, body)
			}
			got := decodeArchive(t, body)
			if got.State != string(store.TaskArchived) {
				t.Errorf("state = %s, want archived", got.State)
			}
			if got.Branch == nil {
				t.Fatalf("no branch object in %s", body)
			}
			if got.Branch.Result != c.wantResult {
				t.Errorf("branch.result = %q, want %q", got.Branch.Result, c.wantResult)
			}
			if got.Branch.Name != task.BranchName {
				t.Errorf("branch.name = %q, want %q", got.Branch.Name, task.BranchName)
			}
			if (got.Branch.Error != "") != c.wantErrMsg {
				t.Errorf("branch.error = %q, want present = %v", got.Branch.Error, c.wantErrMsg)
			}
			// The remote leg is off by default and must not have run.
			if got.Branch.Remote != nil {
				t.Errorf("remote leg ran unasked: %+v", got.Branch.Remote)
			}
			exists := testrepo.Run(t, h.repo, "for-each-ref", "--format=%(refname:short)",
				"refs/heads/"+task.BranchName) != ""
			if exists != c.wantBranch {
				t.Errorf("branch %s exists = %v, want %v", task.BranchName, exists, c.wantBranch)
			}
		})
	}
}

// TestArchiveOmitsTheBranchWhenTheStepDidNotRun: a client that predates the
// field must see exactly what it saw before, so `branch` is absent rather than
// null-with-empty-strings when there was nothing to check.
func TestArchiveOmitsTheBranchWhenTheStepDidNotRun(t *testing.T) {
	h := newActionHarness(t)
	task := queuedTask(t, h)
	setState(t, h, task.ID, store.TaskDone) // no worktree, so no branch of its own

	resp, body := h.doJSON(t, http.MethodPost, fmt.Sprintf("/v1/tasks/%d/archive", task.ID), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("archive: %d %s", resp.StatusCode, body)
	}
	if got := decodeArchive(t, body); got.Branch != nil {
		t.Errorf("branch = %+v, want absent", got.Branch)
	}
	if strings.Contains(string(body), `"branch"`) {
		t.Errorf("body carries a branch key with nothing to say: %s", body)
	}
}

func TestArchiveForceInBody(t *testing.T) {
	h := newActionHarness(t)
	task := queuedTask(t, h)
	setState(t, h, task.ID, store.TaskDone)

	resp, body := h.doJSON(t, http.MethodPost, fmt.Sprintf("/v1/tasks/%d/archive", task.ID),
		map[string]any{"force": true})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("archive: %d %s", resp.StatusCode, body)
	}
}

func TestActionOnMissingTask(t *testing.T) {
	h := newActionHarness(t)
	resp, body := h.doJSON(t, http.MethodPost, "/v1/tasks/4242/cancel", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("cancel missing task: %d %s, want 404", resp.StatusCode, body)
	}
}

// setAwaitingInput moves a task into awaiting_input carrying a pending
// request, the way the engine's RequestInput transition does (§7.4).
func setAwaitingInput(t *testing.T, h *taskHarness, id int64, pendingJSON string) {
	t.Helper()
	setState(t, h, id, store.TaskRunning)
	if _, _, err := h.store.TransitionTask(t.Context(), id, store.TaskRunning,
		store.TaskAwaitingInput, store.TaskChange{PendingInput: &pendingJSON}); err != nil {
		t.Fatalf("set awaiting_input: %v", err)
	}
}

const pendingQuestionJSON = `{"kind":"question","questions":[` +
	`{"text":"Which color?","header":"Color","options":["Red","Blue"]},` +
	`{"text":"Which toppings?","options":["Cheese","Basil"],"multi_select":true}]}`

const pendingPermissionJSON = `{"kind":"permission","permission":{"tool":"Write","summary":"hello.txt"}}`

func TestAnswerWrongState(t *testing.T) {
	h := newActionHarness(t)
	task := queuedTask(t, h)
	resp, body := h.doJSON(t, http.MethodPost, fmt.Sprintf("/v1/tasks/%d/answer", task.ID),
		map[string]any{"answers": map[string]any{"Which color?": "Red"}})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("answer queued task: %d %s, want 409", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), `"state":"queued"`) {
		t.Errorf("409 body %s does not carry details.state", body)
	}
}

func TestAnswerQuestionValidation(t *testing.T) {
	tests := []struct {
		name   string
		body   map[string]any
		status int
	}{
		{"missing a question's answer", map[string]any{
			"answers": map[string]any{"Which color?": "Red"},
		}, http.StatusBadRequest},
		{"two answers for a single-select", map[string]any{
			"answers": map[string]any{
				"Which color?": []string{"Red", "Blue"}, "Which toppings?": "Cheese",
			},
		}, http.StatusBadRequest},
		{"answer matching no question", map[string]any{
			"answers": map[string]any{
				"Which color?": "Red", "Which toppings?": "Cheese", "Bogus?": "x",
			},
		}, http.StatusBadRequest},
		{"allow on a question request", map[string]any{
			"answers": map[string]any{"Which color?": "Red", "Which toppings?": "Cheese"},
			"allow":   true,
		}, http.StatusBadRequest},
		{"free text and multi-select accepted", map[string]any{
			"answers": map[string]any{
				"Which color?":    "Chartreuse",
				"Which toppings?": []string{"Cheese", "Basil"},
			},
		}, http.StatusOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newActionHarness(t)
			task := queuedTask(t, h)
			setAwaitingInput(t, h, task.ID, pendingQuestionJSON)
			resp, body := h.doJSON(t, http.MethodPost,
				fmt.Sprintf("/v1/tasks/%d/answer", task.ID), tt.body)
			if resp.StatusCode != tt.status {
				t.Fatalf("answer: %d %s, want %d", resp.StatusCode, body, tt.status)
			}
			if tt.status == http.StatusOK {
				out := decodeTask(t, body)
				if out.State != string(store.TaskRunning) {
					t.Errorf("state after answer = %s, want running", out.State)
				}
				if len(out.PendingInput) != 0 {
					t.Errorf("pending_input survived the answer: %s", out.PendingInput)
				}
			} else if !strings.Contains(string(body), CodeValidationFailed) {
				t.Errorf("error body %s does not carry %s", body, CodeValidationFailed)
			}
		})
	}
}

func TestAnswerPermissionValidation(t *testing.T) {
	tests := []struct {
		name   string
		body   map[string]any
		status int
	}{
		{"answers on a permission request", map[string]any{
			"answers": map[string]any{"Which color?": "Red"},
		}, http.StatusBadRequest},
		{"missing allow", map[string]any{}, http.StatusBadRequest},
		{"deny accepted", map[string]any{"allow": false}, http.StatusOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newActionHarness(t)
			task := queuedTask(t, h)
			setAwaitingInput(t, h, task.ID, pendingPermissionJSON)
			resp, body := h.doJSON(t, http.MethodPost,
				fmt.Sprintf("/v1/tasks/%d/answer", task.ID), tt.body)
			if resp.StatusCode != tt.status {
				t.Fatalf("answer: %d %s, want %d", resp.StatusCode, body, tt.status)
			}
		})
	}
}

// TestPendingInputOnTaskGet pins §13.2: the full request rides the task.
func TestPendingInputOnTaskGet(t *testing.T) {
	h := newActionHarness(t)
	task := queuedTask(t, h)
	setAwaitingInput(t, h, task.ID, pendingQuestionJSON)
	resp, body := h.doJSON(t, http.MethodGet, fmt.Sprintf("/v1/tasks/%d", task.ID), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get task: %d %s", resp.StatusCode, body)
	}
	out := decodeTask(t, body)
	if string(out.PendingInput) != pendingQuestionJSON {
		t.Errorf("pending_input = %s, want the stored request verbatim", out.PendingInput)
	}
	if !hasAction(out.AvailableActions, "answer") {
		t.Errorf("available_actions %v lack answer", out.AvailableActions)
	}
}

func hasAction(actions []string, want string) bool {
	for _, a := range actions {
		if a == want {
			return true
		}
	}
	return false
}

// longQuestionText is a question the size Claude routinely writes: prose the
// agent authored, well past maxFieldKeyBytes. §7.4 keys an answer by the
// question's exact text (taskrun.validateAnswer matches it verbatim, and the
// claude adapter writes it back to the CLI under that same text, §9.2), so
// nothing between the agent and this route may shorten it.
func longQuestionText() string {
	q := "Which of these two migration strategies should I take for the store " +
		"package, given that the events table is already several million rows " +
		"and the daemon holds a single SQLite connection: " +
		strings.Repeat("a rolling backfill behind a feature flag, or one offline pass? ", 4)
	if len(q) <= maxFieldKeyBytes {
		panic("longQuestionText must exceed maxFieldKeyBytes to exercise the bound")
	}
	return q
}

// TestAnswerLongQuestion pins the §13.1 field bound against §7.4: a question
// the daemon was willing to park on and display is one a human can answer. An
// `answers` key is not a caller-chosen identifier like a `fields` key — it is
// the agent's question text — so the 256 B key bound must not apply to it.
func TestAnswerLongQuestion(t *testing.T) {
	h := newActionHarness(t)
	task := queuedTask(t, h)

	text := longQuestionText()
	pending, err := json.Marshal(map[string]any{
		"kind": "question",
		"questions": []any{map[string]any{
			"text": text, "header": "Migration", "options": []string{"Rolling", "Offline"},
		}},
	})
	if err != nil {
		t.Fatalf("marshal pending: %v", err)
	}
	setAwaitingInput(t, h, task.ID, string(pending))

	resp, body := h.doJSON(t, http.MethodPost, fmt.Sprintf("/v1/tasks/%d/answer", task.ID),
		map[string]any{"answers": map[string]any{text: "Rolling"}})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("answer a %d-byte question: %d %s, want 200", len(text), resp.StatusCode, body)
	}
	out := decodeTask(t, body)
	if out.State != string(store.TaskRunning) {
		t.Errorf("state after answer = %s, want running", out.State)
	}
	if len(out.PendingInput) != 0 {
		t.Errorf("pending_input survived the answer: %s", out.PendingInput)
	}
}

// TestFieldsKeyBoundUnchanged is TestAnswerLongQuestion's guard rail: the
// `fields` key really is a caller-chosen identifier (§8.1.2) and keeps its
// 256 B bound, so relaxing the answers key must not relax this one.
func TestFieldsKeyBoundUnchanged(t *testing.T) {
	h := newActionHarness(t)
	resp, body := h.doJSON(t, http.MethodPost, "/v1/tasks", map[string]any{
		"project_id": h.projectID,
		"title":      "long fields key",
		"fields":     map[string]any{strings.Repeat("k", maxFieldKeyBytes+1): "v"},
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("create with an oversized fields key: %d %s, want 400", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), CodeValidationFailed) {
		t.Errorf("error body %s does not carry %s", body, CodeValidationFailed)
	}
}
