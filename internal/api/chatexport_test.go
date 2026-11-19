package api

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/agent"
	"github.com/lezli01/vincent/internal/agent/claude"
	"github.com/lezli01/vincent/internal/agent/copilot"
	"github.com/lezli01/vincent/internal/chatexport"
	"github.com/lezli01/vincent/internal/chatrun"
	"github.com/lezli01/vincent/internal/chatstate"
	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/gitx"
	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/store/storetest"
	"github.com/lezli01/vincent/internal/testrepo"
)

// exportHarness is GET /v1/chats/{id}/export over the real handler, a real
// store and real git (task 156). Chats and turns are written straight into
// the store, and transcripts straight onto disk, so each case is exactly the
// row and file it names — no agent ever runs.
type exportHarness struct {
	*projectHarness
	projectID int64
	dataDir   string
	repo      string
}

func newExportHarness(t *testing.T) *exportHarness {
	t.Helper()
	dataDir := t.TempDir()
	st, err := storetest.Open(filepath.Join(dataDir, "export.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	repo := testrepo.Init(t, "main")
	project := &store.Project{Name: "proj", Path: repo, DefaultBranch: "main"}
	if err := st.CreateProject(t.Context(), project); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	reg := agent.NewRegistry(
		claude.New(func() string { return "claude" }),
		copilot.New(func() string { return "copilot" }),
	)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	deps := Deps{
		Token: testToken, Config: config.Default, StartedAt: time.Now(), ListenAddr: "127.0.0.1:0",
		RequestStop: func() {}, Logger: log, Store: st, Agents: reg, Git: gitx.New(),
		Catalog: agent.NewCatalogCache(reg), Dirs: config.Dirs{Data: dataDir},
	}
	ts := httptest.NewServer(New(deps).Handler())
	t.Cleanup(ts.Close)
	return &exportHarness{
		projectHarness: &projectHarness{ts: ts, store: st},
		projectID:      project.ID, dataDir: dataDir, repo: repo,
	}
}

func (h *exportHarness) chat(t *testing.T, c *store.Chat) *store.Chat {
	t.Helper()
	c.ProjectID = h.projectID
	if c.Title == "" {
		c.Title = "Export me"
	}
	if c.Agent == "" {
		c.Agent = "claude"
	}
	if c.State == "" {
		c.State = chatstate.Idle
	}
	c.PermissionMode = string(agent.FullAuto)
	if c.BaseBranch == "" {
		c.BaseBranch = "main"
	}
	if err := h.store.CreateChat(t.Context(), c); err != nil {
		t.Fatalf("CreateChat: %v", err)
	}
	return c
}

// turn records a finished turn and, when transcript is non-empty, copies that
// file in as the turn's transcript.
func (h *exportHarness) turn(t *testing.T, c *store.Chat, prompt string, edit func(*store.ChatTurn), transcript string) {
	t.Helper()
	ct, err := h.store.CreateChatTurn(t.Context(), c.ID, prompt)
	if err != nil {
		t.Fatalf("CreateChatTurn: %v", err)
	}
	ct.State = chatstate.TurnDone
	edit(ct)
	if err := h.store.UpdateChatTurn(t.Context(), ct); err != nil {
		t.Fatalf("UpdateChatTurn: %v", err)
	}
	// The turn took the chat to running; it ended, so the chat is idle again.
	if _, err := h.store.SetChatState(t.Context(), c.ID, chatstate.Idle); err != nil {
		t.Fatalf("SetChatState: %v", err)
	}
	if transcript == "" {
		return
	}
	data, err := os.ReadFile(transcript)
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{deps: Deps{Dirs: config.Dirs{Data: h.dataDir}}}
	path := s.chatTurnTranscriptPath(c.ID, ct.Seq)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func (h *exportHarness) export(t *testing.T, id int64, query string) (*http.Response, string) {
	t.Helper()
	resp, body := h.doJSON(t, http.MethodGet, fmt.Sprintf("/v1/chats/%d/export%s", id, query), nil)
	return resp, string(body)
}

func (h *exportHarness) exportJSON(t *testing.T, id int64, query string) chatexport.Export {
	t.Helper()
	resp, body := h.export(t, id, "?format=json"+query)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("export = %d %s", resp.StatusCode, body)
	}
	var e chatexport.Export
	if err := json.Unmarshal([]byte(body), &e); err != nil {
		t.Fatalf("decode export: %v (%s)", err, body)
	}
	return e
}

func ptrTo[T any](v T) *T { return &v }

func TestChatExportFormatsAndHeaders(t *testing.T) {
	h := newExportHarness(t)
	c := h.chat(t, &store.Chat{Title: "Fix the parser"})
	h.turn(t, c, "edit the file", func(ct *store.ChatTurn) {
		ct.ResultText = "Done, **edited**."
		ct.InputTokens, ct.OutputTokens, ct.CostUSD = 10, 20, ptrTo(0.5)
	}, filepath.Join("..", "agent", "claude", "testdata", "stream_edit_2.1.268.jsonl"))

	for _, c2 := range []struct{ query, ctype, ext string }{
		{"", "text/markdown; charset=utf-8", "md"},
		{"?format=md", "text/markdown; charset=utf-8", "md"},
		{"?format=html", "text/html; charset=utf-8", "html"},
		{"?format=json", "application/json", "json"},
	} {
		resp, body := h.export(t, c.ID, c2.query)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s = %d %s", c2.query, resp.StatusCode, body)
		}
		if got := resp.Header.Get("Content-Type"); got != c2.ctype {
			t.Errorf("%s Content-Type = %q, want %q", c2.query, got, c2.ctype)
		}
		_, params, err := mime.ParseMediaType(resp.Header.Get("Content-Disposition"))
		if want := fmt.Sprintf("chat-%d-fix-the-parser.%s", c.ID, c2.ext); err != nil || params["filename"] != want {
			t.Errorf("%s Content-Disposition = %q, want filename %q", c2.query, resp.Header.Get("Content-Disposition"), want)
		}
	}
	// The tool calls come from the captured claude transcript, through the
	// normalizer the transcript route serves.
	e := h.exportJSON(t, c.ID, "")
	if e.Version != 1 || len(e.Turns) != 1 {
		t.Fatalf("export = %+v", e)
	}
	turn := e.Turns[0]
	if len(turn.Tools) == 0 || turn.ToolsNote != "" {
		t.Fatalf("turn tools = %+v note %q, want the captured calls", turn.Tools, turn.ToolsNote)
	}
	if turn.Answer != "Done, **edited**." || turn.CostUSD == nil || *turn.CostUSD != 0.5 {
		t.Errorf("turn = %+v", turn)
	}
	if e.Chat.Project != "proj" || e.Diff != nil || e.DiffUnavailable != "" {
		t.Errorf("chat = %+v diff %v %q", e.Chat, e.Diff, e.DiffUnavailable)
	}
}

func TestChatExportRefusals(t *testing.T) {
	h := newExportHarness(t)
	// A bad format is a 400 before the chat is looked up: chat 999 does not
	// exist, and the answer is still about the parameter.
	for _, q := range []string{"?format=pdf", "?diff=yes"} {
		resp, body := h.export(t, 999, q)
		if resp.StatusCode != http.StatusBadRequest || !strings.Contains(body, CodeValidationFailed) {
			t.Errorf("%s = %d %s, want 400", q, resp.StatusCode, body)
		}
	}
	if resp, body := h.export(t, 999, ""); resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown chat = %d %s, want 404", resp.StatusCode, body)
	}
}

