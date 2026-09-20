package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/agent"
	"github.com/lezli01/vincent/internal/agent/agenttest"
	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/chatstate"
	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/store"
)

// `vincent chat skills` against the real GET /v1/chats/{id}/skills handler,
// its real cache and a real chat runner (task 124.11). The command renders
// only what the route answered, so only the route can say what that is — and
// a hand-written stub here would be the one place the CLI and the daemon
// could drift apart unnoticed.

// skillStub is agenttest.StubSkills with codex's disambiguation for a name
// that repeats: `[$name](path)` (§9.3). The shared stub is deliberately
// literal — sigil plus name — and 124.9's live test pins that, so the one
// case decision 60 exists for (two rows with one name and two invocations)
// gets its adapter here rather than by loosening the shared one.
type skillStub struct{ *agenttest.StubSkills }

// Invocation implements agent.SkillInvoker.
func (s skillStub) Invocation(sk agent.Skill, among []agent.Skill) string {
	seen := 0
	for _, other := range among {
		if other.Name == sk.Name {
			seen++
		}
	}
	if seen > 1 {
		return "[" + s.Syntax.Sigil + sk.Name + "](" + sk.Path + ")"
	}
	return s.StubSkills.Invocation(sk, among)
}

// newChatSkillsLive is the transcript command's harness with a real skill
// cache over two stub adapters: one that lists and invokes in syntax, and one
// that does neither.
func newChatSkillsLive(t *testing.T, syntax agent.SkillSyntax) (*liveHarness, skillStub) {
	t.Helper()
	stub := skillStub{&agenttest.StubSkills{Syntax: syntax}}
	h := newLiveHarness(t, withSkillCache(agent.NewRegistry(stub, agenttest.StubNoSkills{})))
	return h, stub
}

// addSkillChat writes a free chat on agentName, with a directory of its own as its
// worktree. Rows are written through the store because POST /v1/chats refuses
// both stubs: neither can resume a session (task 063 decision 3).
func (h *liveHarness) addSkillChat(t *testing.T, agentName string, state chatstate.State) *store.Chat {
	t.Helper()
	c := &store.Chat{
		ProjectID: h.projectID, Title: "skills", State: state, Agent: agentName,
		PermissionMode: string(agent.FullAuto), BaseBranch: "main", WorktreePath: t.TempDir(),
	}
	if err := h.st.CreateChat(t.Context(), c); err != nil {
		t.Fatalf("CreateChat: %v", err)
	}
	return c
}

// chatSkillsRows is the table's body: everything stdout carried after the
// header, with blank lines dropped.
func chatSkillsRows(t *testing.T, out string) []string {
	t.Helper()
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) == 0 || strings.Join(strings.Fields(lines[0]), " ") !=
		strings.Join(chatSkillsHeader, " ") {
		t.Fatalf("stdout does not start with the table header:\n%s", out)
	}
	return lines[1:]
}

