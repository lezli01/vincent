package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/gitx"
	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/testrepo"
	"github.com/lezli01/vincent/internal/worktree"
)

// projectHarness is a test server with real store, git, and worktree deps.
type projectHarness struct {
	ts    *httptest.Server
	store *store.Store
	wt    *worktree.Manager
}

func newProjectHarness(t *testing.T) *projectHarness {
	t.Helper()
	return newProjectHarnessWithConfig(t, config.Default)
}

// newProjectHarnessWithConfig is the same server with the daemon configuration
// a test wants — the §10 branch sweep is the first thing here to read one.
func newProjectHarnessWithConfig(t *testing.T, cfg func() config.Config) *projectHarness {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	git := gitx.New()
	wt := worktree.NewManager(git, t.TempDir())
	s := New(Deps{
		Token:       testToken,
		Config:      cfg,
		StartedAt:   time.Now(),
		ListenAddr:  "127.0.0.1:0",
		RequestStop: func() {},
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		Store:       st,
		Git:         git,
		Worktrees:   wt,
	})
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return &projectHarness{ts: ts, store: st, wt: wt}
}

// doJSON sends an authenticated request with an optional JSON body.
func (h *projectHarness) doJSON(t *testing.T, method, path string, body any) (*http.Response, []byte) {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, h.ts.URL+path, rd)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+testToken)
	resp, err := h.ts.Client().Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	out, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return resp, out
}

func decodeProject(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("project body not JSON: %v (%s)", err, body)
	}
	return m
}

func (h *projectHarness) mustCreate(t *testing.T, body any) map[string]any {
	t.Helper()
	resp, out := h.doJSON(t, http.MethodPost, "/v1/projects", body)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create project: status %d, body %s", resp.StatusCode, out)
	}
	return decodeProject(t, out)
}

func TestProjectCreateHappyAndGet(t *testing.T) {
	repo := testrepo.Init(t, "main")
	h := newProjectHarness(t)

	p := h.mustCreate(t, map[string]any{"path": repo})
	if p["name"] != filepath.Base(repo) {
		t.Errorf("name = %v, want repo dir name %q", p["name"], filepath.Base(repo))
	}
	if p["default_branch"] != "main" {
		t.Errorf("default_branch = %v, want main", p["default_branch"])
	}
	if p["default_workflow"] != nil || p["max_parallel_tasks"] != nil {
		t.Errorf("optional fields = %v/%v, want null/null", p["default_workflow"], p["max_parallel_tasks"])
	}

	resp, out := h.doJSON(t, http.MethodGet, "/v1/projects", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list: %d %s", resp.StatusCode, out)
	}
	var list []map[string]any
	if err := json.Unmarshal(out, &list); err != nil || len(list) != 1 {
		t.Fatalf("list = %s (err %v), want one project", out, err)
	}
	id := int64(list[0]["id"].(float64))
	resp, out = h.doJSON(t, http.MethodGet, "/v1/projects/"+itoa(id), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get: %d %s", resp.StatusCode, out)
	}

	resp, out = h.doJSON(t, http.MethodGet, "/v1/projects/9999", nil)
	wantError(t, resp, out, http.StatusNotFound, CodeNotFound)
}

func TestProjectCreateExplicitOptions(t *testing.T) {
	repo := testrepo.Init(t, "main")
	testrepo.Run(t, repo, "branch", "dev")
	h := newProjectHarness(t)

	p := h.mustCreate(t, map[string]any{
		"path": repo, "name": "myproj", "default_branch": "dev",
		"default_workflow": "feature-pr", "max_parallel_tasks": 2,
	})
	if p["name"] != "myproj" || p["default_branch"] != "dev" ||
		p["default_workflow"] != "feature-pr" || p["max_parallel_tasks"] != float64(2) {
		t.Errorf("project = %v", p)
	}
}

