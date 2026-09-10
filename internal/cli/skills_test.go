package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"runtime"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/skill"
)

// fakeHome points os.UserHomeDir at a temp directory. Both variables are set
// because the two platforms read different ones, and a test that isolated
// only $HOME would run against the developer's real agent directories on
// Windows.
func fakeHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	if runtime.GOOS == "windows" {
		t.Setenv("USERPROFILE", home)
	}
	return home
}

// runSkills executes one `vincent skills …` invocation in-process and returns
// its stdout, stderr and exit code.
func runSkills(t *testing.T, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	cmd := newSkillsCmd()
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), errOut.String(), asExitCode(err)
}

// TestSkillsListReportsState is the no-daemon promise: with an empty home and
// nothing on PATH, the command still answers, and it answers "not installed"
// rather than failing.
func TestSkillsListReportsState(t *testing.T) {
	fakeHome(t)
	t.Setenv("PATH", t.TempDir())
	out, _, code := runSkills(t, "ls", "--json")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, out)
	}
	var rows []skill.Status
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatalf("--json is not parseable: %v\n%s", err, out)
	}
	if len(rows) == 0 {
		t.Fatal("no rows; this build publishes at least one skill")
	}
	for _, r := range rows {
		if r.State != skill.StateAbsent {
			t.Errorf("%s = %q against an empty home, want %q", r.Name, r.State, skill.StateAbsent)
		}
		if r.Shipped == "" {
			t.Errorf("%s reports no shipped version", r.Name)
		}
	}
}

// TestSkillsInstallWithoutNpx is decision 1's missing-dependency outcome, end
// to end: exit 1 — never 2, because no daemon was asked — a message naming
// the dependency and carrying the command, and a listing in the same test
// that is entirely unaffected.
func TestSkillsInstallWithoutNpx(t *testing.T) {
	fakeHome(t)
	t.Setenv("PATH", t.TempDir())

	out, errOut, code := runSkills(t, "install")
	if code != 1 {
		t.Fatalf("exit %d, want 1\nstdout: %s\nstderr: %s", code, out, errOut)
	}
	if !strings.Contains(errOut, "npx is not installed") {
		t.Errorf("stderr does not name the dependency: %q", errOut)
	}
	if !strings.Contains(errOut, "npx skills add "+skill.Repo) {
		t.Errorf("stderr does not carry the command to run once node is there: %q", errOut)
	}
	if strings.Contains(errOut, "npx.cmd") {
		t.Errorf("the message names the resolved binary rather than the dependency: %q", errOut)
	}

	// Detection's independence from npx is a test, not a claim.
	lsOut, _, lsCode := runSkills(t, "ls")
	if lsCode != 0 {
		t.Fatalf("ls exited %d after a failed install", lsCode)
	}
	if !strings.Contains(lsOut, "not installed") {
		t.Errorf("ls stopped reporting state: %q", lsOut)
	}
}

// TestSkillsInstallNothingToDo: an install with everything already current
// runs no subprocess and exits 0.
func TestSkillsInstallNothingToDo(t *testing.T) {
	home := fakeHome(t)
	t.Setenv("PATH", t.TempDir())
	for _, p := range skill.Publish(nil) {
		dir := home + string(os.PathSeparator) + skill.StoreDir +
			string(os.PathSeparator) + "skills" + string(os.PathSeparator) + p.Path
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		body := "---\nname: " + p.Name + "\nmetadata:\n  version: " + p.Version + "\n---\n"
		if err := os.WriteFile(dir+string(os.PathSeparator)+"SKILL.md", []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	out, errOut, code := runSkills(t, "install")
	if code != 0 {
		t.Fatalf("exit %d: %s %s", code, out, errOut)
	}
	if !strings.Contains(out, "already current") {
		t.Errorf("stdout = %q", out)
	}
}