// A real list is one row per skill, in the API's order, with the adapter's own
// invocation verbatim — including the duplicated name whose two rows differ
// only by it (task 124 decisions 8, 17 and 60).
func TestChatSkillsCommandPrintsTheList(t *testing.T) {
	h, stub := newChatSkillsLive(t, agent.SkillSyntax{Sigil: "$", Position: agent.SkillAnywhere})
	stub.Script(agent.SkillList{
		Skills: []agent.Skill{
			{Name: "deploy", Description: "Ship it (project)", ArgumentHint: "[env]", Path: "/proj/deploy"},
			{Name: "deploy", Description: "Ship it (user)", Path: "/home/deploy"},
			{Name: "review"},
		},
		Problems: []agent.SkillProblem{{Path: "/skills/broken", Message: "missing name"}},
	}, nil)
	chat := h.addSkillChat(t, agenttest.SkillsName, chatstate.Idle)

	out, errOut, code := runCLI(t, "chat", "skills", strconv.FormatInt(chat.ID, 10))
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (stderr %q)", code, errOut)
	}
	rows := chatSkillsRows(t, out)
	if len(rows) != 3 {
		t.Fatalf("printed %d row(s), want 3:\n%s", len(rows), out)
	}
	for i, want := range [][]string{
		{"deploy", "[$deploy](/proj/deploy)", "[env]", "Ship it (project)"},
		{"deploy", "[$deploy](/home/deploy)", "Ship it (user)"},
		{"review", "$review"},
	} {
		for _, cell := range want {
			if !strings.Contains(rows[i], cell) {
				t.Errorf("row %d does not carry %q:\n%s", i, cell, rows[i])
			}
		}
	}
	// The second deploy's ARGS cell is blank, not "-": every cell is the
	// CLI's own word, and vincent adds none (task 124 decision 8).
	if strings.Contains(rows[1], "-") {
		t.Errorf("a cell the agent left empty was filled in:\n%s", rows[1])
	}
	// stdout is the table and nothing else (task 124 decision 63).
	for _, unwanted := range []string{"invoke:", "warning:"} {
		if strings.Contains(out, unwanted) {
			t.Errorf("stdout carries %q, which belongs on stderr:\n%s", unwanted, out)
		}
	}
	if !strings.Contains(errOut, "warning: /skills/broken: missing name") {
		t.Errorf("stderr does not carry the problem:\n%s", errOut)
	}
	if !strings.Contains(errOut, "invoke: vincent chat send "+strconv.FormatInt(chat.ID, 10)+
		" '$NAME your message'") {
		t.Errorf("stderr does not carry the invocation line:\n%s", errOut)
	}
}

// An adapter that cannot list is information, not failure: the verdict and
// its reason go to stderr, stdout carries no table at all, and the command
// exits 0 (task 124 decisions 4 and 63).
func TestChatSkillsCommandUnsupportedExitsZero(t *testing.T) {
	h, _ := newChatSkillsLive(t, agent.SkillSyntax{Sigil: "$", Position: agent.SkillAnywhere})
	chat := h.addSkillChat(t, agenttest.NoSkillsName, chatstate.Idle)

	out, errOut, code := runCLI(t, "chat", "skills", strconv.FormatInt(chat.ID, 10))
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (stderr %q)", code, errOut)
	}
	if out != "" {
		t.Errorf("stdout should carry no table under an unsupported verdict:\n%s", out)
	}
	if !strings.Contains(errOut, "no skill list: "+agenttest.NoSkillsName+
		" does not report the skills it loads") {
		t.Errorf("stderr does not carry the verdict and its reason:\n%s", errOut)
	}
	// It cannot invoke either, so there is no invocation to offer.
	if strings.Contains(errOut, "invoke:") {
		t.Errorf("an adapter that cannot invoke was given an invocation line:\n%s", errOut)
	}
}

// A probe that failed is "nobody can say", and the error is what says so.
func TestChatSkillsCommandUnknownPrintsTheProbeError(t *testing.T) {
	h, stub := newChatSkillsLive(t, agent.SkillSyntax{Sigil: "$", Position: agent.SkillAnywhere})
	stub.Script(agent.SkillList{}, errors.New("probe timed out"))
	chat := h.addSkillChat(t, agenttest.SkillsName, chatstate.Idle)

	out, errOut, code := runCLI(t, "chat", "skills", strconv.FormatInt(chat.ID, 10))
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (stderr %q)", code, errOut)
	}
	if out != "" {
		t.Errorf("stdout should carry no table under an unknown verdict:\n%s", out)
	}
	if !strings.Contains(errOut, "skill list unknown: probe timed out") {
		t.Errorf("stderr does not carry the probe error:\n%s", errOut)
	}
}

