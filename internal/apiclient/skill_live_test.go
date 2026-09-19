package apiclient_test

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
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

// The records task 124.2 added, over a chat turn that really runs: a started
// chat runner publishing to the real broker behind the real handlers, with
// claude and cursor pointed at fakeagent. Task 071 decision 1 is the claim —
// a live chunk and the same line refetched from the turn's transcript are one
// record — and it is proven here from the client's side of the wire, where a
// drift between the two would actually be drawn.

// newChatTurnHarness returns a client for a server whose chats run turns, and
// the project they are created in.
func newChatTurnHarness(t *testing.T) (*apiclient.Client, int64) {
	t.Helper()
	fake := agenttest.BuildFakeAgent(t)
	dataDir := t.TempDir()
	st, err := store.Open(filepath.Join(dataDir, "chats.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	broker := events.New()
	t.Cleanup(broker.Close)
	st.SetEventHook(broker.Publish)
	t.Setenv("FAKEAGENT_SESSION_DIR", t.TempDir())

	git := gitx.New()
	wt := worktree.NewManager(git, dataDir)
	bin := func() string { return fake }
	reg := agent.NewRegistry(claude.New(bin), cursor.New(bin))
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := config.Default
	runner := chatrun.New(chatrun.Deps{
		Store: st, Config: cfg, Worktrees: wt, Agents: reg,
		Events: broker, DataDir: dataDir, Logger: log,
	})
	runner.Start(t.Context())
	t.Cleanup(runner.Stop)

	s := api.New(api.Deps{
		Token: testToken, Config: cfg, StartedAt: time.Now(),
		ListenAddr: "127.0.0.1:0", RequestStop: func() {}, Logger: log,
		Store: st, Broker: broker, Git: git, Worktrees: wt,
		Agents: reg, Catalog: agent.NewCatalogCache(reg), Chats: runner,
		Dirs: config.Dirs{Data: dataDir},
	})
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)

	c := apiclient.New(ts.URL, testToken)
	p, err := c.CreateProject(t.Context(), apiclient.CreateProjectRequest{Path: testrepo.Init(t, "main")})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	return c, p.ID
}

// runChatTurn runs one turn of a new chat on agentName while its stream is
// open, and returns the live chunks published up to and including the first
// for which until reports true, then the turn's refetched transcript.
func runChatTurn(
	t *testing.T, agentName string, until func(apiclient.OutputNote) bool,
) (live []apiclient.OutputNote, refetched []apiclient.TranscriptRecord) {
	t.Helper()
	c, projectID := newChatTurnHarness(t)
	chat, err := c.CreateChat(t.Context(), apiclient.CreateChatRequest{
		ProjectID: projectID, Title: "load a skill", Agent: agentName,
	})
	if err != nil {
		t.Fatalf("CreateChat: %v", err)
	}
	notes := c.StreamChat(t.Context(), chat.ID, testStreamOptions())
	if _, ok := nextNote(t, notes).(apiclient.ConnectedNote); !ok {
		t.Fatal("first note was not ConnectedNote")
	}
	turn, err := c.SendChat(t.Context(), chat.ID, "load the probe skill")
	if err != nil {
		t.Fatalf("SendChat: %v", err)
	}
	for done := false; !done; {
		if out, ok := nextNote(t, notes).(apiclient.OutputNote); ok {
			live = append(live, out)
			done = until(out)
		}
	}
	deadline := time.Now().Add(noteTimeout)
	for {
		_, turns, err := c.GetChat(t.Context(), chat.ID)
		if err != nil {
			t.Fatalf("GetChat: %v", err)
		}
		if len(turns) > 0 && turns[len(turns)-1].State != "running" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the turn never finished")
		}
		time.Sleep(10 * time.Millisecond)
	}
	refetched, _, err = c.ChatTurnTranscript(t.Context(), chat.ID, turn.Seq, apiclient.TranscriptOptions{})
	if err != nil {
		t.Fatalf("ChatTurnTranscript: %v", err)
	}
	return live, refetched
}

// asRecord decodes a live chunk the way a client decodes a transcript line.
// The chunk's own identity — chat_id, turn_id, offset — and its raw line have
// no counterpart on a record, and fall away in the decode.
func asRecord(t *testing.T, n apiclient.OutputNote) apiclient.TranscriptRecord {
	t.Helper()
	var rec apiclient.TranscriptRecord
	if err := json.Unmarshal(n.Payload, &rec); err != nil {
		t.Fatalf("decode chunk: %v", err)
	}
	rec.Type = n.Type
	return rec
}

// TestSkillChunkMatchesItsRefetchedRecord: the agent.skill a claude chat turn
// publishes live is the record the turn's transcript hands back for the same
// line.
func TestSkillChunkMatchesItsRefetchedRecord(t *testing.T) {
	t.Setenv("FAKEAGENT_SCENARIO", "skill-model")
	live, refetched := runChatTurn(t, "claude", func(n apiclient.OutputNote) bool {
		return n.Type == "agent.skill"
	})
	got := asRecord(t, live[len(live)-1])

	var want []apiclient.TranscriptRecord
	for _, rec := range refetched {
		if rec.Type == "agent.skill" {
			rec.Raw = nil
			want = append(want, rec)
		}
	}
	if len(want) != 1 {
		t.Fatalf("refetched agent.skill records = %d, want 1: %+v", len(want), refetched)
	}
	if !reflect.DeepEqual(got, want[0]) {
		t.Errorf("live chunk =\n%+v\nrefetched record =\n%+v", got, want[0])
	}
	if got.Name != "probe" || got.Args != "x" || got.By != "agent" || got.CallID != "toolu_fake_skill_1" {
		t.Errorf("agent.skill = %+v, want probe/x by the agent on its call", got)
	}
	for _, n := range live {
		if n.Type == "agent.raw" && strings.Contains(string(n.Payload), "Base directory for this skill") {
			t.Errorf("the skill's body still reached the tail as agent.raw: %s", n.Payload)
		}
	}
}

// TestInputEchoPublishesNothingLive: a cursor turn's echo of its own prompt
// no longer reaches the tail as agent.raw, and its refetched transcript holds
// it as agent.input_echo — the two agreeing, since neither draws a line.
func TestInputEchoPublishesNothingLive(t *testing.T) {
	t.Setenv("FAKEAGENT_SCENARIO_CURSOR", "success")
	// The fake's edit call comes after the echo, so by the time it arrives
	// every chunk the echo could have produced has too.
	live, refetched := runChatTurn(t, "cursor", func(n apiclient.OutputNote) bool {
		return n.Type == "agent.tool_use"
	})
	for _, n := range live {
		if n.Type == "agent.input_echo" {
			t.Errorf("an input echo published a chunk: %s", n.Payload)
		}
		if n.Type != "agent.raw" {
			continue
		}
		var body struct {
			Line string `json:"line"`
		}
		if err := json.Unmarshal(n.Payload, &body); err != nil {
			t.Fatalf("decode chunk: %v", err)
		}
		if strings.Contains(body.Line, `"type":"user"`) {
			t.Errorf("the echo reached the tail as agent.raw: %s", body.Line)
		}
	}
	var echoes int
	for _, rec := range refetched {
		if rec.Type != "agent.input_echo" {
			continue
		}
		echoes++
		if rec.Text != "" || rec.Line != "" {
			t.Errorf("agent.input_echo carries the prompt: %+v", rec)
		}
	}
	if echoes != 1 {
		t.Errorf("refetched agent.input_echo records = %d, want 1", echoes)
	}
}