func TestChatExportMissingTranscripts(t *testing.T) {
	h := newExportHarness(t)
	c := h.chat(t, &store.Chat{})
	h.turn(t, c, "pruned", func(ct *store.ChatTurn) { ct.ResultText = "an answer" }, "")
	h.turn(t, c, "never ran", func(ct *store.ChatTurn) {
		ct.State, ct.FailReason, ct.ErrorMessage = chatstate.TurnFailed, chatrun.ReasonAgentUnavailable, "claude not found"
	}, "")
	// Archived after its turns: an archived chat takes no new ones, and it
	// still exports.
	if _, err := h.store.SetChatState(t.Context(), c.ID, chatstate.Archived); err != nil {
		t.Fatalf("archive: %v", err)
	}
	e := h.exportJSON(t, c.ID, "")
	if e.Chat.State != string(chatstate.Archived) {
		t.Errorf("state = %q", e.Chat.State)
	}
	if e.Turns[0].ToolsNote != chatexport.NotePruned || e.Turns[0].Answer != "an answer" {
		t.Errorf("pruned turn = %+v", e.Turns[0])
	}
	if e.Turns[1].ToolsNote != chatexport.NoteNeverWritten || e.Turns[1].FailReason != chatrun.ReasonAgentUnavailable {
		t.Errorf("unavailable turn = %+v", e.Turns[1])
	}
}

