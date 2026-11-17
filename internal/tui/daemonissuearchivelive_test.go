package tui

import (
	"io"
	"log/slog"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/api"
	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/issuestate"
	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/store/storetest"
)

// TestDaemonViewRendersAutoArchiveHoldsFromTheRealDoctor is #986's live
// test: a closed issue whose done root is held because a second root is
// still queued is computed by the real /v1/doctor handler, decoded by the
// real client and rendered by the daemon view — issue, task and every
// reason. With archive_tasks_on_issue_close off the block is gone, heading
// and all.
func TestDaemonViewRendersAutoArchiveHoldsFromTheRealDoctor(t *testing.T) {
	const token = "issue-archive-live-token"
	dirs := config.Dirs{Config: t.TempDir(), Data: t.TempDir()}
	st, err := storetest.Open(filepath.Join(dirs.Data, "vincent.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := t.Context()

	p := &store.Project{Name: "p", Path: t.TempDir(), DefaultBranch: "main"}
	if err := st.CreateProject(ctx, p); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	iss, err := st.CreateIssue(ctx, store.NewIssue{ProjectID: p.ID, Title: "an issue"}, issuestate.Human)
	if err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}
	newTask := func(title, branch string) *store.Task {
		t.Helper()
		task := &store.Task{
			ProjectID: p.ID, Title: title, WorkflowName: "adhoc", WorkflowSnapshot: "steps: []",
			BaseBranch: "main", BranchName: branch, State: store.TaskQueued, IssueID: &iss.ID,
		}
		if err := st.CreateTask(ctx, task, nil); err != nil {
			t.Fatalf("CreateTask: %v", err)
		}
		return task
	}
	done := newTask("the finished work", "vincent/finished")
	for _, step := range [][2]store.TaskState{{store.TaskQueued, store.TaskRunning}, {store.TaskRunning, store.TaskDone}} {
		if _, _, err := st.TransitionTask(ctx, done.ID, step[0], step[1], store.TaskChange{}); err != nil {
			t.Fatalf("→ %s: %v", step[1], err)
		}
	}
	// The second root is unsettled, so the issue is not quiescent.
	newTask("the unsettled work", "vincent/unsettled")
	if _, err := st.TransitionIssue(ctx, iss.ID, issuestate.Close, issuestate.Completed, nil, issuestate.Human); err != nil {
		t.Fatalf("close: %v", err)
	}

	var archiveOn atomic.Bool
	archiveOn.Store(true)
	srv := api.New(api.Deps{
		Token: token,
		Config: func() config.Config {
			cfg := config.Default()
			cfg.ArchiveTasksOnIssueClose = archiveOn.Load()
			return cfg
		},
		StartedAt:   time.Now().Add(-time.Minute),
		ListenAddr:  "127.0.0.1:0",
		Dirs:        dirs,
		RequestStop: func() {},
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		Store:       st,
	})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	d := newTestDaemonView(nil, nil)
	d.client = apiclient.New(ts.URL, token)
	fetch := func() string {
		t.Helper()
		msg, ok := d.doctorCmd()().(daemonDoctorMsg)
		if !ok {
			t.Fatal("doctorCmd did not produce a daemonDoctorMsg")
		}
		if msg.err != nil {
			t.Fatalf("GET /v1/doctor: %v", msg.err)
		}
		d.update(msg)
		return ansi.Strip(renderDaemon(d))
	}

	out := fetch()
	held := d.doctor.IssueArchive.Held
	if len(held) != 1 || held[0].TaskID != done.ID {
		t.Fatalf("the doctor report holds %+v, want task %d", held, done.ID)
	}
	if !strings.Contains(out, "auto-archive held") {
		t.Fatalf("the block is missing:\n%s", out)
	}
	row := lineWith(out, "task #"+strconv.FormatInt(done.ID, 10))
	want := []string{"issue #" + strconv.FormatInt(iss.ID, 10), "done", "the finished work"}
	want = append(want, held[0].Reasons...)
	for _, w := range want {
		if !strings.Contains(row, w) {
			t.Errorf("the held row does not show %q: %q", w, row)
		}
	}
	if !strings.Contains(row, "issue_not_quiescent") {
		t.Errorf("the unsettled second root did not hold the task: %q", row)
	}

	archiveOn.Store(false)
	out = fetch()
	if strings.Contains(out, "auto-archive held") || strings.Contains(out, "the finished work") {
		t.Errorf("with the key off the block still renders:\n%s", out)
	}
}
