//go:build unix

package workflow

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/goccy/go-yaml"

	"github.com/lezli01/vincent/internal/testrepo"
)

// The classify paths of github-resolve-issue-dag, by the exit code its
// `classify` step leaves behind.
const (
	planPathBug = iota
	planPathEnhancement
	planPathDocumentation
	planPathCI
)

var planPaths = map[int]string{
	planPathBug:           "bug",
	planPathEnhancement:   "enhancement",
	planPathDocumentation: "documentation",
	planPathCI:            "ci",
}

// planUnit is one line of `.vincent-issue/units.jsonl` as plan-dag writes it.
type planUnit struct {
	ID    string   `json:"id"`
	Title string   `json:"title"`
	Needs []string `json:"needs"`
	Files string   `json:"files"`
	Check string   `json:"check"`
	Brief string   `json:"brief"`
}

func unit(id, files string, needs ...string) planUnit {
	if needs == nil {
		needs = []string{}
	}
	return planUnit{ID: id, Title: "unit " + id, Needs: needs, Files: files, Check: "true", Brief: "brief"}
}

// plan-dag's check enforces mechanically what a human reviewer was once asked
// (#935, #936): the units' `files` expressions must not overlap on a tracked
// path, and none may match a path a later step owns. A violation fails the
// check, which §8.4 hands back to the planner as retry feedback; depth past
// three rounds is only advised.
//
// The check is extracted from the shipped workflow and rendered through the
// step template engine for each classify path, so this asserts what the
// daemon runs. Unix-only: the workflow is `platforms: [posix]` (§8.1.1) and
// its checks run under `/bin/sh` (§8.3).
func TestPlanDAGCheckEnforcesUnitFiles(t *testing.T) {
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not installed")
	}
	check := planDAGCheck(t)

	all := []int{planPathBug, planPathEnhancement, planPathDocumentation, planPathCI}
	offDocs := []int{planPathBug, planPathEnhancement, planPathCI}

	cases := []struct {
		name  string
		units []planUnit
		// fail lists the classify paths on which the check must fail; it
		// passes on every other one.
		fail []int
		// stderr must contain every one of these on a failing path, and on a
		// passing path for the advisory case.
		stderr []string
	}{
		{
			name:  "single unit",
			units: []planUnit{unit("store", "^internal/store/")},
		},
		{
			name: "disjoint units",
			units: []planUnit{
				unit("store", "^internal/store/"),
				unit("api", "^internal/api/", "store"),
			},
		},
		{
			name: "overlap on a tracked path",
			units: []planUnit{
				unit("store", "^internal/store/"),
				unit("api", "^internal/api/|^internal/store/migrations/"),
			},
			fail:   all,
			stderr: []string{"overlap: internal/store/migrations/0001_init.sql matched by store, api"},
		},
		{
			name:   "broad pattern",
			units:  []planUnit{unit("all", ".")},
			fail:   all,
			stderr: []string{"forbidden: unit all matches CHANGELOG.md", "forbidden: unit all matches .vincent-issue/x"},
		},
		{
			name:   "changelog",
			units:  []planUnit{unit("log", "^CHANGELOG\\.md$")},
			fail:   all,
			stderr: []string{"forbidden: unit log matches CHANGELOG.md"},
		},
		{
			name:   "scratch directory",
			units:  []planUnit{unit("scratch", "^\\.vincent-issue/")},
			fail:   all,
			stderr: []string{"forbidden: unit scratch matches .vincent-issue/x"},
		},
		{
			name:   "docs root",
			units:  []planUnit{unit("docs", "^docs/")},
			fail:   offDocs,
			stderr: []string{"forbidden: unit docs matches docs/x"},
		},
		{
			// The case the tracked-file set exists for: no synthetic probe
			// matches an expression this narrow.
			name:   "narrow docs expression",
			units:  []planUnit{unit("spec", "^docs/spec\\.md$")},
			fail:   offDocs,
			stderr: []string{"forbidden: unit spec matches docs/spec.md"},
		},
		{
			name:   "gate records",
			units:  []planUnit{unit("gate", "^docs/gates/")},
			fail:   []int{planPathBug, planPathEnhancement},
			stderr: []string{"forbidden: unit gate matches docs/gates/x.md"},
		},
		{
			name:   "skills",
			units:  []planUnit{unit("skill", "^skills/vincent-workflows/")},
			fail:   offDocs,
			stderr: []string{"forbidden: unit skill matches skills/vincent-workflows/SKILL.md"},
		},
		{
			name:   "built-in workflows",
			units:  []planUnit{unit("builtin", "^internal/workflow/builtin\\.go$")},
			fail:   offDocs,
			stderr: []string{"forbidden: unit builtin matches internal/workflow/builtin.go"},
		},
		{
			name:   "invalid expression",
			units:  []planUnit{unit("bad", "^internal/(store/")},
			fail:   all,
			stderr: []string{"invalid: unit bad"},
		},
		{
			// jq accepts any non-empty files string; once blank lines are
			// stripped nothing is left, and grep -Ef with an empty pattern
			// file matches nothing on GNU and everything on BSD.
			name:   "blank files",
			units:  []planUnit{unit("blank", " \n\t")},
			fail:   all,
			stderr: []string{"invalid: unit blank: files has no non-blank line"},
		},
		{
			name: "four rounds is advised, not refused",
			units: []planUnit{
				unit("a", "^internal/store/migrations/"),
				unit("b", "^internal/store/db\\.go$", "a"),
				unit("c", "^internal/api/", "b"),
				unit("d", "^cmd/", "c"),
			},
			stderr: []string{"advisory: the graph is 4 rounds deep"},
		},
		{
			// One representative of the jq program's clauses, to show the new
			// block did not displace them.
			name: "cycle",
			units: []planUnit{
				unit("a", "^internal/store/", "b"),
				unit("b", "^internal/api/", "a"),
			},
			fail: all,
		},
		{
			name: "seven units",
			units: []planUnit{
				unit("a", "^a/"), unit("b", "^b/"), unit("c", "^c/"), unit("d", "^d/"),
				unit("e", "^e/"), unit("f", "^f/"), unit("g", "^g/"),
			},
			fail: all,
		},
	}

	for _, tc := range cases {
		for _, path := range all {
			t.Run(tc.name+"/"+planPaths[path], func(t *testing.T) {
				script := renderPlanDAGCheck(t, check, path)
				repo := planDAGRepo(t, tc.units)
				failed, stderr := runPlanDAGCheck(t, repo, script)

				wantFail := false
				for _, p := range tc.fail {
					wantFail = wantFail || p == path
				}
				if failed != wantFail {
					t.Fatalf("check failed = %v, want %v\nstderr:\n%s", failed, wantFail, stderr)
				}
				if wantFail || tc.fail == nil {
					for _, want := range tc.stderr {
						if !strings.Contains(stderr, want) {
							t.Errorf("stderr lacks %q:\n%s", want, stderr)
						}
					}
				}
				if out := testrepo.Run(t, repo, "status", "--porcelain", "--untracked-files=all"); out != "?? .vincent-issue/units.jsonl" {
					t.Errorf("the check left the worktree changed:\n%s", out)
				}
			})
		}
	}
}

