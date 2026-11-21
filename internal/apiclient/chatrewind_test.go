package apiclient_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"testing"

	"github.com/lezli01/vincent/internal/apiclient"
)

// rewindStub answers one route with a canned status and body and records
// what reached it. The API unit's live tests hold these types to the real
// handlers; this one pins the client's half of the wire contract alone.
type rewindStub struct {
	mu     sync.Mutex
	method string
	path   string
	query  string
	body   []byte
}

func (s *rewindStub) serve(t *testing.T, status int, resp string) *apiclient.Client {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		s.method, s.path, s.query, s.body = r.Method, r.URL.Path, r.URL.RawQuery, b
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, resp)
	}))
	t.Cleanup(ts.Close)
	return apiclient.New(ts.URL, "tok")
}

func (s *rewindStub) sentJSON(t *testing.T) map[string]any {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	var m map[string]any
	if err := json.Unmarshal(s.body, &m); err != nil {
		t.Fatalf("request body %q: %v", s.body, err)
	}
	return m
}

func TestRewindChatSendsBodyAndDecodesResult(t *testing.T) {
	var s rewindStub
	c := s.serve(t, http.StatusOK, `{"chat":{"id":7,"state":"idle","resume_pending":"seeded",
		"rewound_states":[{"k":1,"ref":"refs/vincent/0123456789abcdef/chats/7/rewound-1","sha":"abc","blocked":""}]},
		"rewind_id":"r1","ref":"refs/vincent/0123456789abcdef/chats/7/rewound-1","skipped":["locked.bin"],
		"seeded":true,"rewound_turns":[2,3]}`)
	got, err := c.RewindChat(context.Background(), 7, apiclient.RewindRequest{Turn: 2, Mode: "both"})
	if err != nil {
		t.Fatal(err)
	}
	if s.method != http.MethodPost || s.path != "/v1/chats/7/rewind" {
		t.Errorf("request = %s %s", s.method, s.path)
	}
	if body := s.sentJSON(t); !reflect.DeepEqual(body, map[string]any{"turn": 2.0, "mode": "both"}) {
		t.Errorf("body = %v", body)
	}
	want := apiclient.RewindResult{
		RewindID: "r1", Ref: "refs/vincent/0123456789abcdef/chats/7/rewound-1", Skipped: []string{"locked.bin"},
		Seeded: true, RewoundTurns: []int{2, 3},
	}
	if got.RewindID != want.RewindID || got.Ref != want.Ref || !got.Seeded ||
		!reflect.DeepEqual(got.Skipped, want.Skipped) || !reflect.DeepEqual(got.RewoundTurns, want.RewoundTurns) {
		t.Errorf("result = %+v", got)
	}
	if got.Chat.ID != 7 || got.Chat.ResumePending != "seeded" {
		t.Errorf("chat = %+v", got.Chat)
	}
	wantStates := []apiclient.ChatRewoundState{{K: 1, Ref: "refs/vincent/0123456789abcdef/chats/7/rewound-1", SHA: "abc"}}
	if !reflect.DeepEqual(got.Chat.RewoundStates, wantStates) {
		t.Errorf("rewound_states = %+v", got.Chat.RewoundStates)
	}
}

// A forward rewind sends `rewound` alone: turn and mode are omitempty, so
// the daemon never sees a turn 0 or a mode it must reject.
func TestRewindChatForwardOmitsTurnAndMode(t *testing.T) {
	var s rewindStub
	c := s.serve(t, http.StatusOK, `{"chat":{"id":7},"rewind_id":"r2","ref":"refs/vincent/0123456789abcdef/chats/7/rewound-2"}`)
	if _, err := c.RewindChat(context.Background(), 7, apiclient.RewindRequest{Rewound: 1}); err != nil {
		t.Fatal(err)
	}
	if body := s.sentJSON(t); !reflect.DeepEqual(body, map[string]any{"rewound": 1.0}) {
		t.Errorf("body = %v", body)
	}
}

func TestRewindChatConflictKeepsCode(t *testing.T) {
	var s rewindStub
	c := s.serve(t, http.StatusConflict,
		`{"error":{"code":"chat_rewind_foreign_commits","message":"someone else committed"}}`)
	_, err := c.RewindChat(context.Background(), 7, apiclient.RewindRequest{Turn: 1, Mode: "code"})
	var apiErr *apiclient.Error
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusConflict || apiErr.Code != "chat_rewind_foreign_commits" {
		t.Fatalf("err = %v", err)
	}
}

