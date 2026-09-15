package cli

import (
	"encoding/json"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/apiclient"
)

// `vincent task show --step` and the `hold` row (task 100). The renderer is
// exercised over apiclient.StepRun values directly: what is asserted is the
// text, and every field it reads is already on the wire.

func strp(s string) *string { return &s }

// fullAttempt is a retried agent step with every recorded field set.
func fullAttempt() apiclient.StepRun {
	code, checkCode := 1, 2
	in, out := int64(1200), int64(340)
	cost := 0.42
	started := time.Date(2026, 9, 15, 10, 0, 0, 0, time.UTC)
	finished := started.Add(90 * time.Second)
	return apiclient.StepRun{
		ID: 501, StepIndex: 1, StepID: "implement", StepType: "agent",
		Attempt: 2, Iteration: 3, LoopTotal: 5, LoopItem: strp("api"), State: "failed",
		Agent: strp("claude"), Model: strp("opus"), Effort: strp("high"),
		AgentSource: strp("step"), ModelSource: strp("workflow"),
		ExitCode: &code, CheckExitCode: &checkCode,
		FailureReason: strp("check_failed"), ResultSummary: "two packages still fail",
		TranscriptPath: strp("/tmp/1-2.jsonl"),
		InputTokens:    &in, OutputTokens: &out, CostUSD: &cost,
		InputWaitMS: 45_000, PromptOverride: true,
		RenderedPrompt: strp("Implement OPS-42.\n\n" +
			"<previous-attempt-failure attempt=\"1\">\nthe build did not compile\n</previous-attempt-failure>\n"),
		RenderedCheck:   strp("go test ./..."),
		RenderedIf:      strp("true"),
		RenderedForEach: strp(`["api","web"]`),
		PermissionMode:  strp("full"),
		TimeoutMS:       30 * 60 * 1000,
		Shell:           strp("/bin/sh"),
		WorkDir:         strp("/tmp/wt/7"),
		StartedAt:       started,
		FinishedAt:      &finished,
	}
}

func renderAttempt(run apiclient.StepRun) string {
	return renderStepRun(run, apiclient.WorkflowStep{}, nil, time.Date(2026, 9, 15, 11, 0, 0, 0, time.UTC))
}

func TestRenderStepRunNotRecordedIsNotRenderedEmpty(t *testing.T) {
	missing := renderAttempt(apiclient.StepRun{ID: 1, StepID: "implement", StepType: "agent", Attempt: 1})
	empty := renderAttempt(apiclient.StepRun{
		ID: 1, StepID: "implement", StepType: "agent", Attempt: 1,
		RenderedPrompt: strp(""),
	})

	if !strings.Contains(missing, "  rendered prompt:\n    "+stepNotRecorded+"\n") {
		t.Errorf("nil prompt does not say it was not recorded:\n%s", missing)
	}
	if !strings.Contains(empty, "  rendered prompt:\n    "+stepRenderedEmpty+"\n") {
		t.Errorf("empty prompt does not say it rendered empty:\n%s", empty)
	}
	if missing == empty {
		t.Error("a missing record and an empty render print the same text")
	}
}

func TestRenderStepRunCommandStep(t *testing.T) {
	got := renderAttempt(apiclient.StepRun{
		ID: 9, StepID: "verify", StepType: "command", Attempt: 1, State: "succeeded",
		RenderedRun: strp("go test ./...\n"), Shell: strp("/bin/sh"), WorkDir: strp("/tmp/wt/7"),
	})
	for _, want := range []string{
		"run 9  step verify  attempt 1  state succeeded\n",
		"  rendered run:\n    go test ./...\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q:\n%s", want, got)
		}
	}
	for _, re := range []string{`(?m)^  shell +/bin/sh$`, `(?m)^  working dir +/tmp/wt/7$`} {
		if !regexp.MustCompile(re).MatchString(got) {
			t.Errorf("no line matching %s:\n%s", re, got)
		}
	}
	// `check:` is optional on a step, so an absent one is not a missing
	// record; neither is a prompt on a command step.
	for _, absent := range []string{"rendered check", "rendered prompt", "if: rendered to"} {
		if strings.Contains(got, absent) {
			t.Errorf("unrecorded %q is printed:\n%s", absent, got)
		}
	}
}

func TestRenderStepRunTruncationNotice(t *testing.T) {
	run := apiclient.StepRun{ID: 1, StepType: "command", RenderedRun: strp("echo hi")}
	if strings.Contains(renderAttempt(run), "64 KiB") {
		t.Error("an untruncated record carries the truncation notice")
	}
	run.InputTruncated = true
	if got := renderAttempt(run); !strings.Contains(got, "input:\n  the record was cut at its 64 KiB ceiling") {
		t.Errorf("a truncated record does not open its input section with the notice:\n%s", got)
	}
}

