package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/agent"
	"github.com/lezli01/vincent/internal/agent/agenttest"
	"github.com/lezli01/vincent/internal/chatrun"
	"github.com/lezli01/vincent/internal/chatstate"
	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/store"
)

// skillsHarness is GET /v1/chats/{id}/skills over the real handler, the real
// skill cache and a started chat runner (task 124.9, #505). The adapters are
// the two skill stubs, never a shipped CLI (task 124 decision 15): a
// capability proven against a real adapter inverts itself the day that CLI
// changes.
//
// Chat creation over the API refuses both stubs — neither can resume, and
// that refusal is task 063 decision 3 — so the rows are written through the
// store, each with a directory of its own standing in for its worktree.
type skillsHarness struct {
	*projectHarness
	deps      Deps
	stub      *agenttest.StubSkills
	chats     *chatrun.Runner
	projectID int64
	// inContainer is what the chat runner's InContainer reports for every
	// linked chat; containerAsks counts the times it was asked.
	inContainer   atomic.Bool
	containerAsks atomic.Int32
}

func newSkillsHarness(t *testing.T) *skillsHarness {
	t.Helper()
	dataDir := t.TempDir()
	st, err := store.Open(filepath.Join(dataDir, "skills.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	project := &store.Project{Name: "proj", Path: t.TempDir(), DefaultBranch: "main"}
	if err := st.CreateProject(t.Context(), project); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	// A non-default syntax, so an invocation the route built itself rather
	// than asking the adapter would show.
	stub := &agenttest.StubSkills{Syntax: agent.SkillSyntax{Sigil: "$", Position: agent.SkillAnywhere}}
	reg := agent.NewRegistry(stub, agenttest.StubNoSkills{})
	cache := agent.NewSkillCache()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	h := &skillsHarness{stub: stub, projectID: project.ID}
	h.chats = chatrun.New(chatrun.Deps{
		Store: st, Config: config.Default, Agents: reg, DataDir: dataDir, Logger: log,
		InContainer: func(context.Context, int64) (bool, error) {
			h.containerAsks.Add(1)
			return h.inContainer.Load(), nil
		},
		InvalidateSkills: cache.Invalidate,
	})
	h.chats.Start(t.Context())
	t.Cleanup(h.chats.Stop)

	h.deps = Deps{
		Token: testToken, Config: config.Default, StartedAt: time.Now(), ListenAddr: "127.0.0.1:0",
		RequestStop: func() {}, Logger: log, Store: st, Agents: reg,
		Catalog: agent.NewCatalogCache(reg), Chats: h.chats, Skills: cache,
	}
	ts := httptest.NewServer(New(h.deps).Handler())
	t.Cleanup(ts.Close)
	h.projectHarness = &projectHarness{ts: ts, store: st}
	return h
}

// freeChat writes a chat of the harness's project on agentName, in state, with
// a fresh directory as its worktree.
func (h *skillsHarness) freeChat(t *testing.T, agentName string, state chatstate.State) *store.Chat {
	t.Helper()
	c := &store.Chat{
		ProjectID: h.projectID, Title: "skills", State: state, Agent: agentName,
		PermissionMode: string(agent.FullAuto), BaseBranch: "main", WorktreePath: t.TempDir(),
	}
	if err := h.store.CreateChat(t.Context(), c); err != nil {
		t.Fatalf("CreateChat: %v", err)
	}
	return c
}

// linkedChat writes a blocked task working in a fresh directory, and a chat on
// the listing stub opened on it.
func (h *skillsHarness) linkedChat(t *testing.T) (*store.Task, *store.Chat) {
	t.Helper()
	ctx := t.Context()
	task := &store.Task{
		ProjectID: h.projectID, Title: "linked", WorkflowName: "t", WorkflowSnapshot: "steps: []",
		BaseBranch: "main", State: store.TaskBlocked, WorktreePath: t.TempDir(),
	}
	branch := func(id int64) (string, error) { return fmt.Sprintf("vincent/%d-linked", id), nil }
	if err := h.store.CreateTask(ctx, task, branch); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	c := &store.Chat{Title: "why", Agent: agenttest.SkillsName, PermissionMode: string(agent.FullAuto)}
	if err := h.store.OpenLinkedChat(ctx, task.ID, store.TaskBlocked, c); err != nil {
		t.Fatalf("OpenLinkedChat: %v", err)
	}
	return task, c
}

// get fetches a chat's skills and returns the status and the raw body.
func (h *skillsHarness) get(t *testing.T, chatID int64, query string) (int, []byte) {
	t.Helper()
	resp, body := h.doJSON(t, http.MethodGet, fmt.Sprintf("/v1/chats/%d/skills%s", chatID, query), nil)
	return resp.StatusCode, body
}

// list fetches a chat's skills, requires a 200, and decodes the body.
func (h *skillsHarness) list(t *testing.T, chatID int64, query string) chatSkillsBody {
	t.Helper()
	code, body := h.get(t, chatID, query)
	if code != http.StatusOK {
		t.Fatalf("GET skills%s = %d, want 200 (%s)", query, code, body)
	}
	var out chatSkillsBody
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("skills body: %v (%s)", err, body)
	}
	return out
}

// decodeErrorBody decodes a §13.1 error envelope.
func decodeErrorBody(t *testing.T, body []byte) errorDetail {
	t.Helper()
	var out errorBody
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("error body: %v (%s)", err, body)
	}
	return out.Error
}