// cursor's shape: an adapter that invokes skills it cannot list still gets the
// invocation line, which is the case task 124 decision 2 exists for.
func TestChatSkillsCommandInvokesWhatItCannotList(t *testing.T) {
	h, stub := newChatSkillsLive(t, agent.SkillSyntax{Sigil: "/", Position: agent.SkillAnywhere})
	stub.Script(agent.SkillList{}, fmt.Errorf("stub: %w", agent.ErrSkillsUnsupported))
	chat := h.addSkillChat(t, agenttest.SkillsName, chatstate.Idle)

	out, errOut, code := runCLI(t, "chat", "skills", strconv.FormatInt(chat.ID, 10))
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (stderr %q)", code, errOut)
	}
	if out != "" {
		t.Errorf("stdout should carry no table:\n%s", out)
	}
	if !strings.Contains(errOut, "no skill list: stub: "+agent.ErrSkillsUnsupported.Error()) {
		t.Errorf("stderr does not carry the positive no:\n%s", errOut)
	}
	if !strings.Contains(errOut, "invoke: vincent chat send ") {
		t.Errorf("an adapter that can invoke was given no invocation line:\n%s", errOut)
	}
}

// The invocation line and its quoting note come from the sigil and the
// position on the wire, never from the adapter's name (task 124 decision 61).
func TestChatSkillsCommandInvokeLineKeysOffTheSigil(t *testing.T) {
	for _, tc := range []struct {
		name    string
		syntax  agent.SkillSyntax
		wants   []string
		unwants []string
	}{
		{
			name:   "dollar anywhere",
			syntax: agent.SkillSyntax{Sigil: "$", Position: agent.SkillAnywhere},
			wants: []string{
				"'$NAME your message'", "anywhere in the message",
				"bash, zsh and pwsh expand", "keep the single quotes",
			},
			unwants: []string{"MSYS_NO_PATHCONV", "must start the message"},
		},
		{
			name:   "slash leading",
			syntax: agent.SkillSyntax{Sigil: "/", Position: agent.SkillLeading},
			wants: []string{
				"'/NAME your message'", "must start the message",
				"Git Bash", "MSYS_NO_PATHCONV=1", "keep the single quotes",
			},
			unwants: []string{"bash, zsh and pwsh expand", "anywhere in the message"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, stub := newChatSkillsLive(t, tc.syntax)
			stub.Script(agent.SkillList{Skills: []agent.Skill{{Name: "review"}}}, nil)
			chat := h.addSkillChat(t, agenttest.SkillsName, chatstate.Idle)

			_, errOut, code := runCLI(t, "chat", "skills", strconv.FormatInt(chat.ID, 10))
			if code != 0 {
				t.Fatalf("exit = %d, want 0 (stderr %q)", code, errOut)
			}
			for _, w := range tc.wants {
				if !strings.Contains(errOut, w) {
					t.Errorf("stderr does not carry %q:\n%s", w, errOut)
				}
			}
			for _, u := range tc.unwants {
				if strings.Contains(errOut, u) {
					t.Errorf("stderr carries %q, which belongs to the other sigil:\n%s", u, errOut)
				}
			}
			// The hazard belongs to the character, not to the CLI that uses
			// it: naming the adapter here would go stale the day it changes.
			if strings.Contains(errOut, agenttest.SkillsName) {
				t.Errorf("the invocation line names the adapter:\n%s", errOut)
			}
		})
	}
}