func TestRenderStepRunMarksAppendedFailure(t *testing.T) {
	const divider = "--- appended by vincent: the previous attempt's failure ---"
	got := renderAttempt(fullAttempt())
	want := "    Implement OPS-42.\n    " + divider + "\n    <previous-attempt-failure attempt=\"1\">\n"
	if !strings.Contains(got, want) {
		t.Errorf("no divider between the workflow's text and the block:\n%s", got)
	}

	first := fullAttempt()
	first.RenderedPrompt = strp("Implement OPS-42.")
	if got := renderAttempt(first); strings.Contains(got, divider) {
		t.Errorf("a prompt with nothing appended prints the divider:\n%s", got)
	}
}

func TestRenderStepRunResolution(t *testing.T) {
	got := renderAttempt(fullAttempt())
	for _, re := range []string{
		`(?m)^  agent +claude \(from the step\)$`,
		`(?m)^  model +opus \(from the workflow\)$`,
		`(?m)^  effort +high \(level not recorded\)$`,
		`(?m)^  timeout +30m$`,
		`(?m)^  check timeout +not recorded`,
		`(?m)^  resolved from +this task's own workflow$`,
	} {
		if !regexp.MustCompile(re).MatchString(got) {
			t.Errorf("no line matching %s:\n%s", re, got)
		}
	}

	def := apiclient.WorkflowStep{ResolvedFrom: []string{"release", "lint"}}
	if got := renderStepRun(fullAttempt(), def, nil, time.Now()); !strings.Contains(got, "release -> lint") {
		t.Errorf("include chain not printed:\n%s", got)
	}
}

func TestRenderStepRunControlFlow(t *testing.T) {
	got := renderStepRun(fullAttempt(), apiclient.WorkflowStep{}, strp("web"), time.Now())
	for _, re := range []string{
		`(?m)^  if: rendered to +true$`,
		`(?m)^  iteration +3 of 5$`,
		`(?m)^  for_each item +api$`,
		`(?m)^  for_each list +1\. api\n +2\. web$`,
		`(?m)^  fan-out lane +web$`,
	} {
		if !regexp.MustCompile(re).MatchString(got) {
			t.Errorf("no line matching %s:\n%s", re, got)
		}
	}

	plain := renderAttempt(apiclient.StepRun{ID: 1, StepType: "command", RenderedRun: strp("exit 0")})
	for _, absent := range []string{"if: rendered to", "for_each", "fan-out lane"} {
		if strings.Contains(plain, absent) {
			t.Errorf("unrecorded %q is printed:\n%s", absent, plain)
		}
	}
}

func TestRenderStepRunIsASCII(t *testing.T) {
	got := renderStepRun(fullAttempt(), apiclient.WorkflowStep{ResolvedFrom: []string{"a", "b"}},
		strp("web"), time.Now())
	for i, r := range got {
		if r > 0x7f {
			t.Fatalf("non-ASCII %q at byte %d:\n%s", r, i, got)
		}
	}
	for _, section := range []string{"\ninput:\n", "\nresolution:\n", "\ncontrol flow:\n", "\noutcome:\n"} {
		if !strings.Contains(got, section) {
			t.Errorf("section %q missing:\n%s", section, got)
		}
	}
}

func TestTaskHoldRows(t *testing.T) {
	until := time.Date(2026, 9, 15, 14, 20, 0, 0, time.UTC)
	local := until.Local().Format(time.RFC3339)
	for _, tc := range []struct {
		name   string
		reason *string
		until  *time.Time
		want   [][2]string
	}{
		{name: "ordinary queue", until: &until},
		{name: "empty reason", reason: strp(""), until: &until},
		{
			name: "usage limit", reason: strp("usage_limit"), until: &until,
			want: [][2]string{{"hold", "usage_limit until " + local}},
		},
		{
			name: "retry backoff", reason: strp("retry_backoff"), until: &until,
			want: [][2]string{{"hold", "retry_backoff until " + local}},
		},
		{
			name: "no resume time", reason: strp("usage_limit"),
			want: [][2]string{{"hold", "usage_limit"}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := taskHoldRows(apiclient.TaskDetail{Task: apiclient.Task{
				State: "queued", QueuedReason: tc.reason, AdmitNotBefore: tc.until,
			}})
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("rows = %q, want %q", got, tc.want)
			}
		})
	}
}

