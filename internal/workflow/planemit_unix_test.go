//go:build unix

package workflow

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/goccy/go-yaml"

	"github.com/lezli01/vincent/internal/testrepo"
)

// plan-emit's stdout is the lane list `build` fans out over, so it must be
// exactly `jq -c '.'` of units.jsonl and nothing more; the readable plan that
// replaced the human gate (#936) goes to stderr, which `.Result` never
// carries (#311). This pins both halves against the shipped step.
//
// Unix-only: the workflow is `platforms: [posix]` (§8.1.1) and its command
// steps run under `/bin/sh` (§8.3).
func TestPlanEmitStdoutIsTheLaneList(t *testing.T) {
	jq, err := exec.LookPath("jq")
	if err != nil {
		t.Skip("jq not installed")
	}
	body, err := Render("run", planEmitRun(t), RenderContext{})
	if err != nil {
		t.Fatalf("render plan-emit: %v", err)
	}

	units := []planUnit{
		unit("store", "^internal/store/\n^internal/store/migrations/"),
		unit("api", "^internal/api/", "store"),
		unit("cli", "^internal/cli/", "store", "api"),
		unit("docs", "^docs/guides/"),
	}
	repo := planDAGRepo(t, units)

	// A stand-in `vincent` that records the status line, ahead of the real
	// one should this machine have it on PATH.
	bin := t.TempDir()
	statusLog := filepath.Join(bin, "status.log")
	stub := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> '" + statusLog + "'\n"
	if err := os.WriteFile(filepath.Join(bin, "vincent"), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("/bin/sh", "-c", body)
	cmd.Dir = repo
	cmd.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("plan-emit: %v\nstderr:\n%s", err, stderr.String())
	}

	want, err := exec.Command(jq, "-c", ".", filepath.Join(repo, ".vincent-issue", "units.jsonl")).Output()
	if err != nil {
		t.Fatalf("jq -c: %v", err)
	}
	if stdout.String() != string(want) {
		t.Errorf("stdout is not the lane list:\n got: %q\nwant: %q", stdout.String(), string(want))
	}

	for _, line := range []string{
		"plan: 4 unit(s) in 3 round(s), 2 in the first",
		"store: unit store | needs: none | files: ^internal/store/ | ^internal/store/migrations/",
		"api: unit api | needs: store | files: ^internal/api/",
		"cli: unit cli | needs: store, api | files: ^internal/cli/",
		"docs: unit docs | needs: none | files: ^docs/guides/",
	} {
		if !strings.Contains(stderr.String(), line+"\n") {
			t.Errorf("stderr lacks the line %q:\n%s", line, stderr.String())
		}
	}

	got, err := os.ReadFile(statusLog)
	if err != nil {
		t.Fatalf("plan-emit never set its status: %v", err)
	}
	if s := string(got); s != "status -- 4 unit(s) in 3 round(s), 2 in the first\n" {
		t.Errorf("status line = %q", s)
	}

	// Without any `vincent` on PATH the status helper must swallow the
	// failure rather than fail the step. jq is linked into a directory of its
	// own so PATH can carry it without whatever else shares its directory.
	jqOnly := t.TempDir()
	if err := os.Symlink(jq, filepath.Join(jqOnly, "jq")); err != nil {
		t.Fatal(err)
	}
	cmd = exec.Command("/bin/sh", "-c", body)
	cmd.Dir = repo
	cmd.Env = append(os.Environ(), "PATH="+jqOnly+string(os.PathListSeparator)+"/usr/bin:/bin")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("plan-emit without vincent on PATH: %v", err)
	}
	if string(out) != string(want) {
		t.Errorf("stdout without vincent on PATH is not the lane list:\n%s", out)
	}
	if s := testrepo.Run(t, repo, "status", "--porcelain", "--untracked-files=all"); s != "?? .vincent-issue/units.jsonl" {
		t.Errorf("plan-emit left the worktree changed:\n%s", s)
	}
}

// planEmitRun reads plan-emit's `run` out of the shipped workflow rather than
// restating it.
func planEmitRun(t *testing.T) string {
	t.Helper()
	path := filepath.Join("..", "..", ".vincent", "workflows", "github-resolve-issue-dag.yaml")
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var doc struct {
		Steps []struct {
			ID  string `yaml:"id"`
			Run string `yaml:"run"`
		} `yaml:"steps"`
	}
	if err := yaml.Unmarshal(src, &doc); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	for _, s := range doc.Steps {
		if s.ID == "plan-emit" && s.Run != "" {
			return s.Run
		}
	}
	t.Fatalf("no plan-emit run in %s", path)
	return ""
}
