package api

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/gitx"
	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/store/storetest"
	"github.com/lezli01/vincent/internal/testrepo"
	"github.com/lezli01/vincent/internal/worktree"
)

// deliveryServer is an API over a real repository, so the delivery routes'
// git questions — the branch tip, a head_sha on the branch — are asked of
// git itself.
type deliveryServer struct {
	ts      *httptest.Server
	st      *store.Store
	repo    string
	project *store.Project
}

func newDeliveryServer(t *testing.T) *deliveryServer {
	t.Helper()
	st, err := storetest.Open(filepath.Join(t.TempDir(), "vincent.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	repo := testrepo.Init(t, "main")
	project := &store.Project{Name: "p", Path: repo, DefaultBranch: "main"}
	if err := st.CreateProject(t.Context(), project); err != nil {
		t.Fatal(err)
	}
	git := gitx.New()
	s := New(Deps{
		Token: testToken, Config: config.Default, StartedAt: time.Now(), ListenAddr: "127.0.0.1:0",
		RequestStop: func() {}, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Store: st, Git: git, Worktrees: worktree.NewManager(git, t.TempDir()),
	})
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return &deliveryServer{ts: ts, st: st, repo: repo, project: project}
}

// task creates a task in state on a branch cut from main.
func (d *deliveryServer) task(t *testing.T, branch string, state store.TaskState) *store.Task {
	t.Helper()
	testrepo.Run(t, d.repo, "branch", branch, "main")
	task := &store.Task{
		ProjectID: d.project.ID, Title: "t " + branch, WorkflowName: "test", WorkflowSnapshot: "steps: []",
		BaseBranch: "main", BranchName: branch, State: state,
	}
	if err := d.st.CreateTask(t.Context(), task, nil); err != nil {
		t.Fatal(err)
	}
	return task
}

func (d *deliveryServer) url(path string, args ...any) string {
	return d.ts.URL + fmt.Sprintf(path, args...)
}

func decodeDelivery(t *testing.T, resp *http.Response) taskDeliveryResponse {
	t.Helper()
	var out taskDeliveryResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return out
}

func readErrorCode(t *testing.T, resp *http.Response) (string, map[string]string) {
	t.Helper()
	var e errorBody
	if err := json.NewDecoder(resp.Body).Decode(&e); err != nil {
		t.Fatalf("decode error: %v", err)
	}
	return e.Error.Code, e.Error.Details
}

// TestTaskDeliveryRoutes: GET answers null before a delivery exists; POST
// records one on a running task with the branch, base and tip filled in by
// the daemon, and GET then reads it back in full.
func TestTaskDeliveryRoutes(t *testing.T) {
	d := newDeliveryServer(t)
	task := d.task(t, "work", store.TaskRunning)
	resp := doJSON(t, http.MethodGet, d.url("/v1/tasks/%d/delivery", task.ID), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET = %d", resp.StatusCode)
	}
	if got := decodeDelivery(t, resp); got.TaskID != task.ID || got.Delivery != nil {
		t.Fatalf("GET before any delivery = %+v", got)
	}
	resp = doJSON(t, http.MethodPost, d.url("/v1/tasks/%d/delivery", task.ID), map[string]string{
		"title": "Fix it", "body": "## What\nFixed.", "summary": "brief", "proof": "go test ./...",
	})
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("POST = %d: %s", resp.StatusCode, b)
	}
	tip := strings.TrimSpace(testrepo.Run(t, d.repo, "rev-parse", "refs/heads/work"))
	got := decodeDelivery(t, resp).Delivery
	if got == nil || got.Kind != "change" || got.Branch != "work" || got.Base != "main" || got.HeadSHA != tip ||
		got.Body != "## What\nFixed." || got.Implicit {
		t.Fatalf("POST delivery = %+v, want a change on work @ %s", got, tip)
	}
	resp = doJSON(t, http.MethodGet, d.url("/v1/tasks/%d/delivery", task.ID), nil)
	if back := decodeDelivery(t, resp).Delivery; back == nil || back.Summary != "brief" || back.Proof != "go test ./..." {
		t.Fatalf("GET after POST = %+v", back)
	}
}

// TestTaskDeliveryRefusals: a task neither running nor done is 409 with its
// state; an unknown kind or close reason, and a head_sha off the task's
// branch, are 400.
func TestTaskDeliveryRefusals(t *testing.T) {
	d := newDeliveryServer(t)
	queued := d.task(t, "queued", store.TaskQueued)
	resp := doJSON(t, http.MethodPost, d.url("/v1/tasks/%d/delivery", queued.ID), map[string]string{"title": "x"})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("POST on queued = %d, want 409", resp.StatusCode)
	}
	if code, details := readErrorCode(t, resp); code != CodeInvalidState || details["state"] != "queued" {
		t.Fatalf("409 = %s %v, want invalid_state with state queued", code, details)
	}
	done := d.task(t, "done", store.TaskDone)
	testrepo.Run(t, d.repo, "checkout", "-q", "-b", "elsewhere", "main")
	testrepo.WriteFile(t, d.repo, "x.txt", "x\n")
	testrepo.Run(t, d.repo, "add", "x.txt")
	testrepo.Run(t, d.repo, "commit", "-q", "-m", "elsewhere")
	testrepo.Run(t, d.repo, "checkout", "-q", "main")
	offBranch := strings.TrimSpace(testrepo.Run(t, d.repo, "rev-parse", "refs/heads/elsewhere"))
	for name, body := range map[string]map[string]string{
		"kind":         {"kind": "patch"},
		"close_reason": {"close_reason": "fixed"},
		"off branch":   {"head_sha": offBranch},
		"not a commit": {"head_sha": "deadbeef"},
		"option":       {"head_sha": "--all"},
	} {
		resp := doJSON(t, http.MethodPost, d.url("/v1/tasks/%d/delivery", done.ID), body)
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: POST = %d, want 400", name, resp.StatusCode)
		}
	}
	onBranch := strings.TrimSpace(testrepo.Run(t, d.repo, "rev-parse", "refs/heads/done"))
	resp = doJSON(t, http.MethodPost, d.url("/v1/tasks/%d/delivery", done.ID), map[string]string{
		"kind": "reply", "head_sha": onBranch[:10], "close_reason": "not_planned",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST on done with a short head on the branch = %d", resp.StatusCode)
	}
	if got := decodeDelivery(t, resp).Delivery; got.HeadSHA != onBranch || got.Kind != "reply" {
		t.Fatalf("delivery = %+v, want the full sha %s", got, onBranch)
	}
}