// wantCalls fails unless the stub has been asked exactly n times.
func (h *skillsHarness) wantCalls(t *testing.T, n int, when string) {
	t.Helper()
	if got := h.stub.Calls(); got != n {
		t.Fatalf("%s: stub listed %d time(s), want %d", when, got, n)
	}
}

var testSkills = agent.SkillList{
	Skills: []agent.Skill{
		{Name: "review", Description: "Review a branch (project)", ArgumentHint: "[base]"},
		{Name: "deploy", Aliases: []string{"ship"}, Scope: "repo", Plugin: "ops", Path: "/skills/deploy"},
		{Name: "review", Scope: "user"},
	},
	Problems: []agent.SkillProblem{{Path: "/skills/broken/SKILL.md", Message: "missing name"}},
}

// TestChatSkillsListsAFreeChatBeforeAnyTurn is the route's first promise: a
// chat's worktree exists from creation, so the list is there before the first
// message, and it is asked about exactly that directory.
func TestChatSkillsListsAFreeChatBeforeAnyTurn(t *testing.T) {
	h := newSkillsHarness(t)
	h.stub.Script(testSkills, nil)
	c := h.freeChat(t, agenttest.SkillsName, chatstate.Idle)

	got := h.list(t, c.ID, "")
	if got.ChatID != c.ID || got.Agent != agenttest.SkillsName || got.WorkDir != c.WorktreePath {
		t.Errorf("identity = (%d, %q, %q), want (%d, %q, %q)",
			got.ChatID, got.Agent, got.WorkDir, c.ID, agenttest.SkillsName, c.WorktreePath)
	}
	if got.ListVerdict != "supported" || got.UnavailableReason != "" || got.ProbeError != nil {
		t.Errorf("verdict = %q, reason %q, probe_error %v; want supported, none, null",
			got.ListVerdict, got.UnavailableReason, got.ProbeError)
	}
	if got.ProbedAt == nil {
		t.Error("probed_at is null for a list that was just probed")
	} else if _, err := time.Parse(time.RFC3339, *got.ProbedAt); err != nil {
		t.Errorf("probed_at %q is not RFC3339: %v", *got.ProbedAt, err)
	}
	if len(got.Skills) != len(testSkills.Skills) {
		t.Fatalf("skills = %+v, want %d", got.Skills, len(testSkills.Skills))
	}
	qs := h.stub.Queries()
	if len(qs) != 1 || qs[0].WorkDir != c.WorktreePath {
		t.Fatalf("stub asked about %+v, want once about %q", qs, c.WorktreePath)
	}
}

// TestChatSkillsListsALinkedChatInItsTasksWorktree: a linked chat's next turn
// runs in its task's worktree (task 119), so that is the directory listed.
func TestChatSkillsListsALinkedChatInItsTasksWorktree(t *testing.T) {
	h := newSkillsHarness(t)
	h.stub.Script(testSkills, nil)
	task, c := h.linkedChat(t)

	got := h.list(t, c.ID, "")
	if got.WorkDir != task.WorktreePath || got.ListVerdict != "supported" {
		t.Errorf("work_dir %q verdict %q, want %q supported", got.WorkDir, got.ListVerdict, task.WorktreePath)
	}
	qs := h.stub.Queries()
	if len(qs) != 1 || qs[0].WorkDir != task.WorktreePath {
		t.Fatalf("stub asked about %+v, want once about the task's %q", qs, task.WorktreePath)
	}
	if h.containerAsks.Load() == 0 {
		t.Error("a linked chat was listed without asking where its task runs")
	}
}

