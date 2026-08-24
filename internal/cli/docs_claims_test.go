package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/daemon"
	"github.com/lezli01/vincent/internal/taskstate"
)

// The published pages make claims about this command tree — which
// subcommands exist, and which columns their tables carry. Nothing enforced
// them, so both drifted: task 001's cleanup guidance rests on a branch column
// `vincent task ls` has never rendered (issue #137). These tests hold every
// user-facing page to what the tree and its tables actually do, in the
// package that owns both.
//
// They are drift checks, not feature checks: a page that stops making the
// claim satisfies them exactly as well as a command that starts backing it.

// docPages are the user-facing pages. `docs/tasks/` and `docs/history/` are
// maintainer records of what was planned and decided — they are allowed to
// describe a surface that does not exist yet — and `docs/spec.md` describes
// the API rather than the CLI.
func docPages(t *testing.T) map[string]string {
	t.Helper()
	root := filepath.Join("..", "..")
	pages := map[string]string{"README.md": ""}
	err := filepath.WalkDir(filepath.Join(root, "docs"), func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		slashed := filepath.ToSlash(rel)
		if d.IsDir() {
			switch slashed {
			case "docs/tasks", "docs/history", "docs/gates", "docs/assets":
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(slashed, ".md") && slashed != "docs/spec.md" {
			pages[slashed] = ""
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk docs: %v", err)
	}
	for name := range pages {
		b, readErr := os.ReadFile(filepath.Join(root, filepath.FromSlash(name)))
		if readErr != nil {
			t.Fatalf("read %s: %v", name, readErr)
		}
		pages[name] = string(b)
	}
	return pages
}

// TestDocsClaimsTaskListShowsBranch: four pages send a reader to `vincent task
// ls --archived` to find the branches vincent made — the cleanup path task 001
// adopted when configurable names broke `git branch --list 'vincent/*'`. The
// table has no such column, and `--json` has no such field, so the claim has
// never been true.
func TestDocsClaimsTaskListShowsBranch(t *testing.T) {
	var claims []string
	for name, body := range docPages(t) {
		for i, line := range strings.Split(body, "\n") {
			if strings.Contains(line, "task ls") && strings.Contains(strings.ToLower(line), "branch") {
				claims = append(claims, name+":"+strconv.Itoa(i+1)+": "+strings.TrimSpace(line))
			}
		}
	}
	if len(claims) == 0 {
		t.Skip("no page claims `task ls` reports a branch")
	}

	out := runTaskLs(t, "--archived")
	header, _, _ := strings.Cut(out, "\n")
	columns := strings.Fields(header)
	if !slices.Contains(columns, "BRANCH") {
		t.Errorf("`vincent task ls --archived` columns = %v, want a BRANCH column claimed by:\n  %s",
			columns, strings.Join(claims, "\n  "))
	}

	var rows []map[string]json.RawMessage
	if err := json.Unmarshal([]byte(runTaskLs(t, "--archived", "--json")), &rows); err != nil {
		t.Fatalf("`task ls --json` is not JSON: %v", err)
	}
	if len(rows) == 0 {
		t.Fatal("`task ls --json` returned no rows")
	}
	if _, ok := rows[0]["branch_name"]; !ok {
		t.Errorf("`task ls --json` row has no branch_name (keys drop it in apiclient.Task), "+
			"so no output mode backs:\n  %s", strings.Join(claims, "\n  "))
	}
}

// TestDocsClaimsTUIActionsHaveSubcommands: README, quickstart and the
// scripting guide state that everything the TUI does is also a subcommand.
// The TUI offers every §6 human action; `vincent task` exposes cancel alone
// of the nine (#89).
func TestDocsClaimsTUIActionsHaveSubcommands(t *testing.T) {
	parity := regexp.MustCompile(`(?i)everything the tui (?:does|can do) is (?:also )?a subcommand`)
	collapse := regexp.MustCompile(`\s+`)
	var claims []string
	for name, body := range docPages(t) {
		if parity.MatchString(collapse.ReplaceAllString(body, " ")) {
			claims = append(claims, name)
		}
	}
	if len(claims) == 0 {
		t.Skip("no page claims TUI/CLI parity")
	}

	have := map[string]bool{}
	for _, sub := range taskCmdNames() {
		have[sub] = true
	}
	seen := map[taskstate.Action]bool{}
	var missing []string
	for _, state := range taskstate.All {
		for _, action := range taskstate.HumanActionsFrom(state) {
			if seen[action] {
				continue
			}
			seen[action] = true
			if !have[string(action)] {
				missing = append(missing, string(action))
			}
		}
	}
	if len(missing) > 0 {
		t.Errorf("`vincent task` has no subcommand for the human actions %v, "+
			"but these pages claim TUI/CLI parity: %v", missing, claims)
	}
}

// taskCmdNames lists the `vincent task` subcommands by their invoked name.
func taskCmdNames() []string {
	var names []string
	for _, c := range newTaskCmd().Commands() {
		names = append(names, c.Name())
	}
	return names
}

// stubTask is one row of GET /v1/tasks as the daemon serves it — the server's
// listTaskResponse embeds taskResponse, so branch_name is on the wire and only
// the client drops it.
const stubTask = `[{
  "id": 7,
  "project_id": 1,
  "project_name": "vincent",
  "title": "File the release issue",
  "workflow": "github-create-issue",
  "state": "archived",
  "base_branch": "main",
  "branch_name": "vincent/7-file-the-release-issue",
  "worktree_path": null,
  "priority": 0,
  "current_step": 1,
  "step_total": 1,
  "step_name": "file",
  "block_reason": null,
  "pause_requested": false,
  "available_actions": [],
  "queued_reason": null,
  "admit_not_before": null,
  "cost_usd": null,
  "input_tokens": 0,
  "output_tokens": 0,
  "created_at": "2026-08-19T10:00:00Z",
  "updated_at": "2026-08-19T10:05:00Z",
  "started_at": null,
  "finished_at": null,
  "archived_at": "2026-08-19T10:05:00Z"
}]`

// runTaskLs runs `vincent task ls` in-process against a stub daemon: the
// rendering is the assertion, so nothing here needs a real store, worktree or
// agent.
func runTaskLs(t *testing.T, args ...string) string {
	t.Helper()
	dataDir := t.TempDir()
	t.Setenv(config.EnvDataDir, dataDir)
	t.Setenv(config.EnvConfigDir, t.TempDir())

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/health":
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		case "/v1/tasks":
			_, _ = w.Write([]byte(stubTask))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)

	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse stub URL: %v", err)
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatalf("stub port: %v", err)
	}
	if _, err := daemon.EnsureToken(dataDir); err != nil {
		t.Fatalf("token: %v", err)
	}
	if err := daemon.WriteRuntimeInfo(dataDir, daemon.RuntimeInfo{
		Port: port, PID: os.Getpid(), StartedAt: time.Now(),
	}); err != nil {
		t.Fatalf("daemon.json: %v", err)
	}

	var buf bytes.Buffer
	root := newRootCmd()
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetArgs(append([]string{"task", "ls"}, args...))
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("task ls: %v (%s)", err, buf.String())
	}
	return buf.String()
}
