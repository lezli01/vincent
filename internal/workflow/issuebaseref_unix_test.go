//go:build unix

package workflow

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/testrepo"
)

// Issue #449. The issue-resolution workflows decide "has this branch produced
// commits yet?" and "what did this change touch?" against
// `{{.Task.BaseBranch}}` — the **local** base ref. §5.3 already says that ref
// is not the fork point once creation fetched the base: `base_sha` exists
// precisely "because once a task branch starts at a fetched remote tip,
// `base_branch` names a moving ref that is no longer where the task began",
// and it lists the two readers that were given the fix. A workflow template is
// a third reader, and §8.4's `.Task` exposes only `BaseBranch`.
//
// Nothing fetches before these checks read the ref, and the local branch is
// shared with every other worktree of the repo, so a `master` another worktree
// holds checked out stays behind `origin/master` for as long as the task runs.
// The range then spans commits the branch does not own, and both symptoms in
// the report follow from it: a `-gt 0` commit count passes on a branch with no
// commits of its own, and a `-eq 0` one fails for a step that committed
// nothing.
//
// `handle-dependabot.yaml` is scanned alongside the three the report tables.
// Its two sites drive no check — they print the changed-file list and name the
// range in the prose an agent is told to read — but they are the same stale
// read into a report a human approves a merge from.
//
// Unix-only on purpose, for the reason TestDAGResolverBuildScopeCoversStagedGoFiles
// gives: all of these files declare `platforms: [posix]` (§8.1.1) and their
// check bodies run under the daemon's `/bin/sh` (§8.3), so there is no Windows
// behaviour of these pipelines to assert.
func TestIssueWorkflowsReadABaseRefAnotherWorktreeHoldsStale(t *testing.T) {
	repo := staleLocalBaseRepo(t)
	rc := RenderContext{Task: TaskContext{
		ID:         1,
		BaseBranch: "master",
		BranchName: "vincent/1-449-stale-base",
	}}

	t.Run("commit counts", func(t *testing.T) {
		var ran int
		for _, s := range issueWorkflowSites(t, revListCountClause) {
			clause, err := Render("check", s.text, rc)
			if err != nil {
				t.Fatalf("render %s: %v", s, err)
			}
			// `verify` counts against the branch's own remote ref, which is a
			// different question and not this one.
			if rng := headRange.FindString(clause); rng == "" || strings.Contains(rng, rc.Task.BranchName) {
				continue
			}
			ran++
			t.Run(s.name(), func(t *testing.T) {
				// The branch owns no commits, so exactly one of the two
				// spellings may succeed: `diagnose`'s `-eq 0`, which asserts
				// that a step allowed to ask questions committed nothing.
				wantOK := strings.Contains(clause, "-eq 0")
				out, err := runSh(repo, clause)
				if gotOK := err == nil; gotOK != wantOK {
					t.Errorf("%s\n  %s\n  succeeded = %v, want %v (the branch has no commits of its own)\n%s",
						s, clause, gotOK, wantOK, out)
				}
			})
		}
		if ran == 0 {
			t.Fatal("no base-relative commit counts found; the scan no longer matches the shipped files")
		}
	})

	t.Run("changed files", func(t *testing.T) {
		var ran int
		for _, s := range issueWorkflowSites(t, diffNameOnlyLine) {
			line, err := Render("run", s.text, rc)
			if err != nil {
				t.Fatalf("render %s: %v", s, err)
			}
			rng := headRange.FindString(line)
			// `verify` diffs against the branch's own remote ref, and
			// `docs-sync` against the sha it recorded before it ran; both are
			// different questions and not this one.
			if rng == "" || strings.Contains(rng, rc.Task.BranchName) {
				continue
			}
			ran++
			t.Run(s.name(), func(t *testing.T) {
				out, err := runSh(repo, "git diff --name-only "+rng)
				if err != nil {
					t.Fatalf("git diff --name-only %s: %v\n%s", rng, err, out)
				}
				if strings.TrimSpace(out) != "" {
					t.Errorf("%s\n  git diff --name-only %s listed files this task never touched:\n%s",
						s, rng, out)
				}
			})
		}
		if ran == 0 {
			t.Fatal("no base-relative diff sites found; the scan no longer matches the shipped files")
		}
	})

	// The ranges the workflows hand an agent in prose rather than run
	// themselves: the DAG's join and repair prompts tell it to read
	// `git log --oneline <base>..HEAD` for what the lanes produced, and
	// handle-dependabot's approval gate calls `git diff <base>...HEAD` "the
	// bump itself". No check reads them, so a stale base here misleads the
	// agent and the human instead of failing a step — the same defect, one
	// remove away.
	t.Run("prose ranges", func(t *testing.T) {
		var ran int
		for _, s := range issueWorkflowSites(t, proseRangeCommand) {
			// `git diff A..HEAD` in a design comment is not a site; only a
			// range built from the task's base is.
			if !strings.Contains(s.text, "{{") {
				continue
			}
			cmd, err := Render("prompt", s.text, rc)
			if err != nil {
				t.Fatalf("render %s: %v", s, err)
			}
			if rng := headRange.FindString(cmd); rng == "" || strings.Contains(rng, rc.Task.BranchName) {
				continue
			}
			ran++
			t.Run(s.name(), func(t *testing.T) {
				out, err := runSh(repo, cmd)
				if err != nil {
					t.Fatalf("%s: %v\n%s", cmd, err, out)
				}
				if strings.TrimSpace(out) != "" {
					t.Errorf("%s\n  %s\n  showed work this task never did:\n%s", s, cmd, out)
				}
			})
		}
		if ran == 0 {
			t.Fatal("no base-relative prose ranges found; the scan no longer matches the shipped files")
		}
	})
}