// --json is the response object, and its collections are arrays even when
// they are empty: a script's `| jq '.skills[]'` must not fail on a type.
func TestChatSkillsCommandJSON(t *testing.T) {
	h, stub := newChatSkillsLive(t, agent.SkillSyntax{Sigil: "$", Position: agent.SkillAnywhere})
	stub.Script(agent.SkillList{
		Skills: []agent.Skill{{
			Name: "deploy", Description: "Ship it", ArgumentHint: "[env]",
			Aliases: []string{"ship"}, Scope: "repo", Plugin: "ops", Path: "/skills/deploy",
		}},
		Problems: []agent.SkillProblem{{Path: "/skills/broken", Message: "missing name"}},
	}, nil)
	chat := h.addSkillChat(t, agenttest.SkillsName, chatstate.Idle)

	out, errOut, code := runCLI(t, "chat", "skills", strconv.FormatInt(chat.ID, 10), "--json")
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (stderr %q)", code, errOut)
	}
	var got apiclient.ChatSkills
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("decode %q: %v", out, err)
	}
	if got.ChatID != chat.ID || got.Agent != agenttest.SkillsName || got.WorkDir != chat.WorktreePath {
		t.Errorf("identity = %d %q %q, want %d %q %q", got.ChatID, got.Agent, got.WorkDir,
			chat.ID, agenttest.SkillsName, chat.WorktreePath)
	}
	if got.ListVerdict != apiclient.InputVerdictSupported ||
		got.InvokeVerdict != apiclient.InputVerdictSupported ||
		got.InvokeSigil != "$" || got.InvokePosition != string(agent.SkillAnywhere) {
		t.Errorf("verdicts = %+v, want a supported list and a $/anywhere invocation", got)
	}
	if got.ProbedAt == nil {
		t.Error("probed_at is null for a list that was just probed")
	}
	want := apiclient.ChatSkill{
		Name: "deploy", Invocation: "$deploy", Description: "Ship it", ArgumentHint: "[env]",
		Aliases: []string{"ship"}, Scope: "repo", Plugin: "ops", Path: "/skills/deploy",
	}
	if len(got.Skills) != 1 || got.Skills[0].Name != want.Name ||
		got.Skills[0].Invocation != want.Invocation || got.Skills[0].Scope != want.Scope ||
		got.Skills[0].Plugin != want.Plugin || got.Skills[0].Path != want.Path ||
		len(got.Skills[0].Aliases) != 1 {
		t.Errorf("skills = %+v, want [%+v]", got.Skills, want)
	}
	if len(got.Problems) != 1 || got.Problems[0].Path != "/skills/broken" {
		t.Errorf("problems = %+v, want the one the CLI reported", got.Problems)
	}

	// The fields the table leaves out are exactly what --json is for, and an
	// empty list still decodes as an array rather than as null.
	stub.Script(agent.SkillList{}, nil)
	empty, errOut, code := runCLI(t, "chat", "skills", strconv.FormatInt(chat.ID, 10),
		"--json", "--refresh")
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (stderr %q)", code, errOut)
	}
	var raw struct {
		Skills   []apiclient.ChatSkill        `json:"skills"`
		Problems []apiclient.ChatSkillProblem `json:"problems"`
	}
	if err := json.Unmarshal([]byte(empty), &raw); err != nil {
		t.Fatalf("decode %q: %v", empty, err)
	}
	if raw.Skills == nil || raw.Problems == nil {
		t.Errorf("an empty answer sent null: skills %#v problems %#v", raw.Skills, raw.Problems)
	}
}

// --refresh is counted on the wire, not assumed from the flag.
func TestChatSkillsCommandRefreshReachesTheDaemon(t *testing.T) {
	h, stub := newChatSkillsLive(t, agent.SkillSyntax{Sigil: "$", Position: agent.SkillAnywhere})
	stub.Script(agent.SkillList{Skills: []agent.Skill{{Name: "review"}}}, nil)
	id := strconv.FormatInt(h.addSkillChat(t, agenttest.SkillsName, chatstate.Idle).ID, 10)

	refreshes, cached := h.chatSkillsRefreshes.Load(), h.chatSkillsCached.Load()
	if _, errOut, code := runCLI(t, "chat", "skills", id); code != 0 {
		t.Fatalf("chat skills: code %d, stderr %q", code, errOut)
	}
	if got := h.chatSkillsRefreshes.Load() - refreshes; got != 0 {
		t.Errorf("plain `chat skills` sent %d refresh request(s), want 0", got)
	}
	if got := h.chatSkillsCached.Load() - cached; got != 1 {
		t.Errorf("plain `chat skills` sent %d cached request(s), want 1", got)
	}

	refreshes, cached = h.chatSkillsRefreshes.Load(), h.chatSkillsCached.Load()
	if _, errOut, code := runCLI(t, "chat", "skills", id, "--refresh"); code != 0 {
		t.Fatalf("chat skills --refresh: code %d, stderr %q", code, errOut)
	}
	if got := h.chatSkillsRefreshes.Load() - refreshes; got != 1 {
		t.Errorf("`chat skills --refresh` sent %d refresh request(s), want 1", got)
	}
	if got := h.chatSkillsCached.Load() - cached; got != 0 {
		t.Errorf("`chat skills --refresh` sent %d cached request(s), want 0", got)
	}
	if n := stub.Calls(); n != 2 {
		t.Errorf("the stub was asked %d time(s), want 2 — one probe and one forced", n)
	}
}

