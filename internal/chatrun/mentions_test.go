package chatrun

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/agent"
	"github.com/lezli01/vincent/internal/agent/agenttest"
	"github.com/lezli01/vincent/internal/chatstate"
	"github.com/lezli01/vincent/internal/issuestate"
	"github.com/lezli01/vincent/internal/store"
)

// TestParseMentions is task 166's token grammar: each form, the boundaries
// on both sides, several mentions in one message, and a duplicate counted
// once.
func TestParseMentions(t *testing.T) {
	issue := func(id int64, tok string) Mention { return Mention{Kind: MentionIssue, Token: tok, ID: id} }
	for _, tt := range []struct {
		name string
		text string
		want []Mention
	}{
		{"issue", "look at #issue-3 please", []Mention{issue(3, "#issue-3")}},
		{"task", "#task-12", []Mention{{Kind: MentionTask, Token: "#task-12", ID: 12}}},
		{"pr", "(#pr-7)", []Mention{{Kind: MentionPull, Token: "#pr-7", ID: 7}}},
		{"sentence end", "see #issue-3.", []Mention{issue(3, "#issue-3")}},
		{"no boundary before", "foo#issue-1", nil},
		{"markdown heading", "# heading\n## issue-1", nil},
		{"no digits", "#issue- and #issue", nil},
		{"trailing letter", "#issue-1x", nil},
		{"trailing hyphen", "#issue-1-2", nil},
		{"zero", "#issue-0", nil},
		{"several", "#issue-1 and #task-2, #pr-3", []Mention{
			issue(1, "#issue-1"),
			{Kind: MentionTask, Token: "#task-2", ID: 2},
			{Kind: MentionPull, Token: "#pr-3", ID: 3},
		}},
		{"duplicate once", "#issue-1 then #issue-1 and #issue-01", []Mention{issue(1, "#issue-1")}},
		{"line range", "@a/b.go#L20-45 ok", []Mention{
			{Kind: MentionLines, Token: "@a/b.go#L20-45", Path: "a/b.go", From: 20, To: 45},
		}},
		{"single line", "@b.go#L7", []Mention{{Kind: MentionLines, Token: "@b.go#L7", Path: "b.go", From: 7, To: 7}}},
		{"github spelling", "@b.go#L2-L3", []Mention{{Kind: MentionLines, Token: "@b.go#L2-L3", Path: "b.go", From: 2, To: 3}}},
		{"bare path is not vincent's", "@a/b.go and @c.go", nil},
		{"reversed range", "@b.go#L9-3", nil},
		{"email is not a mention", "me@b.go#L3", nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := ParseMentions(tt.text); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ParseMentions(%q) = %#v, want %#v", tt.text, got, tt.want)
			}
		})
	}
}

func TestParseMentionsIsBounded(t *testing.T) {
	var b strings.Builder
	for i := 1; i <= MaxMentions+5; i++ {
		b.WriteString("#issue-" + strconv.Itoa(i) + " ")
	}
	if got := len(ParseMentions(b.String())); got != MaxMentions {
		t.Errorf("parsed %d mentions, want the cap %d", got, MaxMentions)
	}
}

