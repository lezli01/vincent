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
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/lezli01/vincent/examples"
	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/daemon"
	"github.com/lezli01/vincent/internal/workflow"
)

// runInit runs `vincent workflow init` in-process against isolated
// directories and returns its combined output and exit code. Everything but
// the --project branch is daemon-free by design, so nothing here starts one:
// a test that needed a daemon to write a global workflow would be asserting
// the opposite of the feature.
func runInit(t *testing.T, cfgDir, dataDir string, args ...string) (string, int) {
	t.Helper()
	t.Setenv(config.EnvConfigDir, cfgDir)
	t.Setenv(config.EnvDataDir, dataDir)

	var buf bytes.Buffer
	root := newRootCmd()
	// Execute() silences cobra's own error printing so exitError can carry a
	// code without a second "Error: exit code 1" line; match it here, or the
	// assertions read output the real binary never produces.
	root.SilenceErrors = true
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetArgs(append([]string{"workflow", "init"}, args...))
	code := asExitCode(root.ExecuteContext(context.Background()))
	return buf.String(), code
}

// snapshotTree records every file under dir with its contents, so a refusal
// can be held to leaving the filesystem byte-identical rather than merely to
// its exit code.
func snapshotTree(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		rel, relErr := filepath.Rel(dir, path)
		if relErr != nil {
			return relErr
		}
		out[filepath.ToSlash(rel)] = string(b)
		return nil
	})
	if err != nil {
		t.Fatalf("snapshot %s: %v", dir, err)
	}
	return out
}

func assertUnchanged(t *testing.T, dir string, before map[string]string) {
	t.Helper()
	after := snapshotTree(t, dir)
	if len(after) != len(before) {
		t.Fatalf("refusal changed the tree: %d files before, %d after (%v)",
			len(before), len(after), keysOf(after))
	}
	for name, body := range before {
		if after[name] != body {
			t.Errorf("refusal rewrote %s", name)
		}
	}
}

func keysOf(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// globalDir is where a default-scope `init` writes.
func globalDir(cfgDir string) string {
	return filepath.Join(cfgDir, workflow.GlobalDirName)
}

// TestWorkflowInitWritesGlobalSkeleton is the default shape: no daemon, no
// flags, no pre-existing directory. The file it writes has to be one the
// registry will key under the requested name and that `validate` accepts.
func TestWorkflowInitWritesGlobalSkeleton(t *testing.T) {
	cfgDir, dataDir := t.TempDir(), t.TempDir()

	out, code := runInit(t, cfgDir, dataDir, "my-flow")
	if code != 0 {
		t.Fatalf("init: code %d, out %q", code, out)
	}
	path := filepath.Join(globalDir(cfgDir), "my-flow.yaml")
	if !strings.Contains(out, path) {
		t.Errorf("init printed %q, want the path %q", out, path)
	}
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("written file: %v", err)
	}
	wf, warns, err := workflow.Parse(src, localValidateOptions())
	if err != nil {
		t.Fatalf("written file does not validate: %v", err)
	}
	if len(warns) > 0 {
		t.Errorf("written file validates with warnings: %v", warns)
	}
	if wf.Name != "my-flow" {
		t.Errorf("name: = %q, want my-flow — the registry keys on it, not the file name", wf.Name)
	}
	// The default scope has no project to shadow and no built-in named
	// my-flow, so nothing should have warned.
	if strings.Contains(out, "warning") {
		t.Errorf("unprompted warning on a clean write:\n%s", out)
	}
}

// TestWorkflowInitFromExample: `--from` hands over the shipped file itself,
// comments included — that is the whole reason the rewrite is a text edit.
func TestWorkflowInitFromExample(t *testing.T) {
	for _, example := range examples.Names() {
		t.Run(example, func(t *testing.T) {
			cfgDir, dataDir := t.TempDir(), t.TempDir()
			out, code := runInit(t, cfgDir, dataDir, "renamed", "--from", example)
			if code != 0 {
				t.Fatalf("init --from %s: code %d, out %q", example, code, out)
			}
			src, err := os.ReadFile(filepath.Join(globalDir(cfgDir), "renamed.yaml"))
			if err != nil {
				t.Fatalf("written file: %v", err)
			}
			wf, _, err := workflow.Parse(src, localValidateOptions())
			if err != nil {
				t.Fatalf("written file does not validate: %v", err)
			}
			if wf.Name != "renamed" {
				t.Errorf("name: = %q, want renamed", wf.Name)
			}
			original, err := examples.Read(example)
			if err != nil {
				t.Fatalf("read example: %v", err)
			}
			if got, want := strings.Count(string(src), "#"), strings.Count(string(original), "#"); got != want {
				t.Errorf("written file has %d comment characters, the example has %d", got, want)
			}
		})
	}
}

// TestWorkflowInitJSON: the --json shape every data subcommand carries (PR U
// decision), including the shadow list a script would branch on.
func TestWorkflowInitJSON(t *testing.T) {
	cfgDir, dataDir := t.TempDir(), t.TempDir()
	out, code := runInit(t, cfgDir, dataDir, "adhoc", "--from", "feature-pr", "--json")
	if code != 0 {
		t.Fatalf("init --json: code %d, out %q", code, out)
	}
	var res struct {
		File    string   `json:"file"`
		Name    string   `json:"name"`
		Scope   string   `json:"scope"`
		From    string   `json:"from"`
		Shadows []string `json:"shadows"`
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("init --json is not JSON: %v (%q)", err, out)
	}
	if res.File != filepath.Join(globalDir(cfgDir), "adhoc.yaml") ||
		res.Name != "adhoc" || res.Scope != "global" || res.From != "feature-pr" {
		t.Errorf("init --json = %+v", res)
	}
	if len(res.Shadows) != 1 || res.Shadows[0] != "builtin" {
		t.Errorf("shadows = %v, want [builtin] — adhoc is a built-in", res.Shadows)
	}
}

// TestWorkflowInitWarnsShadowingABuiltin: shadowing is legitimate under §5.2,
// so it warns and writes. A command that refused here would make an
// intentional override impossible from the CLI.
func TestWorkflowInitWarnsShadowingABuiltin(t *testing.T) {
	cfgDir, dataDir := t.TempDir(), t.TempDir()
	out, code := runInit(t, cfgDir, dataDir, "create-workflow")
	if code != 0 {
		t.Fatalf("init over a built-in name: code %d, out %q", code, out)
	}
	if !strings.Contains(out, "shadows") || !strings.Contains(out, "builtin") {
		t.Errorf("no shadow warning for a built-in name:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(globalDir(cfgDir), "create-workflow.yaml")); err != nil {
		t.Errorf("warned but did not write: %v", err)
	}
}

// TestWorkflowInitRefusesExistingPath: never clobber. The write uses
// O_CREATE|O_EXCL, so this is a syscall guarantee rather than a
// stat-then-write race.
func TestWorkflowInitRefusesExistingPath(t *testing.T) {
	cfgDir, dataDir := t.TempDir(), t.TempDir()
	dir := globalDir(cfgDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// Declares a *different* name, so this is the plain path collision: the
	// duplicate-name check has nothing to say and the O_EXCL open is what
	// refuses.
	handwritten := "# do not lose me\nname: something-else\nsteps:\n  - id: run\n    type: command\n    run: 'true'\n"
	if err := os.WriteFile(filepath.Join(dir, "my-flow.yaml"), []byte(handwritten), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	before := snapshotTree(t, cfgDir)

	out, code := runInit(t, cfgDir, dataDir, "my-flow")
	if code == 0 {
		t.Fatalf("init over an existing file succeeded: %q", out)
	}
	if !strings.Contains(out, "already exists") {
		t.Errorf("refusal does not say why:\n%s", out)
	}
	assertUnchanged(t, cfgDir, before)
}

// TestWorkflowInitRerunRefusesPlainly: running the same command twice is the
// commonest way to hit a collision, and the file init wrote declares the name
// init is now asking for. The answer has to be "that file already exists",
// not a duplicate-name report describing a clash with itself.
func TestWorkflowInitRerunRefusesPlainly(t *testing.T) {
	cfgDir, dataDir := t.TempDir(), t.TempDir()
	if out, code := runInit(t, cfgDir, dataDir, "my-flow"); code != 0 {
		t.Fatalf("first init: code %d, out %q", code, out)
	}
	before := snapshotTree(t, cfgDir)

	out, code := runInit(t, cfgDir, dataDir, "my-flow")
	if code == 0 {
		t.Fatalf("second init succeeded: %q", out)
	}
	if !strings.Contains(out, "already exists") {
		t.Errorf("rerun refusal reads as something other than a path collision:\n%s", out)
	}
	assertUnchanged(t, cfgDir, before)
}

// TestWorkflowInitRefusesDuplicateName is the collision the file name hides.
// The registry keys on name:, and within one scope the first file by sorted
// path keeps it — so a sibling sorting *before* the new file would leave the
// new one invalid, and one sorting *after* would be invalidated by it. Both
// directions are damage, so both are refused.
func TestWorkflowInitRefusesDuplicateName(t *testing.T) {
	for _, sibling := range []string{"aaa.yaml", "zzz.yml"} {
		t.Run(sibling, func(t *testing.T) {
			cfgDir, dataDir := t.TempDir(), t.TempDir()
			dir := globalDir(cfgDir)
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatalf("mkdir: %v", err)
			}
			seed := "name: my-flow\nsteps:\n  - id: run\n    type: command\n    run: 'true'\n"
			if err := os.WriteFile(filepath.Join(dir, sibling), []byte(seed), 0o644); err != nil {
				t.Fatalf("seed: %v", err)
			}
			before := snapshotTree(t, cfgDir)

			out, code := runInit(t, cfgDir, dataDir, "my-flow")
			if code == 0 {
				t.Fatalf("init succeeded onto a taken name: %q", out)
			}
			if !strings.Contains(out, sibling) {
				t.Errorf("refusal does not name the file holding %q:\n%s", "my-flow", out)
			}
			assertUnchanged(t, cfgDir, before)
		})
	}
}

// TestWorkflowInitIgnoresUnparseableSibling: a file that does not parse has
// no knowable name, so it cannot block anything. It is already visible as an
// invalid entry in `workflow ls`, which is where it belongs.
func TestWorkflowInitIgnoresUnparseableSibling(t *testing.T) {
	cfgDir, dataDir := t.TempDir(), t.TempDir()
	dir := globalDir(cfgDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "broken.yaml"), []byte("name: [unterminated\n"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if out, code := runInit(t, cfgDir, dataDir, "my-flow"); code != 0 {
		t.Fatalf("an unparseable sibling blocked init: code %d, out %q", code, out)
	}
}

// TestWorkflowInitRefusesBadName: <name> is also a file name, exactly the
// dual role behind task 024 decision 10, so it is held to create-workflow's
// stricter pattern rather than to the looser §8.2 rule for a name: field.
func TestWorkflowInitRefusesBadName(t *testing.T) {
	for _, name := range []string{"My-Flow", "my flow", "../escape", "a/b", "-leading-dash", ""} {
		t.Run(strconv.Quote(name), func(t *testing.T) {
			cfgDir, dataDir := t.TempDir(), t.TempDir()
			before := snapshotTree(t, cfgDir)
			out, code := runInit(t, cfgDir, dataDir, name)
			if code == 0 {
				t.Fatalf("init accepted %q: %q", name, out)
			}
			assertUnchanged(t, cfgDir, before)
		})
	}
}

// TestWorkflowInitRefusesUnknownExample: the error lists what would have
// worked, and the list comes from the embedded filesystem rather than from a
// hardcoded set that would drift from examples/.
func TestWorkflowInitRefusesUnknownExample(t *testing.T) {
	cfgDir, dataDir := t.TempDir(), t.TempDir()
	before := snapshotTree(t, cfgDir)

	out, code := runInit(t, cfgDir, dataDir, "my-flow", "--from", "no-such-example")
	if code == 0 {
		t.Fatalf("init accepted an unknown --from: %q", out)
	}
	for _, name := range examples.Names() {
		if !strings.Contains(out, name) {
			t.Errorf("the unknown --from error never offers %q:\n%s", name, out)
		}
	}
	assertUnchanged(t, cfgDir, before)
}

// TestWorkflowInitProjectNeedsDaemon: --project is the one branch that needs
// a daemon, because only the daemon knows which projects exist. Exit 2 is
// "no daemon answered", distinct from exit 1 "your request was rejected",
// and nothing is written either way.
func TestWorkflowInitProjectNeedsDaemon(t *testing.T) {
	cfgDir, dataDir := t.TempDir(), t.TempDir()
	before := snapshotTree(t, cfgDir)

	out, code := runInit(t, cfgDir, dataDir, "my-flow", "--project", "1")
	if code != 2 {
		t.Fatalf("init --project with no daemon: code %d, want 2 (out %q)", code, out)
	}
	assertUnchanged(t, cfgDir, before)
}

// stubProjectDaemon publishes a daemon.json and token pointing at an httptest
// server that serves one project rooted at repo. The write is what is under
// test, so nothing here needs a real store or scheduler.
func stubProjectDaemon(t *testing.T, dataDir, repo string) {
	t.Helper()
	body, err := json.Marshal([]map[string]any{{
		"id": 1, "name": "stub", "path": repo, "default_branch": "main",
		"default_workflow": nil, "max_parallel_tasks": nil, "branch_template": nil,
		"created_at": time.Now().UTC(), "updated_at": time.Now().UTC(),
	}})
	if err != nil {
		t.Fatalf("marshal stub project: %v", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/health":
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		case "/v1/projects":
			_, _ = w.Write(body)
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
}

// TestWorkflowInitProjectScope: the id resolves to the repository root and
// the file lands in that repo's .vincent/workflows, which is where the
// registry watcher is looking.
func TestWorkflowInitProjectScope(t *testing.T) {
	cfgDir, dataDir, repo := t.TempDir(), t.TempDir(), t.TempDir()
	stubProjectDaemon(t, dataDir, repo)

	out, code := runInit(t, cfgDir, dataDir, "my-flow", "--project", "1")
	if code != 0 {
		t.Fatalf("init --project: code %d, out %q", code, out)
	}
	path := filepath.Join(repo, workflow.ProjectDirName, "my-flow.yaml")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("project-scope file: %v", err)
	}
	if !strings.Contains(out, path) {
		t.Errorf("init printed %q, want %q", out, path)
	}
}

// TestWorkflowInitProjectWarnsShadowingGlobal: a project file taking a name
// the global scope already uses is the §5.2 override working, so it warns
// and writes. Both directories are readable locally once the repo root is
// known, which is why this warning is possible at all.
func TestWorkflowInitProjectWarnsShadowingGlobal(t *testing.T) {
	cfgDir, dataDir, repo := t.TempDir(), t.TempDir(), t.TempDir()
	stubProjectDaemon(t, dataDir, repo)
	if err := os.MkdirAll(globalDir(cfgDir), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	seed := "name: my-flow\nsteps:\n  - id: run\n    type: command\n    run: 'true'\n"
	if err := os.WriteFile(filepath.Join(globalDir(cfgDir), "elsewhere.yaml"), []byte(seed), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	out, code := runInit(t, cfgDir, dataDir, "my-flow", "--project", "1")
	if code != 0 {
		t.Fatalf("init --project over a global name: code %d, out %q", code, out)
	}
	if !strings.Contains(out, "shadows") || !strings.Contains(out, "global") {
		t.Errorf("no shadow warning for a name the global scope holds:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(repo, workflow.ProjectDirName, "my-flow.yaml")); err != nil {
		t.Errorf("warned but did not write: %v", err)
	}
}

// TestWorkflowInitUnknownProject: the daemon answered, it just has no such
// project. That is exit 1 — a rejected request — not exit 2.
func TestWorkflowInitUnknownProject(t *testing.T) {
	cfgDir, dataDir, repo := t.TempDir(), t.TempDir(), t.TempDir()
	stubProjectDaemon(t, dataDir, repo)
	before := snapshotTree(t, cfgDir)

	out, code := runInit(t, cfgDir, dataDir, "my-flow", "--project", "99")
	if code != 1 {
		t.Fatalf("init --project 99: code %d, want 1 (out %q)", code, out)
	}
	assertUnchanged(t, cfgDir, before)
	if _, err := os.Stat(filepath.Join(repo, workflow.ProjectDirName)); !os.IsNotExist(err) {
		t.Errorf("a rejected --project still created %s", workflow.ProjectDirName)
	}
}
