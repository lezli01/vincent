package apiclient_test

import (
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/agent"
	"github.com/lezli01/vincent/internal/agent/agenttest"
	"github.com/lezli01/vincent/internal/api"
	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/chatrun"
	"github.com/lezli01/vincent/internal/chatstate"
	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/store"
)

// Task 124.9's client half: ChatSkills against the real GET
// /v1/chats/{id}/skills handler, its real skill cache and a real chat runner,
// so every wire field — the nulls and the empty arrays included — is decoded
// by the type a client will actually use.

// chatSkillsHarness serves the route over the two skill stubs. Chat rows are
// written through the store: POST /v1/chats refuses both stubs, neither of
// which can resume a session (task 063 decision 3).
type chatSkillsHarness struct {
	client    *apiclient.Client
	store     *store.Store
	stub      *agenttest.StubSkills
	projectID int64
}

func newChatSkillsHarness(t *testing.T) *chatSkillsHarness {
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
	stub := &agenttest.StubSkills{Syntax: agent.SkillSyntax{Sigil: "$", Position: agent.SkillAnywhere}}
	reg := agent.NewRegistry(stub, agenttest.StubNoSkills{})
	cache := agent.NewSkillCache()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	chats := chatrun.New(chatrun.Deps{
		Store: st, Config: config.Default, Agents: reg, DataDir: dataDir, Logger: log,
		InvalidateSkills: cache.Invalidate,
	})
	chats.Start(t.Context())
	t.Cleanup(chats.Stop)

	s := api.New(api.Deps{
		Token: testToken, Config: config.Default, StartedAt: time.Now(),
		ListenAddr: "127.0.0.1:0", RequestStop: func() {}, Logger: log,
		Store: st, Agents: reg, Catalog: agent.NewCatalogCache(reg), Chats: chats, Skills: cache,
	})
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return &chatSkillsHarness{
		client: apiclient.New(ts.URL, testToken), store: st, stub: stub, projectID: project.ID,
	}
}

