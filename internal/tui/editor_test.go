package tui

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/lezli01/vincent/internal/apiclient"
)

// retryRecorder is a daemon stand-in that records what the retry endpoint was
// asked to do. The real handler is covered in internal/apiclient; what is
// under test here is which override the editor path sends, and whether it
// sends one at all.
type retryRecorder struct {
	calls []map[string]string
}

func (r *retryRecorder) client(t *testing.T) *apiclient.Client {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)
		call := map[string]string{}
		if len(body) > 0 {
			_ = json.Unmarshal(body, &call)
		}
		r.calls = append(r.calls, call)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":1,"state":"queued","available_actions":["cancel"]}`)
	}))
	t.Cleanup(ts.Close)
	return apiclient.New(ts.URL, "token")
}

// editorDetail is a detail view parked on a blocked agent step, with the
// editor replaced by a function that writes what the "human" typed.
func editorDetail(t *testing.T, client *apiclient.Client, typed string, runErr error) *detail {
	t.Helper()
	d := newDetail(testCtx(t))
	d.client = client
	d.taskID = 1
	d.loaded = true
	d.task = apiclient.TaskDetail{
		Task: apiclient.Task{
			ID: 1, State: "blocked", CurrentStep: 0,
			AvailableActions: []string{apiclient.ActionRetry, apiclient.ActionCancel},
		},
		WorkflowSteps: []apiclient.WorkflowStep{
			{Index: 0, ID: "implement", Type: "agent", Prompt: "original prompt"},
		},
	}
	d.exec = func(cmd *exec.Cmd, fn tea.ExecCallback) tea.Cmd {
		path := cmd.Args[len(cmd.Args)-1]
		if runErr == nil {
			if err := os.WriteFile(path, []byte(typed), 0o600); err != nil {
				t.Fatalf("write edited file: %v", err)
			}
		}
		return func() tea.Msg { return fn(runErr) }
	}
	return d
}

// runEdit presses E and follows the messages through to whatever request the
// path ends up making.
func runEdit(t *testing.T, d *detail) {
	t.Helper()
	_, cmd := d.updateKey(keyPress("E"))
	if cmd == nil {
		return
	}
	msg := runCmd(t, cmd, 10*time.Second)
	edit, ok := msg.(editRetryMsg)
	if !ok {
		t.Fatalf("editor produced %T, want editRetryMsg", msg)
	}
	if next := d.applyEdit(edit); next != nil {
		runCmd(t, next, 10*time.Second)
	}
}

// TestEditRetrySendsTheEditedPrompt: the text a human left in the file is the
// override, under the field the step type dictates.
func TestEditRetrySendsTheEditedPrompt(t *testing.T) {
	rec := &retryRecorder{}
	d := editorDetail(t, rec.client(t), "try it this way instead", nil)
	runEdit(t, d)

	if len(rec.calls) != 1 {
		t.Fatalf("retry called %d times, want 1", len(rec.calls))
	}
	if got := rec.calls[0]["prompt_override"]; got != "try it this way instead" {
		t.Errorf("prompt_override = %q, want the edited text", got)
	}
	if _, ok := rec.calls[0]["run_override"]; ok {
		t.Error("a run_override was sent for an agent step")
	}
}

// TestEditRetryUnchangedSendsAPlainRetry: flagging the attempt ✎ when nothing
// was edited would lie to the timeline.
func TestEditRetryUnchangedSendsAPlainRetry(t *testing.T) {
	rec := &retryRecorder{}
	d := editorDetail(t, rec.client(t), "original prompt", nil)
	runEdit(t, d)

	if len(rec.calls) != 1 {
		t.Fatalf("retry called %d times, want 1", len(rec.calls))
	}
	if len(rec.calls[0]) != 0 {
		t.Errorf("body = %v, want no override at all", rec.calls[0])
	}
}

// TestEditRetryEmptyFileAborts: an empty prompt is never what someone meant,
// and the daemon would happily run it.
func TestEditRetryEmptyFileAborts(t *testing.T) {
	rec := &retryRecorder{}
	d := editorDetail(t, rec.client(t), "   \n", nil)
	runEdit(t, d)

	if len(rec.calls) != 0 {
		t.Fatalf("an emptied file still retried: %v", rec.calls)
	}
	if !strings.Contains(d.actions.status, "empty") {
		t.Errorf("status = %q, want it to explain the abort", d.actions.status)
	}
}

// TestEditRetryReportsAFailedEditor keeps a broken $EDITOR from looking like
// a silently ignored keypress.
func TestEditRetryReportsAFailedEditor(t *testing.T) {
	rec := &retryRecorder{}
	d := editorDetail(t, rec.client(t), "", os.ErrPermission)
	runEdit(t, d)

	if len(rec.calls) != 0 {
		t.Fatalf("a failed editor still retried: %v", rec.calls)
	}
	if !d.actions.statusBad {
		t.Errorf("status = %q, want an error", d.actions.status)
	}
}

// TestEditRetryPicksRunForCommandSteps: the field follows the step type, so
// the client never sends the pair the daemon rejects.
func TestEditRetryPicksRunForCommandSteps(t *testing.T) {
	rec := &retryRecorder{}
	d := editorDetail(t, rec.client(t), "make test", nil)
	d.task.WorkflowSteps = []apiclient.WorkflowStep{
		{Index: 0, ID: "verify", Type: "command", Run: "make check"},
	}
	runEdit(t, d)

	if len(rec.calls) != 1 {
		t.Fatalf("retry called %d times, want 1", len(rec.calls))
	}
	if got := rec.calls[0]["run_override"]; got != "make test" {
		t.Errorf("run_override = %q, want the edited command", got)
	}
}

// TestEditRetryUnavailableOnAGate: a manual step has no prompt and no
// command, so the key does nothing and says why.
func TestEditRetryUnavailableOnAGate(t *testing.T) {
	rec := &retryRecorder{}
	d := editorDetail(t, rec.client(t), "whatever", nil)
	d.task.WorkflowSteps = []apiclient.WorkflowStep{
		{Index: 0, ID: "review", Type: "manual", Instructions: "look at the diff"},
	}
	runEdit(t, d)

	if len(rec.calls) != 0 {
		t.Fatalf("a gate was edit+retried: %v", rec.calls)
	}
	if !strings.Contains(d.actions.status, "gate") {
		t.Errorf("status = %q, want it to name the reason", d.actions.status)
	}
	if strings.Contains(strings.Join(d.detailHints(), " "), "edit+retry") {
		t.Error("the bar offers edit+retry on a step with nothing to edit")
	}
}