// TestChatSkillsRefusesTerminalChats is task 124 decision 4: 409 only for
// state. A terminal chat has no next turn and so no directory a list could be
// about, and the refusal comes before refresh is read — nothing is probed. A
// live chat is listed: a probe is not a turn and holds no chat slot.
func TestChatSkillsRefusesTerminalChats(t *testing.T) {
	h := newSkillsHarness(t)
	h.stub.Script(testSkills, nil)
	closedTask, closed := h.linkedChat(t)
	if _, err := h.store.CloseChat(t.Context(), closed.ID); err != nil {
		t.Fatalf("CloseChat on task %d: %v", closedTask.ID, err)
	}
	terminal := map[chatstate.State]int64{
		chatstate.Archived:  h.freeChat(t, agenttest.SkillsName, chatstate.Archived).ID,
		chatstate.HandedOff: h.freeChat(t, agenttest.SkillsName, chatstate.HandedOff).ID,
		chatstate.Closed:    closed.ID,
	}
	for st, id := range terminal {
		for _, q := range []string{"", "?refresh=true"} {
			code, body := h.get(t, id, q)
			if code != http.StatusConflict {
				t.Fatalf("%s%s = %d, want 409 (%s)", st, q, code, body)
			}
			e := decodeErrorBody(t, body)
			if e.Code != CodeInvalidState || e.Details["state"] != string(st) || e.Details["action"] != "skills" {
				t.Errorf("%s%s error = %+v, want invalid_state with state %q and action skills", st, q, e, st)
			}
		}
	}
	h.wantCalls(t, 0, "after every terminal chat was asked")

	for _, st := range []chatstate.State{chatstate.Idle, chatstate.Running, chatstate.AwaitingInput} {
		if got := h.list(t, h.freeChat(t, agenttest.SkillsName, st).ID, ""); got.ListVerdict != "supported" {
			t.Errorf("%s chat verdict = %q, want supported", st, got.ListVerdict)
		}
	}
}

// TestChatSkillsLinkedTaskWithNoWorktree reuses the open-time code (task 119):
// a linked chat whose task lost its worktree has nowhere to list, and the
// details name the task to look at.
func TestChatSkillsLinkedTaskWithNoWorktree(t *testing.T) {
	h := newSkillsHarness(t)
	task, c := h.linkedChat(t)
	task.WorktreePath = ""
	if err := h.store.UpdateTask(t.Context(), task); err != nil {
		t.Fatalf("UpdateTask: %v", err)
	}

	code, body := h.get(t, c.ID, "")
	if code != http.StatusConflict {
		t.Fatalf("GET skills = %d, want 409 (%s)", code, body)
	}
	e := decodeErrorBody(t, body)
	if e.Code != CodeTaskHasNoWorktree || e.Details["task_id"] != fmt.Sprint(task.ID) {
		t.Errorf("error = %+v, want %s with task_id %d", e, CodeTaskHasNoWorktree, task.ID)
	}
	h.wantCalls(t, 0, "with no worktree")
}

// TestChatSkillsAdapterThatCannotListOrInvoke is task 124 decision 40, proven
// against StubNoSkills and never against cursor: the route words the refusal
// itself, both verdicts are a positive no, and the syntax is empty.
func TestChatSkillsAdapterThatCannotListOrInvoke(t *testing.T) {
	h := newSkillsHarness(t)
	c := h.freeChat(t, agenttest.NoSkillsName, chatstate.Idle)

	got := h.list(t, c.ID, "?refresh=true")
	want := agenttest.NoSkillsName + " does not report the skills it loads"
	if got.ListVerdict != "unsupported" || got.UnavailableReason != want {
		t.Errorf("list = %q %q, want unsupported %q", got.ListVerdict, got.UnavailableReason, want)
	}
	if got.InvokeVerdict != "unsupported" || got.InvokeSigil != "" || got.InvokePosition != "" {
		t.Errorf("invoke = %q %q %q, want unsupported with no syntax",
			got.InvokeVerdict, got.InvokeSigil, got.InvokePosition)
	}
	if got.ProbeError != nil || got.ProbedAt != nil || got.WorkDir != c.WorktreePath {
		t.Errorf("probe_error %v probed_at %v work_dir %q; want null, null, %q",
			got.ProbeError, got.ProbedAt, got.WorkDir, c.WorktreePath)
	}
	h.wantCalls(t, 0, "for an adapter that cannot list")
}

