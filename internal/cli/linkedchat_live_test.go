package cli

import (
	"encoding/json"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/store"
)

// `vincent task chat` and `vincent chat close` (task 119) against the real
// handlers. The task is written straight into the store in the state the
// engine would have left it — blocked, with a worktree claimed — because the
// action moves nothing and runs nothing: opening a chat is a row and a lock,
// and closing one is the reverse.

// linkedChatSnapshot is a snapshot the handler can parse: the chat's opening
// context is assembled from the step the task stopped on.
const linkedChatSnapshot = `name: adhoc
steps:
  - id: implement
    type: agent
    prompt: do the thing
`

// addStoppedTask creates a blocked task on branch, with a worktree when
// withWorktree. Branches are unique per project, so each task names its own.
func (h *liveHarness) addStoppedTask(t *testing.T, branch string, withWorktree bool) *store.Task {
	t.Helper()
	ctx := t.Context()
	task := &store.Task{
		ProjectID: h.projectID, Title: "flaky check", WorkflowName: "adhoc",
		WorkflowSnapshot: linkedChatSnapshot, BaseBranch: "main", BranchName: branch,
		State: store.TaskBlocked, BlockReason: "check_failed",
	}
	if err := h.st.CreateTask(ctx, task, nil); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if withWorktree {
		if err := h.st.ClaimTaskWorktree(ctx, task.ID, t.TempDir(), "", nil); err != nil {
			t.Fatalf("ClaimTaskWorktree: %v", err)
		}
	}
	return task
}

// taskLock reads the task's lock as both endpoints serve it: the detail and
// the list row must agree, since a board acts on the row.
func taskLock(t *testing.T, h *liveHarness, id int64) (openChat *int64, actions []string) {
	t.Helper()
	c, err := apiclient.Discover(h.dataDir)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	detail, err := c.GetTask(t.Context(), id)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	rows, err := c.ListTasks(t.Context(), apiclient.ListTasksOptions{})
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	i := slices.IndexFunc(rows, func(r apiclient.Task) bool { return r.ID == id })
	if i < 0 {
		t.Fatalf("task %d is not listed", id)
	}
	row := rows[i]
	if !equalIDs(detail.OpenChatID, row.OpenChatID) ||
		!slices.Equal(detail.AvailableActions, row.AvailableActions) {
		t.Fatalf("detail (open_chat_id %v, actions %v) and list row (%v, %v) disagree",
			ptrString(detail.OpenChatID), detail.AvailableActions, ptrString(row.OpenChatID), row.AvailableActions)
	}
	return detail.OpenChatID, detail.AvailableActions
}

func equalIDs(a, b *int64) bool {
	return (a == nil) == (b == nil) && (a == nil || *a == *b)
}

func ptrString(p *int64) string {
	if p == nil {
		return "nil"
	}
	return strconv.FormatInt(*p, 10)
}

// listChatIDs runs `vincent chat list --json` with extra flags.
func listChatIDs(t *testing.T, extra ...string) []int64 {
	t.Helper()
	out, errOut, code := runCLI(t, append([]string{"chat", "list", "--json"}, extra...)...)
	if code != 0 {
		t.Fatalf("chat list %v: exit %d (%s)", extra, code, errOut)
	}
	var chats []apiclient.Chat
	if err := json.Unmarshal([]byte(out), &chats); err != nil {
		t.Fatalf("chat list %v: %v\n%s", extra, err, out)
	}
	ids := []int64{}
	for i := range chats {
		ids = append(ids, chats[i].ID)
	}
	return ids
}

