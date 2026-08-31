package codex

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/agent"
)

// resumedThreadID is the id testdata/resume_0.150.1.jsonl was scrubbed to. The
// capture's own UUID is replaced, but with one value on every line, because
// the fact being pinned is that a resumed run reports the id it was given.
const resumedThreadID = "01a0586e-5a43-7001-9628-cde66a53e993"

// TestBuildArgsResume pins the resumed invocation against codex-cli 0.150.1
// (§9.3, task 070). Two things are load-bearing and neither is inferable from
// the fresh shape:
//
//   - `resume` is a subcommand taking the id as a positional, so the argv
//     changes shape rather than gaining a flag.
//   - `exec resume` has no `-s/--sandbox` — it carries only
//     --dangerously-bypass-approvals-and-sandbox — so Restricted has no
//     spelling here and a resumed run is always full-auto (decision 1,
//     guarded in internal/api by TestChatsAreAlwaysFullAuto).
//
// The prompt stays off argv: `resume`'s help documents `-` for stdin, but a
// run with no PROMPT argument reads stdin anyway ("Reading prompt from
// stdin…" on the capture's stderr), which is what RunSpec.Prompt's
// stdin-only contract needs under the Windows argv limit.
func TestBuildArgsResume(t *testing.T) {
	for _, tt := range []struct {
		name string
		spec agent.RunSpec
		want []string
	}{
		{
			name: "resume is a subcommand with the prompt on stdin",
			spec: agent.RunSpec{PermissionMode: agent.FullAuto, ResumeSessionID: "t-1"},
			want: []string{
				"exec", "--json", "resume", "t-1",
				"--dangerously-bypass-approvals-and-sandbox",
			},
		},
		{
			name: "restricted has no spelling on resume, so the run is full-auto",
			spec: agent.RunSpec{PermissionMode: agent.Restricted, ResumeSessionID: "t-1"},
			want: []string{
				"exec", "--json", "resume", "t-1",
				"--dangerously-bypass-approvals-and-sandbox",
			},
		},
		{
			name: "model and effort still ride behind the positional",
			spec: agent.RunSpec{
				Model: "gpt-5.6-sol", Effort: "xhigh", ResumeSessionID: "t-1",
			},
			want: []string{
				"exec", "--json", "resume", "t-1",
				"--dangerously-bypass-approvals-and-sandbox",
				"-m", "gpt-5.6-sol", "-c", "model_reasoning_effort=xhigh",
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := buildArgs(tt.spec)
			if strings.Join(got, " ") != strings.Join(tt.want, " ") {
				t.Errorf("buildArgs = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestResumeFixtureReportsItsThreadID pins the stream half: a resumed run
// opens with `thread.started` carrying the id it was handed, which is what
// RunResult.SessionID must end up holding.
func TestResumeFixtureReportsItsThreadID(t *testing.T) {
	var last string
	for _, line := range fixtureLines(t, "resume_0.150.1.jsonl") {
		if id := threadIDOf(line); id != "" {
			last = id
		}
	}
	if last != resumedThreadID {
		t.Fatalf("thread id = %q, want %q", last, resumedThreadID)
	}
	// And the turn itself parsed, so the id did not come from a stream the
	// normalizer choked on.
	res := terminal(t, parseFixture(t, "resume_0.150.1.jsonl"))
	if res.IsError || res.ResultText == "" {
		t.Fatalf("resumed turn = %+v, want a completed turn with text", res)
	}
}

// TestThreadStartedIsNotAnEvent states what `thread.started` is for: it
// carries the run's identity, not something a transcript reader needs
// normalized. It still arrives verbatim, which is what Raw is for.
func TestThreadStartedIsNotAnEvent(t *testing.T) {
	ev := (&stream{}).parse([]byte(`{"type":"thread.started","thread_id":"t-1"}`))
	if ev.Type != agent.EventUnknown {
		t.Errorf("thread.started = %q, want %q", ev.Type, agent.EventUnknown)
	}
	if len(ev.Raw) == 0 {
		t.Error("thread.started lost its raw line")
	}
	if got := threadIDOf(ev.Raw); got != "t-1" {
		t.Errorf("threadIDOf = %q, want t-1", got)
	}
	if got := threadIDOf([]byte("not json")); got != "" {
		t.Errorf("threadIDOf(garbage) = %q, want empty", got)
	}
}

// TestClassifyResumeMatchesTheCapturedRefusal is decision 2's narrow
// classifier, against the wording codex-cli 0.150.1 actually printed
// (testdata/resume_lost_0.150.1.txt). The refusal arrives on stderr with no
// JSONL at all, which is why Wait's no-result branch is the shape under test.
func TestClassifyResumeMatchesTheCapturedRefusal(t *testing.T) {
	stderr := readFixtureText(t, "resume_lost_0.150.1.txt")
	res := agent.RunResult{
		ExitCode: 1, IsError: true,
		ErrorMessage: "stream ended without a result event: " + stderr,
	}
	f := classifyResume(res, stderr, true)
	if f == nil || f.Kind != agent.FailureSessionLost {
		t.Fatalf("classifyResume = %+v, want %s", f, agent.FailureSessionLost)
	}
}

// TestClassifyResumeIsNarrow is §9.2's rule verbatim, and the reason a
// workflow step can never be misdiagnosed: a run that passed no thread id is
// never a lost session, whatever it says.
func TestClassifyResumeIsNarrow(t *testing.T) {
	stderr := readFixtureText(t, "resume_lost_0.150.1.txt")
	failed := agent.RunResult{ExitCode: 1, IsError: true, ErrorMessage: stderr}
	if f := classifyResume(failed, stderr, false); f != nil {
		t.Errorf("a step that never resumed was classified %+v", f)
	}
	// A resumed run that failed for some other reason is not one either.
	other := agent.RunResult{ExitCode: 3, IsError: true, ErrorMessage: "the model refused"}
	if f := classifyResume(other, "", true); f != nil {
		t.Errorf("an unrelated failure was classified %+v", f)
	}
	// Nor is a resumed run that succeeded.
	ok := agent.RunResult{ResultText: "no rollout found for thread id, said the agent"}
	if f := classifyResume(ok, "", true); f != nil {
		t.Errorf("a successful resume was classified %+v", f)
	}
}

// TestSupportsResume is the capability statement §5.5 reads.
func TestSupportsResume(t *testing.T) {
	if !agent.CanResume(New(nil)) {
		t.Error("codex reports it cannot resume; it can, since 0.150.1 was pinned")
	}
}

// fixtureLines returns a fixture's non-empty lines, for the assertions that
// are about a raw line rather than a normalized event.
func fixtureLines(t *testing.T, name string) [][]byte {
	t.Helper()
	f, err := os.Open(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	defer func() { _ = f.Close() }()
	var out [][]byte
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), maxLineBytes)
	for sc.Scan() {
		if line := strings.TrimSpace(sc.Text()); line != "" {
			out = append(out, []byte(line))
		}
	}
	return out
}

func readFixtureText(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return strings.TrimSpace(string(b))
}