func TestChatExportCopilotCostIsAbsent(t *testing.T) {
	h := newExportHarness(t)
	c := h.chat(t, &store.Chat{Agent: "copilot"})
	h.turn(t, c, "hi", func(ct *store.ChatTurn) { ct.ResultText = "hello" }, "")
	_, md := h.export(t, c.ID, "")
	if strings.Contains(md, "$") {
		t.Errorf("copilot export names a cost:\n%s", md)
	}
}

// worktreeWithChanges is a worktree of the harness repo on its own branch
// with one commit past main and one dirty tracked file.
func (h *exportHarness) worktreeWithChanges(t *testing.T, name string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), name)
	testrepo.Run(t, h.repo, "worktree", "add", "-b", name, dir, "main")
	if err := os.WriteFile(filepath.Join(dir, "committed.txt"), []byte("committed line\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	testrepo.Run(t, dir, "add", "committed.txt")
	testrepo.Run(t, dir, "commit", "-m", "a commit")
	// Dirty README.md, the tracked file testrepo.Init commits.
	f, err := os.OpenFile(filepath.Join(dir, "README.md"), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString("dirty line\n")
	_ = f.Close()
	return dir
}

func TestChatExportDiffOfAFreeChat(t *testing.T) {
	h := newExportHarness(t)
	dir := h.worktreeWithChanges(t, "free")
	c := h.chat(t, &store.Chat{WorktreePath: dir})
	e := h.exportJSON(t, c.ID, "&diff=true")
	if e.Diff == nil {
		t.Fatalf("no diff: unavailable %q", e.DiffUnavailable)
	}
	for _, want := range []string{"+committed line", "+dirty line"} {
		if !strings.Contains(*e.Diff, want) {
			t.Errorf("diff lacks %q:\n%s", want, *e.Diff)
		}
	}
	_, md := h.export(t, c.ID, "?diff=true")
	if !strings.Contains(md, "```diff\n") || !strings.Contains(md, "+dirty line") {
		t.Errorf("markdown lacks the diff block:\n%s", md)
	}
}

func TestChatExportDiffOfALinkedChatIsTheTasks(t *testing.T) {
	h := newExportHarness(t)
	dir := h.worktreeWithChanges(t, "linked")
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
	e := h.exportJSON(t, c.ID, "&diff=true")
	if e.Diff == nil || !strings.Contains(*e.Diff, "+committed line") {
		t.Fatalf("linked diff = %v unavailable %q", e.Diff, e.DiffUnavailable)
	}
	if e.Chat.LinkedTaskID == nil || *e.Chat.LinkedTaskID != task.ID {
		t.Errorf("linked_task_id = %v", e.Chat.LinkedTaskID)
	}
	// And it is byte for byte the task's own diff route.
	_, taskDiff := h.doJSON(t, http.MethodGet, fmt.Sprintf("/v1/tasks/%d/diff", task.ID), nil)
	if string(taskDiff) != *e.Diff {
		t.Errorf("export diff differs from the task diff:\nexport:\n%s\ntask:\n%s", *e.Diff, taskDiff)
	}
}

func TestChatExportDiffUnavailableStillExports(t *testing.T) {
	h := newExportHarness(t)
	gone := filepath.Join(t.TempDir(), "gone")
	for name, c := range map[string]*store.Chat{
		"handed off":  {State: chatstate.HandedOff},
		"no worktree": {State: chatstate.Archived},
		"missing dir": {State: chatstate.Closed, WorktreePath: gone},
	} {
		c = h.chat(t, c)
		resp, body := h.export(t, c.ID, "?format=json&diff=true")
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s: export = %d %s", name, resp.StatusCode, body)
		}
		var raw map[string]any
		if err := json.Unmarshal([]byte(body), &raw); err != nil {
			t.Fatal(err)
		}
		if v, ok := raw["diff"]; !ok || v != nil {
			t.Errorf("%s: diff = %v, want null", name, v)
		}
		if s, _ := raw["diff_unavailable"].(string); s == "" {
			t.Errorf("%s: no diff_unavailable reason: %s", name, body)
		}
		_, md := h.export(t, c.ID, "?diff=true")
		if !strings.Contains(md, "diff unavailable: ") {
			t.Errorf("%s: markdown lacks the note", name)
		}
	}
}