// The daemon's refusals are exit 1 with its own words: exit 0 is for a
// question it answered, and the two must not be confused in a script.
func TestChatSkillsCommandRefusalsExitOne(t *testing.T) {
	h, _ := newChatSkillsLive(t, agent.SkillSyntax{Sigil: "$", Position: agent.SkillAnywhere})

	t.Run("unknown chat", func(t *testing.T) {
		out, errOut, code := runCLI(t, "chat", "skills", "9999")
		if code != 1 {
			t.Fatalf("exit = %d, want 1 (stdout %q, stderr %q)", code, out, errOut)
		}
		if !strings.HasPrefix(errOut, "Error: ") {
			t.Errorf("stderr does not carry the daemon's message: %q", errOut)
		}
	})

	t.Run("terminal chat", func(t *testing.T) {
		chat := h.addSkillChat(t, agenttest.SkillsName, chatstate.Archived)
		out, errOut, code := runCLI(t, "chat", "skills", strconv.FormatInt(chat.ID, 10))
		if code != 1 {
			t.Fatalf("exit = %d, want 1 (stdout %q, stderr %q)", code, out, errOut)
		}
		if !strings.Contains(errOut, "no next turn to list skills for") {
			t.Errorf("stderr does not say why: %q", errOut)
		}
	})

	t.Run("linked task with no worktree", func(t *testing.T) {
		// Opened on a task that had one, and then lost it: OpenLinkedChat
		// refuses a task with none, so this is the only way the route's
		// task_has_no_worktree conflict can be reached at all.
		task := h.addStoppedTask(t, "no-worktree", true)
		chat := &store.Chat{
			Title: "linked", Agent: agenttest.SkillsName, PermissionMode: string(agent.FullAuto),
		}
		if err := h.st.OpenLinkedChat(t.Context(), task.ID, store.TaskBlocked, chat); err != nil {
			t.Fatalf("OpenLinkedChat: %v", err)
		}
		if err := h.st.ClaimTaskWorktree(t.Context(), task.ID, "", "", nil); err != nil {
			t.Fatalf("ClaimTaskWorktree: %v", err)
		}
		out, errOut, code := runCLI(t, "chat", "skills", strconv.FormatInt(chat.ID, 10))
		if code != 1 {
			t.Fatalf("exit = %d, want 1 (stdout %q, stderr %q)", code, out, errOut)
		}
		if !strings.HasPrefix(errOut, "Error: ") {
			t.Errorf("stderr does not carry the daemon's message: %q", errOut)
		}
	})
}

// No daemon is exit 2 through the shared withClient path: a script must be
// able to tell "start the daemon" from "fix your request" (PR U decision).
func TestChatSkillsCommandWithNoDaemonExitsTwo(t *testing.T) {
	t.Setenv(config.EnvDataDir, t.TempDir())
	t.Setenv(config.EnvConfigDir, t.TempDir())
	out, errOut, code := runCLI(t, "chat", "skills", "1")
	if code != 2 {
		t.Fatalf("exit = %d, want 2 (stdout %q, stderr %q)", code, out, errOut)
	}
	if !strings.Contains(errOut, "no running daemon found") {
		t.Errorf("stderr does not name the missing daemon: %q", errOut)
	}
}
