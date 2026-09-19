package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/workflow"
)

// The daemon-free half of a global update-workflows run (task 123): `workflow
// ls --global` and `workflow apply`, through the real cobra tree against
// VINCENT_CONFIG_DIR / VINCENT_DATA_DIR. The refusals themselves are
// internal/workflow's tests; these pin the command's contract.

func runWorkflowCmd(t *testing.T, d triggerDirs, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	t.Setenv(config.EnvConfigDir, d.config)
	t.Setenv(config.EnvDataDir, d.data)
	var out, errOut bytes.Buffer
	root := newRootCmd()
	root.SilenceErrors = true
	root.SetOut(&out)
	root.SetErr(&errOut)
	root.SetArgs(append([]string{"workflow"}, args...))
	code = asExitCode(root.ExecuteContext(context.Background()))
	return out.String(), errOut.String(), code
}

func globalWorkflowDoc(name, comment string) string {
	return "# " + comment + "\nname: " + name + "\nsteps:\n  - id: one\n    type: command\n    run: 'git status'\n"
}

func TestWorkflowLsGlobal(t *testing.T) {
	d := triggerDirs{config: t.TempDir(), data: t.TempDir()}
	dir := filepath.Join(d.config, workflow.GlobalDirName)

	// No directory at all, then an empty one: both are "none", exit 1.
	for _, stage := range []string{"missing", "empty"} {
		stdout, _, code := runWorkflowCmd(t, d, "ls", "--global")
		if code != 1 || stdout != "" {
			t.Errorf("%s: ls --global = %d %q, want exit 1 and nothing printed", stage, code, stdout)
		}
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}

	alpha := d.write(t, filepath.Join(dir, "alpha.yaml"), globalWorkflowDoc("alpha", "ok"))
	broken := d.write(t, filepath.Join(dir, "broken.yml"), "name: broken\nsteps: nope\n")
	stdout, stderr, code := runWorkflowCmd(t, d, "ls", "--global")
	if code != 0 || stdout != alpha+"\n"+broken+"\n" {
		t.Fatalf("ls --global = %d %q (stderr %q), want both absolute paths", code, stdout, stderr)
	}

	stdout, _, code = runWorkflowCmd(t, d, "ls", "--global", "--json")
	var listed []workflow.GlobalListing
	if err := json.Unmarshal([]byte(stdout), &listed); err != nil || code != 0 || len(listed) != 2 {
		t.Fatalf("ls --global --json = %d %q (%v)", code, stdout, err)
	}
	for i, path := range []string{alpha, broken} {
		want, err := workflow.Version(path)
		if err != nil {
			t.Fatal(err)
		}
		if listed[i].File != path || listed[i].Version != want {
			t.Errorf("listing %d = %+v, want %s at version %s", i, listed[i], path, want)
		}
	}
	if !listed[0].Valid || listed[1].Valid || listed[1].Name != "broken" || len(listed[1].Errors) == 0 {
		t.Errorf("validity = %+v, want alpha valid and broken listed invalid with its errors", listed)
	}

	// A usage error from cobra, before anything is listed. The message goes
	// to the root's error printer, which this harness silences.
	if stdout, _, code := runWorkflowCmd(t, d, "ls", "--global", "--project", "3"); code == 0 || stdout != "" {
		t.Errorf("ls --global --project = %d %q, want a usage error and no listing", code, stdout)
	}
}

func TestWorkflowApplyChecksThenInstalls(t *testing.T) {
	d := triggerDirs{config: t.TempDir(), data: t.TempDir()}
	dir := filepath.Join(d.config, workflow.GlobalDirName)
	alpha := d.write(t, filepath.Join(dir, "alpha.yaml"), globalWorkflowDoc("alpha", "live"))
	version, err := workflow.Version(alpha)
	if err != nil {
		t.Fatal(err)
	}
	staging := workflow.ProposalDir(d.data, 42)
	staged := d.write(t, filepath.Join(staging, "alpha.yaml"), globalWorkflowDoc("renamed", "rewritten"))
	d.write(t, filepath.Join(staging, workflow.ProposalManifest), `{"alpha.yaml": "`+version+`"}`)

	for _, args := range [][]string{{"--check"}, {}} {
		_, stderr, code := runWorkflowCmd(t, d, append([]string{"apply", "--proposal", "42"}, args...)...)
		if code != 1 || !strings.Contains(stderr, "alpha.yaml: name: renames") || !strings.Contains(stderr, "nothing was written") {
			t.Fatalf("apply %v of a rename = %d %q, want exit 1 naming the rename", args, code, stderr)
		}
	}

	d.write(t, staged, globalWorkflowDoc("alpha", "rewritten"))
	stdout, stderr, code := runWorkflowCmd(t, d, "apply", "--proposal", "42", "--check")
	if code != 0 || stdout != staged+"\n" {
		t.Fatalf("apply --check = %d %q (stderr %q), want the staged path", code, stdout, stderr)
	}
	if b, _ := os.ReadFile(alpha); !strings.Contains(string(b), "# live") {
		t.Errorf("--check wrote alpha.yaml: %q", b)
	}

	stdout, stderr, code = runWorkflowCmd(t, d, "apply", "--proposal", "42")
	if code != 0 || stdout != "wrote "+alpha+"\nremoved "+staging+"\n" {
		t.Fatalf("apply = %d %q (stderr %q)", code, stdout, stderr)
	}
	if b, _ := os.ReadFile(alpha); !strings.Contains(string(b), "# rewritten") {
		t.Errorf("alpha.yaml = %q, want the staged content", b)
	}
	if _, stderr, code := runWorkflowCmd(t, d, "apply", "--proposal", "42"); code != 1 || !strings.Contains(stderr, "no proposal") {
		t.Errorf("apply with nothing staged = %d %q", code, stderr)
	}
	if _, _, code := runWorkflowCmd(t, d, "apply", "--proposal", "0"); code != 1 {
		t.Errorf("apply --proposal 0 = %d, want 1", code)
	}
}
