package workflow

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestShippedExamplesValidate parses every workflow in examples/ against the
// real catalogs, so a broken example fails the build rather than a reader.
//
// The done-when for the examples (T4.4, T5.6) names `vincent workflow
// validate` in CI, which is T4.2 and does not exist yet. This test is the
// same assertion one layer down — the CLI subcommand will call this very
// parser — so the examples are guarded from the day they ship rather than
// from the day the CLI lands.
func TestShippedExamplesValidate(t *testing.T) {
	root := filepath.Join("..", "..", "examples")
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("read examples/: %v", err)
	}

	// Curated catalogs only: this must not spawn a probe, and an example is
	// not allowed to depend on which CLIs the machine happens to have. The
	// loop ceiling is the built-in default, matching what `vincent workflow
	// validate` uses: an example must validate on a machine with no config
	// file, so it cannot lean on a raised `loop.max_iterations`.
	opts := curatedOptions()

	found := 0
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".yaml" {
			continue
		}
		found++
		t.Run(e.Name(), func(t *testing.T) {
			src, err := os.ReadFile(filepath.Join(root, e.Name()))
			if err != nil {
				t.Fatalf("read: %v", err)
			}
			wf, warns, err := Parse(src, opts)
			if err != nil {
				t.Fatalf("does not validate: %v", err)
			}
			if len(warns) > 0 {
				// A shipped example is a template people copy; a catalog
				// warning in one teaches the warning along with the workflow.
				t.Errorf("validates with warnings: %v", warns)
			}
			if wf.Name == "" || len(wf.Steps) == 0 {
				t.Errorf("parsed to an empty workflow: %+v", wf)
			}
			// Parsing is syntax; §8.4 renders with missingkey=error, so an
			// example whose templates do not *execute* fails on a reader's
			// first task rather than here. That is the gap task 044's
			// `vincent workflow render` closes, and this is the same
			// assertion one layer down.
			renderAll(t, wf)
		})
	}
	if found == 0 {
		t.Fatal("examples/ contains no .yaml files; this test would pass vacuously")
	}
}

// commandPosition anchors a pattern where a shell reads a command word: the
// start of a line, or just after `&&`, `||`, `|`, `;` or `(`.
const commandPosition = `(?m)(?:^|&&|\|\||[|;(])\s*`

// portabilityHazards is the "Does not" column of the guide's §10.2 — what a
// command body spelled for `/bin/sh` does that `pwsh` will not — as patterns.
// It is a heuristic, not a parser: it reads the unrendered text, knows nothing
// about quoting, and can be fooled either way. What it is for is the mistake
// fix-and-test shipped with for months, a POSIX-only body in a file that
// claimed to run everywhere, which no reviewer caught because the comment
// beside it said the syntax was portable.
var portabilityHazards = []struct {
	what string
	re   *regexp.Regexp
}{
	{"`!` (pwsh reads it as -not, and rejects a bare command after it)", regexp.MustCompile(commandPosition + `!`)},
	{"`[ … ]`", regexp.MustCompile(commandPosition + `\[\[?\s`)},
	{"`test -…`", regexp.MustCompile(commandPosition + `test\s+-`)},
	{"`for`/`if`/`case`", regexp.MustCompile(commandPosition + `(?:for|if|case)(?:\s|$)`)},
	{"`touch`/`seq`/`cat`/`grep`", regexp.MustCompile(commandPosition + `(?:touch|seq|cat|grep)(?:\s|$)`)},
}

var (
	exitCommand = regexp.MustCompile(commandPosition + `exit(?:\s|$)`)
	wholeExit   = regexp.MustCompile(`^exit(?:\s+[0-9]+)?$`)
)

// portabilityFindings reports every `run:` and `check:` body in wf, at any
// depth, that uses something outside the sh∩pwsh intersection. A step that
// pins `shell:` chose its shell and is not judged; whether the file as a whole
// is meant to run on Windows is the caller's question.
//
// PreviewSteps is the walk because it already reaches everywhere a body can
// sit: top level, a `parallel` group's members, a `loop` body, a declared
// lane's inline steps, a `lane:` template's inline steps and `merge.agent`.
func portabilityFindings(wf *Workflow) []string {
	var out []string
	for _, ps := range PreviewSteps(wf) {
		if ps.Step.Shell != "" {
			continue
		}
		for field, body := range map[string]string{"run": ps.Step.Run, "check": ps.Step.Check} {
			if body == "" {
				continue
			}
			for _, h := range portabilityHazards {
				if h.re.MatchString(body) {
					out = append(out, ps.Path+"."+field+" uses "+h.what+": "+body)
				}
			}
			// pwsh's `&&` takes pipelines, so `… && exit 0` parses as a
			// command named `exit`. Only a body that is nothing but exit is
			// portable.
			if exitCommand.MatchString(body) && !wholeExit.MatchString(strings.TrimSpace(body)) {
				out = append(out, ps.Path+"."+field+" uses `exit` as less than the whole body: "+body)
			}
		}
	}
	return out
}