// Open, refuse a second, list by task, refuse the chat-side actions that
// would reach the task's worktree, close, and open again — through both
// output modes of both commands.
func TestTaskChatAndChatCloseRoundTrip(t *testing.T) {
	h := newLiveHarness(t)
	task := h.addStoppedTask(t, "vincent/flaky-check", true)
	other := h.addStoppedTask(t, "vincent/other-check", true)
	tid := strconv.FormatInt(task.ID, 10)

	if open, actions := taskLock(t, h, task.ID); open != nil || !slices.Contains(actions, apiclient.ActionChat) {
		t.Fatalf("before: open_chat_id %s, actions %v; want none and %q offered",
			ptrString(open), actions, apiclient.ActionChat)
	}

	// Open, as JSON: the body is the chat, naming the task it works in.
	out, errOut, code := runCLI(t, "task", "chat", tid, "--title", "why did it block", "--json")
	if code != 0 {
		t.Fatalf("task chat --json: exit %d (%s)", code, errOut)
	}
	var chat apiclient.Chat
	if err := json.Unmarshal([]byte(out), &chat); err != nil {
		t.Fatalf("task chat --json: %v\n%s", err, out)
	}
	if chat.LinkedTaskID == nil || *chat.LinkedTaskID != task.ID {
		t.Errorf("linked_task_id = %s, want %d", ptrString(chat.LinkedTaskID), task.ID)
	}
	if chat.State != "idle" || chat.Title != "why did it block" || chat.Agent != "claude" ||
		chat.Branch != task.BranchName {
		t.Errorf("chat = state %q title %q agent %q branch %q; want idle, the flag's title, "+
			"the first resuming adapter, and the task's branch", chat.State, chat.Title, chat.Agent, chat.Branch)
	}
	cid := strconv.FormatInt(chat.ID, 10)

	// The task is locked: it names the chat, and offers only cancel.
	if open, actions := taskLock(t, h, task.ID); !equalIDs(open, &chat.ID) ||
		!slices.Equal(actions, []string{apiclient.ActionCancel}) {
		t.Fatalf("locked: open_chat_id %s, actions %v; want %d and [cancel]", ptrString(open), actions, chat.ID)
	}

	// A second chat is refused, and the refusal names the open one as commands.
	_, errOut, code = runCLI(t, "task", "chat", tid)
	if code != 1 {
		t.Errorf("second task chat: exit %d, want 1", code)
	}
	if !strings.HasPrefix(errOut, "Error: ") || !strings.Contains(errOut, "vincent chat close "+cid) {
		t.Errorf("second task chat stderr = %q, want the daemon's message and the way out", errOut)
	}

	// --task narrows the listing to this task's chats.
	if got := listChatIDs(t, "--task", tid); !slices.Equal(got, []int64{chat.ID}) {
		t.Errorf("chat list --task %d = %v, want [%d]", task.ID, got, chat.ID)
	}
	if got := listChatIDs(t, "--task", strconv.FormatInt(other.ID, 10)); len(got) != 0 {
		t.Errorf("chat list --task %d = %v, want none", other.ID, got)
	}

	// The worktree is the task's, so archiving the chat is refused.
	_, errOut, code = runCLI(t, "chat", "archive", cid)
	if code != 1 || !strings.Contains(errOut, "close the chat instead") {
		t.Errorf("chat archive: exit %d stderr %q; want 1 and the daemon's refusal", code, errOut)
	}

	// Close, as a human reads it.
	out, errOut, code = runCLI(t, "chat", "close", cid)
	if code != 0 {
		t.Fatalf("chat close: exit %d (%s)", code, errOut)
	}
	if want := "chat " + cid + " closed; task " + tid + " is unlocked\n"; out != want {
		t.Errorf("chat close stdout = %q, want %q", out, want)
	}
	if open, actions := taskLock(t, h, task.ID); open != nil || !slices.Contains(actions, apiclient.ActionChat) {
		t.Fatalf("after close: open_chat_id %s, actions %v; want none and %q offered again",
			ptrString(open), actions, apiclient.ActionChat)
	}
	// Closed is terminal: hidden by default, listed with --archived.
	if got := listChatIDs(t, "--task", tid); len(got) != 0 {
		t.Errorf("chat list --task after close = %v, want none", got)
	}
	if got := listChatIDs(t, "--task", tid, "--archived"); !slices.Equal(got, []int64{chat.ID}) {
		t.Errorf("chat list --task --archived after close = %v, want [%d]", got, chat.ID)
	}

	// Closing twice is refused.
	_, errOut, code = runCLI(t, "chat", "close", cid, "--json")
	if code != 1 || !strings.Contains(errOut, "already closed") {
		t.Errorf("second chat close: exit %d stderr %q; want 1 and the daemon's refusal", code, errOut)
	}

	// The lock lifted, so a new chat opens — as a human reads it, titled
	// after the task — and closes as JSON.
	out, errOut, code = runCLI(t, "task", "chat", tid)
	if code != 0 {
		t.Fatalf("reopen: exit %d (%s)", code, errOut)
	}
	fields := strings.Fields(out)
	if len(fields) < 6 || fields[0] != "chat" || fields[2] != "opened" || fields[5] != tid ||
		!strings.Contains(out, task.Title) || !strings.Contains(out, "vincent chat close "+fields[1]) {
		t.Fatalf("reopen stdout = %q, want the chat on task %d, titled after it, and how to close it", out, task.ID)
	}
	out, errOut, code = runCLI(t, "chat", "close", fields[1], "--json")
	if code != 0 {
		t.Fatalf("chat close --json: exit %d (%s)", code, errOut)
	}
	var closed apiclient.Chat
	if err := json.Unmarshal([]byte(out), &closed); err != nil {
		t.Fatalf("chat close --json: %v\n%s", err, out)
	}
	if strconv.FormatInt(closed.ID, 10) != fields[1] || closed.State != "closed" ||
		closed.LinkedTaskID == nil || *closed.LinkedTaskID != task.ID {
		t.Errorf("closed = id %d state %q linked %s; want %s, closed, %d",
			closed.ID, closed.State, ptrString(closed.LinkedTaskID), fields[1], task.ID)
	}
}

// The two refusals that are not about the lock: a task with no worktree has
// nothing to talk about, and a running task is not stopped.
func TestTaskChatRefusals(t *testing.T) {
	h := newLiveHarness(t)
	bare := h.addStoppedTask(t, "vincent/never-admitted", false)

	_, errOut, code := runCLI(t, "task", "chat", strconv.FormatInt(bare.ID, 10))
	if code != 1 || !strings.Contains(errOut, "has no worktree") {
		t.Errorf("no worktree: exit %d stderr %q; want 1 and the daemon's refusal", code, errOut)
	}
	// The harness's own task is running.
	_, errOut, code = runCLI(t, "task", "chat", strconv.FormatInt(h.taskID, 10))
	if code != 1 || !strings.HasPrefix(errOut, "Error: ") {
		t.Errorf("running task: exit %d stderr %q; want 1 and the daemon's refusal", code, errOut)
	}
	if strings.Contains(errOut, "vincent chat close") {
		t.Errorf("a refusal that is not the lock named a chat to close: %q", errOut)
	}
	if got := listChatIDs(t, "--archived"); len(got) != 0 {
		t.Errorf("refused opens left chats behind: %v", got)
	}
}
