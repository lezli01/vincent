package cli

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/testrepo"
)

// `vincent task diff` runs against the real API handlers and a real git
// repository, on the transcript command's harness: the command's piped output
// is promised to be the endpoint's bytes, and only the endpoint can say what
// those are.

// diffRepo is a repository on `main`, configured so a developer's global git
// config cannot change the diff's spelling under the test. Callers branch
// `feature` off it at whatever point their base should be.
func diffRepo(t *testing.T) string {
	t.Helper()
	dir := testrepo.Init(t, "main")
	for _, kv := range [][2]string{
		{"core.autocrlf", "false"},
		{"diff.renames", "true"},
		{"diff.noprefix", "false"},
		{"diff.mnemonicPrefix", "false"},
		{"color.ui", "false"},
	} {
		testrepo.Run(t, dir, "config", kv[0], kv[1])
	}
	return dir
}

// addDiffTask records a task whose worktree is dir, based on main.
func (h *liveHarness) addDiffTask(t *testing.T, dir string) string {
	t.Helper()
	task := &store.Task{
		ProjectID: h.projectID, Title: "read the diff", WorkflowName: "adhoc",
		WorkflowSnapshot: "x", BaseBranch: "main", BranchName: "feature",
		State: store.TaskRunning, WorktreePath: dir,
	}
	if err := h.st.CreateTask(t.Context(), task, nil); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	return strconv.FormatInt(task.ID, 10)
}

func (h *liveHarness) client(t *testing.T) *apiclient.Client {
	t.Helper()
	c, err := apiclient.Discover(h.dataDir)
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	return c
}

func commitAll(t *testing.T, dir, msg string) {
	t.Helper()
	testrepo.Run(t, dir, "add", "-A")
	testrepo.Run(t, dir, "commit", "-q", "-m", msg)
}

// laneRepo is a fan-out parent: its own commit, two `--no-ff` lane merges in
// the message §7.6 fixes, and uncommitted work on top. It returns the two
// merge shas.
func laneRepo(t *testing.T) (dir, mergeA, mergeB string) {
	t.Helper()
	dir = diffRepo(t)
	testrepo.Run(t, dir, "checkout", "-q", "-b", "feature")
	testrepo.WriteFile(t, dir, "own.txt", "own\n")
	commitAll(t, dir, "the parent's own work")
	merges := make([]string, 0, 2)
	for i, lane := range []string{"a", "b"} {
		testrepo.Run(t, dir, "checkout", "-q", "-b", "lane-"+lane, "feature")
		testrepo.WriteFile(t, dir, lane+".txt", "lane "+lane+"\n")
		commitAll(t, dir, "lane "+lane)
		testrepo.Run(t, dir, "checkout", "-q", "feature")
		testrepo.Run(t, dir, "merge", "--no-ff", "-q", "-m",
			"Merge lane '"+lane+"' of task "+strconv.Itoa(41+i), "lane-"+lane)
		merges = append(merges, testrepo.Run(t, dir, "rev-parse", "HEAD"))
	}
	testrepo.WriteFile(t, dir, "own.txt", "own\nmore\n")
	return dir, merges[0], merges[1]
}

// Piped, the plain form is the endpoint's body byte for byte — uncommitted
// changes included, and not one escape sequence added.
func TestTaskDiffPlainIsTheEndpointBody(t *testing.T) {
	h := newLiveHarness(t)
	dir := diffRepo(t)
	testrepo.Run(t, dir, "checkout", "-q", "-b", "feature")
	testrepo.WriteFile(t, dir, "feature.txt", "committed\n")
	commitAll(t, dir, "feature")
	testrepo.WriteFile(t, dir, "README.md", "test repo\nuncommitted\n")
	id := h.addDiffTask(t, dir)

	out, errOut, code := runCLI(t, "task", "diff", id)
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (%s)", code, errOut)
	}
	n, _ := strconv.ParseInt(id, 10, 64)
	want, err := h.client(t).Diff(t.Context(), n)
	if err != nil {
		t.Fatalf("endpoint: %v", err)
	}
	if out != want {
		t.Errorf("output =\n%q\nwant the endpoint body\n%q", out, want)
	}
	for _, s := range []string{"feature.txt", "+uncommitted"} {
		if !strings.Contains(out, s) {
			t.Errorf("output is missing %q:\n%s", s, out)
		}
	}
	if strings.Contains(out, "\x1b") {
		t.Errorf("piped output carries an escape sequence:\n%q", out)
	}

	out, _, code = runCLI(t, "task", "diff", id, "--json")
	if code != 0 {
		t.Fatalf("--json exit = %d, want 0", code)
	}
	var body struct {
		Diff *string `json:"diff"`
	}
	if err := json.Unmarshal([]byte(out), &body); err != nil || body.Diff == nil {
		t.Fatalf("--json = %q, want {\"diff\": ...} (%v)", out, err)
	}
	if *body.Diff != want {
		t.Errorf("--json diff = %q, want the endpoint body %q", *body.Diff, want)
	}
}

