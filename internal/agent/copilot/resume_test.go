package copilot

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/agent"
)

// resumeFixtureSession is the session resume_1.0.105.jsonl resumed: the
// result line's sessionId, which session_id_1.0.105.jsonl started.
const resumeFixtureSession = "08c643a5-5d5d-4e0e-8a60-6d4051ef9d5b"

// TestBuildArgsResume pins the resumed argv (task 138.11): `--resume=<id>`
// in its `=` spelling, no --session-id, and every other flag a first run
// passes.
func TestBuildArgsResume(t *testing.T) {
	const id = "11111111-2222-4333-8444-555555555555"
	got, err := buildArgs(agent.RunSpec{
		PermissionMode: agent.FullAuto, ResumeSessionID: id, Model: "gpt-6-luna", Effort: "low",
	}, id)
	if err != nil {
		t.Fatalf("buildArgs: %v", err)
	}
	want := []string{
		"--output-format", "json", "--no-auto-update", "--resume=" + id,
		"--allow-all-tools", "--allow-all-paths", "--allow-all-urls",
		"--model", "gpt-6-luna", "--reasoning-effort", "low",
	}
	if !slices.Equal(got, want) {
		t.Errorf("buildArgs = %q, want %q", got, want)
	}
	for _, a := range got {
		if a == "--session-id" || strings.HasPrefix(a, "--trust-folder") {
			t.Errorf("resumed argv carries %q", a)
		}
	}
}

// TestResumeFixture: the resumed run's session is the one it resumed, and
// its tokens are the checkpoint's session totals, the first run's included
// (§9.9; chatrun takes the delta, decision 1).
func TestResumeFixture(t *testing.T) {
	res := resultOf(t, parseFixture(t, "resume_1.0.105.jsonl"))
	got := assemble(0, resumeFixtureSession, true, res, nil, "")
	if got.IsError || got.Failure != nil {
		t.Fatalf("resumed run = %+v, want a clean success", got)
	}
	if got.SessionID != resumeFixtureSession {
		t.Errorf("SessionID = %q, want %q", got.SessionID, resumeFixtureSession)
	}
	if got.InputTokens != 13105 || got.OutputTokens != 46 || got.CacheReadTokens != 13056 {
		t.Errorf("tokens = %d/%d/%d, want the session totals 13105/46/13056",
			got.InputTokens, got.OutputTokens, got.CacheReadTokens)
	}
}

// TestClassifyResume: resume_unknown_1.0.105.txt is session_lost on a
// resumed run, and nothing on a fresh run or a successful resume.
func TestClassifyResume(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("testdata", "resume_unknown_1.0.105.txt"))
	if err != nil {
		t.Fatal(err)
	}
	refused := assemble(1, "5fb73928-d652-46ae-b08b-9548a753cfd6", true, nil, nil, string(b))
	if refused.Failure == nil || refused.Failure.Kind != agent.FailureSessionLost {
		t.Errorf("refused resume Failure = %+v, want session_lost", refused.Failure)
	}
	if !refused.IsError {
		t.Error("refused resume IsError = false")
	}
	if f := assemble(1, "id", false, nil, nil, string(b)).Failure; f != nil {
		t.Errorf("fresh run Failure = %+v, want nil: only a resume can lose its session", f)
	}
	ok := agent.RunResult{ResultText: string(b)}
	if f := classifyResume(ok, string(b), true); f != nil {
		t.Errorf("successful resume Failure = %+v, want nil", f)
	}
}

// TestCapabilities: copilot can hold a chat, a restricted one too (task
// 138.10: restricted_resume_1.0.105.jsonl stayed bounded), and reports
// session totals (decision 1).
func TestCapabilities(t *testing.T) {
	a := New(nil)
	if !agent.CanResume(a) {
		t.Error("CanResume = false, want true (resume_1.0.105.jsonl)")
	}
	if !agent.CanResumeRestricted(a) {
		t.Error("CanResumeRestricted = false, want true (restricted_resume_1.0.105.jsonl)")
	}
	if !agent.UsageIsSessionTotal(a) {
		t.Error("UsageIsSessionTotal = false, want true")
	}
}

// TestRunResume drives two runs through the fake CLI's session store: the
// second resumes the first's minted id, recalls its prompt and reports it as
// SessionID; a resume of an id the store never held is session_lost.
func TestRunResume(t *testing.T) {
	a := fakeAdapter(t)
	env := append(os.Environ(), "FAKEAGENT_SCENARIO=success", "FAKEAGENT_SESSION_DIR="+t.TempDir())
	run := func(prompt, resume string) (agent.RunResult, string) {
		t.Helper()
		h, err := a.Start(t.Context(), agent.RunSpec{
			Prompt: prompt, WorkDir: t.TempDir(), PermissionMode: agent.FullAuto,
			Env: env, ResumeSessionID: resume,
		})
		if err != nil {
			t.Fatalf("Start: %v", err)
		}
		var text strings.Builder
		for ev := range h.Events() {
			text.WriteString(ev.Text)
			text.WriteString("\n")
		}
		res, err := h.Wait()
		if err != nil {
			t.Fatalf("Wait: %v", err)
		}
		if resume != "" && !slices.Contains(h.Argv(), "--resume="+resume) {
			t.Errorf("argv %q lacks --resume=%s", h.Argv(), resume)
		}
		return res, text.String()
	}

	first, _ := run("remember the codeword", "")
	if first.IsError || !uuidRe.MatchString(first.SessionID) {
		t.Fatalf("first run = %+v", first)
	}
	second, text := run("what was it?", first.SessionID)
	if second.IsError || second.SessionID != first.SessionID {
		t.Fatalf("resumed run = %+v, want a success on session %s", second, first.SessionID)
	}
	if !strings.Contains(text, "recalled: remember the codeword") {
		t.Errorf("resumed run did not recall turn 1:\n%s", text)
	}
	if second.InputTokens != 200 {
		t.Errorf("resumed InputTokens = %d, want the session total 200", second.InputTokens)
	}

	const unknown = "5fb73928-d652-46ae-b08b-9548a753cfd6"
	lost, _ := run("hello?", unknown)
	if lost.Failure == nil || lost.Failure.Kind != agent.FailureSessionLost {
		t.Errorf("unknown resume Failure = %+v, want session_lost", lost.Failure)
	}
	if lost.SessionID != unknown {
		t.Errorf("unknown resume SessionID = %q, want %q", lost.SessionID, unknown)
	}
}