// TestChatSkillsProbeOutcomes maps the cache's three answers (§9.6, task 124
// decision 16): ErrSkillsUnsupported is the one positive no, any other error
// is unknown, and a failure after a clean list keeps that list beside the
// error (T4.22).
func TestChatSkillsProbeOutcomes(t *testing.T) {
	t.Run("unsupported build", func(t *testing.T) {
		h := newSkillsHarness(t)
		err := fmt.Errorf("stubskills 0.1 predates listing: %w", agent.ErrSkillsUnsupported)
		h.stub.Script(agent.SkillList{}, err)
		got := h.list(t, h.freeChat(t, agenttest.SkillsName, chatstate.Idle).ID, "")
		if got.ListVerdict != "unsupported" || got.UnavailableReason != err.Error() || got.ProbeError != nil {
			t.Errorf("got %q %q %v, want unsupported %q with no probe_error",
				got.ListVerdict, got.UnavailableReason, got.ProbeError, err.Error())
		}
		// The invoke half is the adapter's, not the build's.
		if got.InvokeVerdict != "supported" {
			t.Errorf("invoke_verdict = %q, want supported", got.InvokeVerdict)
		}
	})
	t.Run("failure with no earlier list", func(t *testing.T) {
		h := newSkillsHarness(t)
		h.stub.Script(agent.SkillList{}, errors.New("probe timed out"))
		got := h.list(t, h.freeChat(t, agenttest.SkillsName, chatstate.Idle).ID, "")
		if got.ListVerdict != "unknown" || got.ProbeError == nil || *got.ProbeError != "probe timed out" {
			t.Errorf("got %q %v, want unknown with probe_error", got.ListVerdict, got.ProbeError)
		}
		if got.UnavailableReason != "" || got.ProbedAt != nil || len(got.Skills) != 0 {
			t.Errorf("reason %q probed_at %v skills %v; want none of them", got.UnavailableReason, got.ProbedAt, got.Skills)
		}
	})
	t.Run("failure after a clean list", func(t *testing.T) {
		h := newSkillsHarness(t)
		h.stub.Script(testSkills, nil)
		c := h.freeChat(t, agenttest.SkillsName, chatstate.Idle)
		first := h.list(t, c.ID, "")
		h.stub.Script(agent.SkillList{}, errors.New("probe timed out"))
		got := h.list(t, c.ID, "?refresh=true")
		h.wantCalls(t, 2, "after the forced second probe")
		if got.ListVerdict != "supported" || got.ProbeError == nil || *got.ProbeError != "probe timed out" {
			t.Errorf("got %q %v, want supported with probe_error", got.ListVerdict, got.ProbeError)
		}
		if !reflect.DeepEqual(got.Skills, first.Skills) || got.ProbedAt == nil || *got.ProbedAt != *first.ProbedAt {
			t.Errorf("kept list = %+v at %v, want the earlier %+v at %v",
				got.Skills, got.ProbedAt, first.Skills, first.ProbedAt)
		}
	})
}

// TestChatSkillsContainerRunChatSpawnsNothing is task 124 decision 11: a host
// probe would list skills the agent in the container never loads. The answer
// is unknown with a reason, the cache is never reached — refresh or not — and
// the invoke half, a fact about the adapter, is still served.
func TestChatSkillsContainerRunChatSpawnsNothing(t *testing.T) {
	h := newSkillsHarness(t)
	h.stub.Script(testSkills, nil)
	h.inContainer.Store(true)
	task, c := h.linkedChat(t)

	for _, q := range []string{"", "?refresh=true"} {
		got := h.list(t, c.ID, q)
		if got.ListVerdict != "unknown" || got.UnavailableReason == "" {
			t.Errorf("%q: got %q %q, want unknown with a reason", q, got.ListVerdict, got.UnavailableReason)
		}
		if got.WorkDir != task.WorktreePath || len(got.Skills) != 0 || got.ProbeError != nil {
			t.Errorf("%q: work_dir %q skills %v probe_error %v", q, got.WorkDir, got.Skills, got.ProbeError)
		}
		if got.InvokeVerdict != "supported" || got.InvokeSigil != "$" || got.InvokePosition != "anywhere" {
			t.Errorf("%q: invoke = %q %q %q, want the stub's syntax",
				q, got.InvokeVerdict, got.InvokeSigil, got.InvokePosition)
		}
	}
	h.wantCalls(t, 0, "for a container-run chat")
}

