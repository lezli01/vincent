package apiclient_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/agent"
	"github.com/lezli01/vincent/internal/agent/agenttest"
	"github.com/lezli01/vincent/internal/agent/claude"
	"github.com/lezli01/vincent/internal/agent/cursor"
	"github.com/lezli01/vincent/internal/api"
	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/chatrun"
	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/events"
	"github.com/lezli01/vincent/internal/gitx"
	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/testrepo"
	"github.com/lezli01/vincent/internal/worktree"
)

// Task 124.2's two records over a real chat turn: a started chat runner on
// fakeagent, the real handlers, and this client on both ends — the SSE tail
// and the refetched transcript — which is task 071 decision 1 for agent.skill
// and agent.input_echo.

// chatTurnHarness serves real chat turns: fakeagent behind claude and cursor,
// a started chat runner publishing to the broker, and the API over both.
type chatTurnHarness struct {
	client    *apiclient.Client
	projectID int64
}

func newChatTurnHarness(t *testing.T) *chatTurnHarness {
	t.Helper()
	fake := agenttest.BuildFakeAgent(t)
	t.Setenv("FAKEAGENT_SESSION_DIR", t.TempDir())
	dataDir := t.TempDir()
	st, err := store.Open(filepath.Join(dataDir, "chats.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	broker := events.New()
	t.Cleanup(broker.Close)
	st.SetEventHook(broker.Publish)

	git := gitx.New()
	wt := worktree.NewManager(git, dataDir)
	bin := func() string { return fake }
	reg := agent.NewRegistry(claude.New(bin), cursor.New(bin))
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	runner := chatrun.New(chatrun.Deps{
		Store: st, Config: config.Default, Worktrees: wt, Agents: reg,
		DataDir: dataDir, Logger: log, Events: broker,
	})
	runner.Start(t.Context())
	t.Cleanup(runner.Stop)

	s := api.New(api.Deps{
		Token: testToken, Config: config.Default, StartedAt: time.Now(),
		ListenAddr: "127.0.0.1:0", RequestStop: func() {}, Logger: log,
		Store: st, Broker: broker, Git: git, Worktrees: wt, Agents: reg,
		Catalog: agent.NewCatalogCache(reg), Chats: runner,
		Dirs: config.Dirs{Data: dataDir},
	})
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)

	h := &chatTurnHarness{client: apiclient.New(ts.URL, testToken)}
	p, err := h.client.CreateProject(t.Context(), apiclient.CreateProjectRequest{Path: testrepo.Init(t, "main")})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	h.projectID = p.ID
	return h
}

// turn runs one chat turn on agentName to its end and returns every output
// chunk the chat's stream delivered, and the turn's refetched transcript.
func (h *chatTurnHarness) turn(t *testing.T, agentName string) ([]apiclient.OutputNote, []apiclient.TranscriptRecord) {
	t.Helper()
	return h.turnSaying(t, agentName, "load the echo probe")
}

// turnSaying is turn, with the message the human sent — which for task
// 124.10 is the whole input: a leading `/name` is what the CLI expands.
func (h *chatTurnHarness) turnSaying(
	t *testing.T, agentName, message string,
) ([]apiclient.OutputNote, []apiclient.TranscriptRecord) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	chat, err := h.client.CreateChat(ctx, apiclient.CreateChatRequest{
		ProjectID: h.projectID, Title: "skills", Agent: agentName,
	})
	if err != nil {
		t.Fatalf("CreateChat(%s): %v", agentName, err)
	}

	notes := h.client.StreamChat(ctx, chat.ID, testStreamOptions())
	if _, ok := nextNote(t, notes).(apiclient.ConnectedNote); !ok {
		t.Fatal("first note was not ConnectedNote")
	}
	var (
		mu     sync.Mutex
		chunks []apiclient.OutputNote
		last   = time.Now()
	)
	go func() {
		for n := range notes {
			if out, ok := n.(apiclient.OutputNote); ok {
				mu.Lock()
				chunks = append(chunks, out)
				last = time.Now()
				mu.Unlock()
			}
		}
	}()

	sent, err := h.client.SendChat(ctx, chat.ID, message)
	if err != nil {
		t.Fatalf("SendChat: %v", err)
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		_, turns, err := h.client.GetChat(ctx, chat.ID)
		if err != nil {
			t.Fatalf("GetChat: %v", err)
		}
		if n := len(turns); n > 0 && turns[n-1].Seq == sent.Seq && turns[n-1].State != "running" {
			if turns[n-1].State != "done" {
				t.Fatalf("turn ended %s (%s)", turns[n-1].State, turns[n-1].ErrorMessage)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the turn never finished")
		}
		time.Sleep(20 * time.Millisecond)
	}
	// The chunks were all published before the turn ended; wait for the
	// stream to go quiet so every one has been delivered.
	for {
		mu.Lock()
		quiet := time.Since(last) > 300*time.Millisecond
		mu.Unlock()
		if quiet {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	records, _, err := h.client.ChatTurnTranscript(ctx, chat.ID, sent.Seq, apiclient.TranscriptOptions{})
	if err != nil {
		t.Fatalf("ChatTurnTranscript: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	return append([]apiclient.OutputNote(nil), chunks...), records
}

// TestSkillChunkMatchesItsRecord: the agent.skill a claude turn publishes
// live, decoded through this client, is exactly the record the finished
// turn's transcript hands back for the same line.
func TestSkillChunkMatchesItsRecord(t *testing.T) {
	t.Setenv("FAKEAGENT_SCENARIO", "skill-model")
	h := newChatTurnHarness(t)
	chunks, records := h.turn(t, "claude")

	var live []apiclient.TranscriptRecord
	for _, c := range chunks {
		if c.Type != "agent.skill" {
			continue
		}
		var rec apiclient.TranscriptRecord
		if err := json.Unmarshal(c.Payload, &rec); err != nil {
			t.Fatalf("decode chunk: %v", err)
		}
		rec.Type = c.Type
		live = append(live, rec)
	}
	var stored []apiclient.TranscriptRecord
	for _, rec := range records {
		if rec.Type == "agent.skill" {
			rec.Raw = nil
			stored = append(stored, rec)
		}
		if rec.Type == "agent.raw" && strings.Contains(rec.Line, "isSynthetic") {
			t.Errorf("the skill body is still agent.raw: %s", rec.Line)
		}
	}
	if len(live) != 1 || !reflect.DeepEqual(live, stored) {
		t.Fatalf("live %+v, stored %+v: want the one load, identical", live, stored)
	}
	want := apiclient.TranscriptRecord{
		Type: "agent.skill", Name: "echo-probe", Args: "zebra", By: "agent", CallID: "toolu_fake_skill_1",
	}
	if !reflect.DeepEqual(live[0], want) {
		t.Errorf("record = %+v, want %+v", live[0], want)
	}
}

// TestHumanSkillChunkMatchesItsRecord is task 124.10's half of the same
// agreement: the agent.skill a turn publishes for a skill the *human*
// invoked is the record the transcript route hands back for that line, and
// neither the invocation nor any other replayed line comes back as agent.raw.
func TestHumanSkillChunkMatchesItsRecord(t *testing.T) {
	t.Setenv("FAKEAGENT_SCENARIO", "skill-human")
	h := newChatTurnHarness(t)
	chunks, records := h.turnSaying(t, "claude", "/echo-probe zebra")

	var live []apiclient.TranscriptRecord
	for _, c := range chunks {
		if c.Type != "agent.skill" {
			continue
		}
		var rec apiclient.TranscriptRecord
		if err := json.Unmarshal(c.Payload, &rec); err != nil {
			t.Fatalf("decode chunk: %v", err)
		}
		rec.Type = c.Type
		live = append(live, rec)
	}
	var stored []apiclient.TranscriptRecord
	for _, rec := range records {
		if rec.Type == "agent.skill" {
			rec.Raw = nil
			stored = append(stored, rec)
		}
		if rec.Type == "agent.raw" && strings.Contains(rec.Line, `"isReplay"`) {
			t.Errorf("a replayed line came back as agent.raw: %s", rec.Line)
		}
	}
	if len(live) != 1 || !reflect.DeepEqual(live, stored) {
		t.Fatalf("live %+v, stored %+v: want the one invocation, identical", live, stored)
	}
	want := apiclient.TranscriptRecord{
		Type: "agent.skill", Name: "echo-probe", Args: "zebra", By: "human",
	}
	if !reflect.DeepEqual(live[0], want) {
		t.Errorf("record = %+v, want %+v", live[0], want)
	}
}

// TestCursorEchoIsNotRaw: a cursor turn's prompt echo publishes no chunk at
// all and refetches as agent.input_echo, never as agent.raw.
func TestCursorEchoIsNotRaw(t *testing.T) {
	t.Setenv("FAKEAGENT_SCENARIO_CURSOR", "success")
	h := newChatTurnHarness(t)
	chunks, records := h.turn(t, "cursor")

	for _, c := range chunks {
		if c.Type == "agent.input_echo" || (c.Type == "agent.raw" && strings.Contains(string(c.Payload), `\"type\":\"user\"`)) {
			t.Errorf("the echo published a %s chunk: %s", c.Type, c.Payload)
		}
	}
	var echoes int
	for _, rec := range records {
		switch rec.Type {
		case "agent.input_echo":
			echoes++
		case "agent.raw":
			if strings.Contains(rec.Line, `"type":"user"`) {
				t.Errorf("the echo refetched as agent.raw: %s", rec.Line)
			}
		}
	}
	if echoes != 1 {
		t.Errorf("input_echo records = %d, want the one echo", echoes)
	}
}
