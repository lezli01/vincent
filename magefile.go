//go:build mage

// Build targets for vincent. Run via `go run mage.go <target> [<target>...]`
// (zero-install) or `go tool mage <target>`; list targets with -l.
package main

import (
	"errors"
	"fmt"
	"hash/fnv"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
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
// it — and each runs to the end whether or not the other fails. CI never
// sets it; it exists so scripts/test-rerun-check.sh can prove every scope
// from any host.
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
//
// Shard mode (#730): VINCENT_TEST_SHARD=i/n runs only shard i of n, so one
// leg's suite can be spread over n jobs; unset, the target runs everything,
// exactly as above. Only CI's Windows leg sets it. Linux and macOS are
// already under ~5 minutes for their slowest package, and macOS already uses
// all five of the account's concurrent macOS jobs, so sharding there would
// only queue. Windows' test step took 16.8 minutes (run 37504276643) because
// of three long poles — internal/api, internal/taskrun and internal/worktree
// — and splitting the suite by package cannot shorten a pole: api and
// taskrun would each still be a ~15-minute shard. Their time is spread over
// hundreds of top-level tests instead (api 468, longest 25.7 s; taskrun 332,
// longest 18.8 s; worktree 81, longest 44 s), so those packages, the
// splitPackages, are split by test name: each top-level test goes to shard
// fnv1a32(name) % n, run through one `-run '^(A|B|…)$'` per package. Every
// other package runs whole in exactly one shard, round-robin over the sorted
// import paths. The race scope still applies per package — a package's
// share is raced iff the package is in scope — and every invocation of a
// shard runs at once, as the plain and race passes do. A split package's
// reports carry the suffix -split-<last path element>, plus -race when it is
// raced (test-split-api.json, test-split-taskrun-race.json), and the summary
// heading names the shard. A malformed value fails the target before any
// test runs or any report is written. VINCENT_TEST_SPLIT, a comma-separated
// list of import paths, replaces splitPackages; like VINCENT_TEST_DIR, it
// exists only for scripts/test-rerun-check.sh.
func TestCI() (err error) {
	// Before anything is written, so a malformed shard leaves no report.
	shard, shards, err := parseShard(os.Getenv("VINCENT_TEST_SHARD"))
	if err != nil {
		return err
	}
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
	// A stale report from an earlier run would be summarized, or read, as
	// this one's — and a run in another scope or shard writes a different
	// set of files, so every pass's reports go, not only the ones this run
	// will overwrite (review F2).
	for _, pattern := range []string{"reruns*.txt", "junit*.xml", "test*.json"} {
		stale, err := filepath.Glob(filepath.Join(report, pattern))
		if err != nil {
			return err
		}
		for _, f := range stale {
			if err := os.Remove(f); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return err
			}
		}
	}
	var passes []testPass
	defer func() {
		reruns := make([]string, 0, len(passes))
		for _, p := range passes {
			reruns = append(reruns, filepath.Join(report, "reruns"+p.suffix+".txt"))
		}
		heading := runtime.GOOS
		if shards > 0 {
			heading += fmt.Sprintf(", shard %d/%d", shard, shards)
		}
		if serr := summarizeReruns(heading, reruns, err != nil); serr != nil && err == nil {
			err = serr
		}
	}()

	dir := os.Getenv("VINCENT_TEST_DIR")
	scope := os.Getenv("VINCENT_TEST_RACE")
	if scope == "" {
		scope = defaultRaceScope()
	}
	if shards > 0 {
		if passes, err = shardPasses(dir, scope, shard, shards); err != nil {
			return err
		}
	} else if passes, err = unshardedPasses(dir, scope); err != nil {
		return err
	}
	// The passes run at once, not one after the other (review F1): run in
	// series, the race pass's long package queued behind the whole plain
	// pass, and the Windows step was no shorter than the full race run it
	// replaced. Their output interleaves line by line; each pass's reports
	// are its own.
	errs := make([]chan error, len(passes))
	for i, p := range passes {
		errs[i] = make(chan error, 1)
		go func() { errs[i] <- gotestsum(bin, dir, report, p) }()
	}
	var all []error
	for _, e := range errs {
		all = append(all, <-e)
	}
	return errors.Join(all...)
}

