package copilot

import (
	"errors"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/agent"
	"github.com/lezli01/vincent/internal/agent/agenttest"
)

// TestResumeAtUnsupported: copilot cannot resume a session at a point (task
// 167 decision A), so a spec asking for it is refused from Start — never run
// as a resume of the whole session — and the capability says so.
func TestResumeAtUnsupported(t *testing.T) {
	a := fakeAdapter(t)
	if agent.CanResumeAtPoint(a) {
		t.Error("CanResumeAtPoint = true")
	}
	rec := &agenttest.RecordingLauncher{}
	_, err := a.Start(t.Context(), agent.RunSpec{
		Prompt: "x", WorkDir: t.TempDir(), PermissionMode: agent.FullAuto,
		ResumeSessionID: "s1", ResumeAt: "u1", Launcher: rec,
	})
	if !errors.Is(err, agent.ErrResumeAtUnsupported) {
		t.Fatalf("Start = %v, want ErrResumeAtUnsupported", err)
	}
	if n := len(rec.Launches()); n != 0 {
		t.Errorf("launched %d processes for a refused run", n)
	}
}

// TestSeededRunPlacesThePreambleAheadOfTheMessage is the fallback a rewound
// copilot chat gets (task 167 decision G): a fresh session — no resume on argv —
// whose stdin is the preamble ahead of the human's message, which sits alone
// and verbatim inside the `<message>` tags. copilot takes context only on
// stdin, so the preamble never reaches argv either.
func TestSeededRunPlacesThePreambleAheadOfTheMessage(t *testing.T) {
	const preamble, prompt = "Earlier in this conversation:\nturn one\n", "what next?"
	rec := &agenttest.RecordingLauncher{}
	spec := agent.RunSpec{
		Prompt: prompt, Preamble: preamble, WorkDir: t.TempDir(),
		PermissionMode: agent.FullAuto, Launcher: rec,
	}
	h, err := fakeAdapter(t).Start(t.Context(), spec)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	drain(t, h)
	if _, err := h.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	launches := rec.Launches()
	if len(launches) != 1 {
		t.Fatalf("launches = %d, want 1", len(launches))
	}
	for _, arg := range launches[0].Command.Args {
		if arg == "--resume" || arg == "resume" || strings.HasPrefix(arg, "--resume=") {
			t.Errorf("a seeded run resumed: %q", launches[0].Command.Args)
		}
		if strings.Contains(arg, "turn one") {
			t.Errorf("the preamble reached argv: %q", arg)
		}
	}
	stdin := string(launches[0].Stdin())
	if stdin != spec.JoinedPrompt() {
		t.Fatalf("stdin = %q, want the joined prompt %q", stdin, spec.JoinedPrompt())
	}
	before, rest, ok := strings.Cut(stdin, "<message>\n")
	if !ok {
		t.Fatalf("stdin %q has no <message> tag", stdin)
	}
	message, _, ok := strings.Cut(rest, "\n</message>")
	if !ok {
		t.Fatalf("stdin %q has no closing tag", stdin)
	}
	if message != prompt {
		t.Errorf("message = %q, want exactly the human's %q", message, prompt)
	}
	if !strings.Contains(before, strings.TrimSuffix(preamble, "\n")) {
		t.Errorf("preamble not ahead of the message: %q", before)
	}
}