// TestChatSkillsUnregisteredAdapter is task 124 decision 41: an adapter the
// daemon no longer has is "nobody can say" on both halves, never a no.
func TestChatSkillsUnregisteredAdapter(t *testing.T) {
	h := newSkillsHarness(t)
	got := h.list(t, h.freeChat(t, "gone", chatstate.Idle).ID, "")
	if got.ListVerdict != "unknown" || got.ProbeError == nil || *got.ProbeError != `no adapter named "gone"` {
		t.Errorf("list = %q %v, want unknown with probe_error", got.ListVerdict, got.ProbeError)
	}
	if got.InvokeVerdict != "unknown" || got.InvokeSigil != "" || got.InvokePosition != "" {
		t.Errorf("invoke = %q %q %q, want unknown with no syntax",
			got.InvokeVerdict, got.InvokeSigil, got.InvokePosition)
	}
	h.wantCalls(t, 0, "for an unregistered adapter")
}

// TestChatSkillsWireShape pins what a client reads: the adapter's own
// invocation and syntax (task 124 decision 9) with a non-default sigil, the
// CLI's order with its duplicate names, and arrays that are never null.
func TestChatSkillsWireShape(t *testing.T) {
	h := newSkillsHarness(t)
	h.stub.Script(testSkills, nil)
	c := h.freeChat(t, agenttest.SkillsName, chatstate.Idle)

	got := h.list(t, c.ID, "")
	if got.InvokeVerdict != "supported" || got.InvokeSigil != "$" || got.InvokePosition != string(agent.SkillAnywhere) {
		t.Errorf("invoke = %q %q %q, want supported $ anywhere", got.InvokeVerdict, got.InvokeSigil, got.InvokePosition)
	}
	want := []chatSkillBody{
		{Name: "review", Invocation: "$review", Description: "Review a branch (project)", ArgumentHint: "[base]", Aliases: []string{}},
		{Name: "deploy", Invocation: "$deploy", Aliases: []string{"ship"}, Scope: "repo", Plugin: "ops", Path: "/skills/deploy"},
		{Name: "review", Invocation: "$review", Aliases: []string{}, Scope: "user"},
	}
	if !reflect.DeepEqual(got.Skills, want) {
		t.Errorf("skills =\n%+v\nwant\n%+v", got.Skills, want)
	}
	for i, sk := range testSkills.Skills {
		if inv := h.stub.Invocation(sk, testSkills.Skills); got.Skills[i].Invocation != inv {
			t.Errorf("skills[%d].invocation = %q, want the adapter's %q", i, got.Skills[i].Invocation, inv)
		}
	}
	wantProblems := []chatProblemBody{{Path: "/skills/broken/SKILL.md", Message: "missing name"}}
	if !reflect.DeepEqual(got.Problems, wantProblems) {
		t.Errorf("problems = %+v, want %+v", got.Problems, wantProblems)
	}

	// An empty clean list, and a no, both render every array as [].
	h.stub.Script(agent.SkillList{}, nil)
	empty := h.freeChat(t, agenttest.SkillsName, chatstate.Idle)
	no := h.freeChat(t, agenttest.NoSkillsName, chatstate.Idle)
	for _, id := range []int64{empty.ID, no.ID, c.ID} {
		_, body := h.get(t, id, "")
		var raw struct {
			Skills     []map[string]json.RawMessage `json:"skills"`
			Problems   json.RawMessage              `json:"problems"`
			ProbeError json.RawMessage              `json:"probe_error"`
		}
		if err := json.Unmarshal(body, &raw); err != nil {
			t.Fatalf("chat %d body: %v", id, err)
		}
		if raw.Skills == nil || string(raw.Problems) == "null" || string(raw.ProbeError) != "null" {
			t.Errorf("chat %d: skills %v problems %s probe_error %s; want arrays and null (%s)",
				id, raw.Skills, raw.Problems, raw.ProbeError, body)
		}
		for i, sk := range raw.Skills {
			if string(sk["aliases"]) == "null" {
				t.Errorf("chat %d skills[%d].aliases is null", id, i)
			}
		}
	}
}