// --by lane prints every section in the daemon's order under its ASCII header,
// remainder last, and each section is the section's own bytes.
func TestTaskDiffByLanePrintsSectionsInOrder(t *testing.T) {
	h := newLiveHarness(t)
	dir, mergeA, mergeB := laneRepo(t)
	id := h.addDiffTask(t, dir)

	out, errOut, code := runCLI(t, "task", "diff", id, "--by", "lane")
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (%s)", code, errOut)
	}
	headerA := "# lane a (task 41, merge " + mergeA[:12] + ")\n"
	headerB := "# lane b (task 42, merge " + mergeB[:12] + ")\n"
	headerRest := "# remainder (the task's own commits and uncommitted work)\n"
	a, b, rest := strings.Index(out, headerA), strings.Index(out, headerB), strings.Index(out, headerRest)
	if a != 0 || b <= a || rest <= b {
		t.Fatalf("headers out of order (a=%d b=%d remainder=%d):\n%s", a, b, rest, out)
	}
	if lane := out[a:b]; !strings.Contains(lane, "a.txt") || strings.Contains(lane, "b.txt") {
		t.Errorf("lane a's section is not lane a's work:\n%s", lane)
	}
	if remainder := out[rest:]; !strings.Contains(remainder, "+more") || strings.Contains(remainder, "a.txt") {
		t.Errorf("the remainder is not the parent's own work:\n%s", remainder)
	}

	n, _ := strconv.ParseInt(id, 10, 64)
	sections, err := h.client(t).DiffByLane(t.Context(), n)
	if err != nil {
		t.Fatalf("endpoint: %v", err)
	}
	var want strings.Builder
	for _, s := range sections {
		want.WriteString(sectionHeader(s) + "\n" + s.Diff)
	}
	if out != want.String() {
		t.Errorf("output =\n%s\nwant each header followed by its section's bytes:\n%s", out, want.String())
	}
	if strings.Contains(out, "\x1b") {
		t.Errorf("piped output carries an escape sequence:\n%q", out)
	}

	out, _, code = runCLI(t, "task", "diff", id, "--by", "lane", "--json")
	if code != 0 {
		t.Fatalf("--json exit = %d, want 0", code)
	}
	var got []apiclient.DiffSection
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("--by lane --json = %q is not the sections array: %v", out, err)
	}
	if len(got) != 3 || got[0].LaneID != "a" || got[1].MergeCommit != mergeB || !got[2].Remainder {
		t.Errorf("--by lane --json = %+v, want lanes a, b then the remainder", got)
	}
}

// A task that fanned out nothing is one remainder section, and a lane-less
// diff with no change at all still prints that header.
func TestTaskDiffByLaneWithoutLanes(t *testing.T) {
	h := newLiveHarness(t)
	dir := diffRepo(t)
	testrepo.Run(t, dir, "checkout", "-q", "-b", "feature")
	id := h.addDiffTask(t, dir)

	out, _, code := runCLI(t, "task", "diff", id, "--by", "lane")
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	if want := "# remainder (the task's own commits and uncommitted work)\n"; out != want {
		t.Errorf("output = %q, want only the remainder header %q", out, want)
	}

	testrepo.WriteFile(t, dir, "README.md", "changed\n")
	out, _, _ = runCLI(t, "task", "diff", id, "--by", "lane")
	if strings.Count(out, "\n# ") != 0 || !strings.HasPrefix(out, "# remainder") ||
		!strings.Contains(out, "+changed") {
		t.Errorf("a task with no lanes should print one remainder section:\n%s", out)
	}
}