// stubStepDetail is a task detail carrying one recorded command-step run.
func stubStepDetail() string {
	return strings.Replace(stubDetail("done", "", []string{"archive"}), `"steps": []`, `"steps": [{
    "id": 42, "step_index": 0, "step_id": "verify", "step_name": "verify", "step_type": "command",
    "attempt": 1, "iteration": 0, "loop_item": null, "loop_total": 0, "state": "succeeded",
    "agent": null, "model": null, "effort": null, "exit_code": 0, "check_exit_code": null,
    "failure_reason": null, "skip_reason": null, "result_summary": "", "status_message": null,
    "transcript_path": "/tmp/42.jsonl", "input_tokens": null, "output_tokens": null, "cost_usd": null,
    "input_wait_ms": 0, "prompt_override": false, "run_override": false,
    "rendered_prompt": null, "rendered_run": "go test ./...", "rendered_check": null,
    "rendered_if": null, "rendered_for_each": null, "input_truncated": false,
    "agent_source": null, "model_source": null, "effort_source": null, "permission_mode": null,
    "timeout_ms": 600000, "check_timeout_ms": 0, "shell": "/bin/sh", "work_dir": "/tmp/wt/7",
    "started_at": "2026-08-26T10:01:00Z", "finished_at": "2026-08-26T10:02:00Z"
  }]`, 1)
}

func TestTaskShowStepUnknownRun(t *testing.T) {
	out, code := runAgainstStub(t, stubHandler(t, stubStepDetail(), nil), "", "task", "show", "7", "--step", "99")
	if code != 1 || !strings.Contains(out, "Error: task 7 has no step run 99") {
		t.Errorf("unknown run: code %d, out %q; want 1 and the message", code, out)
	}
}

func TestTaskShowStepText(t *testing.T) {
	out, code := runAgainstStub(t, stubHandler(t, stubStepDetail(), nil), "", "task", "show", "7", "--step", "42")
	if code != 0 {
		t.Fatalf("task show --step: code %d, out %q", code, out)
	}
	for _, want := range []string{"run 42  step verify  attempt 1  state succeeded", "  rendered run:\n    go test ./...\n"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q:\n%s", want, out)
		}
	}
	// The task view is not printed underneath: the reader asked for one
	// attempt.
	if strings.Contains(out, "Wire the adapter") {
		t.Errorf("--step printed the task view too:\n%s", out)
	}
}

func TestTaskShowStepJSONIsTheStepRun(t *testing.T) {
	h := stubHandler(t, stubStepDetail(), nil)
	out, code := runAgainstStub(t, h, "", "task", "show", "7", "--step", "42", "--json")
	if code != 0 {
		t.Fatalf("task show --step --json: code %d, out %q", code, out)
	}
	var run apiclient.StepRun
	if err := json.Unmarshal([]byte(out), &run); err != nil {
		t.Fatalf("--step --json is not a StepRun: %v (%q)", err, out)
	}

	out, code = runAgainstStub(t, h, "", "task", "show", "7", "--json")
	if code != 0 {
		t.Fatalf("task show --json: code %d, out %q", code, out)
	}
	var detail apiclient.TaskDetail
	if err := json.Unmarshal([]byte(out), &detail); err != nil || len(detail.Steps) != 1 {
		t.Fatalf("task show --json: %v, %d steps (%q)", err, len(detail.Steps), out)
	}
	if !reflect.DeepEqual(run, detail.Steps[0]) {
		t.Errorf("--step --json = %+v, want the steps[] element %+v", run, detail.Steps[0])
	}
}

func TestTaskShowPrintsHold(t *testing.T) {
	detail := strings.Replace(stubDetail("queued", "", []string{"pause", "cancel"}), `"block_reason": null,`,
		`"block_reason": null, "queued_reason": "usage_limit", "admit_not_before": "2026-09-15T14:20:00Z",`, 1)
	out, code := runAgainstStub(t, stubHandler(t, detail, nil), "", "task", "show", "7")
	if code != 0 {
		t.Fatalf("task show: code %d, out %q", code, out)
	}
	until := time.Date(2026, 9, 15, 14, 20, 0, 0, time.UTC).Local().Format(time.RFC3339)
	if want := "hold      usage_limit until " + until + "\n"; !strings.Contains(out, want) {
		t.Errorf("missing %q:\n%s", want, out)
	}
	if strings.Contains(out, "blocked") {
		t.Errorf("a held task prints a blocked row:\n%s", out)
	}
}
