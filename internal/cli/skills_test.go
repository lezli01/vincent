package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
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

// seedStore writes every published skill's store copy under home at version.
func seedStore(t *testing.T, home string, version string) {
	t.Helper()
	for _, p := range skill.Publish(nil) {
		dir := filepath.Join(home, skill.StoreDir, "skills", p.Path)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		body := "---\nname: " + p.Name + "\nmetadata:\n  version: " + version + "\n---\n"
		if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// TestSkillsInstallForCopilot is task 138.12 decision 4 end to end. PATH holds
// no npx, so any attempt to run one would fail the store-present case.
func TestSkillsInstallForCopilot(t *testing.T) {
	t.Run("store present", func(t *testing.T) {
		home := fakeHome(t)
		t.Setenv("PATH", t.TempDir())
		seedStore(t, home, "0.0.1")
		out, errOut, code := runSkills(t, "install", "--agent", "copilot")
		if code != 0 {
			t.Fatalf("exit %d\nstdout: %s\nstderr: %s", code, out, errOut)
		}
		if !strings.Contains(out, "nothing to link") || strings.Contains(out, "running npx") {
			t.Errorf("stdout = %q", out)
		}

		out, _, code = runSkills(t, "install", "--agent", "copilot", "--json")
		if code != 0 {
			t.Fatalf("--json exit %d: %s", code, out)
		}
		var res []skillsInstallResult
		if err := json.Unmarshal([]byte(out), &res); err != nil {
			t.Fatalf("--json is not parseable: %v\n%s", err, out)
		}
		for _, r := range res {
			if !r.OK || r.Command != "" || r.Note == "" {
				t.Errorf("result = %+v, want ok with a note and no command", r)
			}
		}
	})
	t.Run("store absent", func(t *testing.T) {
		fakeHome(t)
		t.Setenv("PATH", t.TempDir())
		out, errOut, code := runSkills(t, "install", "--agent", "copilot")
		if code != 1 {
			t.Fatalf("exit %d, want 1\nstdout: %s\nstderr: %s", code, out, errOut)
		}
		if !strings.Contains(errOut, "none is installed") || !strings.Contains(errOut, "--agent claude") {
			t.Errorf("stderr = %q", errOut)
		}
		if strings.Contains(errOut, "npx is not installed") {
			t.Errorf("a copilot-only install tried to run npx: %q", errOut)
		}
	})
	t.Run("copilot and claude", func(t *testing.T) {
		fakeHome(t)
		t.Setenv("PATH", t.TempDir())
		_, errOut, code := runSkills(t, "install", "--agent", "copilot,claude")
		if code != 1 {
			t.Fatalf("exit %d, want 1 with no npx", code)
		}
		if !strings.Contains(errOut, "--agent claude-code --yes --global") {
			t.Errorf("the argv does not carry claude's slug alone: %q", errOut)
		}
	})
}

// TestSkillsListNamesCopilotReadingTheStore: the AGENTS column and the
// doctor row mark the store-read entry rather than calling it a link.
func TestSkillsListNamesCopilotReadingTheStore(t *testing.T) {
	home := fakeHome(t)
	t.Setenv("PATH", t.TempDir())
	seedStore(t, home, "0.0.1")
	if err := os.MkdirAll(filepath.Join(home, ".copilot"), 0o755); err != nil {
		t.Fatal(err)
	}
	out, _, code := runSkills(t, "ls")
	if code != 0 || !strings.Contains(out, "copilot (reads store)") {
		t.Errorf("exit %d, ls = %q", code, out)
	}
	out, _, _ = runSkills(t, "ls", "--json")
	if !strings.Contains(out, `"store": true`) {
		t.Errorf("--json does not carry the store-read entry: %s", out)
	}
	rows := doctorSkillRows(skill.Detect(skill.Options{}))
	for _, r := range rows[:len(rows)-1] {
		if !strings.Contains(r[1], "linked into copilot (reads store)") {
			t.Errorf("doctor row = %q", r[1])
		}
	}
}
