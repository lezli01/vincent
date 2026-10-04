package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/store"
)

// projectKeys is the default project shape: GET /v1/projects without
// `stats` must serve exactly these keys, so the opt-in costs a caller who
// never asks for it nothing (task 132.1).
var projectKeys = []string{
	"branch_template", "created_at", "default_branch", "default_workflow", "id",
	"max_parallel_tasks", "name", "path", "slots_used", "updated_at",
}

func objectKeys(t *testing.T, raw []byte) []string {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("decode object: %v (%s)", err, raw)
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// TestProjectStatsAreOptIn: without `stats`, and with stats=false, list and
// get answer the same bytes with today's keys and no `stats`.
func TestProjectStatsAreOptIn(t *testing.T) {
	h := newTaskHarness(t, 0, false)
	seedSlotFixture(t, h)
	for _, path := range []string{"/v1/projects", "/v1/projects/" + itoa(h.projectID)} {
		_, plain := h.doJSON(t, http.MethodGet, path, nil)
		resp, off := h.doJSON(t, http.MethodGet, path+"?stats=false", nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET %s?stats=false: %d %s", path, resp.StatusCode, off)
		}
		if !bytes.Equal(plain, off) {
			t.Errorf("GET %s: stats=false differs from no parameter:\n%s\n%s", path, plain, off)
		}
		row := plain
		if strings.HasPrefix(string(bytes.TrimSpace(plain)), "[") {
			var list []json.RawMessage
			if err := json.Unmarshal(plain, &list); err != nil || len(list) != 1 {
				t.Fatalf("decode list: %v (%s)", err, plain)
			}
			row = list[0]
		}
		if got := objectKeys(t, row); !reflect.DeepEqual(got, projectKeys) {
			t.Errorf("GET %s keys = %v, want %v", path, got, projectKeys)
		}
	}
}

func TestProjectStatsRejectsJunk(t *testing.T) {
	h := newTaskHarness(t, 0, false)
	for _, path := range []string{"/v1/projects", "/v1/projects/" + itoa(h.projectID)} {
		for _, v := range []string{"yes", "1", "TRUE"} {
			resp, body := h.doJSON(t, http.MethodGet, path+"?stats="+v, nil)
			if resp.StatusCode != http.StatusBadRequest || !strings.Contains(string(body), CodeValidationFailed) {
				t.Errorf("GET %s?stats=%s = %d %s, want 400 %s", path, v, resp.StatusCode, body, CodeValidationFailed)
			}
		}
	}
}

// TestProjectStatsFigures runs the #324 fixture — a fan-out parent with a
// running lane, a root asking a question, a queued root — plus a blocked
// lane and an archived root through ?stats=true on both routes.
func TestProjectStatsFigures(t *testing.T) {
	h := newTaskHarness(t, 0, false)
	seedSlotFixture(t, h)
	ctx := t.Context()
	tasks, err := h.store.ListTasks(ctx, store.TaskFilter{})
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	var parentID int64
	for _, task := range tasks {
		if task.Title == "parent" {
			parentID = task.ID
		}
	}
	idx := 0
	for _, task := range []*store.Task{
		{Title: "lane-blocked", State: store.TaskBlocked, ParentTaskID: &parentID, ParentStepIndex: &idx, LaneID: "lane-blocked"},
		{Title: "root-archived", State: store.TaskArchived},
	} {
		task.ProjectID, task.WorkflowName, task.WorkflowSnapshot = h.projectID, "adhoc", "steps: []"
		task.BaseBranch, task.BranchName = "main", "vincent/"+task.Title
		if err := h.store.CreateTask(ctx, task, nil); err != nil {
			t.Fatalf("CreateTask(%s): %v", task.Title, err)
		}
	}

	type statsBody struct {
		SlotsUsed int `json:"slots_used"`
		Stats     *struct {
			Tasks struct {
				ByState   map[string]int `json:"by_state"`
				Active    int            `json:"active"`
				Attention int            `json:"attention"`
			} `json:"tasks"`
			Issues    map[string]int  `json:"issues"`
			Chats     map[string]int  `json:"chats"`
			IssueSync map[string]any  `json:"issue_sync"`
			Last      json.RawMessage `json:"last_activity_at"`
		} `json:"stats"`
	}
	check := func(label string, b statsBody) {
		t.Helper()
		if b.Stats == nil {
			t.Fatalf("%s: stats is null", label)
		}
		want := map[string]int{"awaiting_children": 1, "running": 1, "awaiting_input": 1, "queued": 1, "blocked": 1}
		if !reflect.DeepEqual(b.Stats.Tasks.ByState, want) {
			t.Errorf("%s: by_state = %v, want %v", label, b.Stats.Tasks.ByState, want)
		}
		if b.Stats.Tasks.Active != 5 || b.Stats.Tasks.Attention != 2 {
			t.Errorf("%s: active %d attention %d, want 5 2", label, b.Stats.Tasks.Active, b.Stats.Tasks.Attention)
		}
		if b.SlotsUsed != 2 {
			t.Errorf("%s: slots_used = %d, want 2", label, b.SlotsUsed)
		}
		if b.Stats.Issues["open"] != 0 || b.Stats.Chats["live"] != 0 {
			t.Errorf("%s: issues %v chats %v, want zeros", label, b.Stats.Issues, b.Stats.Chats)
		}
		if _, ok := b.Stats.IssueSync["enabled"]; !ok {
			t.Errorf("%s: issue_sync has no enabled: %v", label, b.Stats.IssueSync)
		}
		if string(b.Stats.Last) == "null" {
			t.Errorf("%s: last_activity_at is null for a project with tasks", label)
		}
	}

	_, body := h.doJSON(t, http.MethodGet, "/v1/projects?stats=true", nil)
	var list []statsBody
	if err := json.Unmarshal(body, &list); err != nil || len(list) != 1 {
		t.Fatalf("decode list: %v (%s)", err, body)
	}
	check("list", list[0])

	_, body = h.doJSON(t, http.MethodGet, "/v1/projects/"+itoa(h.projectID)+"?stats=true", nil)
	var one statsBody
	if err := json.Unmarshal(body, &one); err != nil {
		t.Fatalf("decode get: %v (%s)", err, body)
	}
	check("get", one)
}

// TestProjectStatsEmptyProject: a project with nothing gives zeros, an empty
// by_state object and a null last_activity_at — never a missing key.
func TestProjectStatsEmptyProject(t *testing.T) {
	h := newTaskHarness(t, 0, false)
	_, body := h.doJSON(t, http.MethodGet, "/v1/projects/"+itoa(h.projectID)+"?stats=true", nil)
	var got struct {
		Stats map[string]json.RawMessage `json:"stats"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decode: %v (%s)", err, body)
	}
	for key, want := range map[string]string{
		"tasks":            `{"by_state":{},"active":0,"attention":0}`,
		"issues":           `{"open":0,"open_imported":0,"active":0}`,
		"chats":            `{"live":0,"awaiting_input":0}`,
		"last_activity_at": `null`,
	} {
		if string(got.Stats[key]) != want {
			t.Errorf("stats.%s = %s, want %s", key, got.Stats[key], want)
		}
	}
}

// syncBuffer is a log sink the handler goroutines and the test may share.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// TestProjectStatsDegradeToNull: a failed count is `"stats": null` with a
// warning in the log and a 200, never a 500 — the slots_used precedent.
func TestProjectStatsDegradeToNull(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	p := &store.Project{Name: "p", Path: "/p", DefaultBranch: "main"}
	if err := st.CreateProject(t.Context(), p); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	logs := &syncBuffer{}
	s := New(Deps{
		Token: testToken, Config: config.Default, StartedAt: time.Now(), ListenAddr: "127.0.0.1:0",
		RequestStop: func() {}, Logger: slog.New(slog.NewTextHandler(logs, nil)), Store: st,
	})
	s.statsSource = func(context.Context) (map[int64]store.ProjectStats, error) {
		return nil, errors.New("disk on fire")
	}
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	h := &projectHarness{ts: ts, store: st}

	for _, path := range []string{"/v1/projects?stats=true", "/v1/projects/" + itoa(p.ID) + "?stats=true"} {
		resp, body := h.doJSON(t, http.MethodGet, path, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET %s = %d %s, want 200", path, resp.StatusCode, body)
		}
		if !strings.Contains(string(body), `"stats":null`) {
			t.Errorf("GET %s = %s, want stats null", path, body)
		}
	}
	if !strings.Contains(logs.String(), "disk on fire") {
		t.Errorf("no warning logged: %q", logs.String())
	}
}