// planDAGRepo is the tree a planner finds: every root the check treats
// specially, plus ordinary packages, with the plan written untracked.
func planDAGRepo(t *testing.T, units []planUnit) string {
	t.Helper()
	repo := testrepo.Init(t, "main")
	for _, p := range []string{
		"CHANGELOG.md",
		"docs/spec.md",
		"docs/gates/x.md",
		"skills/vincent-workflows/SKILL.md",
		"internal/workflow/builtin.go",
		"internal/store/db.go",
		"internal/store/migrations/0001_init.sql",
		"internal/api/server.go",
		"cmd/vincent/main.go",
	} {
		testrepo.WriteFile(t, repo, p, p+"\n")
	}
	testrepo.Run(t, repo, "add", "-A")
	testrepo.Run(t, repo, "commit", "-q", "-m", "base")

	var buf bytes.Buffer
	for _, u := range units {
		line, err := json.Marshal(u)
		if err != nil {
			t.Fatal(err)
		}
		buf.Write(line)
		buf.WriteByte('\n')
	}
	testrepo.WriteFile(t, repo, ".vincent-issue/units.jsonl", buf.String())
	return repo
}

func runPlanDAGCheck(t *testing.T, repo, script string) (failed bool, stderr string) {
	t.Helper()
	cmd := exec.Command("/bin/sh", "-c", script)
	cmd.Dir = repo
	var errBuf bytes.Buffer
	cmd.Stderr = &errBuf
	err := cmd.Run()
	var exit *exec.ExitError
	if err != nil && !errors.As(err, &exit) {
		t.Fatalf("run check: %v", err)
	}
	return err != nil, errBuf.String()
}

func renderPlanDAGCheck(t *testing.T, check string, path int) string {
	t.Helper()
	script, err := Render("check", check, RenderContext{
		Steps: map[string]StepResult{"classify": {Status: "succeeded", ExitCode: path}},
	})
	if err != nil {
		t.Fatalf("render plan-dag check for the %s path: %v", planPaths[path], err)
	}
	return script
}

// planDAGCheck reads plan-dag's `check` out of the shipped workflow rather
// than restating it.
func planDAGCheck(t *testing.T) string {
	t.Helper()
	path := filepath.Join("..", "..", ".vincent", "workflows", "github-resolve-issue-dag.yaml")
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var doc struct {
		Steps []struct {
			ID    string `yaml:"id"`
			Check string `yaml:"check"`
		} `yaml:"steps"`
	}
	if err := yaml.Unmarshal(src, &doc); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	for _, s := range doc.Steps {
		if s.ID == "plan-dag" {
			if s.Check == "" {
				t.Fatalf("plan-dag in %s has no check", path)
			}
			return s.Check
		}
	}
	t.Fatalf("no plan-dag step in %s", path)
	return ""
}
