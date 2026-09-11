package cli

import (
	"bytes"
	"encoding/json"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/goccy/go-yaml"

	"github.com/lezli01/vincent/examples"
	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/testrepo"
	"github.com/lezli01/vincent/internal/workflow"
)

// ciGuide is the page whose "Starting tasks from CI" section (task 091.1)
// carries the snippets under test, and ciSection is that section's heading.
const (
	ciGuide   = "docs/guides/scripting.md"
	ciSection = "## Starting tasks from CI"
)

// ciBuild is one build as a CI system presents it to a step: the environment
// it sets, and — for TeamCity — the `%…%` references it substitutes into the
// script text before any shell sees it.
type ciBuild struct {
	env    map[string]string
	params map[string]string
}

// ciSnippet is one snippet lifted from the guide, the argv its CI system runs
// it under, and three builds: one, the same one again, and the next one.
type ciSnippet struct {
	name   string
	argv   []string
	script string
	first  ciBuild
	again  ciBuild
	next   ciBuild
}

// TestDocsCISnippetsCreateOnce: the scripting guide hands a reader three CI
// snippets that call `POST /v1/tasks` with an Idempotency-Key derived from the
// build. What they claim is a behaviour — the same build makes one task
// however often the step runs, and the next build makes another — and a
// snippet that does not parse, reads a variable its CI system never sets,
// changes its body between attempts or keys on a constant makes that claim
// false while still reading well. So each is lifted out of the page verbatim
// and run, against a real daemon rather than the handler, because the
// snippets read daemon.json and the token off disk the way the page tells a
// runner to.
//
// The shells run with -u, so a snippet that starts reading a variable nobody
// supplies here fails rather than expanding to nothing — the CI variables of
// the machine running this suite are stripped for the same reason.
func TestDocsCISnippetsCreateOnce(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the snippets are POSIX shell for a runner's sh and bash; " +
			"on Windows the bash on PATH may be WSL's launcher rather than Git Bash")
	}
	for _, tool := range []string{"sh", "bash", "curl", "jq"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not found, and every snippet runs it", tool)
		}
	}
	snippets := ciSnippets(t)

	dataDir, cfgDir := t.TempDir(), t.TempDir()
	t.Cleanup(func() {
		cmd := exec.Command(vincentBin, "daemon", "stop", "--force")
		cmd.Env = append(hermeticEnv(),
			config.EnvDataDir+"="+dataDir, config.EnvConfigDir+"="+cfgDir)
		_, _ = cmd.CombinedOutput()
	})
	// fix-and-test is two agent steps, and a machine that happens to have
	// claude installed must not start one: what is counted is tasks created.
	if err := os.WriteFile(filepath.Join(cfgDir, config.FileName), []byte(
		"agents:\n  claude:\n    path: \"/nonexistent/claude\"\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	// The snippets name the example workflow, installed the way the page
	// says to — the shipped file, not a stand-in with the same name, so a
	// body the real workflow's field contract refuses fails here.
	src, err := examples.Read("fix-and-test")
	if err != nil {
		t.Fatalf("read the fix-and-test example: %v", err)
	}
	repo := testrepo.Init(t, "main")
	testrepo.WriteFile(t, repo,
		filepath.Join(workflow.ProjectDirName, "fix-and-test"+examples.Ext), string(src))
	testrepo.Run(t, repo, "add", ".")
	testrepo.Run(t, repo, "commit", "-q", "-m", "add the fix-and-test workflow")

	if out, code := runVincent(t, dataDir, cfgDir, "daemon", "start"); code != 0 {
		t.Fatalf("daemon start: code %d, out %q", code, out)
	}
	out, code := runVincent(t, dataDir, cfgDir, "project", "add", repo, "--json")
	if code != 0 {
		t.Fatalf("project add: code %d, out %q", code, out)
	}
	var project struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal([]byte(out), &project); err != nil {
		t.Fatalf("project add --json is not JSON: %v (%q)", err, out)
	}
	if project.ID != 1 {
		t.Fatalf("project id = %d, but the snippets send project_id 1", project.ID)
	}

	for _, s := range snippets {
		first := runCISnippet(t, s, s.first, dataDir)
		again := runCISnippet(t, s, s.again, dataDir)
		next := runCISnippet(t, s, s.next, dataDir)
		if again.ID != first.ID {
			t.Errorf("%s: the same build ran twice and made tasks %d and %d, want one",
				s.name, first.ID, again.ID)
		}
		if next.ID == first.ID {
			t.Errorf("%s: the next build got task %d back, want a new one — the key does not vary per build",
				s.name, first.ID)
		}
		if first.Workflow != "fix-and-test" {
			t.Errorf("%s: task %d runs %q, want fix-and-test", s.name, first.ID, first.Workflow)
		}
	}

	// Not trusting the responses that claim a replay: count the rows.
	out, code = runVincent(t, dataDir, cfgDir, "task", "ls", "--json")
	if code != 0 {
		t.Fatalf("task ls: code %d, out %q", code, out)
	}
	var tasks []json.RawMessage
	if err := json.Unmarshal([]byte(out), &tasks); err != nil {
		t.Fatalf("task ls --json is not JSON: %v (%q)", err, out)
	}
	if want := 2 * len(snippets); len(tasks) != want {
		t.Errorf("%d tasks exist, want %d: one per build from each of %d snippets",
			len(tasks), want, len(snippets))
	}
}

// ciSnippets reads the guide's CI section and returns its three snippets,
// failing when one is missing or a fourth appears: a snippet on the page is
// one this test runs.
func ciSnippets(t *testing.T) []ciSnippet {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", filepath.FromSlash(ciGuide)))
	if err != nil {
		t.Fatalf("read %s: %v", ciGuide, err)
	}
	_, section, ok := strings.Cut(string(raw), "\n"+ciSection+"\n")
	if !ok {
		t.Fatalf("%s has no %q section", ciGuide, ciSection)
	}
	section, _, _ = strings.Cut(section, "\n## ")

	blocks := map[string][]string{}
	for _, m := range regexp.MustCompile("(?s)\n```(\\w+)\n(.*?)\n```").FindAllStringSubmatch(section, -1) {
		blocks[m[1]] = append(blocks[m[1]], m[2]+"\n")
	}
	for _, lang := range []string{"yaml", "groovy", "sh"} {
		if len(blocks[lang]) != 1 {
			t.Fatalf("%s: %d ```%s blocks under %q, want 1", ciGuide, len(blocks[lang]), lang, ciSection)
		}
	}
	if len(blocks) != 3 {
		t.Fatalf("%s: code blocks %v under %q, want yaml, groovy and sh — add a new one to this test",
			ciGuide, slices.Sorted(maps.Keys(blocks)), ciSection)
	}

	github := ciBuild{env: map[string]string{
		"GITHUB_SERVER_URL":  "https://github.com",
		"GITHUB_REPOSITORY":  "acme/app",
		"GITHUB_WORKFLOW":    "ci",
		"GITHUB_REF_NAME":    "main",
		"GITHUB_RUN_ID":      "1001",
		"GITHUB_RUN_ATTEMPT": "1",
	}}
	jenkins := ciBuild{env: map[string]string{
		"JOB_NAME":     "apps/app",
		"BUILD_NUMBER": "7",
		"BUILD_TAG":    "jenkins-apps-app-7",
		"BUILD_URL":    "https://jenkins.example/job/apps/job/app/7/",
	}}
	teamcity := ciBuild{
		env: map[string]string{"TEAMCITY_BUILDCONF_NAME": "App tests"},
		params: map[string]string{
			"teamcity.serverUrl": "https://teamcity.example",
			"teamcity.build.id":  "4242",
		},
	}
	return []ciSnippet{
		{
			name:   "GitHub Actions",
			argv:   []string{"bash", "--noprofile", "--norc", "-euo", "pipefail", "-c"},
			script: githubActionsStep(t, blocks["yaml"][0]),
			first:  github,
			// Re-run failed jobs: the same run, the next attempt.
			again: github.with(map[string]string{"GITHUB_RUN_ATTEMPT": "2"}, nil),
			next:  github.with(map[string]string{"GITHUB_RUN_ID": "1002"}, nil),
		},
		{
			name:   "Jenkins",
			argv:   []string{"sh", "-eu", "-c"},
			script: jenkinsShell(t, blocks["groovy"][0]),
			first:  jenkins,
			again:  jenkins,
			next: jenkins.with(map[string]string{
				"BUILD_NUMBER": "8",
				"BUILD_TAG":    "jenkins-apps-app-8",
				"BUILD_URL":    "https://jenkins.example/job/apps/job/app/8/",
			}, nil),
		},
		{
			name:   "TeamCity",
			argv:   []string{"sh", "-eu", "-c"},
			script: blocks["sh"][0],
			first:  teamcity,
			again:  teamcity,
			next:   teamcity.with(nil, map[string]string{"teamcity.build.id": "4243"}),
		},
	}
}

// with returns a copy of b with env and params overlaid.
func (b ciBuild) with(env, params map[string]string) ciBuild {
	out := ciBuild{env: maps.Clone(b.env), params: maps.Clone(b.params)}
	if out.params == nil && params != nil {
		out.params = map[string]string{}
	}
	maps.Copy(out.env, env)
	maps.Copy(out.params, params)
	return out
}

// githubActionsStep parses the workflow the page shows — a YAML error in it
// is a documentation bug — and returns the `run:` of the step that calls the
// API, which has to declare `shell: bash` for the page's Windows claim.
func githubActionsStep(t *testing.T, doc string) string {
	t.Helper()
	var wf struct {
		Jobs map[string]struct {
			Steps []struct {
				Shell string `yaml:"shell"`
				Run   string `yaml:"run"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal([]byte(doc), &wf); err != nil {
		t.Fatalf("the GitHub Actions snippet is not YAML: %v", err)
	}
	var runs []string
	for _, job := range wf.Jobs {
		for _, step := range job.Steps {
			if !strings.Contains(step.Run, "/v1/tasks") {
				continue
			}
			if step.Shell != "bash" {
				t.Errorf("the GitHub Actions step has shell %q, want bash", step.Shell)
			}
			runs = append(runs, step.Run)
		}
	}
	if len(runs) != 1 {
		t.Fatalf("the GitHub Actions snippet has %d steps calling /v1/tasks, want 1", len(runs))
	}
	return runs[0]
}

// jenkinsShell returns the script of the Jenkinsfile's triple-quoted `sh` step as
// the shell receives it. Inside a triple-single-quoted Groovy string a
// backslash ending a line is a line continuation and is dropped with its
// newline; any other backslash is a Groovy escape the page says it avoids, so
// one is a failure rather than something to reinterpret.
func jenkinsShell(t *testing.T, doc string) string {
	t.Helper()
	m := regexp.MustCompile(`(?s)sh '''\n(.*?)'''`).FindStringSubmatch(doc)
	if m == nil {
		t.Fatal("the Jenkins snippet has no sh ''' … ''' step")
	}
	body := m[1]
	for i := range len(body) {
		if body[i] == '\\' && (i+1 == len(body) || body[i+1] != '\n') {
			t.Fatalf("the Jenkins snippet has a backslash Groovy reads as an escape: %q",
				body[i:min(i+8, len(body))])
		}
	}
	return strings.ReplaceAll(body, "\\\n", "")
}

// teamcityRef is a TeamCity parameter reference.
var teamcityRef = regexp.MustCompile(`%([A-Za-z0-9_.]+)%`)

// ciTask is the part of the create response the test reads.
type ciTask struct {
	ID       int64  `json:"id"`
	Workflow string `json:"workflow"`
}

// runCISnippet runs one snippet for one build and decodes the task the API
// answered with. The environment is this process's with every VINCENT_* and
// CI variable removed, plus the build's own and the data directory.
func runCISnippet(t *testing.T, s ciSnippet, b ciBuild, dataDir string) ciTask {
	t.Helper()
	script := s.script
	if b.params != nil {
		script = teamcityRef.ReplaceAllStringFunc(script, func(ref string) string {
			v, ok := b.params[strings.Trim(ref, "%")]
			if !ok {
				t.Fatalf("%s: the snippet references %s, which this test does not supply", s.name, ref)
			}
			return v
		})
		script = strings.ReplaceAll(script, "%%", "%")
	}

	env := []string{config.EnvDataDir + "=" + dataDir}
	for _, kv := range hermeticEnv() {
		name, _, _ := strings.Cut(kv, "=")
		if !isCIVariable(name) {
			env = append(env, kv)
		}
	}
	for k, v := range b.env {
		env = append(env, k+"="+v)
	}

	cmd := exec.Command(s.argv[0], append(s.argv[1:], script)...)
	cmd.Env = env
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("%s snippet: %v\nstderr: %s\nstdout: %s", s.name, err, stderr.String(), out)
	}
	var task ciTask
	if err := json.Unmarshal(out, &task); err != nil || task.ID == 0 {
		t.Fatalf("%s snippet printed %q, want the created task (%v)", s.name, out, err)
	}
	return task
}

// isCIVariable reports whether name is one a CI system sets. This suite runs
// on CI, and a snippet reading GITHUB_SHA would otherwise pass there on the
// runner's own value and fail on a laptop.
func isCIVariable(name string) bool {
	for _, prefix := range []string{"GITHUB_", "RUNNER_", "JENKINS_", "TEAMCITY_", "BUILD_", "JOB_"} {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}