// chat writes a chat on agentName in state, with a fresh directory as its
// worktree.
func (h *chatSkillsHarness) chat(t *testing.T, agentName string, state chatstate.State) *store.Chat {
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

// TestChatSkillsLiveDecodesAList is a supported list, every field of it.
func TestChatSkillsLiveDecodesAList(t *testing.T) {
	h := newChatSkillsHarness(t)
	h.stub.Script(agent.SkillList{
		Skills: []agent.Skill{
			{
				Name: "deploy", Description: "Ship it (project)", ArgumentHint: "[env]",
				Aliases: []string{"ship"}, Scope: "repo", Plugin: "ops", Path: "/skills/deploy",
			},
			{Name: "deploy", Scope: "user"},
		},
		Problems: []agent.SkillProblem{{Path: "/skills/broken", Message: "missing name"}},
	}, nil)
	c := h.chat(t, agenttest.SkillsName, chatstate.Idle)

	before := time.Now().Add(-time.Second)
	got, err := h.client.ChatSkills(t.Context(), c.ID, false)
	if err != nil {
		t.Fatalf("ChatSkills: %v", err)
	}
	if got.ProbedAt == nil || got.ProbedAt.Before(before.Truncate(time.Second)) {
		t.Fatalf("probed_at = %v, want a time since %v", got.ProbedAt, before)
	}
	want := &apiclient.ChatSkills{
		ChatID: c.ID, Agent: agenttest.SkillsName, WorkDir: c.WorktreePath,
		ListVerdict: "supported", ProbedAt: got.ProbedAt,
		InvokeVerdict: "supported", InvokeSigil: "$", InvokePosition: "anywhere",
		Skills: []apiclient.ChatSkill{
			{
				Name: "deploy", Invocation: "$deploy", Description: "Ship it (project)", ArgumentHint: "[env]",
				Aliases: []string{"ship"}, Scope: "repo", Plugin: "ops", Path: "/skills/deploy",
			},
			{Name: "deploy", Invocation: "$deploy", Aliases: []string{}, Scope: "user"},
		},
		Problems: []apiclient.ChatSkillProblem{{Path: "/skills/broken", Message: "missing name"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ChatSkills =\n%+v\nwant\n%+v", got, want)
	}
}

// TestChatSkillsLiveDecodesNullsAndEmptyArrays: a no and a failed probe carry
// the nullable fields both ways, and every array arrives as one, never nil.
func TestChatSkillsLiveDecodesNullsAndEmptyArrays(t *testing.T) {
	h := newChatSkillsHarness(t)

	no, err := h.client.ChatSkills(t.Context(), h.chat(t, agenttest.NoSkillsName, chatstate.Idle).ID, false)
	if err != nil {
		t.Fatalf("ChatSkills on %s: %v", agenttest.NoSkillsName, err)
	}
	if no.ListVerdict != "unsupported" || no.UnavailableReason == "" || no.InvokeVerdict != "unsupported" {
		t.Errorf("no = %+v, want unsupported with a reason", no)
	}
	if no.ProbeError != nil || no.ProbedAt != nil {
		t.Errorf("probe_error %v probed_at %v, want both null", no.ProbeError, no.ProbedAt)
	}
	if no.Skills == nil || len(no.Skills) != 0 || no.Problems == nil || len(no.Problems) != 0 {
		t.Errorf("skills %#v problems %#v, want empty arrays", no.Skills, no.Problems)
	}

	h.stub.Script(agent.SkillList{}, errors.New("probe timed out"))
	failed, err := h.client.ChatSkills(t.Context(), h.chat(t, agenttest.SkillsName, chatstate.Idle).ID, false)
	if err != nil {
		t.Fatalf("ChatSkills on a failing probe: %v", err)
	}
	if failed.ListVerdict != "unknown" || failed.ProbeError == nil || *failed.ProbeError != "probe timed out" {
		t.Errorf("failed = %q %v, want unknown with probe_error", failed.ListVerdict, failed.ProbeError)
	}
	if failed.ProbedAt != nil || failed.Skills == nil || failed.Problems == nil {
		t.Errorf("probed_at %v skills %#v problems %#v, want null and arrays",
			failed.ProbedAt, failed.Skills, failed.Problems)
	}
}

// TestChatSkillsLiveRefreshRoundTrips: refresh=true reaches the daemon as a
// forced probe, and its absence is served from the cache.
func TestChatSkillsLiveRefreshRoundTrips(t *testing.T) {
	h := newChatSkillsHarness(t)
	h.stub.Script(agent.SkillList{Skills: []agent.Skill{{Name: "review"}}}, nil)
	c := h.chat(t, agenttest.SkillsName, chatstate.Idle)

	for range 2 {
		if _, err := h.client.ChatSkills(t.Context(), c.ID, false); err != nil {
			t.Fatalf("ChatSkills: %v", err)
		}
	}
	if n := h.stub.Calls(); n != 1 {
		t.Fatalf("stub listed %d time(s) without refresh, want 1", n)
	}
	if _, err := h.client.ChatSkills(t.Context(), c.ID, true); err != nil {
		t.Fatalf("ChatSkills(refresh): %v", err)
	}
	if n := h.stub.Calls(); n != 2 {
		t.Errorf("stub listed %d time(s) after refresh, want 2", n)
	}
}

// TestChatSkillsLiveTerminalChatIsATypedConflict: the one refusal a client
// branches on arrives as an *apiclient.Error carrying the details.
func TestChatSkillsLiveTerminalChatIsATypedConflict(t *testing.T) {
	h := newChatSkillsHarness(t)
	_, err := h.client.ChatSkills(t.Context(), h.chat(t, agenttest.SkillsName, chatstate.Archived).ID, true)
	var apiErr *apiclient.Error
	if !errors.As(err, &apiErr) {
		t.Fatalf("ChatSkills on an archived chat = %v, want *apiclient.Error", err)
	}
	if apiErr.Status != http.StatusConflict || apiErr.Code != api.CodeInvalidState ||
		apiErr.Details["state"] != string(chatstate.Archived) || apiErr.Details["action"] != "skills" {
		t.Errorf("error = %+v, want 409 invalid_state {state: archived, action: skills}", apiErr)
	}
	if n := h.stub.Calls(); n != 0 {
		t.Errorf("stub listed %d time(s) for an archived chat", n)
	}
}