// --stat reads files and counts off the real diff: a binary file, a rename, a
// deletion, a new file, uncommitted lines, and a removed line whose content
// starts with `--`, which a prefix-only reader would take for a file marker.
func TestTaskDiffStat(t *testing.T) {
	h := newLiveHarness(t)
	dir := diffRepo(t)
	testrepo.WriteFile(t, dir, "keep.txt", "one\n-- a comment\ntwo\n")
	testrepo.WriteFile(t, dir, "old.txt", "1\n2\n3\n4\n5\n6\n7\n8\n9\n10\n")
	testrepo.WriteFile(t, dir, "gone.txt", "bye\n")
	commitAll(t, dir, "base files")
	testrepo.Run(t, dir, "checkout", "-q", "-b", "feature")
	testrepo.WriteFile(t, dir, "keep.txt", "one\ntwo\n")
	testrepo.Run(t, dir, "mv", "old.txt", "new.txt")
	testrepo.Run(t, dir, "rm", "-q", "gone.txt")
	testrepo.WriteFile(t, dir, "blob.bin", "\x00\x01\x02\x00")
	testrepo.WriteFile(t, dir, "added.txt", "x\ny\n")
	commitAll(t, dir, "feature")
	testrepo.WriteFile(t, dir, "added.txt", "x\ny\nz\n")
	id := h.addDiffTask(t, dir)

	out, errOut, code := runCLI(t, "task", "diff", id, "--stat", "--json")
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (%s)", code, errOut)
	}
	var rows []fileStat
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatalf("--stat --json = %q: %v", out, err)
	}
	got := map[string]fileStat{}
	for _, r := range rows {
		got[r.Path] = r
	}
	want := map[string]fileStat{
		"added.txt": {Path: "added.txt", Added: 3},
		"blob.bin":  {Path: "blob.bin", Binary: true},
		"gone.txt":  {Path: "gone.txt", Removed: 1},
		"keep.txt":  {Path: "keep.txt", Removed: 1},
		"new.txt":   {Path: "new.txt"},
	}
	if len(got) != len(want) || len(rows) != len(want) {
		t.Errorf("--stat --json = %+v, want %+v", rows, want)
	}
	for path, w := range want {
		if got[path] != w {
			t.Errorf("%s = %+v, want %+v", path, got[path], w)
		}
	}

	out, _, code = runCLI(t, "task", "diff", id, "--stat")
	if code != 0 {
		t.Fatalf("--stat exit = %d, want 0", code)
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if strings.Join(strings.Fields(lines[0]), " ") != "FILE ADDED REMOVED" {
		t.Errorf("--stat header = %q", lines[0])
	}
	for _, want := range []string{"keep.txt +0 -1", "blob.bin binary binary", "added.txt +3 -0"} {
		found := false
		for _, l := range lines[1:] {
			found = found || strings.Join(strings.Fields(l), " ") == want
		}
		if !found {
			t.Errorf("--stat is missing the row %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "\x1b") {
		t.Errorf("the stat table carries an escape sequence:\n%q", out)
	}
}

// --stat --by lane credits each file to its section, with a leading LANE
// column that reads `-` for the remainder.
func TestTaskDiffStatByLane(t *testing.T) {
	h := newLiveHarness(t)
	dir, _, _ := laneRepo(t)
	id := h.addDiffTask(t, dir)

	out, errOut, code := runCLI(t, "task", "diff", id, "--stat", "--by", "lane")
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (%s)", code, errOut)
	}
	var got []string
	for _, l := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		got = append(got, strings.Join(strings.Fields(l), " "))
	}
	want := []string{
		"LANE FILE ADDED REMOVED",
		"a a.txt +1 -0",
		"b b.txt +1 -0",
		"- own.txt +2 -0",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("--stat --by lane =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}

	out, _, code = runCLI(t, "task", "diff", id, "--stat", "--by", "lane", "--json")
	if code != 0 {
		t.Fatalf("--json exit = %d, want 0", code)
	}
	var rows []laneFileStat
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatalf("--stat --by lane --json = %q: %v", out, err)
	}
	if len(rows) != 3 || rows[0].LaneID != "a" || rows[0].ChildTaskID != 41 || rows[0].Path != "a.txt" ||
		!rows[2].Remainder || rows[2].Path != "own.txt" || rows[2].Added != 2 {
		t.Errorf("--stat --by lane --json = %+v", rows)
	}
	for _, key := range []string{`"lane_id"`, `"child_task_id"`, `"remainder"`, `"path"`, `"binary"`} {
		if !strings.Contains(out, key) {
			t.Errorf("--stat --by lane --json is missing %s:\n%s", key, out)
		}
	}
}

// An empty diff is nothing on stdout and exit 0, as `git diff` does; every JSON
// form of it is an empty value, never null.
func TestTaskDiffEmpty(t *testing.T) {
	h := newLiveHarness(t)
	dir := diffRepo(t)
	testrepo.Run(t, dir, "checkout", "-q", "-b", "feature")
	id := h.addDiffTask(t, dir)

	for _, tc := range []struct {
		args []string
		want string
	}{
		{nil, ""},
		{[]string{"--json"}, `{"diff":""}`},
		{[]string{"--stat", "--json"}, `[]`},
		{[]string{"--stat", "--by", "lane", "--json"}, `[]`},
		{[]string{"--stat"}, "FILE ADDED REMOVED"},
	} {
		out, errOut, code := runCLI(t, append([]string{"task", "diff", id}, tc.args...)...)
		if code != 0 {
			t.Errorf("%v: exit = %d, want 0 (%s)", tc.args, code, errOut)
		}
		if got := strings.Join(strings.Fields(out), ""); got != strings.ReplaceAll(tc.want, " ", "") {
			t.Errorf("%v: output = %q, want %q", tc.args, out, tc.want)
		}
	}

	out, _, _ := runCLI(t, "task", "diff", id, "--by", "lane", "--json")
	var sections []apiclient.DiffSection
	if err := json.Unmarshal([]byte(out), &sections); err != nil || len(sections) != 1 || !sections[0].Remainder {
		t.Errorf("--by lane --json on an empty diff = %q, want one remainder section (%v)", out, err)
	}
}

// --by accepts only `lane`, refused before any request in the daemon's words;
// a task with no worktree and an unknown task are the daemon's refusals,
// carried through.
func TestTaskDiffErrors(t *testing.T) {
	h := newLiveHarness(t)
	id := strconv.FormatInt(h.taskID, 10)

	out, errOut, code := runCLI(t, "task", "diff", id, "--by", "x")
	if code != 1 {
		t.Errorf("--by x exit = %d, want 1", code)
	}
	if out != "" || !strings.Contains(errOut, `by must be "lane"; got "x"`) {
		t.Errorf("--by x: stdout %q, stderr %q", out, errOut)
	}
	if n := h.diffCalls.Load(); n != 0 {
		t.Errorf("%d diff requests; --by is refused before sending one", n)
	}

	for _, args := range [][]string{{id}, {id, "--by", "lane"}, {id, "--stat"}} {
		out, errOut, code = runCLI(t, append([]string{"task", "diff"}, args...)...)
		if code != 1 {
			t.Errorf("%v on a task with no worktree: exit = %d, want 1", args, code)
		}
		if out != "" || !strings.Contains(errOut, "Error: task has no worktree yet") {
			t.Errorf("%v: stdout %q, stderr %q, want the daemon's 409 wording", args, out, errOut)
		}
	}

	_, errOut, code = runCLI(t, "task", "diff", "999999")
	if code != 1 || !strings.HasPrefix(errOut, "Error: ") {
		t.Errorf("unknown task: exit %d, stderr %q", code, errOut)
	}
	if _, _, code = runCLI(t, "task", "diff", "seven"); code != 1 {
		t.Errorf("non-numeric id: exit %d, want 1", code)
	}
}