// testPass is one gotestsum invocation of TestCI: its packages, whether it
// runs under -race, the suffix its reports carry, and, for a split
// package's share of a shard, the -run expression selecting that share.
type testPass struct {
	pkgs   []string
	race   bool
	suffix string
	run    string
}

// unshardedPasses is TestCI's passes without VINCENT_TEST_SHARD: the whole
// suite in one pass for `all` and `none`, and for a package list a plain
// pass over the rest beside a race pass over the listed packages.
func unshardedPasses(dir, scope string) ([]testPass, error) {
	switch scope {
	case "all":
		return []testPass{{pkgs: []string{"./..."}, race: true}}, nil
	case "none":
		return []testPass{{pkgs: []string{"./..."}}}, nil
	}
	raced, err := listPackages(dir, strings.Split(scope, ","))
	if err != nil {
		return nil, err
	}
	all, err := listPackages(dir, []string{"./..."})
	if err != nil {
		return nil, err
	}
	inRace := make(map[string]bool, len(raced))
	for _, p := range raced {
		inRace[p] = true
	}
	var passes []testPass
	var plain []string
	for _, p := range all {
		if !inRace[p] {
			plain = append(plain, p)
		}
	}
	if len(plain) > 0 {
		passes = append(passes, testPass{pkgs: plain})
	}
	return append(passes, testPass{pkgs: raced, race: true, suffix: "-race"}), nil
}

// splitPackages are the packages shard mode splits by test name rather than
// running whole in one shard, each a long pole of CI's Windows leg
// (run 37504276643). VINCENT_TEST_SPLIT replaces the list.
var splitPackages = []string{
	"github.com/lezli01/vincent/internal/api",      // 942 s, plain
	"github.com/lezli01/vincent/internal/taskrun",  // 930 s, under -race
	"github.com/lezli01/vincent/internal/worktree", // 426 s, plain
}

// shardPasses is shard i of n's passes (see TestCI): its round-robin share
// of the whole packages, as a plain and a race pass, and for each split
// package the tests that hash to it.
func shardPasses(dir, scope string, shard, shards int) ([]testPass, error) {
	all, err := listPackages(dir, []string{"./..."})
	if err != nil {
		return nil, err
	}
	inRace := make(map[string]bool)
	switch scope {
	case "all":
		for _, p := range all {
			inRace[p] = true
		}
	case "none":
	default:
		raced, err := listPackages(dir, strings.Split(scope, ","))
		if err != nil {
			return nil, err
		}
		for _, p := range raced {
			inRace[p] = true
		}
	}
	split := splitPackages
	if s := os.Getenv("VINCENT_TEST_SPLIT"); s != "" {
		split = nil
		for _, p := range strings.Split(s, ",") {
			if p = strings.TrimSpace(p); p != "" {
				split = append(split, p)
			}
		}
	}
	isSplit := make(map[string]bool, len(split))
	for _, p := range split {
		isSplit[p] = true
	}
	sort.Strings(all)
	var plain, raced []string
	var splitPasses []testPass
	k := 0
	for _, p := range all {
		if isSplit[p] {
			pass, ok, err := splitPass(dir, p, inRace[p], shard, shards)
			if err != nil {
				return nil, err
			}
			if ok {
				splitPasses = append(splitPasses, pass)
			}
			continue
		}
		if k%shards == shard-1 {
			if inRace[p] {
				raced = append(raced, p)
			} else {
				plain = append(plain, p)
			}
		}
		k++
	}
	var passes []testPass
	if len(plain) > 0 {
		passes = append(passes, testPass{pkgs: plain})
	}
	if len(raced) > 0 {
		passes = append(passes, testPass{pkgs: raced, race: true, suffix: "-race"})
	}
	return append(passes, splitPasses...), nil
}

