//go:build mage

// Build targets for vincent. Run via `go run mage.go <target> [<target>...]`
// (zero-install) or `go tool mage <target>`; list targets with -l.
package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/magefile/mage/sh"
)

const versionPkg = "github.com/lezli01/vincent/internal/version"

// Build compiles the vincent binary into bin/ with build info injected.
func Build() error {
	out := filepath.Join("bin", "vincent")
	if runtime.GOOS == "windows" {
		out += ".exe"
	}
	return sh.RunV("go", "build", "-trimpath", "-ldflags", ldflags(), "-o", out, "./cmd/vincent")
}

// Test runs all tests.
func Test() error {
	return sh.RunV("go", "test", "./...")
}

// TestRace runs all tests with the race detector (needs a C toolchain; CI has one).
//
// The explicit -timeout is headroom, not a preference. It is a *per-package*
// deadline, and on the Windows CI leg under the race detector `internal/api`
// once took around 7.5 minutes of Go's 10-minute default while the leg's own
// throughput varies by a factor of two between runs of the same commit. A
// package that is healthy at 75% of the limit turns a slow runner into a red
// build, with a goroutine dump that looks like a hang and is not one. 20
// minutes held until `internal/api` grew to 1120–1170 s of it on green master
// runs and a slower runner crossed 1200 s mid-suite, every test in the dump
// one second old. 30 minutes keeps a genuine hang reported by `go test` — with
// that dump, which is the useful artifact — rather than by the job's own
// timeout-minutes in ci.yml, which stays the wider of the two.
func TestRace() error {
	return sh.RunV("go", "test", "-race", "-timeout", "30m", "./...")
}