func TestProjectCreateValidationFailures(t *testing.T) {
	repo := testrepo.Init(t, "main")
	sub := filepath.Join(repo, "subdir")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	bare := t.TempDir()
	testrepo.Run(t, bare, "init", "-q", "--bare", ".")
	linked := filepath.Join(t.TempDir(), "linked")
	testrepo.Run(t, repo, "worktree", "add", "-q", linked)
	plainDir := t.TempDir()
	h := newProjectHarness(t)

	tests := []struct {
		name    string
		body    map[string]any
		wantMsg string
	}{
		{"missing path", map[string]any{}, "path is required"},
		{"relative path", map[string]any{"path": "some/relative"}, "absolute"},
		{"nonexistent", map[string]any{"path": filepath.Join(plainDir, "gone")}, "does not exist"},
		{"not a repo", map[string]any{"path": plainDir}, "not a git repository"},
		{"bare repo", map[string]any{"path": bare}, "bare repository"},
		{"subdirectory", map[string]any{"path": sub}, "toplevel"},
		{"linked worktree", map[string]any{"path": linked}, "linked git worktree"},
		{"branch missing", map[string]any{"path": repo, "default_branch": "nope"}, "does not resolve"},
		{"zero cap", map[string]any{"path": repo, "max_parallel_tasks": 0}, "at least 1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp, out := h.doJSON(t, http.MethodPost, "/v1/projects", tt.body)
			wantError(t, resp, out, http.StatusBadRequest, CodeValidationFailed)
			if !strings.Contains(string(out), tt.wantMsg) {
				t.Errorf("message %s does not mention %q", out, tt.wantMsg)
			}
		})
	}

	t.Run("unknown field", func(t *testing.T) {
		resp, out := h.doJSON(t, http.MethodPost, "/v1/projects",
			map[string]any{"path": repo, "pathh": true})
		wantError(t, resp, out, http.StatusBadRequest, CodeValidationFailed)
	})
	t.Run("malformed json", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodPost, h.ts.URL+"/v1/projects", strings.NewReader("{nope"))
		req.Header.Set("Authorization", "Bearer "+testToken)
		resp, err := h.ts.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		out, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		wantError(t, resp, out, http.StatusBadRequest, CodeInvalidJSON)
	})
}

func TestProjectCreateDuplicate(t *testing.T) {
	repo := testrepo.Init(t, "main")
	h := newProjectHarness(t)
	h.mustCreate(t, map[string]any{"path": repo})

	resp, out := h.doJSON(t, http.MethodPost, "/v1/projects",
		map[string]any{"path": repo, "name": "other-name"})
	wantError(t, resp, out, http.StatusBadRequest, CodeValidationFailed)
	if !strings.Contains(string(out), "already registered") {
		t.Errorf("message %s does not mention the existing registration", out)
	}

	// A differently-spelled alias of the same directory is still a duplicate
	// (identity comparison, T1.5 decision).
	var alias string
	if runtime.GOOS == "windows" {
		alias = strings.ToUpper(repo[:1]) + repo[1:]
		if alias == repo {
			alias = strings.ToLower(repo[:1]) + repo[1:]
		}
	} else {
		alias = filepath.Join(t.TempDir(), "alias")
		if err := os.Symlink(repo, alias); err != nil {
			t.Skipf("symlink: %v", err)
		}
	}
	resp, out = h.doJSON(t, http.MethodPost, "/v1/projects",
		map[string]any{"path": alias, "name": "alias-name"})
	wantError(t, resp, out, http.StatusBadRequest, CodeValidationFailed)
}

func TestProjectCreateNameConflict(t *testing.T) {
	h := newProjectHarness(t)
	h.mustCreate(t, map[string]any{"path": testrepo.Init(t, "main"), "name": "same"})
	resp, out := h.doJSON(t, http.MethodPost, "/v1/projects",
		map[string]any{"path": testrepo.Init(t, "main"), "name": "same"})
	wantError(t, resp, out, http.StatusBadRequest, CodeValidationFailed)
	if !strings.Contains(string(out), "already in use") {
		t.Errorf("message %s does not mention the name conflict", out)
	}
}

