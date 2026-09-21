package api

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/agent"
	"github.com/lezli01/vincent/internal/agent/agenttest"
	"github.com/lezli01/vincent/internal/agent/claude"
	"github.com/lezli01/vincent/internal/chatrun"
	"github.com/lezli01/vincent/internal/chatstate"
	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/gitx"
	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/testrepo"
	"github.com/lezli01/vincent/internal/worktree"
)

// filesHarness is GET /v1/chats/{id}/files over the real handler, a real
// worktree manager and a started chat runner (task 126.6, #550).
//
// Unlike the skills route's harness this one registers a *shipped* adapter:
// the mention syntax is the thing under test and claude is the only place the
// `@"…"`-on-a-space rule is written, so proving a client never has to rebuild
// it means proving it against that adapter. Nothing ever launches it — the
// route reads two static methods — which is why a path that is not a CLI is
// enough. StubNoMentions is registered beside it for the refusal, per task 126
// decision 25.
type filesHarness struct {
	*projectHarness
	projectID int64
}

func newFilesHarness(t *testing.T) *filesHarness {
	t.Helper()
	dataDir := t.TempDir()
	st, err := store.Open(filepath.Join(dataDir, "files.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	project := &store.Project{Name: "proj", Path: t.TempDir(), DefaultBranch: "main"}
	if err := st.CreateProject(t.Context(), project); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	reg := agent.NewRegistry(claude.New(func() string { return "claude" }), agenttest.StubNoMentions{})
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	chats := chatrun.New(chatrun.Deps{
		Store: st, Config: config.Default, Agents: reg, DataDir: dataDir, Logger: log,
	})
	chats.Start(t.Context())
	t.Cleanup(chats.Stop)

	deps := Deps{
		Token: testToken, Config: config.Default, StartedAt: time.Now(), ListenAddr: "127.0.0.1:0",
		RequestStop: func() {}, Logger: log, Store: st, Agents: reg,
		Catalog: agent.NewCatalogCache(reg), Chats: chats,
		Worktrees: worktree.NewManager(gitx.New(), dataDir),
	}
	ts := httptest.NewServer(New(deps).Handler())
	t.Cleanup(ts.Close)
	return &filesHarness{projectHarness: &projectHarness{ts: ts, store: st}, projectID: project.ID}
}

// filesRepo is testrepo.Init with the developer's global git config taken out
// of the answer: `--exclude-standard` reads core.excludesFile, and a personal
// global ignore rule would silently remove a row these tests expect, on that
// machine only. internal/worktree's own file tests isolate it the same way.
func filesRepo(t *testing.T) string {
	t.Helper()
	dir := testrepo.Init(t, "main")
	excludes := filepath.Join(t.TempDir(), "empty-excludes")
	if err := os.WriteFile(excludes, nil, 0o644); err != nil {
		t.Fatalf("write empty excludes file: %v", err)
	}
	testrepo.Run(t, dir, "config", "core.excludesFile", excludes)
	return dir
}

// chat writes a chat of the harness's project on agentName, in state, working
// in dir.
func (h *filesHarness) chat(t *testing.T, agentName string, state chatstate.State, dir string) *store.Chat {
	t.Helper()
	c := &store.Chat{
		ProjectID: h.projectID, Title: "files", State: state, Agent: agentName,
		PermissionMode: string(agent.FullAuto), BaseBranch: "main", WorktreePath: dir,
	}
	if err := h.store.CreateChat(t.Context(), c); err != nil {
		t.Fatalf("CreateChat: %v", err)
	}
	return c
}

// linkedChat writes a blocked task working in dir, and a claude chat opened on
// it.
func (h *filesHarness) linkedChat(t *testing.T, dir string) (*store.Task, *store.Chat) {
	t.Helper()
	ctx := t.Context()
	task := &store.Task{
		ProjectID: h.projectID, Title: "linked", WorkflowName: "t", WorkflowSnapshot: "steps: []",
		BaseBranch: "main", State: store.TaskBlocked, WorktreePath: dir,
	}
	branch := func(id int64) (string, error) { return fmt.Sprintf("vincent/%d-linked", id), nil }
	if err := h.store.CreateTask(ctx, task, branch); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	c := &store.Chat{Title: "why", Agent: "claude", PermissionMode: string(agent.FullAuto)}
	if err := h.store.OpenLinkedChat(ctx, task.ID, store.TaskBlocked, c); err != nil {
		t.Fatalf("OpenLinkedChat: %v", err)
	}
	return task, c
}

// get fetches a chat's files and returns the status and the raw body.
func (h *filesHarness) get(t *testing.T, chatID int64, query string) (int, []byte) {
	t.Helper()
	resp, body := h.doJSON(t, http.MethodGet, fmt.Sprintf("/v1/chats/%d/files%s", chatID, query), nil)
	return resp.StatusCode, body
}

// list fetches a chat's files, requires a 200, and decodes the body.
func (h *filesHarness) list(t *testing.T, chatID int64, query string) chatFilesBody {
	t.Helper()
	code, body := h.get(t, chatID, query)
	if code != http.StatusOK {
		t.Fatalf("GET files%s = %d, want 200 (%s)", query, code, body)
	}
	var out chatFilesBody
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("files body: %v (%s)", err, body)
	}
	return out
}

// paths is the body's rows as plain paths.
func paths(body chatFilesBody) []string {
	out := make([]string, 0, len(body.Files))
	for _, f := range body.Files {
		out = append(out, f.Path)
	}
	return out
}

// TestChatFilesListsAFreeChatWorktree is the route's first promise: the files
// of the directory the next turn starts in, each carrying the adapter's own
// mention text.
func TestChatFilesListsAFreeChatWorktree(t *testing.T) {
	h := newFilesHarness(t)
	dir := filesRepo(t)
	testrepo.WriteFile(t, dir, "untracked.go", "package main\n")
	c := h.chat(t, "claude", chatstate.Idle, dir)

	got := h.list(t, c.ID, "")
	if got.ChatID != c.ID || got.Agent != "claude" || got.WorkDir != dir {
		t.Errorf("identity = (%d, %q, %q), want (%d, %q, %q)",
			got.ChatID, got.Agent, got.WorkDir, c.ID, "claude", dir)
	}
	if got.MentionSigil != "@" || got.MentionPosition != "anywhere" || !got.MentionExpands {
		t.Errorf("mention = (%q, %q, %v), want (@, anywhere, true)",
			got.MentionSigil, got.MentionPosition, got.MentionExpands)
	}
	if got.Truncated {
		t.Error("truncated is true for a listing well under the cap")
	}
	// README.md is committed and untracked.go was never added: the listing
	// is `--cached --others`, which is what someone typing `@` means by "the
	// files of the project".
	// Sorted for the comparison only: the wire order is git's own and this
	// route does not sort (task 126 decision 21).
	want := []string{"README.md", "untracked.go"}
	p := paths(got)
	slices.Sort(p)
	if !slices.Equal(p, want) {
		t.Fatalf("files = %q, want %q", p, want)
	}
	for _, f := range got.Files {
		if f.Mention != "@"+f.Path {
			t.Errorf("mention for %q = %q, want %q", f.Path, f.Mention, "@"+f.Path)
		}
	}
}

// TestChatFilesMentionQuotesASpace is the one row a client must never rebuild
// for itself: the quoting rule has one definition and it lives in the adapter.
func TestChatFilesMentionQuotesASpace(t *testing.T) {
	h := newFilesHarness(t)
	dir := filesRepo(t)
	if err := os.Mkdir(filepath.Join(dir, "dir with space"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	testrepo.WriteFile(t, dir, filepath.Join("dir with space", "q.txt"), "x\n")
	c := h.chat(t, "claude", chatstate.Idle, dir)

	got := h.list(t, c.ID, "")
	const want = `@"dir with space/q.txt"`
	idx := slices.IndexFunc(got.Files, func(f chatFileBody) bool { return f.Mention == want })
	if idx < 0 {
		t.Fatalf("no row mentions %s; got %+v", want, got.Files)
	}
	// The path itself is git's own bytes: forward slashes on every platform,
	// unquoted (task 126 decision 21).
	if p := got.Files[idx].Path; p != "dir with space/q.txt" {
		t.Errorf("path = %q, want %q", p, "dir with space/q.txt")
	}
}

// TestChatFilesListsTheLinkedTasksWorktree: a linked chat's next turn runs in
// the task's worktree, so that is the directory the list is about.
func TestChatFilesListsTheLinkedTasksWorktree(t *testing.T) {
	h := newFilesHarness(t)
	dir := filesRepo(t)
	task, c := h.linkedChat(t, dir)

	got := h.list(t, c.ID, "")
	if got.WorkDir != task.WorktreePath {
		t.Errorf("work_dir = %q, want the task's %q", got.WorkDir, task.WorktreePath)
	}
	if p := paths(got); !slices.Equal(p, []string{"README.md"}) {
		t.Errorf("files = %q, want the task worktree's", p)
	}
}

// TestChatFilesRefusesATerminalChatBeforeAnyGit pins the ordering: a terminal
// chat has no next turn, and so no directory a listing could be about.
//
// The chat points at a directory that does not exist, so a listing would have
// answered 400 — the 409 is proof that nothing enumerated.
func TestChatFilesRefusesATerminalChatBeforeAnyGit(t *testing.T) {
	h := newFilesHarness(t)
	missing := filepath.Join(t.TempDir(), "gone")
	for _, state := range []chatstate.State{chatstate.Archived, chatstate.HandedOff, chatstate.Closed} {
		c := h.chat(t, "claude", state, missing)
		code, body := h.get(t, c.ID, "")
		if code != http.StatusConflict {
			t.Fatalf("%s: GET files = %d, want 409 (%s)", state, code, body)
		}
		detail := decodeErrorBody(t, body)
		if detail.Code != CodeInvalidState {
			t.Errorf("%s: code = %q, want %q", state, detail.Code, CodeInvalidState)
		}
		if detail.Details["state"] != string(state) || detail.Details["action"] != "files" {
			t.Errorf("%s: details = %v, want state %q and action files", state, detail.Details, state)
		}
	}
}

// TestChatFilesRefusesALinkedChatWithNoWorktree: the task never got one, so
// there is no directory — the shape the skills route already answers with.
func TestChatFilesRefusesALinkedChatWithNoWorktree(t *testing.T) {
	h := newFilesHarness(t)
	// Opened on a task that has a worktree — OpenLinkedChat refuses one that
	// does not — and then the claim is dropped, which is the repaired task
	// this 409 is about.
	task, c := h.linkedChat(t, filesRepo(t))
	task.WorktreePath = ""
	if err := h.store.UpdateTask(t.Context(), task); err != nil {
		t.Fatalf("UpdateTask: %v", err)
	}

	code, body := h.get(t, c.ID, "")
	if code != http.StatusConflict {
		t.Fatalf("GET files = %d, want 409 (%s)", code, body)
	}
	detail := decodeErrorBody(t, body)
	if detail.Code != CodeTaskHasNoWorktree {
		t.Errorf("code = %q, want %q", detail.Code, CodeTaskHasNoWorktree)
	}
	if detail.Details["task_id"] != fmt.Sprint(task.ID) {
		t.Errorf("details = %v, want task_id %d", detail.Details, task.ID)
	}
}

// TestChatFilesMissingWorkspaceIs400 is the caller's repository problem, and
// the mapping handleProjectBranches already performs for a missing project
// path.
func TestChatFilesMissingWorkspaceIs400(t *testing.T) {
	h := newFilesHarness(t)
	c := h.chat(t, "claude", chatstate.Idle, filepath.Join(t.TempDir(), "gone"))

	code, body := h.get(t, c.ID, "")
	if code != http.StatusBadRequest {
		t.Fatalf("GET files = %d, want 400 (%s)", code, body)
	}
	if got := decodeErrorBody(t, body).Code; got != CodeValidationFailed {
		t.Errorf("code = %q, want %q", got, CodeValidationFailed)
	}
}

// TestChatFilesNonRepositoryIs500: the directory is there and git refused it.
// That is not something the caller can fix by asking differently, so it is not
// a 400.
func TestChatFilesNonRepositoryIs500(t *testing.T) {
	h := newFilesHarness(t)
	c := h.chat(t, "claude", chatstate.Idle, t.TempDir())

	code, body := h.get(t, c.ID, "")
	if code != http.StatusInternalServerError {
		t.Fatalf("GET files = %d, want 500 (%s)", code, body)
	}
	if got := decodeErrorBody(t, body).Code; got != CodeInternal {
		t.Errorf("code = %q, want %q", got, CodeInternal)
	}
}

// TestChatFilesLimitRejectsWhatIsNotAPositiveInteger, the spelling
// handleProjectGitHubIssues uses.
func TestChatFilesLimitRejectsWhatIsNotAPositiveInteger(t *testing.T) {
	h := newFilesHarness(t)
	c := h.chat(t, "claude", chatstate.Idle, filesRepo(t))

	for _, raw := range []string{"0", "-1", "abc", "1.5"} {
		code, body := h.get(t, c.ID, "?limit="+raw)
		if code != http.StatusBadRequest {
			t.Errorf("limit=%s: GET files = %d, want 400 (%s)", raw, code, body)
			continue
		}
		if got := decodeErrorBody(t, body).Code; got != CodeValidationFailed {
			t.Errorf("limit=%s: code = %q, want %q", raw, got, CodeValidationFailed)
		}
	}
}

// TestChatFilesLimitTruncates: rows cut by either bound set truncated, and
// exactly the asked-for count arrives.
func TestChatFilesLimitTruncates(t *testing.T) {
	h := newFilesHarness(t)
	dir := filesRepo(t)
	for _, name := range []string{"a.txt", "b.txt", "c.txt"} {
		testrepo.WriteFile(t, dir, name, "x\n")
	}
	c := h.chat(t, "claude", chatstate.Idle, dir)

	full := h.list(t, c.ID, "")
	if full.Truncated || len(full.Files) != 4 {
		t.Fatalf("unbounded listing = %d rows, truncated %v; want 4 and false",
			len(full.Files), full.Truncated)
	}
	got := h.list(t, c.ID, "?limit=2")
	if !got.Truncated || len(got.Files) != 2 {
		t.Fatalf("limit=2 = %d rows, truncated %v; want 2 and true", len(got.Files), got.Truncated)
	}
	// The kept rows are the listing's first, in git's order: a limit narrows
	// the answer, it does not reshuffle it.
	if !slices.Equal(paths(got), paths(full)[:2]) {
		t.Errorf("limit=2 kept %q, want the first two of %q", paths(got), paths(full))
	}
}

// TestChatFilesLimitOnlyEverLowersTheCap is task 126 decision 36 as
// arithmetic: a client can ask for less and never for more. It is asserted on
// the parser rather than over the wire because proving the clamp end to end
// would mean creating more than chatFilesMax files.
func TestChatFilesLimitOnlyEverLowersTheCap(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		raw  string
		want int
		ok   bool
	}{
		{"", chatFilesMax, true},
		{"  ", chatFilesMax, true},
		{"1", 1, true},
		{"49999", 49999, true},
		{fmt.Sprint(chatFilesMax), chatFilesMax, true},
		{fmt.Sprint(chatFilesMax + 1), chatFilesMax, true},
		{"999999999", chatFilesMax, true},
		{"0", 0, false},
		{"-1", 0, false},
		{"abc", 0, false},
	} {
		got, ok := chatFilesLimit(tc.raw)
		if got != tc.want || ok != tc.ok {
			t.Errorf("chatFilesLimit(%q) = (%d, %v), want (%d, %v)", tc.raw, got, ok, tc.want, tc.ok)
		}
	}
}

// TestChatFilesWithoutAMentionCapableAdapter: an adapter that cannot mention
// files is an empty sigil, not a refusal (task 126 decision 38). The paths are
// true regardless of who reads them, so they are served either way.
func TestChatFilesWithoutAMentionCapableAdapter(t *testing.T) {
	h := newFilesHarness(t)
	dir := filesRepo(t)

	for _, agentName := range []string{agenttest.NoMentionsName, "no-such-adapter"} {
		c := h.chat(t, agentName, chatstate.Idle, dir)
		got := h.list(t, c.ID, "")
		if got.MentionSigil != "" || got.MentionPosition != "" || got.MentionExpands {
			t.Errorf("%s: mention = (%q, %q, %v), want empty", agentName,
				got.MentionSigil, got.MentionPosition, got.MentionExpands)
		}
		if !slices.Equal(paths(got), []string{"README.md"}) {
			t.Errorf("%s: files = %q, want the listing anyway", agentName, paths(got))
		}
		for _, f := range got.Files {
			if f.Mention != "" {
				t.Errorf("%s: mention for %q = %q, want empty", agentName, f.Path, f.Mention)
			}
		}
	}
}

// TestChatFilesEmptyListingIsAnArray: "none" has one spelling on the wire, and
// it is never null.
func TestChatFilesEmptyListingIsAnArray(t *testing.T) {
	h := newFilesHarness(t)
	c := h.chat(t, "claude", chatstate.Idle, testrepo.InitEmpty(t, "main"))

	code, body := h.get(t, c.ID, "")
	if code != http.StatusOK {
		t.Fatalf("GET files = %d, want 200 (%s)", code, body)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatalf("files body: %v (%s)", err, body)
	}
	if string(raw["files"]) != "[]" {
		t.Errorf("files = %s, want []", raw["files"])
	}
}

// TestChatFilesDropsHostilePathsSilently: a row that cannot cross a JSON wire
// honestly is absent, and the count of them reaches the daemon log and not the
// body (task 126 decision 39).
func TestChatFilesDropsHostilePathsSilently(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows filenames cannot carry a control character or invalid UTF-8")
	}
	h := newFilesHarness(t)
	dir := filesRepo(t)
	var hostile []string
	for _, name := range []string{"new\nline.txt", "bad\xff.txt"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x\n"), 0o644); err != nil {
			t.Logf("this filesystem refuses %q (%v); not asserting on it", name, err)
			continue
		}
		hostile = append(hostile, name)
	}
	if len(hostile) == 0 {
		t.Skip("this filesystem hosts neither a control character nor invalid UTF-8 in a filename")
	}
	c := h.chat(t, "claude", chatstate.Idle, dir)

	code, body := h.get(t, c.ID, "")
	if code != http.StatusOK {
		t.Fatalf("GET files = %d, want 200 (%s)", code, body)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatalf("files body: %v (%s)", err, body)
	}
	// The whole key set, so a `dropped` field cannot be added without this
	// failing: the number is the operator's, not the client's.
	want := []string{
		"agent", "chat_id", "files", "mention_expands", "mention_position",
		"mention_sigil", "truncated", "work_dir",
	}
	keys := make([]string, 0, len(raw))
	for k := range raw {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	if !slices.Equal(keys, want) {
		t.Errorf("body keys = %q, want %q", keys, want)
	}
	var got chatFilesBody
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("files body: %v", err)
	}
	if !slices.Equal(paths(got), []string{"README.md"}) {
		t.Errorf("files = %q, want only the drawable row", paths(got))
	}
}