// TestShippedExamplesArePortable holds every shipped example that may run on
// Windows to the guide's §10.2 intersection. A file whose `platforms:` leaves
// Windows out has declared what it did not port, which is the other honest
// answer (§10.3), and is skipped.
func TestShippedExamplesArePortable(t *testing.T) {
	root := filepath.Join("..", "..", "examples")
	paths, err := filepath.Glob(filepath.Join(root, "*.yaml"))
	if err != nil {
		t.Fatalf("glob examples: %v", err)
	}
	if len(paths) == 0 {
		t.Fatal("examples/ contains no .yaml files; this test would pass vacuously")
	}
	scanned := 0
	for _, path := range paths {
		t.Run(filepath.Base(path), func(t *testing.T) {
			src, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read: %v", err)
			}
			wf, _, err := Parse(src, curatedOptions())
			if err != nil {
				t.Fatalf("does not validate: %v", err)
			}
			if !wf.SupportsPlatform("windows") {
				t.Skipf("declares platforms %v, so Windows is not asked to run it", wf.Platforms)
			}
			scanned++
			for _, f := range portabilityFindings(wf) {
				t.Errorf("%s; spell it in the intersection of sh and pwsh (guide §10.2), pin `shell:`, or declare `platforms:` (§10.3)", f)
			}
		})
	}
	if scanned == 0 {
		t.Fatal("every example is POSIX-only; this test would pass vacuously")
	}
}

// TestPortabilityScanCatchesUndeclaredPOSIX is the scan's negative case, on
// the file that motivated it: fix-and-test's `check: '! go test ./...'` is
// exactly what the scan exists to catch, and it is skipped only because the
// file declares `platforms: [posix]`. Take the declaration away and the scan
// must name that check — otherwise TestShippedExamplesArePortable proves
// nothing.
func TestPortabilityScanCatchesUndeclaredPOSIX(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "..", "examples", "fix-and-test.yaml"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	// A Windows checkout may carry CRLF line endings.
	stripped := regexp.MustCompile(`(?m)^platforms:.*\r?\n`).ReplaceAll(src, nil)
	if len(stripped) == len(src) {
		t.Fatal("fix-and-test.yaml declares no platforms: line to remove")
	}
	wf, _, err := Parse(stripped, curatedOptions())
	if err != nil {
		t.Fatalf("does not validate without platforms: %v", err)
	}
	if !wf.SupportsPlatform("windows") {
		t.Fatalf("still restricted after removing the line: %v", wf.Platforms)
	}
	found := false
	for _, f := range portabilityFindings(wf) {
		if strings.HasPrefix(f, "steps[0].check uses `!`") {
			found = true
		}
	}
	if !found {
		t.Errorf("the scan did not flag reproduce's inverted check; findings: %v", portabilityFindings(wf))
	}
}

// TestPortabilityScanPatterns pins what each hazard does and does not match,
// including the portable spellings the shipped examples rely on.
func TestPortabilityScanPatterns(t *testing.T) {
	for body, want := range map[string]bool{
		"go build ./... && go test ./...":                false,
		`git add -A && git commit -m "{{.Task.Title}}"`:  false,
		"git diff --cached --quiet {{.Task.BaseBranch}}": false,
		"git push -u origin {{.Task.BranchName}}":        false,
		"git config -f out.ini gate.done true":           false,
		"git cat-file -e HEAD:go.mod":                    false,
		"go test -run TestIfCase ./...":                  false,
		"exit 0":                                         false,
		"exit":                                           false,
		"! go test ./...":                                true,
		"go vet ./... && ! go test ./...":                true,
		"[ -f go.mod ]":                                  true,
		"test -f go.mod && go test ./...":                true,
		"for f in *.go; do gofmt -l $f; done":            true,
		"if go test ./...; then exit 1; fi":              true,
		"case $X in a) go test;; esac":                   true,
		"touch done":                                     true,
		"go list ./... | grep -v vendor":                 true,
		"cat go.mod":                                     true,
		"seq 3":                                          true,
		"go test ./... && exit 0":                        true,
		"go test ./...\nexit 1":                          true,
	} {
		wf := &Workflow{Steps: []Step{{ID: "s", Type: StepCommand, Run: body}}}
		if got := len(portabilityFindings(wf)) > 0; got != want {
			t.Errorf("%q flagged = %v, want %v", body, got, want)
		}
		// A pinned shell is the author's choice and is never judged.
		wf.Steps[0].Shell = "sh"
		if got := portabilityFindings(wf); len(got) > 0 {
			t.Errorf("%q with shell: sh was flagged: %v", body, got)
		}
	}
}