// TestLinesBlock covers a range's resolution against the workspace: in
// range, clamped at the end, a single line, CRLF, and every case that stays
// prose.
func TestLinesBlock(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		t.Helper()
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("sub/a.txt", "one\ntwo\nthree\nfour\n")
	write("crlf.txt", "one\r\ntwo\r\nthree\r\n")
	write("bin.dat", "a\nb\x00c\n")
	write("fence.md", "```go\nx\n```\n")
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, tt := range []struct {
		name, path string
		from, to   int
		want       string // "" = stays prose
	}{
		{"in range", "sub/a.txt", 2, 3, "<file path=\"sub/a.txt\" lines=\"2-3\">\n```\ntwo\nthree\n```\n</file>\n\n"},
		{"single line", "sub/a.txt", 4, 4, "<file path=\"sub/a.txt\" lines=\"4\">\n```\nfour\n```\n</file>\n\n"},
		{"clamped at the end", "sub/a.txt", 3, 99, "<file path=\"sub/a.txt\" lines=\"3-4\">\n```\nthree\nfour\n```\n</file>\n\n"},
		{"crlf", "crlf.txt", 1, 2, "<file path=\"crlf.txt\" lines=\"1-2\">\n```\none\ntwo\n```\n</file>\n\n"},
		{"longer fence", "fence.md", 1, 3, "<file path=\"fence.md\" lines=\"1-3\">\n````\n```go\nx\n```\n````\n</file>\n\n"},
		{"starts past the end", "sub/a.txt", 5, 6, ""},
		{"missing file", "nope.txt", 1, 2, ""},
		{"directory", "sub", 1, 1, ""},
		{"binary", "bin.dat", 1, 2, ""},
		{"climbs out", "../" + filepath.Base(filepath.Dir(outside)) + "/secret.txt", 1, 1, ""},
		{"climbs out inside", "sub/../../x", 1, 1, ""},
		{"absolute", filepath.ToSlash(outside), 1, 1, ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := linesBlock(dir, Mention{Kind: MentionLines, Path: tt.path, From: tt.from, To: tt.to})
			if ok != (tt.want != "") || got != tt.want {
				t.Errorf("linesBlock(%s#L%d-%d) = %q, %v; want %q", tt.path, tt.from, tt.to, got, ok, tt.want)
			}
		})
	}
}

func TestLinesBlockRefusesASymlinkOut(t *testing.T) {
	dir := t.TempDir()
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "link.txt")); err != nil {
		t.Skipf("no symlinks here: %v", err)
	}
	if got, ok := linesBlock(dir, Mention{Kind: MentionLines, Path: "link.txt", From: 1, To: 1}); ok {
		t.Errorf("a symlink out of the workspace was inlined: %q", got)
	}
}

