package skill

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// stubNpx builds a recorder onto a directory and puts it first on PATH. It is
// a compiled Go program rather than a shell script for the reason
// cmd/fakeagent is one: a `.sh` stub does not exist on Windows and a `.cmd`
// stub mangles its own argv quoting, and the argv is the entire assertion.
func stubNpx(t *testing.T) (record string) {
	t.Helper()
	dir := t.TempDir()
	record = filepath.Join(dir, "argv.txt")
	src := filepath.Join(dir, "main.go")
	prog := `package main

import (
	"os"
	"strings"
)

func main() {
	_ = os.WriteFile(os.Getenv("VINCENT_TEST_ARGV"), []byte(strings.Join(os.Args[1:], "\n")), 0o644)
}
`
	if err := os.WriteFile(src, []byte(prog), 0o644); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "npx")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	build := exec.Command("go", "build", "-o", bin, src)
	build.Dir = dir
	if out, err := build.CombinedOutput(); err != nil {
		t.Skipf("cannot build the npx stub: %v\n%s", err, out)
	}
	t.Setenv("VINCENT_TEST_ARGV", record)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return record
}

// TestInstallArgvReachesTheProcess is decision 1's guard, from the other end:
// InstallArgs is what the child actually receives, flags and all.
func TestInstallArgvReachesTheProcess(t *testing.T) {
	record := stubNpx(t)
	var out bytes.Buffer
	if err := Install(context.Background(), "vincent-workflows", Slugs(nil), &out); err != nil {
		t.Fatalf("install: %v (%s)", err, out.String())
	}
	b, err := os.ReadFile(record)
	if err != nil {
		t.Fatalf("the stub recorded nothing: %v", err)
	}
	got := strings.Split(string(b), "\n")
	want := []string{
		"skills", "add", "lezli01/vincent",
		"--skill", "vincent-workflows",
		"--agent", "claude-code,codex,cursor",
		"--yes", "--global",
	}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("argv =\n  %v\nwant\n  %v", got, want)
	}
}

// TestInstallWithoutNpx is the missing-dependency outcome: an error naming
// the dependency and carrying the line to run once node is there — and never
// `npx.cmd`, which is what LookPath resolves on Windows and not what anybody
// installs.
func TestInstallWithoutNpx(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	err := Install(context.Background(), "vincent-workflows", nil, &bytes.Buffer{})
	if err == nil {
		t.Fatal("want an error with no npx on PATH")
	}
	if !errors.Is(err, ErrNoInstaller) {
		t.Errorf("error %v does not wrap ErrNoInstaller", err)
	}
	msg := err.Error()
	if !strings.Contains(msg, "npx is not installed") || strings.Contains(msg, "npx.cmd") {
		t.Errorf("message does not name the dependency plainly: %q", msg)
	}
	if !strings.Contains(msg, "npx skills add lezli01/vincent --skill vincent-workflows") {
		t.Errorf("message does not carry the command to run: %q", msg)
	}
}

// TestDetectNeedsNoNpx is decision 2 as a test rather than a claim: with
// nothing at all on PATH, detection still answers.
func TestDetectNeedsNoNpx(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	home := t.TempDir()
	installAt(t, home, ".agents", "vincent-workflows", "1.0.0")
	got := Detect(Options{Home: home})
	if len(got) == 0 {
		t.Fatal("no rows")
	}
	for _, s := range got {
		if s.State == "" {
			t.Errorf("%s has no state", s.Name)
		}
	}
}