func TestForkChatSendsBodyAndDecodesResult(t *testing.T) {
	var s rewindStub
	c := s.serve(t, http.StatusCreated, `{"chat":{"id":9,"state":"idle",
		"forked_from":{"chat_id":7,"seq":2}},"skipped":[],"seeded":false}`)
	got, err := c.ForkChat(context.Background(), 7, apiclient.ForkRequest{Turn: 2})
	if err != nil {
		t.Fatal(err)
	}
	if s.method != http.MethodPost || s.path != "/v1/chats/7/fork" {
		t.Errorf("request = %s %s", s.method, s.path)
	}
	// turn is always sent; an empty title is not.
	if body := s.sentJSON(t); !reflect.DeepEqual(body, map[string]any{"turn": 2.0}) {
		t.Errorf("body = %v", body)
	}
	if got.Chat.ID != 9 || got.Seeded || got.Chat.ForkedFrom == nil ||
		*got.Chat.ForkedFrom != (apiclient.ChatForkedFrom{ChatID: 7, Seq: 2}) {
		t.Errorf("result = %+v (forked_from %+v)", got, got.Chat.ForkedFrom)
	}

	if _, err := c.ForkChat(context.Background(), 7, apiclient.ForkRequest{Turn: 1, Title: "spike"}); err != nil {
		t.Fatal(err)
	}
	if body := s.sentJSON(t); !reflect.DeepEqual(body, map[string]any{"turn": 1.0, "title": "spike"}) {
		t.Errorf("body = %v", body)
	}
}

func TestChatTurnDecodesRewindFields(t *testing.T) {
	var s rewindStub
	c := s.serve(t, http.StatusOK, `{"chat":{"id":7},"turns":[
		{"id":1,"chat_id":7,"seq":1,"state":"done","started_at":"2026-11-21T10:00:00Z",
		 "checkpoint_sha":"c1","head_before":"h0","head_after":"h1",
		 "rewound_at":"2026-11-21T11:00:00Z","rewind_id":"r1","seeded":true,
		 "resume_anchor":"uuid-1",
		 "rewind":{"code":"chat_rewind_foreign_commits","conversation":"","fork":"","conversation_seeded":true}},
		{"id":2,"chat_id":7,"seq":2,"state":"done","started_at":"2026-11-21T10:05:00Z"}]}`)
	_, turns, err := c.GetChat(context.Background(), 7)
	if err != nil {
		t.Fatal(err)
	}
	if len(turns) != 2 {
		t.Fatalf("turns = %d", len(turns))
	}
	t1 := turns[0]
	if t1.CheckpointSHA != "c1" || t1.HeadBefore != "h0" || t1.HeadAfter != "h1" ||
		t1.RewoundAt == nil || t1.RewindID != "r1" || !t1.Seeded || t1.ResumeAnchor != "uuid-1" {
		t.Errorf("turn 1 = %+v", t1)
	}
	wantRewind := apiclient.ChatTurnRewind{Code: "chat_rewind_foreign_commits", ConversationSeeded: true}
	if t1.Rewind == nil || *t1.Rewind != wantRewind {
		t.Errorf("turn 1 rewind = %+v", t1.Rewind)
	}
	if t2 := turns[1]; t2.RewoundAt != nil || t2.Rewind != nil || t2.Seeded {
		t.Errorf("turn 2 = %+v", t2)
	}
}

func TestChatExportIncludeRewoundQuery(t *testing.T) {
	var s rewindStub
	c := s.serve(t, http.StatusOK, `{}`)
	if _, err := c.ChatExport(context.Background(), 7, apiclient.ChatExportOptions{Format: "json"}); err != nil {
		t.Fatal(err)
	}
	if s.query != "format=json" {
		t.Errorf("default query = %q, want no include_rewound", s.query)
	}
	if _, err := c.ChatExport(context.Background(), 7, apiclient.ChatExportOptions{IncludeRewound: true}); err != nil {
		t.Fatal(err)
	}
	if s.query != "include_rewound=true" {
		t.Errorf("query = %q", s.query)
	}
}