// splitPass is the pass running the top-level tests of split package pkg
// that belong to shard i of n, and false when the shard owns none of them.
//
// The -run is per package, not one for the whole shard: a package's share
// of api's 468 test names is ~5 KB of argv, while every split package's
// share in one expression would pass Windows' 32 K command-line limit as the
// packages grow.
func splitPass(dir, pkg string, race bool, shard, shards int) (testPass, bool, error) {
	// Listed with the run's own -race, so a race-tagged test file is
	// counted exactly when it is compiled.
	args := []string{"test", "-list", ".*"}
	if race {
		args = append(args, "-race")
	}
	cmd := exec.Command("go", append(args, pkg)...)
	cmd.Dir = dir
	cmd.Stderr = os.Stderr
	out, err := cmd.Output()
	if err != nil {
		return testPass{}, false, fmt.Errorf("go test -list %s: %w", pkg, err)
	}
	var mine []string
	for _, name := range strings.Fields(string(out)) {
		if !topLevelTest.MatchString(name) {
			continue // the trailing "ok  pkg 0.1s" line
		}
		h := fnv.New32a()
		_, _ = h.Write([]byte(name))
		if int(h.Sum32()%uint32(shards)) == shard-1 { //nolint:gosec // shards is a validated positive int.
			mine = append(mine, name)
		}
	}
	if len(mine) == 0 {
		return testPass{}, false, nil
	}
	suffix := "-split-" + path.Base(pkg)
	if race {
		suffix += "-race"
	}
	return testPass{
		pkgs:   []string{pkg},
		race:   race,
		suffix: suffix,
		run:    "^(" + strings.Join(mine, "|") + ")$",
	}, true, nil
}

// topLevelTest matches the names `go test -list` prints for top-level tests,
// fuzz targets and examples.
var topLevelTest = regexp.MustCompile(`^(Test|Fuzz|Example)\w*$`)

// parseShard reads VINCENT_TEST_SHARD: "" is no sharding (0, 0), and
// anything but i/n with 1 <= i <= n is an error.
func parseShard(v string) (shard, shards int, err error) {
	if v == "" {
		return 0, 0, nil
	}
	is, ns, ok := strings.Cut(v, "/")
	if ok {
		shard, err = strconv.Atoi(is)
		if err == nil {
			shards, err = strconv.Atoi(ns)
		}
	}
	if !ok || err != nil || shard < 1 || shard > shards {
		return 0, 0, fmt.Errorf("VINCENT_TEST_SHARD=%q: want i/n with 1 <= i <= n", v)
	}
	return shard, shards, nil
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

// gotestsum runs one of TestCI's passes, writing its reports to report with
// the pass's suffix before each extension.
//
// A split share's -run goes to `go test`, after the --: gotestsum's rerun
// removes it and adds its own -run naming only the failed tests, so the
// shard's expression neither widens a rerun nor blocks one.
func gotestsum(bin, dir, report string, p testPass) error {
	args := []string{
		"--format", "standard-quiet",
		"--rerun-fails=2",
		"--rerun-fails-abort-on-data-race",
		"--rerun-fails-report", filepath.Join(report, "reruns"+p.suffix+".txt"),
		"--packages", strings.Join(p.pkgs, " "),
		"--junitfile", filepath.Join(report, "junit"+p.suffix+".xml"),
		"--jsonfile", filepath.Join(report, "test"+p.suffix+".json"),
		"--",
	}
	name := "gotestsum"
	if p.race {
		args = append(args, "-race")
		name += " -race"
	}
	if p.run != "" {
		args = append(args, "-run", p.run)
		name += " " + p.pkgs[0]
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
// Actions. heading is the OS, and the shard when sharded.
func summarizeReruns(heading string, reruns []string, failed bool) error {
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
	fmt.Fprintf(&b, "### Tests rerun (%s)\n\n", heading)
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
