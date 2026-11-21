package claude

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/agent"
	"github.com/lezli01/vincent/internal/agent/agenttest"
)

// resumeAtFixture is a real claude 2.1.330 run (task 167): session
// da304d43-… had answered "Reply with exactly: ALPHA" (assistant message
// 8e579f24-…) and then "…BETA"; this is `--resume da304d43-…
// --resume-session-at 8e579f24-… --fork-session --model haiku` asked which
// words it had replied with. The init line is cut to the keys vincent reads,
// with the cwd rewritten; every other line is verbatim.
const (
	resumeAtFixture      = "stream_resume_at_2.1.330.jsonl"
	resumeAtResumed      = "da304d43-db3c-4487-98e7-34f0942d3bdb"
	resumeAtForked       = "90cb29cf-a20d-4fed-b9f4-4a4006410d8d"
	resumeAtAnchor       = "b4855a7a-6049-40e2-bff6-155cf403befc"
	resumeAtResultLineID = "1c5828c3-d463-49f5-a240-c744f79bd1f5"
)

// TestResumeAtProbeFixtures pins detection to both answers: the flag refused
// by name is present, commander's unknown option is absent, and output vincent
// has never seen is absent too.
func TestResumeAtProbeFixtures(t *testing.T) {
	for _, tt := range []struct {
		fixture string
		want    bool
	}{
		{"probe_resume_at_present_2.1.330.txt", true},
		{"probe_resume_at_absent_2.1.330.txt", false},
	} {
		b, err := os.ReadFile(filepath.Join("testdata", tt.fixture))
		if err != nil {
			t.Fatal(err)
		}
		if got := parseResumeAtProbe(string(b)); got != tt.want {
			t.Errorf("%s: present = %v, want %v", tt.fixture, got, tt.want)
		}
	}
	if parseResumeAtProbe("Segmentation fault\n") {
		t.Error("an unrecognised answer read as present")
	}
}

