package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/daemon"
)

// `task show` renders the §7.4 request a task is parked on, and `task answer`
// answers it by the numbers that rendering prints. Both are exercised against
// a hand-written daemon rather than a live one: what is asserted is the
// rendering and the request body, and pinning a real agent to a question it
// asks mid-run would test the agent instead.

// stubDetail is GET /v1/tasks/{id} as the daemon serves it, with the pending
// request and available actions the real detail response carries.
func stubDetail(state, pendingInput string, actions []string) string {
	acts, _ := json.Marshal(actions)
	pending := ""
	if pendingInput != "" {
		pending = `"pending_input": ` + pendingInput + `,`
	}
	return `{
  "id": 7,
  "project_id": 1,
  "project_name": "vincent",
  "title": "Wire the adapter",
  "workflow": "adhoc",
  "state": "` + state + `",
  "base_branch": "main",
  "branch_name": "vincent/7-wire-the-adapter",
  "priority": 0,
  "current_step": 0,
  "step_total": 2,
  "block_reason": null,
  "available_actions": ` + string(acts) + `,
  ` + pending + `
  "steps": [],
  "created_at": "2026-08-26T10:00:00Z",
  "updated_at": "2026-08-26T10:05:00Z"
}`
}

const questionRequest = `{
  "kind": "question",
  "questions": [
    {"text": "Which database?", "options": ["postgres", "sqlite"]},
    {"text": "Which regions?", "header": "deploy", "options": ["eu", "us"], "multi_select": true}
  ]
}`

const permissionRequest = `{
  "kind": "permission",
  "permission": {"tool": "Bash", "summary": "rm -rf build/"}
}`

func TestTaskShowRendersPendingQuestions(t *testing.T) {
	out, code := runAgainstStub(t, stubHandler(t, stubDetail("awaiting_input", questionRequest,
		[]string{"answer", "cancel"}), nil), "", "task", "show", "7")
	if code != 0 {
		t.Fatalf("task show: code %d, out %q", code, out)
	}
	for _, want := range []string{
		"actions   answer, cancel",
		"awaiting input: question",
		"1. Which database?",
		"suggested: postgres, sqlite",
		"2. deploy: Which regions?",
		"(one or more)",
		"vincent task answer 7 --answer 1=<value>",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("task show is missing %q:\n%s", want, out)
		}
	}
}

func TestTaskShowRendersPermissionRequest(t *testing.T) {
	out, code := runAgainstStub(t, stubHandler(t, stubDetail("awaiting_input", permissionRequest,
		[]string{"answer", "cancel"}), nil), "", "task", "show", "7")
	if code != 0 {
		t.Fatalf("task show: code %d, out %q", code, out)
	}
	for _, want := range []string{
		"awaiting input: permission", "tool     Bash", "summary  rm -rf build/", "--allow",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("task show is missing %q:\n%s", want, out)
		}
	}
}

// A task waiting on nothing says nothing: the block exists to be acted on, and
// an empty one would read as a rendering bug.
func TestTaskShowWithoutPendingInput(t *testing.T) {
	out, code := runAgainstStub(t, stubHandler(t, stubDetail("running", "", []string{"cancel", "pause"}), nil),
		"", "task", "show", "7")
	if code != 0 {
		t.Fatalf("task show: code %d, out %q", code, out)
	}
	if strings.Contains(out, "awaiting input") {
		t.Errorf("task show renders a request the task does not have:\n%s", out)
	}
	if !strings.Contains(out, "actions   cancel, pause") {
		t.Errorf("task show does not print the available actions:\n%s", out)
	}
}