// TestCI is the suite as CI runs it: under gotestsum (pinned via the go.mod
// tool directive), rerunning failed tests and writing reports (#736), with
// the race detector scoped per OS (#728).
//
// It is a separate target so CI still runs a mage target developers can run
// locally, while Test and TestRace stay plain `go test`: a local run should
// fail on the first failure, not paper over it. In CI a test that fails and
// then passes on a rerun no longer reds the job — that is the point, since an
// intermittent failure otherwise costs a whole re-run of the leg — so every
// rerun is made visible instead of silent.
//
// Race scope (#728, the author's decision, option 1): Linux runs the whole
// suite under -race; macOS runs it without the race detector; Windows runs it
// without, plus a -race pass over internal/procx, internal/daemon,
// internal/service and internal/taskrun. Measured on green master after the
// template DB (#726), the race step took ~4 minutes on ubuntu, ~7 on macOS
// and 10–16 on Windows, which made Windows the critical path of the required
// check. The Windows failures that cost reruns were load timeouts, not race
// reports, and a data race is overwhelmingly platform-independent: one in
// shared code is caught by the Linux leg, whichever OS it would also fire
// on. What the race detector can only see on Windows is Windows-specific code
// under concurrency — process spawn and kill (procx), the daemon's lifecycle
// (daemon), the Windows service (service) and the actor that spawns and
// kills agent processes (taskrun) — so those four keep it there.
// internal/api and internal/tui carry a little build-tagged code too, but
// they are the heavy packages and their races are the platform-independent
// kind. This is deliberately weaker proof on macOS and Windows than a full
// race run, chosen for the wall time; TestRace, the local default, is
// unchanged and still races everything.
//
// VINCENT_TEST_RACE overrides the scope: `all`, `none`, or a comma-separated
// list of package patterns. A list is two passes over disjoint packages, run
// concurrently — the rest of ./... without -race, and the listed ones with
// it — and each runs to the end whether or not the other fails. CI never sets it; it exists so
// scripts/test-rerun-check.sh can prove every scope from any host.
//
// Rerun budget: up to 2 reruns of each failed test (gotestsum reruns only the
// failed tests, by -run), and none at all when the first pass has more than
// gotestsum's default 10 failures — that many is a regression, and rerunning
// it would only burn the Windows leg's budget. A data race is never rerun
// (--rerun-fails-abort-on-data-race): a race is a correctness bug, and an
// intermittent one, so a rerun that happened not to race would turn it green.
// -timeout 30m is TestRace's, for TestRace's reasons, on every pass.
//
// Reports land in bin/test-report/ (VINCENT_TEST_REPORT_DIR overrides):
// junit.xml and test.json carry each test's elapsed time, reruns.txt lists
// every test that was rerun; a separate race pass writes junit-race.xml,
// test-race.json and reruns-race.txt beside them. When $GITHUB_STEP_SUMMARY
// is set, a "Tests rerun" section listing both rerun files is appended to
// the job summary — on the failure path too, so a test that failed every
// attempt is named there as well as failing the job. VINCENT_TEST_DIR runs
// the suite in another module; only scripts/test-rerun-check.sh uses it, to
// prove this target against tests that fail on purpose.
func TestCI() (err error) {
	// Resolved here, from this module, so the suite may run in a module
	// that does not pin gotestsum.
	bin, err := sh.Output("go", "tool", "-n", "gotestsum")
	if err != nil {
		return fmt.Errorf("locating gotestsum: %w", err)
	}
	report := os.Getenv("VINCENT_TEST_REPORT_DIR")
	if report == "" {
		report = filepath.Join("bin", "test-report")
	}
	if report, err = filepath.Abs(report); err != nil {
		return err
	}
	if err := os.MkdirAll(report, 0o750); err != nil {
		return err
	}
	reruns := []string{filepath.Join(report, "reruns.txt"), filepath.Join(report, "reruns-race.txt")}
	// A stale report from an earlier run would be summarized as this one's.
	for _, f := range reruns {
		if err := os.Remove(f); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	defer func() {
		if serr := summarizeReruns(reruns, err != nil); serr != nil && err == nil {
			err = serr
		}
	}()

	dir := os.Getenv("VINCENT_TEST_DIR")
	scope := os.Getenv("VINCENT_TEST_RACE")
	if scope == "" {
		scope = defaultRaceScope()
	}
	switch scope {
	case "all":
		return gotestsum(bin, dir, report, []string{"./..."}, true, "")
	case "none":
		return gotestsum(bin, dir, report, []string{"./..."}, false, "")
	}
	raced, err := listPackages(dir, strings.Split(scope, ","))
	if err != nil {
		return err
	}
	all, err := listPackages(dir, []string{"./..."})
	if err != nil {
		return err
	}
	inRace := make(map[string]bool, len(raced))
	for _, p := range raced {
		inRace[p] = true
	}
	var plain []string
	for _, p := range all {
		if !inRace[p] {
			plain = append(plain, p)
		}
	}
	// The passes run at once, not one after the other (review F1): run in
	// series, the race pass's long package queued behind the whole plain
	// pass, and the Windows step was no shorter than the full race run it
	// replaced. Their output interleaves line by line; each pass's reports
	// are its own.
	raceErr := make(chan error, 1)
	go func() { raceErr <- gotestsum(bin, dir, report, raced, true, "-race") }()
	var plainErr error
	if len(plain) > 0 {
		plainErr = gotestsum(bin, dir, report, plain, false, "")
	}
	return errors.Join(plainErr, <-raceErr)
}

// defaultRaceScope is TestCI's race scope when VINCENT_TEST_RACE is unset;
// see TestCI for why each OS gets the one it does.
func defaultRaceScope() string {
	switch runtime.GOOS {
	case "linux":
		return "all"
	case "windows":
		return "./internal/procx,./internal/daemon,./internal/service,./internal/taskrun"
	default:
		return "none"
	}
}

// listPackages resolves package patterns to import paths, in dir's module.
func listPackages(dir string, patterns []string) ([]string, error) {
	cmd := exec.Command("go", append([]string{"list"}, patterns...)...)
	cmd.Dir = dir
	cmd.Stderr = os.Stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("go list %s: %w", strings.Join(patterns, " "), err)
	}
	return strings.Fields(string(out)), nil
}

// gotestsum runs one of TestCI's passes over pkgs, with or without -race,
// writing its reports to report with suffix before each extension.
func gotestsum(bin, dir, report string, pkgs []string, race bool, suffix string) error {
	args := []string{
		"--format", "standard-quiet",
		"--rerun-fails=2",
		"--rerun-fails-abort-on-data-race",
		"--rerun-fails-report", filepath.Join(report, "reruns"+suffix+".txt"),
		"--packages", strings.Join(pkgs, " "),
		"--junitfile", filepath.Join(report, "junit"+suffix+".xml"),
		"--jsonfile", filepath.Join(report, "test"+suffix+".json"),
		"--",
	}
	name := "gotestsum"
	if race {
		args = append(args, "-race")
		name += " -race"
	}
	args = append(args, "-timeout", "30m")
	cmd := exec.Command(bin, args...)
	cmd.Dir = dir
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	return nil
}