// TestResumeAtArgv: a resumed-at-point turn puts the anchor and the fork flag
// beside --resume, and a ResumeAt with no session is refused before any argv.
func TestResumeAtArgv(t *testing.T) {
	args, err := buildArgs(agent.RunSpec{
		ResumeSessionID: "s1", ResumeAt: "u1", PermissionMode: agent.FullAuto,
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	i := slices.Index(args, "--resume")
	want := []string{"--resume", "s1", "--resume-session-at", "u1", "--fork-session"}
	if i < 0 || i+len(want) > len(args) || !slices.Equal(args[i:i+len(want)], want) {
		t.Errorf("argv %q: want %q in order", args, want)
	}

	plain, err := buildArgs(agent.RunSpec{ResumeSessionID: "s1", PermissionMode: agent.FullAuto}, true)
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(plain, "--resume-session-at") || slices.Contains(plain, "--fork-session") {
		t.Errorf("a plain resume grew the fork flags: %q", plain)
	}

	if _, err := buildArgs(agent.RunSpec{ResumeAt: "u1"}, true); err == nil {
		t.Error("ResumeAt without ResumeSessionID built an argv")
	}
}

// TestFixtureResumeAtAnchor: the parser's anchor is the last assistant line's
// uuid, never the result line's, and the session the run reports is the
// fork's.
func TestFixtureResumeAtAnchor(t *testing.T) {
	events, r := fixtureEvents(t, resumeAtFixture)
	if r.lines.anchor != resumeAtAnchor {
		t.Errorf("anchor = %q, want the assistant message %q", r.lines.anchor, resumeAtAnchor)
	}
	if r.lines.anchor == resumeAtResultLineID {
		t.Error("anchor is the result line's uuid")
	}
	if !slices.ContainsFunc(events, func(e agent.Event) bool { return e.Type == agent.EventResult }) {
		t.Fatal("capture does not parse to a result")
	}
	b, err := os.ReadFile(filepath.Join("testdata", resumeAtFixture))
	if err != nil {
		t.Fatal(err)
	}
	for line := range strings.SplitSeq(strings.TrimSpace(string(b)), "\n") {
		if sid := sessionIDOf([]byte(line)); sid != resumeAtForked {
			t.Errorf("line session = %q, want the fork %q (not the resumed %q)", sid, resumeAtForked, resumeAtResumed)
		}
	}
	if strings.Contains(string(b), "BETA") {
		t.Error("the forked run recalls BETA, which it was resumed before")
	}
}

// TestSubagentLineNeverMovesTheAnchor: a subagent's assistant line lives in
// its own sidechain, so it is no point the main session can resume at.
func TestSubagentLineNeverMovesTheAnchor(t *testing.T) {
	var p streamParser
	p.parse([]byte(`{"type":"assistant","uuid":"main-1","parent_tool_use_id":null,"message":{"content":[]}}`))
	p.parse([]byte(`{"type":"assistant","uuid":"sub-1","parent_tool_use_id":"toolu_1","message":{"content":[]}}`))
	p.parse([]byte(`{"type":"result","subtype":"success","uuid":"res-1","result":"ok"}`))
	if p.anchor != "main-1" {
		t.Errorf("anchor = %q, want main-1", p.anchor)
	}
}

// TestResumeAtCapabilityAgainstFake: detection through the real probe, against
// the fake answering as a build with the flag and as one without it. The probe
// inherits the process environment, which is where the switch is set.
func TestResumeAtCapabilityAgainstFake(t *testing.T) {
	path := agenttest.BuildFakeAgent(t)
	if !New(func() string { return path }).SupportsResumeAtPoint() {
		t.Error("present: SupportsResumeAtPoint = false")
	}
	a := New(func() string { return path })
	if !agent.CanResumeAtPoint(a) {
		t.Error("present: CanResumeAtPoint = false")
	}
	t.Setenv("FAKEAGENT_CLAUDE_NO_RESUME_AT", "1")
	if New(func() string { return path }).SupportsResumeAtPoint() {
		t.Error("absent: SupportsResumeAtPoint = true")
	}
	if !a.SupportsResumeAtPoint() {
		t.Error("the cached verdict for an unchanged binary was probed again")
	}
	if New(func() string { return filepath.Join(t.TempDir(), "no-claude") }).SupportsResumeAtPoint() {
		t.Error("a missing binary supports resume-at")
	}
}

// runTurn starts one turn against the fake and waits for it.
func runTurn(t *testing.T, a *Adapter, spec agent.RunSpec) (agent.RunResult, []agent.Event) {
	t.Helper()
	spec.PermissionMode = agent.FullAuto
	spec.WorkDir = t.TempDir()
	h, err := a.Start(t.Context(), spec)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	events := drain(t, h)
	res, err := h.Wait()
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if res.IsError {
		t.Fatalf("turn failed: %+v", res)
	}
	return res, events
}

func outputText(events []agent.Event) string {
	var b strings.Builder
	for _, ev := range events {
		if ev.Type == agent.EventOutput {
			b.WriteString(ev.Text)
			b.WriteString("\n")
		}
	}
	return b.String()
}

// TestResumeAtForksAtTheAnchor is the contract end to end: every turn reports
// an anchor, and a turn resumed at the first turn's anchor runs in a new
// session that remembers the first turn and not the second.
func TestResumeAtForksAtTheAnchor(t *testing.T) {
	a := fakeAdapter(t)
	t.Setenv("FAKEAGENT_SESSION_DIR", t.TempDir())

	first, _ := runTurn(t, a, agent.RunSpec{Prompt: "turn one"})
	second, _ := runTurn(t, a, agent.RunSpec{Prompt: "turn two", ResumeSessionID: first.SessionID})
	if first.ResumeAnchor == "" || second.ResumeAnchor == "" || first.ResumeAnchor == second.ResumeAnchor {
		t.Fatalf("anchors = %q, %q; want two distinct", first.ResumeAnchor, second.ResumeAnchor)
	}
	if second.SessionID != first.SessionID {
		t.Fatalf("a plain resume changed the session: %q -> %q", first.SessionID, second.SessionID)
	}

	forked, events := runTurn(t, a, agent.RunSpec{
		Prompt: "turn three", ResumeSessionID: second.SessionID, ResumeAt: first.ResumeAnchor,
	})
	if forked.SessionID == "" || forked.SessionID == second.SessionID {
		t.Errorf("forked SessionID = %q, want a new one (resumed %q)", forked.SessionID, second.SessionID)
	}
	if forked.ResumeAnchor == "" {
		t.Error("the forked turn reported no anchor")
	}
	out := outputText(events)
	if !strings.Contains(out, "recalled: turn one") || strings.Contains(out, "turn two") {
		t.Errorf("forked turn recalls %q; want turn one and not turn two", out)
	}

	// The abandoned session is intact: resuming it still sees both turns.
	_, events = runTurn(t, a, agent.RunSpec{Prompt: "turn four", ResumeSessionID: second.SessionID})
	if out := outputText(events); !strings.Contains(out, "turn one | turn two") {
		t.Errorf("original session recalls %q, want both turns", out)
	}
}

// TestResumeAtUnsupportedWithoutTheFlag: a claude without the flag refuses a
// resume-at from Start instead of resuming the whole session.
func TestResumeAtUnsupportedWithoutTheFlag(t *testing.T) {
	t.Setenv("FAKEAGENT_CLAUDE_NO_RESUME_AT", "1")
	_, err := fakeAdapter(t).Start(t.Context(), agent.RunSpec{
		Prompt: "x", WorkDir: t.TempDir(), PermissionMode: agent.FullAuto,
		ResumeSessionID: "s1", ResumeAt: "u1",
	})
	if !errors.Is(err, agent.ErrResumeAtUnsupported) {
		t.Fatalf("Start = %v, want ErrResumeAtUnsupported", err)
	}
}

// TestSeededRunWithoutTheCapability is the fallback a claude without the flag
// gets (task 167 decision G): a fresh session, the kept turns as its own text
// block ahead of the human's, and the human's block exactly their message.
func TestSeededRunWithoutTheCapability(t *testing.T) {
	t.Setenv("FAKEAGENT_CLAUDE_NO_RESUME_AT", "1")
	a := fakeAdapter(t)
	if a.SupportsResumeAtPoint() {
		t.Fatal("the fake answered present under the switch")
	}
	rec := &agenttest.RecordingLauncher{}
	const preamble, prompt = "Earlier in this conversation:\nturn one\n", "what next?"
	runTurn(t, a, agent.RunSpec{Prompt: prompt, Preamble: preamble, Launcher: rec})
	launches := rec.Launches()
	if len(launches) != 1 {
		t.Fatalf("launches = %d, want 1", len(launches))
	}
	if args := launches[0].Command.Args; slices.Contains(args, "--resume") ||
		slices.Contains(args, "--resume-session-at") || slices.Contains(args, "--fork-session") {
		t.Errorf("a seeded run resumed: %q", args)
	}
	stdin := strings.TrimSpace(string(launches[0].Stdin()))
	var msg struct {
		Message struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal([]byte(strings.SplitN(stdin, "\n", 2)[0]), &msg); err != nil {
		t.Fatalf("stdin %q: %v", stdin, err)
	}
	var blocks []string
	for _, c := range msg.Message.Content {
		blocks = append(blocks, c.Text)
	}
	if !slices.Equal(blocks, []string{preamble, prompt}) {
		t.Errorf("blocks = %q, want the preamble then exactly the message", blocks)
	}
}
