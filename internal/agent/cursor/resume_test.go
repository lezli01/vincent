package cursor

import (
	"os"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/agent"
)

// resumedSessionID is the id testdata/resume_2026.08.11.jsonl was scrubbed to.
// The capture's own UUID is replaced, but with one value on every line,
// because the fact being pinned is that a resumed run reports the id it was
// given — the id vincent then hands back on the turn after.
const resumedSessionID = "4b7d1e28-3a90-4f6c-8b21-5d0c7e93af14"

// TestBuildArgsResume pins the resumed invocation against cursor-agent
// 2026.08.11-e8db854 (§9.7, task 072).
//
// `--resume [chatId]` takes an *optional* value, which would be a hazard for a
// CLI that also took a positional prompt — the prompt would be eaten as the
// flag's value. It is safe here only because this adapter puts the prompt on
// stdin and passes no positional at all, so the assertion that matters is that
// the id is the last thing on argv and nothing follows it.
func TestBuildArgsResume(t *testing.T) {
	restore := SetSandboxAvailable(true)
	defer restore()
	for _, tt := range []struct {
		name string
		spec agent.RunSpec
		want []string
	}{
		{
			name: "full auto",
			spec: agent.RunSpec{PermissionMode: agent.FullAuto, ResumeSessionID: "s-1"},
			want: []string{
				"-p", "--output-format", "stream-json", "--trust", "--force",
				"--model", "auto", "--resume", "s-1",
			},
		},
		{
			name: "restricted keeps its sandbox; resume is orthogonal here",
			spec: agent.RunSpec{PermissionMode: agent.Restricted, ResumeSessionID: "s-1"},
			want: []string{
				"-p", "--output-format", "stream-json", "--trust", "--sandbox", "enabled",
				"--model", "auto", "--resume", "s-1",
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := buildArgs(tt.spec)
			if err != nil {
				t.Fatalf("buildArgs: %v", err)
			}
			if strings.Join(got, " ") != strings.Join(tt.want, " ") {
				t.Fatalf("buildArgs = %q, want %q", got, tt.want)
			}
			if got[len(got)-1] != "s-1" {
				t.Errorf("the resume id is not last on argv (%q); an optional-value "+
					"flag must have nothing after it to swallow", got)
			}
		})
	}
}

// TestResumeFixtureReportsItsSessionID pins the stream half: a resumed run
// stamps the id it was handed on every line, which is what RunResult.SessionID
// must end up holding.
func TestResumeFixtureReportsItsSessionID(t *testing.T) {
	var last string
	for _, line := range strings.Split(readFixture(t, "resume_2026.08.11.jsonl"), "\n") {
		if line = strings.TrimSpace(line); line == "" {
			continue
		}
		if id := sessionIDOf([]byte(line)); id != "" {
			last = id
		}
	}
	if last != resumedSessionID {
		t.Fatalf("session id = %q, want %q", last, resumedSessionID)
	}
	if got := sessionIDOf([]byte("not json")); got != "" {
		t.Errorf("sessionIDOf(garbage) = %q, want empty", got)
	}
}

// TestUnknownSessionIsAdoptedNotRefused states the gap positively (§9.7, task
// 070 decision 2), because a capability an adapter lacks is stated and never
// emulated.
//
// cursor-agent 2026.08.11 does not refuse `--resume` of an id it has never
// seen. It starts a fresh chat *under that id*, stamps it on every line and
// exits 0 — captured in testdata/resume_unknown_2026.08.11.jsonl by resuming
// an all-zero UUID. So this adapter ships no FailureSessionLost: there is no
// refusal to recognize, and inventing one would be a guess about a CLI that
// answered.
func TestUnknownSessionIsAdoptedNotRefused(t *testing.T) {
	const unknown = "00000000-0000-4000-8000-000000000000"
	var res *agent.RunResult
	for _, ev := range parseFixture(t, "resume_unknown_2026.08.11.jsonl") {
		if ev.Type == agent.EventResult {
			res = ev.Result
		}
	}
	if res == nil {
		t.Fatal("no result event: the capture would have to be of a refusal")
	}
	if res.IsError {
		t.Fatalf("the capture is of a refused resume after all: %+v", res)
	}
	for _, line := range strings.Split(readFixture(t, "resume_unknown_2026.08.11.jsonl"), "\n") {
		if line = strings.TrimSpace(line); line == "" {
			continue
		}
		if id := sessionIDOf([]byte(line)); id != "" && id != unknown {
			t.Fatalf("session id = %q, want the unknown id %q it was handed", id, unknown)
		}
	}
}

// TestSupportsResume is the capability statement §5.5 reads.
func TestSupportsResume(t *testing.T) {
	if !agent.CanResume(New(nil)) {
		t.Error("cursor reports it cannot resume; it can, since 2026.08.11 was pinned")
	}
}

func readFixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return string(b)
}