// staleLocalBaseRepo reproduces the state task 287 ran in: the project's local
// `master` sits where it did when the task was created, `origin/master` has
// two merged pull requests on top of it, and the task branch was cut from the
// fetched tip the way §10 creation cuts it when `fetch_base_branch` is on.
// The branch has committed nothing of its own.
func staleLocalBaseRepo(t *testing.T) string {
	t.Helper()
	origin := testrepo.InitBare(t)

	repo := testrepo.Init(t, "master")
	testrepo.Run(t, repo, "remote", "add", "origin", origin)
	testrepo.Run(t, repo, "push", "-q", "origin", "master")
	testrepo.Run(t, origin, "symbolic-ref", "HEAD", "refs/heads/master")

	// A second checkout stands in for every other worktree of the repo: it is
	// where the merges land, and it is why this checkout's `master` cannot be
	// fast-forwarded — the ref is held elsewhere.
	parent := t.TempDir()
	peer := filepath.Join(parent, "peer")
	testrepo.Run(t, parent, "clone", "-q", origin, "peer")
	testrepo.Run(t, peer, "config", "user.name", "vincent-test")
	testrepo.Run(t, peer, "config", "user.email", "vincent-test@example.invalid")
	testrepo.Run(t, peer, "config", "commit.gpgsign", "false")
	for _, merged := range []string{"internal/api/pull440.go", "internal/tui/pull443.go"} {
		testrepo.WriteFile(t, peer, merged, "package merged\n")
		testrepo.Run(t, peer, "add", "-A")
		testrepo.Run(t, peer, "commit", "-q", "-m", "Merge pull request for "+merged)
	}
	testrepo.Run(t, peer, "push", "-q", "origin", "master")

	// Creation fetches the base and cuts the branch from the fetched tip
	// (§5.3, §10). The fetch moves `refs/remotes/origin/master` and never the
	// local branch, which is the whole of the hazard.
	testrepo.Run(t, repo, "fetch", "-q", "origin")
	testrepo.Run(t, repo, "checkout", "-q", "--no-track", "-b", "vincent/1-449-stale-base", "origin/master")
	return repo
}

// issueWorkflowSites returns every line of the shipped issue-resolution
// workflows that re matches, with the file and line number the report names,
// so a failure points at the site rather than at a pattern. The clauses are
// pulled out of the files rather than restated, the way stagedBuildScope does
// it: the test asserts what the workflows actually run.
func issueWorkflowSites(t *testing.T, re *regexp.Regexp) []workflowSite {
	t.Helper()
	var sites []workflowSite
	for _, name := range []string{
		"github-resolve-issue.yaml",
		"github-resolve-issue-dag.yaml",
		"github-resolve-issue-unit.yaml",
		"handle-dependabot.yaml",
	} {
		path := filepath.Join("..", "..", ".vincent", "workflows", name)
		f, err := os.Open(path)
		if err != nil {
			t.Fatalf("open %s: %v", path, err)
		}
		scan := bufio.NewScanner(f)
		scan.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
		for line := 1; scan.Scan(); line++ {
			if m := re.FindString(scan.Text()); m != "" {
				sites = append(sites, workflowSite{file: name, line: line, text: m})
			}
		}
		err = scan.Err()
		_ = f.Close()
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
	}
	return sites
}

type workflowSite struct {
	file string
	line int
	text string
}

func (s workflowSite) String() string { return fmt.Sprintf(".vincent/workflows/%s:%d", s.file, s.line) }

func (s workflowSite) name() string {
	return fmt.Sprintf("%s_%d", strings.TrimSuffix(s.file, ".yaml"), s.line)
}

var (
	// The commit-count clause of `diagnose`, `implement`, the repair loop and
	// the DAG's unit lane. `[^"]+` so a fix that changes the range — a
	// remote-tracking ref, or the recorded base commit — is still matched and
	// still asserted.
	revListCountClause = regexp.MustCompile(`test "\$\(git rev-list --count [^"]+\)" -(?:gt|eq) 0`)
	// `docs-scope`, the `.github/workflows` scope probe, the unit lane's
	// stray-file guard and handle-dependabot's report. The whole line, because
	// the range is read off it after rendering.
	diffNameOnlyLine = regexp.MustCompile(`git diff --name-only .*HEAD`)
	// A whole `git log --oneline <ref>..HEAD` / `git diff <ref>..HEAD` written
	// into prompt prose. The ref alternation excludes an option-looking token,
	// which is what keeps the `--name-only` sites above out of this group.
	proseRangeCommand = regexp.MustCompile(`git (?:log --oneline|diff) (?:[A-Za-z0-9_./-]|\{\{[^}]*\}\})+\.\.\.?HEAD`)
	headRange         = regexp.MustCompile(`[A-Za-z0-9_./-]+\.\.\.?HEAD`)
)

// runSh runs one clause under /bin/sh in dir, the way §8.3 runs a check body
// on a POSIX host.
func runSh(dir, script string) (string, error) {
	cmd := exec.Command("/bin/sh", "-c", script)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return string(out), err
}