func TestDetectDefaultBranch(t *testing.T) {
	h := newProjectHarness(t)
	create := func(t *testing.T, repo string) (map[string]any, *http.Response, []byte) {
		t.Helper()
		resp, out := h.doJSON(t, http.MethodPost, "/v1/projects",
			map[string]any{"path": repo, "name": t.Name()})
		if resp.StatusCode != http.StatusCreated {
			return nil, resp, out
		}
		return decodeProject(t, out), resp, out
	}

	t.Run("origin HEAD with local branch", func(t *testing.T) {
		repo := testrepo.Init(t, "main")
		testrepo.Run(t, repo, "branch", "develop")
		testrepo.Run(t, repo, "update-ref", "refs/remotes/origin/develop", "refs/heads/develop")
		testrepo.Run(t, repo, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/develop")
		p, _, out := create(t, repo)
		if p == nil || p["default_branch"] != "develop" {
			t.Errorf("default_branch = %v (%s), want develop", p, out)
		}
	})
	t.Run("origin HEAD without local branch falls through", func(t *testing.T) {
		repo := testrepo.Init(t, "main")
		testrepo.Run(t, repo, "update-ref", "refs/remotes/origin/develop", "refs/heads/main")
		testrepo.Run(t, repo, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/develop")
		p, _, out := create(t, repo)
		if p == nil || p["default_branch"] != "main" {
			t.Errorf("default_branch = %v (%s), want main", p, out)
		}
	})
	t.Run("master fallback", func(t *testing.T) {
		p, _, out := create(t, testrepo.Init(t, "master"))
		if p == nil || p["default_branch"] != "master" {
			t.Errorf("default_branch = %v (%s), want master", p, out)
		}
	})
	t.Run("current HEAD branch fallback", func(t *testing.T) {
		p, _, out := create(t, testrepo.Init(t, "trunk"))
		if p == nil || p["default_branch"] != "trunk" {
			t.Errorf("default_branch = %v (%s), want trunk", p, out)
		}
	})
	t.Run("detached HEAD rejected", func(t *testing.T) {
		repo := testrepo.Init(t, "trunk")
		testrepo.Run(t, repo, "checkout", "-q", "--detach")
		testrepo.Run(t, repo, "branch", "-D", "trunk")
		_, resp, out := create(t, repo)
		wantError(t, resp, out, http.StatusBadRequest, CodeValidationFailed)
	})
	t.Run("unborn HEAD rejected", func(t *testing.T) {
		_, resp, out := create(t, testrepo.InitEmpty(t, "main"))
		wantError(t, resp, out, http.StatusBadRequest, CodeValidationFailed)
	})
}

func TestProjectPatch(t *testing.T) {
	repo := testrepo.Init(t, "main")
	testrepo.Run(t, repo, "branch", "dev")
	h := newProjectHarness(t)
	p := h.mustCreate(t, map[string]any{"path": repo, "default_workflow": "wf", "max_parallel_tasks": 3})
	id := itoa(int64(p["id"].(float64)))

	t.Run("rename and retarget branch", func(t *testing.T) {
		resp, out := h.doJSON(t, http.MethodPatch, "/v1/projects/"+id,
			map[string]any{"name": "renamed", "default_branch": "dev"})
		got := decodeProject(t, out)
		if resp.StatusCode != http.StatusOK || got["name"] != "renamed" || got["default_branch"] != "dev" {
			t.Errorf("patch = %d %s", resp.StatusCode, out)
		}
	})
	t.Run("null clears optional fields", func(t *testing.T) {
		resp, out := h.doJSON(t, http.MethodPatch, "/v1/projects/"+id,
			map[string]any{"default_workflow": nil, "max_parallel_tasks": nil})
		got := decodeProject(t, out)
		if resp.StatusCode != http.StatusOK || got["default_workflow"] != nil || got["max_parallel_tasks"] != nil {
			t.Errorf("patch = %d %s", resp.StatusCode, out)
		}
	})
	t.Run("null on required field", func(t *testing.T) {
		resp, out := h.doJSON(t, http.MethodPatch, "/v1/projects/"+id, map[string]any{"name": nil})
		wantError(t, resp, out, http.StatusBadRequest, CodeValidationFailed)
	})
	t.Run("missing branch", func(t *testing.T) {
		resp, out := h.doJSON(t, http.MethodPatch, "/v1/projects/"+id,
			map[string]any{"default_branch": "nope"})
		wantError(t, resp, out, http.StatusBadRequest, CodeValidationFailed)
	})
	t.Run("zero cap", func(t *testing.T) {
		resp, out := h.doJSON(t, http.MethodPatch, "/v1/projects/"+id,
			map[string]any{"max_parallel_tasks": 0})
		wantError(t, resp, out, http.StatusBadRequest, CodeValidationFailed)
	})
	t.Run("unknown id", func(t *testing.T) {
		resp, out := h.doJSON(t, http.MethodPatch, "/v1/projects/9999", map[string]any{"name": "x"})
		wantError(t, resp, out, http.StatusNotFound, CodeNotFound)
	})
}

func TestProjectPatchRepoint(t *testing.T) {
	repo := testrepo.Init(t, "main")
	h := newProjectHarness(t)
	p := h.mustCreate(t, map[string]any{"path": repo})
	id := itoa(int64(p["id"].(float64)))

	t.Run("repoint without the stored branch", func(t *testing.T) {
		other := testrepo.Init(t, "trunk")
		resp, out := h.doJSON(t, http.MethodPatch, "/v1/projects/"+id, map[string]any{"path": other})
		wantError(t, resp, out, http.StatusBadRequest, CodeValidationFailed)
		if !strings.Contains(string(out), "default_branch") {
			t.Errorf("message %s should point at default_branch", out)
		}
	})
	t.Run("repoint with branch in same request", func(t *testing.T) {
		other := testrepo.Init(t, "trunk")
		resp, out := h.doJSON(t, http.MethodPatch, "/v1/projects/"+id,
			map[string]any{"path": other, "default_branch": "trunk"})
		got := decodeProject(t, out)
		if resp.StatusCode != http.StatusOK || got["path"] != other || got["default_branch"] != "trunk" {
			t.Errorf("repoint = %d %s", resp.StatusCode, out)
		}
	})
	t.Run("repoint onto an already-registered repo", func(t *testing.T) {
		second := testrepo.Init(t, "main")
		h.mustCreate(t, map[string]any{"path": second, "name": "second"})
		resp, out := h.doJSON(t, http.MethodPatch, "/v1/projects/"+id, map[string]any{"path": second})
		wantError(t, resp, out, http.StatusBadRequest, CodeValidationFailed)
	})
}

func TestProjectDelete(t *testing.T) {
	h := newProjectHarness(t)
	ctx := context.Background()

	t.Run("no tasks", func(t *testing.T) {
		p := h.mustCreate(t, map[string]any{"path": testrepo.Init(t, "main"), "name": "empty"})
		id := itoa(int64(p["id"].(float64)))
		resp, out := h.doJSON(t, http.MethodDelete, "/v1/projects/"+id, nil)
		if resp.StatusCode != http.StatusNoContent {
			t.Fatalf("delete = %d %s", resp.StatusCode, out)
		}
		resp, out = h.doJSON(t, http.MethodGet, "/v1/projects/"+id, nil)
		wantError(t, resp, out, http.StatusNotFound, CodeNotFound)
	})

	t.Run("archived tasks only", func(t *testing.T) {
		p := h.mustCreate(t, map[string]any{"path": testrepo.Init(t, "main"), "name": "archived-only"})
		id := int64(p["id"].(float64))
		insertTask(t, h.store, id, store.TaskArchived)
		resp, out := h.doJSON(t, http.MethodDelete, "/v1/projects/"+itoa(id), nil)
		if resp.StatusCode != http.StatusNoContent {
			t.Fatalf("delete = %d %s", resp.StatusCode, out)
		}
	})

	t.Run("non-archived without force", func(t *testing.T) {
		p := h.mustCreate(t, map[string]any{"path": testrepo.Init(t, "main"), "name": "guarded"})
		id := int64(p["id"].(float64))
		insertTask(t, h.store, id, store.TaskQueued)
		resp, out := h.doJSON(t, http.MethodDelete, "/v1/projects/"+itoa(id), nil)
		wantError(t, resp, out, http.StatusConflict, CodeInvalidState)
	})

	t.Run("force with running task", func(t *testing.T) {
		p := h.mustCreate(t, map[string]any{"path": testrepo.Init(t, "main"), "name": "running"})
		id := int64(p["id"].(float64))
		insertTask(t, h.store, id, store.TaskRunning)
		resp, out := h.doJSON(t, http.MethodDelete, "/v1/projects/"+itoa(id)+"?force=true", nil)
		wantError(t, resp, out, http.StatusConflict, CodeInvalidState)
	})

	t.Run("force archives and removes worktrees", func(t *testing.T) {
		repo := testrepo.Init(t, "main")
		p := h.mustCreate(t, map[string]any{"path": repo, "name": "forced"})
		id := int64(p["id"].(float64))
		taskID := insertTask(t, h.store, id, store.TaskQueued)
		wtPath, err := h.wt.Create(ctx, repo, worktree.TaskOwner(taskID), "vincent/"+itoa(taskID)+"-x", "main", false)
		if err != nil {
			t.Fatalf("worktree create: %v", err)
		}
		testrepo.WriteFile(t, wtPath, "dirty.txt", "uncommitted\n") // dirty: force must still win
		tk, err := h.store.GetTask(ctx, taskID)
		if err != nil {
			t.Fatal(err)
		}
		tk.WorktreePath = wtPath
		if err := h.store.UpdateTask(ctx, tk); err != nil {
			t.Fatal(err)
		}

		resp, out := h.doJSON(t, http.MethodDelete, "/v1/projects/"+itoa(id)+"?force=true", nil)
		if resp.StatusCode != http.StatusNoContent {
			t.Fatalf("forced delete = %d %s", resp.StatusCode, out)
		}
		if _, err := os.Stat(wtPath); !errors.Is(err, os.ErrNotExist) {
			t.Error("worktree dir still exists after forced delete")
		}
		if _, err := h.store.GetTask(ctx, taskID); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("task row survived: %v", err)
		}
		// This row's recorded branch name is not the one the worktree was cut
		// on, so the §10 sweep cannot judge it and leaves it exactly where it
		// is — which is also what every forced delete did before task 008.
		testrepo.Run(t, repo, "rev-parse", "--verify", "refs/heads/vincent/"+itoa(taskID)+"-x")
	})

	// The cascade hard-deletes the rows, so this is the last moment the branch
	// names exist: the sweep covers archived rows too, or their branches become
	// unreachable from anywhere (task 008).
	t.Run("force deletes branches with no commits past their base", func(t *testing.T) {
		repo := testrepo.Init(t, "main")
		p := h.mustCreate(t, map[string]any{"path": repo, "name": "sweep"})
		id := int64(p["id"].(float64))

		empty := branchedTask(t, h, id, store.TaskArchived, "vincent/empty", "main", false)
		withWork := branchedTask(t, h, id, store.TaskQueued, "vincent/has-work", "main", true)
		// A row whose base no longer resolves: unjudgeable, therefore kept, and
		// the cascade must still finish around it.
		unjudgeable := branchedTask(t, h, id, store.TaskArchived, "vincent/lost-base", "gone", false)

		resp, out := h.doJSON(t, http.MethodDelete, "/v1/projects/"+itoa(id)+"?force=true", nil)
		if resp.StatusCode != http.StatusNoContent {
			t.Fatalf("forced delete = %d %s — a branch problem must not fail the cascade",
				resp.StatusCode, out)
		}
		if branchExists(t, repo, empty) {
			t.Errorf("%s survived: it carried no commits past main", empty)
		}
		if !branchExists(t, repo, withWork) {
			t.Errorf("%s was deleted despite carrying a commit", withWork)
		}
		if !branchExists(t, repo, unjudgeable) {
			t.Errorf("%s was deleted on a check nobody could make", unjudgeable)
		}
	})

	t.Run("force deletes no branch when the policy is off", func(t *testing.T) {
		repo := testrepo.Init(t, "main")
		h := newProjectHarnessWithConfig(t, func() config.Config {
			c := config.Default()
			c.DeleteEmptyBranchOnArchive = false
			return c
		})
		p := h.mustCreate(t, map[string]any{"path": repo, "name": "sweep-off"})
		id := int64(p["id"].(float64))
		empty := branchedTask(t, h, id, store.TaskArchived, "vincent/empty", "main", false)

		resp, out := h.doJSON(t, http.MethodDelete, "/v1/projects/"+itoa(id)+"?force=true", nil)
		if resp.StatusCode != http.StatusNoContent {
			t.Fatalf("forced delete = %d %s", resp.StatusCode, out)
		}
		if !branchExists(t, repo, empty) {
			t.Errorf("%s was deleted with delete_empty_branch_on_archive off", empty)
		}
	})
}

// branchedTask inserts a task row naming a branch that really exists in repo,
// optionally carrying a commit of its own. It returns the branch name.
func branchedTask(
	t *testing.T, h *projectHarness, projectID int64,
	state store.TaskState, branch, base string, withCommit bool,
) string {
	t.Helper()
	repo := projectPath(t, h, projectID)
	testrepo.Run(t, repo, "branch", branch)
	if withCommit {
		testrepo.Run(t, repo, "switch", "-q", branch)
		testrepo.WriteFile(t, repo, "work.txt", "real work\n")
		testrepo.Run(t, repo, "add", ".")
		testrepo.Run(t, repo, "commit", "-q", "-m", "the work")
		testrepo.Run(t, repo, "switch", "-q", "main")
	}
	tk := &store.Task{
		ProjectID: projectID, Title: "t", WorkflowName: "adhoc",
		WorkflowSnapshot: "x", BaseBranch: base, BranchName: branch, State: state,
	}
	if err := h.store.CreateTask(context.Background(), tk, nil); err != nil {
		t.Fatalf("insert task: %v", err)
	}
	return branch
}

func projectPath(t *testing.T, h *projectHarness, id int64) string {
	t.Helper()
	p, err := h.store.GetProject(context.Background(), id)
	if err != nil {
		t.Fatalf("GetProject: %v", err)
	}
	return p.Path
}

// branchExists asks the repository itself, not the code under test.
func branchExists(t *testing.T, repo, branch string) bool {
	t.Helper()
	return testrepo.Run(t, repo, "for-each-ref", "--format=%(refname:short)",
		"refs/heads/"+branch) != ""
}

func insertTask(t *testing.T, st *store.Store, projectID int64, state store.TaskState) int64 {
	t.Helper()
	tk := &store.Task{
		ProjectID: projectID, Title: "t", WorkflowName: "adhoc",
		WorkflowSnapshot: "x", BaseBranch: "main", BranchName: "b", State: state,
	}
	if err := st.CreateTask(context.Background(), tk, nil); err != nil {
		t.Fatalf("insert task: %v", err)
	}
	return tk.ID
}

func itoa(id int64) string { return strconv.FormatInt(id, 10) }