// TestChatSkillsServesFromTheCache: the route takes the cache's TTL (§9.6),
// so a second read costs no probe, and `?refresh=true` costs exactly one.
func TestChatSkillsServesFromTheCache(t *testing.T) {
	h := newSkillsHarness(t)
	h.stub.Script(testSkills, nil)
	c := h.freeChat(t, agenttest.SkillsName, chatstate.Idle)

	h.list(t, c.ID, "")
	h.list(t, c.ID, "")
	h.list(t, c.ID, "?refresh=false")
	h.wantCalls(t, 1, "after three reads within the TTL")
	h.list(t, c.ID, "?refresh=true")
	h.wantCalls(t, 2, "after ?refresh=true")
}

// TestChatSkillsTurnEndingInvalidates drives a real turn through the chat
// runner wired to the route's cache: a turn is the one change vincent sees
// happen in the directory, so the next read probes again. A stub turn fails at
// Start, after placement, which is an ending like any other.
func TestChatSkillsTurnEndingInvalidates(t *testing.T) {
	h := newSkillsHarness(t)
	h.stub.Script(testSkills, nil)
	c := h.freeChat(t, agenttest.SkillsName, chatstate.Idle)

	h.list(t, c.ID, "")
	h.list(t, c.ID, "")
	h.wantCalls(t, 1, "before the turn")

	turn, err := h.chats.Send(t.Context(), c.ID, "$review")
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		turns, err := h.store.ListChatTurns(t.Context(), c.ID)
		if err != nil {
			t.Fatalf("ListChatTurns: %v", err)
		}
		if len(turns) == 1 && turns[0].ID == turn.ID && chatstate.TurnTerminal(turns[0].State) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("turn never ended: %+v", turns)
		}
		time.Sleep(10 * time.Millisecond)
	}

	h.list(t, c.ID, "")
	h.wantCalls(t, 2, "after the turn ended")
	if qs := h.stub.Queries(); qs[1].WorkDir != c.WorktreePath {
		t.Errorf("re-probe asked about %q, want %q", qs[1].WorkDir, c.WorktreePath)
	}
}

// TestChatSkillsRequestErrors is the route's non-200 edges besides state: the
// tolerated-nil convention, a bad id and a missing chat.
func TestChatSkillsRequestErrors(t *testing.T) {
	h := newSkillsHarness(t)
	c := h.freeChat(t, agenttest.SkillsName, chatstate.Idle)

	for name, strip := range map[string]func(*Deps){
		"no cache":       func(d *Deps) { d.Skills = nil },
		"no chat runner": func(d *Deps) { d.Chats = nil },
	} {
		d := h.deps
		strip(&d)
		ts := httptest.NewServer(New(d).Handler())
		resp, body := (&projectHarness{ts: ts}).doJSON(t, http.MethodGet, fmt.Sprintf("/v1/chats/%d/skills", c.ID), nil)
		ts.Close()
		if e := decodeErrorBody(t, body); resp.StatusCode != http.StatusInternalServerError || e.Code != CodeInternal {
			t.Errorf("%s: %d %+v, want 500 internal", name, resp.StatusCode, e)
		}
	}

	resp, body := h.doJSON(t, http.MethodGet, "/v1/chats/abc/skills", nil)
	if e := decodeErrorBody(t, body); resp.StatusCode != http.StatusBadRequest || e.Code != CodeValidationFailed {
		t.Errorf("bad id: %d %+v, want 400 validation_failed", resp.StatusCode, e)
	}
	code, body := h.get(t, c.ID+1000, "")
	if e := decodeErrorBody(t, body); code != http.StatusNotFound || e.Code != CodeNotFound {
		t.Errorf("missing chat: %d %+v, want 404 not_found", code, e)
	}
	h.wantCalls(t, 0, "after only refused requests")
}
