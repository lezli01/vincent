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
	return buildNpx(t, `package main

import (
	"os"
	"strings"
)

func main() {
	_ = os.WriteFile(os.Getenv("VINCENT_TEST_ARGV"), []byte(strings.Join(os.Args[1:], "\n")), 0o644)
}
`)
}

// skillsCLIStub is an `npx` that reads `--agent` the way the published
// `skills` CLI does, rather than recording argv for a test to compare against
// a spelling it chose itself. `parseAddOptions` in skills 1.5.15 and 1.7.0
// (dist/cli.mjs) treats `-a`/`--agent` as variadic: every following argument
// up to the next one starting with `-` is one agent name, repeated flags
// accumulate, and nothing splits on commas. Each name must then be a key of
// its agent registry exactly, or it prints `Invalid agents: …` and exits 1.
// valid is the part of that registry this package can name — vincent's three
// slugs and one agent it does not drive.
const skillsCLIStub = `package main

import (
	"fmt"
	"os"
	"strings"
)

var valid = map[string]bool{"claude-code": true, "codex": true, "cursor": true, "cline": true}

func main() {
	args := os.Args[1:]
	if len(args) < 2 || args[0] != "skills" || args[1] != "add" {
		fmt.Println("not a skills add invocation:", args)
		os.Exit(2)
	}
	var agents []string
	for i := 2; i < len(args); i++ {
		if args[i] != "-a" && args[i] != "--agent" {
			continue
		}
		for i+1 < len(args) && args[i+1] != "" && !strings.HasPrefix(args[i+1], "-") {
			i++
			agents = append(agents, args[i])
		}
	}
	if len(agents) == 0 {
		fmt.Println("no --agent selection: the CLI would open its interactive picker")
		os.Exit(1)
	}
	var invalid []string
	for _, a := range agents {
		if !valid[a] {
			invalid = append(invalid, a)
		}
	}
	if len(invalid) > 0 {
		fmt.Println("Invalid agents: " + strings.Join(invalid, ", "))
		os.Exit(1)
	}
	_ = os.WriteFile(os.Getenv("VINCENT_TEST_ARGV"), []byte(strings.Join(agents, "\n")), 0o644)
}
`

// buildNpx compiles prog as `npx` onto a fresh directory, puts that directory
// first on PATH, and returns the file the stub writes its record to.
func buildNpx(t *testing.T, prog string) (record string) {
	t.Helper()
	dir := t.TempDir()
	record = filepath.Join(dir, "argv.txt")
	src := filepath.Join(dir, "main.go")
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

// TestInstallAgentsParseAsTheSkillsCLIDoes is the check the two argv tests
// above cannot make (issue #489): they pin a spelling vincent chose, and a
// comma-joined `--agent claude-code,codex,cursor` satisfied them while the
// real CLI read it as one agent of that name and refused it. Here the child
// parses the argv the way `skills add` does, so every selection must arrive
// as the slugs it was built from — one of them, two, or the CLI default of
// all three.
func TestInstallAgentsParseAsTheSkillsCLIDoes(t *testing.T) {
	for _, tc := range []struct {
		name     string
		adapters []string
		want     []string
	}{
		{name: "one adapter", adapters: []string{"cursor"}, want: []string{"cursor"}},
		{name: "two adapters", adapters: []string{"codex", "claude"}, want: []string{"claude-code", "codex"}},
		{name: "cli default", adapters: nil, want: []string{"claude-code", "codex", "cursor"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			record := buildNpx(t, skillsCLIStub)
			var out bytes.Buffer
			if err := Install(context.Background(), "vincent-triggers", Slugs(tc.adapters), &out); err != nil {
				t.Fatalf("install: %v\n%s", err, out.String())
			}
			b, err := os.ReadFile(record)
			if err != nil {
				t.Fatalf("the stub recorded no agents: %v", err)
			}
			if got := strings.Split(string(b), "\n"); strings.Join(got, " ") != strings.Join(tc.want, " ") {
				t.Errorf("skills add selected agents %q, want %q", got, tc.want)
			}
		})
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