// summarizeReruns appends TestCI's "Tests rerun" section, covering every
// pass's rerun file, to the GitHub job summary, and does nothing outside
// Actions.
func summarizeReruns(reruns []string, failed bool) error {
	summary := os.Getenv("GITHUB_STEP_SUMMARY")
	if summary == "" {
		return nil
	}
	var lines []string
	for _, f := range reruns {
		data, err := os.ReadFile(f)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		for _, l := range strings.Split(string(data), "\n") {
			if l = strings.TrimSpace(l); l != "" {
				lines = append(lines, l)
			}
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "### Tests rerun (%s)\n\n", runtime.GOOS)
	switch {
	case len(lines) > 0:
		b.WriteString("Each line is a test that failed at least once; one whose failures are fewer than its runs passed on a rerun.\n\n")
		for _, l := range lines {
			fmt.Fprintf(&b, "- `%s`\n", l)
		}
	case failed:
		b.WriteString("No test was rerun, and the run failed: more than 10 failures skip reruns entirely, and neither a build failure nor a data race is ever rerun. See the step log.\n")
	default:
		b.WriteString("No test needed a rerun.\n")
	}
	b.WriteString("\n")
	f, err := os.OpenFile(summary, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(b.String()); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// Lint runs golangci-lint, pinned via the go.mod tool directive.
//
// --allow-parallel-runners because vincent's own workloads run this target
// from several worktrees of this repository at once. golangci-lint otherwise
// takes a machine-wide file lock and the losing instance exits 3 with
// "parallel golangci-lint is running" — a lint failure that says nothing about
// the code and that a rerun makes disappear. Its caches are per-analysis and
// the shared one underneath is the go build cache, which is concurrency-safe.
// CI runs one instance per job, so this changes nothing there.
func Lint() error {
	return sh.RunV("go", "tool", "golangci-lint", "run", "--allow-parallel-runners")
}

// LintAll runs golangci-lint once per platform vincent ships on — linux,
// darwin and windows — from this host, so a finding in a build-tagged file
// (`_windows.go`, `_darwin.go`, …) is reported wherever it is run.
//
// It exists because CI lints once, on the Linux leg, for all three platforms
// (#727): the repository has no cgo, so linting with GOOS set sees exactly
// the files a native run would, and the macOS and Windows legs need not spend
// their time on it. Like Vuln it invokes one host-built binary with GOOS in
// its environment: `GOOS=… go tool golangci-lint` cross-builds the linter and
// then cannot execute it (see CLAUDE.md). Every GOOS runs even after one
// fails, so a single run reports the findings of all three.
func LintAll() error {
	bin, err := sh.Output("go", "tool", "-n", "golangci-lint")
	if err != nil {
		return fmt.Errorf("locating golangci-lint: %w", err)
	}
	var failed []string
	for _, goos := range []string{"linux", "darwin", "windows"} {
		fmt.Printf("== golangci-lint GOOS=%s\n", goos)
		if err := sh.RunWithV(map[string]string{"GOOS": goos}, bin, "run", "--allow-parallel-runners"); err != nil {
			failed = append(failed, goos)
		}
	}
	if len(failed) > 0 {
		return fmt.Errorf("golangci-lint failed for GOOS=%s", strings.Join(failed, ","))
	}
	return nil
}

// Vuln reports known vulnerabilities reachable from this module's code, for
// every platform vincent ships on, via govulncheck pinned by the go.mod tool
// directive. Needs network access to fetch the Go vulnerability database.
//
// The GOOS sweep is not ceremony: 15 packages reach the binary only on Windows
// (golang.org/x/sys/windows/svc, modernc.org/libc/*), so a host-only run would
// report a vulnerability in one of those as unreachable. And it has to invoke
// one host-built binary with GOOS set in its environment rather than
// `GOOS=… go tool govulncheck`, which cross-builds the tool and then cannot
// execute it — the same trap `go tool golangci-lint` has (see CLAUDE.md).
func Vuln() error {
	bin, err := sh.Output("go", "tool", "-n", "govulncheck")
	if err != nil {
		return fmt.Errorf("locating govulncheck: %w", err)
	}
	for _, goos := range []string{"linux", "darwin", "windows"} {
		fmt.Printf("== govulncheck GOOS=%s\n", goos)
		if err := sh.RunWithV(map[string]string{"GOOS": goos}, bin, "./..."); err != nil {
			return fmt.Errorf("govulncheck GOOS=%s: %w", goos, err)
		}
	}
	return nil
}

func ldflags() string {
	version := "dev"
	if v, err := sh.Output("git", "describe", "--tags", "--always", "--dirty"); err == nil && v != "" {
		version = v
	}
	commit, _ := sh.Output("git", "rev-parse", "--short", "HEAD")
	date := time.Now().UTC().Format("2006-01-02")
	return fmt.Sprintf("-X %[1]s.version=%[2]s -X %[1]s.commit=%[3]s -X %[1]s.date=%[4]s",
		versionPkg, version, commit, date)
}