// TestTaskDeliveryOnTaskResponses: the list carries a delivery's summary and
// the detail carries it in full, as they carry the workflow snapshot.
func TestTaskDeliveryOnTaskResponses(t *testing.T) {
	d := newDeliveryServer(t)
	task := d.task(t, "shown", store.TaskDone)
	if _, err := d.st.SetTaskDelivery(t.Context(), task.ID, &store.Delivery{
		Kind: store.DeliveryChange, Title: "Shown", Body: "long body", Summary: "brief", RecordedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	var list []taskResponse
	if err := json.NewDecoder(doJSON(t, http.MethodGet, d.url("/v1/tasks"), nil).Body).Decode(&list); err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Delivery == nil || list[0].Delivery.Title != "Shown" ||
		list[0].Delivery.Body != "" || list[0].Delivery.Summary != "" {
		t.Fatalf("list delivery = %+v, want the summary without its long fields", list[0].Delivery)
	}
	var detail taskResponse
	if err := json.NewDecoder(doJSON(t, http.MethodGet, d.url("/v1/tasks/%d", task.ID), nil).Body).Decode(&detail); err != nil {
		t.Fatal(err)
	}
	if detail.Delivery == nil || detail.Delivery.Body != "long body" || detail.Delivery.Summary != "brief" {
		t.Fatalf("detail delivery = %+v, want it in full", detail.Delivery)
	}
}