// `--json` needed no change for either: emitJSON marshals the detail response,
// which has carried both fields all along. This pins that, so the guide's
// claim that a script can read them cannot quietly stop being true.
func TestTaskShowJSONCarriesPendingInputAndActions(t *testing.T) {
	out, code := runAgainstStub(t, stubHandler(t, stubDetail("awaiting_input", questionRequest,
		[]string{"answer", "cancel"}), nil), "", "task", "show", "7", "--json")
	if code != 0 {
		t.Fatalf("task show --json: code %d, out %q", code, out)
	}
	var doc struct {
		AvailableActions []string        `json:"available_actions"`
		PendingInput     json.RawMessage `json:"pending_input"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("task show --json is not JSON: %v (%q)", err, out)
	}
	if len(doc.AvailableActions) != 2 || len(doc.PendingInput) == 0 {
		t.Errorf("task show --json = %s; want available_actions and pending_input", out)
	}
}

func TestTaskAnswerIndexedQuestions(t *testing.T) {
	var posted []byte
	out, code := runAgainstStub(t,
		stubHandler(t, stubDetail("awaiting_input", questionRequest, []string{"answer"}), &posted),
		"", "task", "answer", "7",
		"--answer", "1=postgres", "--answer", "2=eu", "--answer", "2=us")
	if code != 0 {
		t.Fatalf("task answer: code %d, out %q", code, out)
	}
	var body struct {
		Answers map[string][]string `json:"answers"`
		Allow   *bool               `json:"allow"`
	}
	if err := json.Unmarshal(posted, &body); err != nil {
		t.Fatalf("posted body is not JSON: %v (%q)", err, posted)
	}
	// The wire format stays keyed by question text (§13.2); the index is a
	// CLI convenience that never reaches the daemon.
	if got := body.Answers["Which database?"]; len(got) != 1 || got[0] != "postgres" {
		t.Errorf("answers[Which database?] = %v, want [postgres]", got)
	}
	if got := body.Answers["Which regions?"]; len(got) != 2 || got[0] != "eu" || got[1] != "us" {
		t.Errorf("answers[Which regions?] = %v, want [eu us] — a repeated index is a multi-select", got)
	}
	if body.Allow != nil {
		t.Errorf("allow = %v on a question request", *body.Allow)
	}
}

// Everything after the first '=' is the value, the rule --field already
// documents, so a URL or a regex needs no CLI-specific escaping.
func TestTaskAnswerKeepsEverythingAfterTheFirstEquals(t *testing.T) {
	var posted []byte
	_, code := runAgainstStub(t,
		stubHandler(t, stubDetail("awaiting_input", questionRequest, []string{"answer"}), &posted),
		"", "task", "answer", "7",
		"--answer", "1=https://example.test/?a=b", "--answer", "2=eu")
	if code != 0 {
		t.Fatalf("task answer: code %d", code)
	}
	var body struct {
		Answers map[string][]string `json:"answers"`
	}
	if err := json.Unmarshal(posted, &body); err != nil {
		t.Fatalf("posted body is not JSON: %v (%q)", err, posted)
	}
	if got := body.Answers["Which database?"]; len(got) != 1 || got[0] != "https://example.test/?a=b" {
		t.Errorf("answers[Which database?] = %v, want the whole URL", got)
	}
}

func TestTaskAnswerRejectsAnIndexTheTaskIsNotAsking(t *testing.T) {
	var posted []byte
	out, code := runAgainstStub(t,
		stubHandler(t, stubDetail("awaiting_input", questionRequest, []string{"answer"}), &posted),
		"", "task", "answer", "7", "--answer", "5=nope")
	if code != 1 {
		t.Errorf("answer an index that does not exist: code %d, want 1 (out %q)", code, out)
	}
	if posted != nil {
		t.Errorf("a wrong index still posted an answer: %q", posted)
	}
	if !strings.Contains(out, "no question 5") {
		t.Errorf("the error does not say which index is wrong: %q", out)
	}
}

// Local validation is a fast fail, not the authority: an unanswered question
// costs no round trip to be told the obvious.
func TestTaskAnswerRefusesAnIncompleteAnswer(t *testing.T) {
	var posted []byte
	out, code := runAgainstStub(t,
		stubHandler(t, stubDetail("awaiting_input", questionRequest, []string{"answer"}), &posted),
		"", "task", "answer", "7", "--answer", "1=postgres")
	if code != 1 {
		t.Errorf("answer only the first of two questions: code %d, want 1 (out %q)", code, out)
	}
	if posted != nil {
		t.Errorf("an incomplete answer was posted anyway: %q", posted)
	}
}

func TestTaskAnswerPermission(t *testing.T) {
	var posted []byte
	out, code := runAgainstStub(t,
		stubHandler(t, stubDetail("awaiting_input", permissionRequest, []string{"answer"}), &posted),
		"", "task", "answer", "7", "--allow")
	if code != 0 {
		t.Fatalf("task answer --allow: code %d, out %q", code, out)
	}
	var body struct {
		Answers map[string][]string `json:"answers"`
		Allow   *bool               `json:"allow"`
	}
	if err := json.Unmarshal(posted, &body); err != nil {
		t.Fatalf("posted body is not JSON: %v (%q)", err, posted)
	}
	if body.Allow == nil || !*body.Allow || len(body.Answers) != 0 {
		t.Errorf("posted %q, want allow true and no answers", posted)
	}

	// A permission request is decided, not answered.
	posted = nil
	out, code = runAgainstStub(t,
		stubHandler(t, stubDetail("awaiting_input", permissionRequest, []string{"answer"}), &posted),
		"", "task", "answer", "7", "--answer", "1=yes")
	if code != 1 {
		t.Errorf("--answer on a permission request: code %d, want 1 (out %q)", code, out)
	}
	if posted != nil {
		t.Errorf("--answer on a permission request was posted anyway: %q", posted)
	}
}

// --body is the script's entry point: the §13.2 payload goes to the daemon as
// it stands, with no per-flag reconstruction and no request to read the
// question first.
func TestTaskAnswerBodyIsPassedThrough(t *testing.T) {
	var posted []byte
	out, code := runAgainstStub(t, stubHandler(t, "", &posted),
		`{"answers":{"Which database?":["postgres"],"Which regions?":["eu","us"]}}`,
		"task", "answer", "7", "--body", "-")
	if code != 0 {
		t.Fatalf("task answer --body -: code %d, out %q", code, out)
	}
	var body map[string]map[string][]string
	if err := json.Unmarshal(posted, &body); err != nil {
		t.Fatalf("posted body is not JSON: %v (%q)", err, posted)
	}
	if len(body["answers"]) != 2 || body["answers"]["Which regions?"][1] != "us" {
		t.Errorf("posted %q, want the payload as it was written", posted)
	}
}

func TestTaskAnswerRefusesBodyThatIsNotJSON(t *testing.T) {
	var posted []byte
	out, code := runAgainstStub(t, stubHandler(t, "", &posted), "postgres, please",
		"task", "answer", "7", "--body", "-")
	if code != 1 {
		t.Errorf("--body with prose: code %d, want 1 (out %q)", code, out)
	}
	if posted != nil {
		t.Errorf("prose was posted as an answer payload: %q", posted)
	}
}

// stubHandler answers the two routes these commands use: the detail GET and
// the answer POST, whose body it records when posted is non-nil.
func stubHandler(t *testing.T, detail string, posted *[]byte) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/v1/health":
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		case r.URL.Path == "/v1/tasks/7" && r.Method == http.MethodGet:
			if detail == "" {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_, _ = w.Write([]byte(detail))
		case r.URL.Path == "/v1/tasks/7/answer" && r.Method == http.MethodPost:
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Errorf("read posted body: %v", err)
			}
			if posted != nil {
				*posted = body
			}
			_, _ = w.Write([]byte(`{"id":7,"state":"running"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}
}

// runAgainstStub runs the real command tree in-process against a stub daemon,
// with stdin wired for the `-` forms of --body and --prompt-file.
func runAgainstStub(t *testing.T, h http.HandlerFunc, stdin string, args ...string) (string, int) {
	t.Helper()
	dataDir := t.TempDir()
	t.Setenv(config.EnvDataDir, dataDir)
	t.Setenv(config.EnvConfigDir, t.TempDir())

	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse stub URL: %v", err)
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatalf("stub port: %v", err)
	}
	if _, err := daemon.EnsureToken(dataDir); err != nil {
		t.Fatalf("token: %v", err)
	}
	if err := daemon.WriteRuntimeInfo(dataDir, daemon.RuntimeInfo{
		Port: port, PID: os.Getpid(), StartedAt: time.Now(),
	}); err != nil {
		t.Fatalf("daemon.json: %v", err)
	}

	var buf bytes.Buffer
	root := newRootCmd()
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetIn(strings.NewReader(stdin))
	root.SetArgs(args)
	code := asExitCode(root.ExecuteContext(context.Background()))
	return buf.String(), code
}