func TestLinesBlockIsCapped(t *testing.T) {
	dir := t.TempDir()
	line := strings.Repeat("x", 1000)
	var body strings.Builder
	for range 40 {
		body.WriteString(line + "\n")
	}
	if err := os.WriteFile(filepath.Join(dir, "big.txt"), []byte(body.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	got, ok := linesBlock(dir, Mention{Kind: MentionLines, Path: "big.txt", From: 1, To: 40})
	if !ok {
		t.Fatal("a large range stayed prose; want it inlined and truncated")
	}
	if !strings.Contains(got, mentionTruncated) {
		t.Errorf("a capped range carries no truncation marker")
	}
	if len(got) > MentionLinesLimit+200 {
		t.Errorf("block is %d bytes, past the %d cap", len(got), MentionLinesLimit)
	}
}

// TestExpandMentionsResolvesInProjectAndLeavesTheRestAsProse is decision 3:
// vincent's own ids, the chat's project only, and a PR only where there is a
// GitHub lookup that answers.
func TestExpandMentionsResolvesInProjectAndLeavesTheRestAsProse(t *testing.T) {
	h := newHarness(t)
	ctx := t.Context()
	c := h.chat(t)
	iss, err := h.store.CreateIssue(ctx, store.NewIssue{ProjectID: h.project.ID, Title: "Login breaks", Body: "Steps: log in.", Kind: "bug"}, issuestate.Human)
	if err != nil {
		t.Fatal(err)
	}
	other := &store.Project{Name: "other", Path: t.TempDir(), DefaultBranch: "main"}
	if err := h.store.CreateProject(ctx, other); err != nil {
		t.Fatal(err)
	}
	foreign, err := h.store.CreateIssue(ctx, store.NewIssue{ProjectID: other.ID, Title: "Not yours"}, issuestate.Human)
	if err != nil {
		t.Fatal(err)
	}
	task := &store.Task{
		ProjectID: h.project.ID, Title: "Fix login", Description: "Make it work.", WorkflowName: "t",
		WorkflowSnapshot: "steps: []", BaseBranch: "main", BranchName: "vincent/9-fix-login", State: store.TaskBlocked,
	}
	if err := h.store.CreateTask(ctx, task, nil); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(c.WorktreePath, "notes.txt"), []byte("a\nb\nc\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	msg := "see #issue-" + itoa(iss.ID) + " #task-" + itoa(task.ID) + " #issue-" + itoa(foreign.ID) +
		" #issue-999 #pr-5 @notes.txt#L2-3 @../x#L1"

	// No GitHub lookup at all: #pr-5 is prose.
	exp := h.runner.ExpandMentions(ctx, c, msg)
	if got := tokens(exp.Resolved); !reflect.DeepEqual(got, []string{"#issue-" + itoa(iss.ID), "#task-" + itoa(task.ID), "@notes.txt#L2-3"}) {
		t.Errorf("resolved = %v", got)
	}
	if got := tokens(exp.Unresolved); !reflect.DeepEqual(got, []string{"#issue-" + itoa(foreign.ID), "#issue-999", "#pr-5", "@../x#L1"}) {
		t.Errorf("unresolved = %v", got)
	}
	for _, want := range []string{
		"title: Login breaks", "Steps: log in.", `kind="bug"`, "title: Fix login",
		`branch="vincent/9-fix-login"`, "Make it work.", "b\nc",
	} {
		if !strings.Contains(exp.Context, want) {
			t.Errorf("context lacks %q:\n%s", want, exp.Context)
		}
	}
	if strings.Contains(exp.Context, "Not yours") {
		t.Error("another project's issue was expanded")
	}

	// A lookup that fails (GitHub down, no binding) leaves it prose too.
	h.runner.deps.PullRequest = func(context.Context, int64, int) (MentionPullRequest, error) {
		return MentionPullRequest{}, errors.New("github unreachable")
	}
	if exp := h.runner.ExpandMentions(ctx, c, "#pr-5"); exp.Context != "" || len(exp.Unresolved) != 1 {
		t.Errorf("an unreachable GitHub expanded #pr-5: %+v", exp)
	}
	h.runner.deps.PullRequest = func(_ context.Context, projectID int64, n int) (MentionPullRequest, error) {
		if projectID != h.project.ID {
			t.Errorf("PR looked up in project %d, want the chat's %d", projectID, h.project.ID)
		}
		return MentionPullRequest{
			Number: n, Title: "Add login", State: "open", HeadBranch: "feat", BaseBranch: "main",
			URL: "https://github.com/o/r/pull/5",
		}, nil
	}
	exp = h.runner.ExpandMentions(ctx, c, "#pr-5")
	for _, want := range []string{`<pull_request number="5" state="open" head="feat" base="main" url="https://github.com/o/r/pull/5">`, "title: Add login"} {
		if !strings.Contains(exp.Context, want) {
			t.Errorf("PR block lacks %q:\n%s", want, exp.Context)
		}
	}
}

// TestMentionContextIsASnapshotAndThePromptIsVerbatim is decision 2: the
// stored prompt is the typed text, the column holds what the agent saw, and
// an edit to the issue afterwards does not change it.
func TestMentionContextIsASnapshotAndThePromptIsVerbatim(t *testing.T) {
	t.Setenv("FAKEAGENT_SCENARIO", "success")
	h := newHarness(t)
	ctx := t.Context()
	c := h.chat(t)
	iss, err := h.store.CreateIssue(ctx, store.NewIssue{ProjectID: h.project.ID, Title: "Before"}, issuestate.Human)
	if err != nil {
		t.Fatal(err)
	}
	msg := "look at #issue-" + itoa(iss.ID) + " and #issue-404"
	turn := h.sendAndWait(t, c.ID, msg)
	if turn.State != chatstate.TurnDone {
		t.Fatalf("turn = %s (%s)", turn.State, turn.ErrorMessage)
	}
	if turn.Prompt != msg {
		t.Errorf("stored prompt = %q, want the typed text %q", turn.Prompt, msg)
	}
	if !strings.Contains(turn.MentionContext, "title: Before") {
		t.Fatalf("mention_context = %q, want the issue's block", turn.MentionContext)
	}
	after := "After"
	if _, err := h.store.UpdateIssue(ctx, iss.ID, iss.Version, store.IssuePatch{Title: &after}, issuestate.Human); err != nil {
		t.Fatal(err)
	}
	again, err := h.store.GetChatTurn(ctx, turn.ID)
	if err != nil {
		t.Fatal(err)
	}
	if again.MentionContext != turn.MentionContext {
		t.Errorf("mention_context changed after the issue did: %q", again.MentionContext)
	}
	if tr := h.transcript(t, c.ID, turn.Seq); !strings.Contains(tr, "title: Before") {
		t.Errorf("the transcript does not record the mention context:\n%s", tr)
	}
}

// TestMentionContextReachesEveryAdapterIdentically is decision 1: the joined
// prompt is the same bytes on every one-string path, and claude's input mode
// carries the same context as its own block with the message — its own
// `@path#L…` text included — untouched.
func TestMentionContextReachesEveryAdapterIdentically(t *testing.T) {
	const msgTail = " explain @notes.txt#L2"
	var want string
	for _, tt := range []struct {
		name, agent, version string
	}{
		{name: "claude below the input gate", agent: "claude", version: "1.0.0"},
		{name: "codex", agent: "codex"},
		{name: "cursor", agent: "cursor"},
		{name: "copilot", agent: "copilot"},
		{name: "claude input mode", agent: "claude"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("FAKEAGENT_SCENARIO", "success")
			if tt.version != "" {
				t.Setenv("FAKEAGENT_VERSION", tt.version)
			}
			h := newHarness(t)
			if err := os.WriteFile(filepath.Join(h.repo, "notes.txt"), []byte("a\nb\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			rec := &agenttest.RecordingLauncher{}
			c := h.linkedChatOn(t, tt.agent, rec)
			msg := "#task-1" + msgTail
			turn := h.sendAndWait(t, c.ID, msg)
			if turn.State != chatstate.TurnDone {
				t.Fatalf("turn = %s (%s: %s)", turn.State, turn.FailReason, turn.ErrorMessage)
			}
			if !strings.Contains(turn.MentionContext, "title: Probe task") ||
				!strings.Contains(turn.MentionContext, "```\nb\n```") {
				t.Fatalf("mention_context = %q", turn.MentionContext)
			}
			preamble := joinPreamble(linkedOpeningContext, turn.MentionContext)
			launches := rec.Launches()
			if len(launches) != 1 {
				t.Fatalf("launcher saw %d turns", len(launches))
			}
			stdin := launches[0].Stdin()
			if launches[0].Command.StdinPipe && tt.agent == "claude" {
				blocks := userLineTexts(t, stdin)
				if len(blocks) != 2 || strings.TrimSpace(blocks[0]) != strings.TrimSpace(preamble) || blocks[1] != msg {
					t.Errorf("claude input mode sent %q; want the context then %q verbatim", blocks, msg)
				}
				return
			}
			got := string(stdin)
			joined := agent.RunSpec{Prompt: msg, Preamble: preamble}.JoinedPrompt()
			if got != joined {
				t.Errorf("stdin = %q, want %q", got, joined)
			}
			if want == "" {
				want = got
			} else if got != want {
				t.Errorf("%s sent different bytes from the other adapters", tt.name)
			}
		})
	}
}

// TestQueuedMessageIsExpandedWhenItsTurnStarts is decision 2's last clause:
// a message queued behind a live turn expands when it reaches the agent.
func TestQueuedMessageIsExpandedWhenItsTurnStarts(t *testing.T) {
	t.Setenv("FAKEAGENT_SCENARIO", "success")
	t.Setenv("FAKEAGENT_DELAY_MS", "800")
	h := newHarness(t)
	ctx := t.Context()
	c := h.chat(t)
	iss, err := h.store.CreateIssue(ctx, store.NewIssue{ProjectID: h.project.ID, Title: "Queued before"}, issuestate.Human)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.runner.Send(ctx, c.ID, "first"); err != nil {
		t.Fatal(err)
	}
	h.submitQueued(t, c.ID, "#issue-"+itoa(iss.ID))
	after := "Queued after"
	if _, err := h.store.UpdateIssue(ctx, iss.ID, iss.Version, store.IssuePatch{Title: &after}, issuestate.Human); err != nil {
		t.Fatal(err)
	}
	h.waitIdle(t, c.ID)
	turns, err := h.store.ListChatTurns(ctx, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(turns) != 2 {
		t.Fatalf("%d turns, want 2", len(turns))
	}
	if !strings.Contains(turns[1].MentionContext, "title: Queued after") {
		t.Errorf("queued turn's context = %q, want the issue as it was when the turn started", turns[1].MentionContext)
	}
}

func tokens(ms []Mention) []string {
	out := make([]string, 0, len(ms))
	for _, m := range ms {
		out = append(out, m.Token)
	}
	return out
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }
