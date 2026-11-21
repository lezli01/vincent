package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/chatrun"
	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/testrepo"
)

// Task 167's routes over the real handlers and runner: POST
// /v1/chats/{id}/rewind and /fork, the rewind fields on GET, the export's
// include_rewound, and pruning on delete.

type rewindTurn struct {
	Seq       int     `json:"seq"`
	State     string  `json:"state"`
	Prompt    string  `json:"prompt"`
	Checkpt   string  `json:"checkpoint_sha"`
	HeadBef   string  `json:"head_before"`
	RewoundAt *string `json:"rewound_at"`
	RewindID  string  `json:"rewind_id"`
	Rewind    *struct {
		Code, Conversation, Fork string
	} `json:"rewind"`
}

type rewindChatRead struct {
	Chat struct {
		ID            int64  `json:"id"`
		Draft         string `json:"draft"`
		ResumePending string `json:"resume_pending"`
		RewoundStates []struct {
			K       int    `json:"k"`
			Ref     string `json:"ref"`
			Blocked string `json:"blocked"`
		} `json:"rewound_states"`
	} `json:"chat"`
	Turns []rewindTurn `json:"turns"`
}

func (h *chatHarness) readRewind(t *testing.T, id int64) rewindChatRead {
	t.Helper()
	resp, body := h.doJSON(t, http.MethodGet, fmt.Sprintf("/v1/chats/%d", id), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET chat = %d %s", resp.StatusCode, body)
	}
	var out rewindChatRead
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// turnDone sends prompt and waits for the chat to be idle again.
func (h *chatHarness) turnDone(t *testing.T, id int64, prompt string) {
	t.Helper()
	resp, body := h.doJSON(t, http.MethodPost, fmt.Sprintf("/v1/chats/%d/send", id), map[string]any{"message": prompt})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("send = %d %s", resp.StatusCode, body)
	}
	waitUntil(t, func() bool {
		c, err := h.store.GetChat(t.Context(), id)
		return err == nil && c.State == "idle" && !h.runner.Running(id)
	})
	waitUntil(t, func() bool {
		r := h.readRewind(t, id)
		return len(r.Turns) > 0 && r.Turns[len(r.Turns)-1].Rewind != nil &&
			r.Turns[len(r.Turns)-1].Rewind.Code != "chat_turn_live"
	})
}

func (h *chatHarness) post(t *testing.T, path string, req any) (int, map[string]any) {
	t.Helper()
	resp, body := h.doJSON(t, http.MethodPost, path, req)
	var out map[string]any
	_ = json.Unmarshal(body, &out)
	return resp.StatusCode, out
}

// twoTurnChat is a free chat with a.txt "one" before turn 1 and "two"
// before turn 2, and "three" after it.
func twoTurnChat(t *testing.T, h *chatHarness) (int64, string) {
	t.Helper()
	id, chat := handoffFixture(t, h)
	dir, _ := chat["worktree_path"].(string)
	testrepo.WriteFile(t, dir, "a.txt", "one\n")
	h.turnDone(t, id, "first")
	testrepo.WriteFile(t, dir, "a.txt", "two\n")
	h.turnDone(t, id, "second")
	testrepo.WriteFile(t, dir, "a.txt", "three\n")
	return id, dir
}

func readWorktreeFile(t *testing.T, dir, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, rel))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestChatRewindCodeConversationAndForward(t *testing.T) {
	h := newChatHarness(t)
	id, dir := twoTurnChat(t, h)
	r := h.readRewind(t, id)
	if len(r.Turns) != 2 || r.Turns[1].Checkpt == "" || r.Turns[1].HeadBef == "" {
		t.Fatalf("turns carry no checkpoint: %+v", r.Turns)
	}
	if o := r.Turns[1].Rewind; o == nil || o.Code != "" || o.Conversation != "" || o.Fork != "" {
		t.Fatalf("turn 2 options = %+v, want all allowed", o)
	}

	code, body := h.post(t, fmt.Sprintf("/v1/chats/%d/rewind", id), map[string]any{"turn": 2, "mode": "code"})
	if code != http.StatusOK {
		t.Fatalf("code rewind = %d %v", code, body)
	}
	if got := readWorktreeFile(t, dir, "a.txt"); got != "two\n" {
		t.Errorf("a.txt = %q, want the state before turn 2", got)
	}
	ref, _ := body["ref"].(string)
	names, err := h.runner.Refs(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if ref != names.Rewound(id, 1) {
		t.Errorf("ref = %q", ref)
	}
	if turns, _ := body["rewound_turns"].([]any); len(turns) != 0 {
		t.Errorf("a code rewind took turns out: %v", turns)
	}
	r = h.readRewind(t, id)
	if len(r.Chat.RewoundStates) != 1 || r.Chat.RewoundStates[0].Ref != ref {
		t.Fatalf("rewound_states = %+v", r.Chat.RewoundStates)
	}

	// Forward to the state the rewind saved.
	if code, body := h.post(t, fmt.Sprintf("/v1/chats/%d/rewind", id), map[string]any{"rewound": 1}); code != http.StatusOK {
		t.Fatalf("forward rewind = %d %v", code, body)
	}
	if got := readWorktreeFile(t, dir, "a.txt"); got != "three\n" {
		t.Errorf("a.txt after forward = %q, want three", got)
	}

	// The conversation: turn 2 is taken out and its prompt is the draft.
	code, body = h.post(t, fmt.Sprintf("/v1/chats/%d/rewind", id), map[string]any{"turn": 2, "mode": "conversation"})
	if code != http.StatusOK {
		t.Fatalf("conversation rewind = %d %v", code, body)
	}
	if turns, _ := body["rewound_turns"].([]any); len(turns) != 1 || turns[0] != float64(2) {
		t.Errorf("rewound_turns = %v", body["rewound_turns"])
	}
	r = h.readRewind(t, id)
	if len(r.Turns) != 2 || r.Turns[1].RewoundAt == nil || r.Turns[1].RewindID == "" || r.Turns[0].RewoundAt != nil {
		t.Fatalf("GET turns = %+v, want all turns with turn 2 rewound", r.Turns)
	}
	if r.Chat.Draft != "second" || r.Chat.ResumePending == "" {
		t.Errorf("draft %q resume_pending %q", r.Chat.Draft, r.Chat.ResumePending)
	}
	if code, body := h.post(t, fmt.Sprintf("/v1/chats/%d/rewind", id), map[string]any{"turn": 2}); code != http.StatusConflict ||
		errCode(body) != chatrun.ReasonTurnRewound {
		t.Errorf("rewind to a rewound turn = %d %v", code, body)
	}

	// The export leaves the rewound turn out unless asked.
	_, md := h.doJSON(t, http.MethodGet, fmt.Sprintf("/v1/chats/%d/export?format=md", id), nil)
	if strings.Contains(string(md), "## Turn 2") {
		t.Errorf("export shows the rewound turn:\n%s", md)
	}
	_, md = h.doJSON(t, http.MethodGet, fmt.Sprintf("/v1/chats/%d/export?format=md&include_rewound=true", id), nil)
	if !strings.Contains(string(md), "## Turn 2") || !strings.Contains(string(md), "Rewound:") {
		t.Errorf("include_rewound export lacks the marked turn:\n%s", md)
	}
	if resp, _ := h.doJSON(t, http.MethodGet, fmt.Sprintf("/v1/chats/%d/export?include_rewound=yes", id), nil); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("include_rewound=yes = %d, want 400", resp.StatusCode)
	}
}

func TestChatRewindValidationAndRefusals(t *testing.T) {
	h := newChatHarness(t)
	id, dir := twoTurnChat(t, h)
	path := fmt.Sprintf("/v1/chats/%d/rewind", id)
	for _, req := range []map[string]any{
		{}, {"turn": 1, "rewound": 1}, {"rewound": 1, "mode": "both"}, {"turn": 1, "mode": "sideways"}, {"turn": -1},
	} {
		if code, body := h.post(t, path, req); code != http.StatusBadRequest {
			t.Errorf("rewind %v = %d %v, want 400", req, code, body)
		}
	}
	if code, _ := h.post(t, "/v1/chats/999/rewind", map[string]any{"turn": 1}); code != http.StatusNotFound {
		t.Errorf("missing chat = %d, want 404", code)
	}
	if code, _ := h.post(t, path, map[string]any{"turn": 9}); code != http.StatusNotFound {
		t.Errorf("missing turn = %d, want 404", code)
	}
	if code, _ := h.post(t, fmt.Sprintf("/v1/chats/%d/fork", id), map[string]any{}); code != http.StatusBadRequest {
		t.Errorf("fork without a turn = %d, want 400", code)
	}

	// A commit no turn made: the code rewind refuses, a fork does not.
	testrepo.Run(t, dir, "-c", "user.name=x", "-c", "user.email=x@y", "commit", "-q", "--allow-empty", "-m", "mine")
	if code, body := h.post(t, path, map[string]any{"turn": 1, "mode": "code"}); code != http.StatusConflict ||
		errCode(body) != chatrun.ReasonRewindForeignCommits {
		t.Errorf("foreign commits = %d %v", code, body)
	}
	if code, body := h.post(t, fmt.Sprintf("/v1/chats/%d/fork", id), map[string]any{"turn": 1}); code != http.StatusCreated {
		t.Errorf("fork over foreign commits = %d %v, want 201", code, body)
	}

	// A turn whose checkpoint was never taken.
	turns, err := h.store.ListChatTurns(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	turns[0].CheckpointSHA = ""
	if err := h.store.UpdateChatTurn(t.Context(), &turns[0]); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{path, fmt.Sprintf("/v1/chats/%d/fork", id)} {
		if code, body := h.post(t, p, map[string]any{"turn": 1}); code != http.StatusConflict ||
			errCode(body) != chatrun.ReasonCheckpointUnavailable {
			t.Errorf("%s without a checkpoint = %d %v", p, code, body)
		}
	}

	// A live turn.
	t.Setenv("FAKEAGENT_SCENARIO", "hang")
	if resp, body := h.doJSON(t, http.MethodPost, fmt.Sprintf("/v1/chats/%d/send", id), map[string]any{"message": "hold"}); resp.StatusCode != http.StatusAccepted {
		t.Fatalf("send = %d %s", resp.StatusCode, body)
	}
	waitUntil(t, func() bool { return h.runner.Running(id) })
	for _, p := range []string{path, fmt.Sprintf("/v1/chats/%d/fork", id)} {
		if code, body := h.post(t, p, map[string]any{"turn": 2}); code != http.StatusConflict || errCode(body) != CodeChatTurnLive {
			t.Errorf("%s during a live turn = %d %v", p, code, body)
		}
	}
	if code, body := h.post(t, fmt.Sprintf("/v1/chats/%d/cancel", id), nil); code != http.StatusNoContent && code != 0 {
		t.Logf("cancel = %d %v", code, body)
	}
	waitUntil(t, func() bool { return !h.runner.Running(id) })

	// An ended chat.
	waitUntil(t, func() bool {
		code, _ := h.post(t, fmt.Sprintf("/v1/chats/%d/archive?force=true", id), nil)
		return code == http.StatusOK
	})
	if code, body := h.post(t, path, map[string]any{"turn": 2}); code != http.StatusConflict || errCode(body) != chatrun.ReasonChatTerminal {
		t.Errorf("rewind of an archived chat = %d %v", code, body)
	}
}

func TestChatForkCopiesSettingsAndDirtyState(t *testing.T) {
	h := newChatHarness(t)
	id, _ := twoTurnChat(t, h)
	src, err := h.store.GetChat(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	code, body := h.post(t, fmt.Sprintf("/v1/chats/%d/fork", id), map[string]any{"turn": 2})
	if code != http.StatusCreated {
		t.Fatalf("fork = %d %v", code, body)
	}
	chat, _ := body["chat"].(map[string]any)
	forkID := int64(chat["id"].(float64))
	fork, err := h.store.GetChat(t.Context(), forkID)
	if err != nil {
		t.Fatal(err)
	}
	if fork.State != "idle" || h.runner.Running(forkID) {
		t.Errorf("fork state = %s, want idle with no turn", fork.State)
	}
	if fork.Agent != src.Agent || fork.Model != src.Model || fork.Effort != src.Effort || fork.PermissionMode != src.PermissionMode {
		t.Errorf("fork settings %+v differ from the source's", fork)
	}
	if fork.BaseBranch != src.BaseBranch || fork.BaseSHA != src.BaseSHA {
		t.Errorf("fork base %s@%s, want the source's %s@%s", fork.BaseBranch, fork.BaseSHA, src.BaseBranch, src.BaseSHA)
	}
	if fork.LinkedTaskID != nil || fork.IssueID != nil || fork.Branch == src.Branch || fork.WorktreePath == src.WorktreePath {
		t.Errorf("fork inherited the source's link, issue or workspace: %+v", fork)
	}
	if fork.ForkedFromChatID == nil || *fork.ForkedFromChatID != id || fork.ForkedFromSeq == nil || *fork.ForkedFromSeq != 2 {
		t.Errorf("forked_from = %v/%v", fork.ForkedFromChatID, fork.ForkedFromSeq)
	}
	if ff, _ := chat["forked_from"].(map[string]any); ff == nil || ff["seq"] != float64(2) {
		t.Errorf("wire forked_from = %v", chat["forked_from"])
	}
	if got := readWorktreeFile(t, fork.WorktreePath, "a.txt"); got != "two\n" {
		t.Errorf("fork a.txt = %q, want the state before turn 2", got)
	}
	if st := testrepo.Run(t, fork.WorktreePath, "status", "--porcelain"); !strings.Contains(st, "a.txt") ||
		strings.Contains(st, "A  a.txt") {
		t.Errorf("fork status = %q, want a.txt dirty and unstaged", st)
	}
	turns, err := h.store.ListChatTurns(t.Context(), forkID)
	if err != nil || len(turns) != 0 {
		t.Errorf("fork has %d turns (%v), want none", len(turns), err)
	}
}

// TestForkBaseIsTheSources is decision C for the three kinds of source.
func TestForkBaseIsTheSources(t *testing.T) {
	h := newChatHarness(t)
	ctx := t.Context()
	s := &Server{deps: Deps{Store: h.store}}
	task := &store.Task{
		ProjectID: h.projectID, Title: "linked", WorkflowName: "t", WorkflowSnapshot: "steps: []",
		BaseBranch: "release", BaseSHA: "abc123", State: store.TaskBlocked,
	}
	if err := h.store.CreateTask(ctx, task, func(id int64) (string, error) { return fmt.Sprintf("vincent/%d-l", id), nil }); err != nil {
		t.Fatal(err)
	}
	issueID := int64(4)
	for name, c := range map[string]struct {
		chat       *store.Chat
		branch, sh string
	}{
		"free":   {&store.Chat{BaseBranch: "main", BaseSHA: "f00"}, "main", "f00"},
		"issue":  {&store.Chat{BaseBranch: "main", BaseSHA: "1ss", IssueID: &issueID}, "main", "1ss"},
		"linked": {&store.Chat{LinkedTaskID: &task.ID}, "release", "abc123"},
	} {
		b, sha, err := s.forkBase(ctx, c.chat)
		if err != nil || b != c.branch || sha != c.sh {
			t.Errorf("%s: base %s@%s (%v), want %s@%s", name, b, sha, err, c.branch, c.sh)
		}
	}
}

func (h *chatHarness) chatRefs(t *testing.T, id int64) string {
	t.Helper()
	names, err := h.runner.Refs(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(testrepo.Run(t, h.repo, "for-each-ref", "--format=%(refname)", names.Prefix(id)))
}

// TestChatCheckpointRefsPrunedOnDelete is 167.8: a permanent delete, with
// and without delete_branch and forced, removes the refs; a hand-off and an
// archive keep them.
func TestChatCheckpointRefsPrunedOnDelete(t *testing.T) {
	h := newChatHarness(t)
	oneTurn := func() int64 {
		id, _ := handoffFixture(t, h)
		h.turnDone(t, id, "hello")
		if h.chatRefs(t, id) == "" {
			t.Fatalf("chat %d has no checkpoint ref", id)
		}
		return id
	}
	for _, q := range []string{"", "?delete_branch=true"} {
		id := oneTurn()
		if code, body := h.post(t, fmt.Sprintf("/v1/chats/%d/archive?force=true", id), nil); code != http.StatusOK {
			t.Fatalf("archive = %d %v", code, body)
		}
		if h.chatRefs(t, id) == "" {
			t.Errorf("archive pruned chat %d's refs", id)
		}
		if resp, body := h.doJSON(t, http.MethodDelete, fmt.Sprintf("/v1/chats/%d%s", id, q), nil); resp.StatusCode != http.StatusOK {
			t.Fatalf("delete%s = %d %s", q, resp.StatusCode, body)
		}
		if got := h.chatRefs(t, id); got != "" {
			t.Errorf("delete%s left %s", q, got)
		}
	}
	forced := oneTurn()
	if resp, body := h.doJSON(t, http.MethodDelete, fmt.Sprintf("/v1/chats/%d?force=true", forced), nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("force delete = %d %s", resp.StatusCode, body)
	}
	if got := h.chatRefs(t, forced); got != "" {
		t.Errorf("force delete left %s", got)
	}
	handed := oneTurn()
	if code, body := h.handoff(t, handed, map[string]any{"title": "take it"}); code != http.StatusCreated {
		t.Fatalf("handoff = %d %v", code, body)
	}
	if h.chatRefs(t, handed) == "" {
		t.Error("a hand-off pruned the chat's refs")
	}
}
