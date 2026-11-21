package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
)

// rewindCLIStub stands in for the daemon behind `chat rewind`, `chat fork`,
// `chat show`, `chat transcript` and `chat export`, recording each request.
// The API unit's live tests hold the wire to the real handlers; these pin
// what the commands send and print.
type rewindCLIStub struct {
	mu       sync.Mutex
	requests []string // "METHOD path?query"
	bodies   []map[string]any
	status   int    // for rewind/fork; 0 = 200/201
	errBody  string // the error envelope when status is set
}

func (s *rewindCLIStub) log() (reqs []string, bodies []map[string]any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.requests...), append([]map[string]any(nil), s.bodies...)
}

// rewindTurns is chat 7: turn 1 kept, turns 2 and 3 rewound under r1, and
// turn 4 the seeded turn that followed.
const rewindTurns = `[
	{"id":1,"chat_id":7,"seq":1,"prompt":"first","state":"done","result_text":"one",
	 "started_at":"2026-11-21T10:00:00Z"},
	{"id":2,"chat_id":7,"seq":2,"prompt":"second","state":"done","result_text":"two",
	 "started_at":"2026-11-21T10:01:00Z","rewound_at":"2026-11-21T11:00:00Z","rewind_id":"r1"},
	{"id":3,"chat_id":7,"seq":3,"prompt":"third","state":"done","result_text":"three",
	 "started_at":"2026-11-21T10:02:00Z","rewound_at":"2026-11-21T11:00:00Z","rewind_id":"r1"},
	{"id":4,"chat_id":7,"seq":4,"prompt":"fourth","state":"done","result_text":"four",
	 "started_at":"2026-11-21T11:05:00Z","seeded":true}]`

func (s *rewindCLIStub) handler(t *testing.T) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var body map[string]any
		if len(b) > 0 {
			if err := json.Unmarshal(b, &body); err != nil {
				t.Errorf("request body %q: %v", b, err)
			}
		}
		req := r.Method + " " + r.URL.Path
		if r.URL.RawQuery != "" {
			req += "?" + r.URL.RawQuery
		}
		s.mu.Lock()
		s.requests = append(s.requests, req)
		s.bodies = append(s.bodies, body)
		status, errBody := s.status, s.errBody
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/v1/health":
			_, _ = io.WriteString(w, `{"status":"ok"}`)
		case status != 0 && (strings.HasSuffix(r.URL.Path, "/rewind") || strings.HasSuffix(r.URL.Path, "/fork")):
			w.WriteHeader(status)
			_, _ = io.WriteString(w, errBody)
		case r.URL.Path == "/v1/chats/7/rewind":
			_, _ = io.WriteString(w, `{"chat":{"id":7,"state":"idle"},"rewind_id":"r2",
				"ref":"refs/vincent/chats/7/rewound-1","skipped":["locked.bin"],"seeded":true,
				"rewound_turns":[1,4]}`)
		case r.URL.Path == "/v1/chats/7/fork":
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, `{"chat":{"id":9,"title":"spike","agent":"codex",
				"branch":"vincent/9-spike","state":"idle","forked_from":{"chat_id":7,"seq":2}},
				"skipped":[],"seeded":true}`)
		case r.URL.Path == "/v1/chats/7" && r.Method == http.MethodGet:
			_, _ = io.WriteString(w, `{"chat":{"id":7,"title":"talk","state":"idle","agent":"codex",
				"branch":"vincent/7-talk","resume_pending":"seeded",
				"forked_from":{"chat_id":3,"seq":2},
				"rewound_states":[{"k":1,"ref":"refs/vincent/chats/7/rewound-1","sha":"abc","blocked":""}]},
				"turns":`+rewindTurns+`}`)
		case strings.HasPrefix(r.URL.Path, "/v1/chats/7/turns/"):
			_, _ = io.WriteString(w, `{"records":[],"next_offset":0}`)
		case r.URL.Path == "/v1/chats/7/export":
			w.Header().Set("Content-Type", "text/markdown")
			_, _ = io.WriteString(w, "# talk\n")
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"error":{"code":"not_found","message":"no such route"}}`)
		}
	}
}

// lastWrite is the one rewind or fork POST the stub saw.
func (s *rewindCLIStub) lastWrite(t *testing.T) (string, map[string]any) {
	t.Helper()
	reqs, bodies := s.log()
	for i := len(reqs) - 1; i >= 0; i-- {
		if strings.HasPrefix(reqs[i], "POST ") {
			return reqs[i], bodies[i]
		}
	}
	t.Fatalf("no POST reached the stub: %v", reqs)
	return "", nil
}

func TestChatRewindSendsTurnAndPrintsResult(t *testing.T) {
	s := &rewindCLIStub{}
	stdout, stderr, code := runChatSend(t, s.handler(t), "chat", "rewind", "7", "--turn", "1", "--mode", "conversation")
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	req, body := s.lastWrite(t)
	if req != "POST /v1/chats/7/rewind" || len(body) != 2 || body["turn"] != 1.0 || body["mode"] != "conversation" {
		t.Errorf("sent %s %v", req, body)
	}
	for _, want := range []string{
		"chat 7 rewound to before turn 1 (conversation)",
		"rewind id: r2",
		"refs/vincent/chats/7/rewound-1",
		"rewound turns: 1, 4",
		"  locked.bin\n",
		"next turn: seeded, not resumed",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout lacks %q:\n%s", want, stdout)
		}
	}
}

// An omitted --mode is the daemon's default to apply, so it is not sent.
func TestChatRewindDefaultModeIsNotSent(t *testing.T) {
	s := &rewindCLIStub{}
	if _, stderr, code := runChatSend(t, s.handler(t), "chat", "rewind", "7", "--turn", "2"); code != 0 {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	if _, body := s.lastWrite(t); len(body) != 1 || body["turn"] != 2.0 {
		t.Errorf("body = %v, want turn alone", body)
	}
}

func TestChatRewindForward(t *testing.T) {
	s := &rewindCLIStub{}
	stdout, stderr, code := runChatSend(t, s.handler(t), "chat", "rewind", "7", "--rewound", "1", "--json")
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	if _, body := s.lastWrite(t); len(body) != 1 || body["rewound"] != 1.0 {
		t.Errorf("body = %v, want rewound alone", body)
	}
	var got struct {
		RewindID string   `json:"rewind_id"`
		Ref      string   `json:"ref"`
		Skipped  []string `json:"skipped"`
		Seeded   bool     `json:"seeded"`
	}
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("--json output %q: %v", stdout, err)
	}
	if got.RewindID != "r2" || got.Ref != "refs/vincent/chats/7/rewound-1" || !got.Seeded ||
		len(got.Skipped) != 1 || got.Skipped[0] != "locked.bin" {
		t.Errorf("json = %+v", got)
	}
}

func TestChatRewindRefusesBadFlags(t *testing.T) {
	for name, args := range map[string][]string{
		"no target":        {"chat", "rewind", "7"},
		"both targets":     {"chat", "rewind", "7", "--turn", "1", "--rewound", "1"},
		"mode with ahead":  {"chat", "rewind", "7", "--rewound", "1", "--mode", "code"},
		"unknown mode":     {"chat", "rewind", "7", "--turn", "1", "--mode", "all"},
		"turn zero":        {"chat", "rewind", "7", "--turn", "0"},
		"rewound negative": {"chat", "rewind", "7", "--rewound", "-1"},
		"bad chat id":      {"chat", "rewind", "x", "--turn", "1"},
		"fork no turn":     {"chat", "fork", "7"},
		"fork turn zero":   {"chat", "fork", "7", "--turn", "0"},
		"fork blank title": {"chat", "fork", "7", "--turn", "1", "--title", " "},
	} {
		t.Run(name, func(t *testing.T) {
			s := &rewindCLIStub{}
			_, _, code := runChatSend(t, s.handler(t), args...)
			if code == 0 {
				t.Fatal("exit 0, want a refusal")
			}
			if reqs, _ := s.log(); len(reqs) != 0 {
				t.Errorf("refused flags still reached the daemon: %v", reqs)
			}
		})
	}
}

func TestChatRewindConflictsArePlain(t *testing.T) {
	for code, want := range map[string]string{
		"chat_turn_live":              "vincent chat cancel 7",
		"chat_checkpoint_unavailable": "no checkpoint",
		"chat_rewind_foreign_commits": "someone else has committed",
		"chat_turn_rewound":           "already rewound",
		"chat_terminal":               "has ended",
	} {
		t.Run(code, func(t *testing.T) {
			s := &rewindCLIStub{
				status:  http.StatusConflict,
				errBody: `{"error":{"code":"` + code + `","message":"raw daemon text"}}`,
			}
			for _, cmd := range [][]string{
				{"chat", "rewind", "7", "--turn", "2"},
				{"chat", "fork", "7", "--turn", "2"},
			} {
				stdout, stderr, exit := runChatSend(t, s.handler(t), cmd...)
				if exit != 1 || stdout != "" {
					t.Errorf("%v: exit %d stdout %q", cmd, exit, stdout)
				}
				if !strings.Contains(stderr, want) || strings.Contains(stderr, "raw daemon text") {
					t.Errorf("%v: stderr %q, want the plain message with %q", cmd, stderr, want)
				}
			}
		})
	}
	// Anything else keeps the daemon's own message.
	s := &rewindCLIStub{
		status:  http.StatusBadRequest,
		errBody: `{"error":{"code":"validation","message":"turn 99 does not exist"}}`,
	}
	if _, stderr, exit := runChatSend(t, s.handler(t), "chat", "rewind", "7", "--turn", "99"); exit != 1 ||
		!strings.Contains(stderr, "turn 99 does not exist") {
		t.Errorf("exit %d stderr %q", exit, stderr)
	}
}

func TestChatForkSendsTurnAndPrintsNewChat(t *testing.T) {
	s := &rewindCLIStub{}
	stdout, stderr, code := runChatSend(t, s.handler(t), "chat", "fork", "7", "--turn", "2", "--title", "spike")
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	req, body := s.lastWrite(t)
	if req != "POST /v1/chats/7/fork" || len(body) != 2 || body["turn"] != 2.0 || body["title"] != "spike" {
		t.Errorf("sent %s %v", req, body)
	}
	for _, want := range []string{"chat 9  spike  [codex]  vincent/9-spike", "forked from chat 7, before turn 2",
		"first turn: seeded, not resumed"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout lacks %q:\n%s", want, stdout)
		}
	}

	stdout, _, code = runChatSend(t, s.handler(t), "chat", "fork", "7", "--turn", "2", "--json")
	var got struct {
		Chat struct {
			ID int64 `json:"id"`
		} `json:"chat"`
	}
	if code != 0 || json.Unmarshal([]byte(stdout), &got) != nil || got.Chat.ID != 9 {
		t.Errorf("--json: exit %d, %q", code, stdout)
	}
	if _, body := s.lastWrite(t); len(body) != 1 {
		t.Errorf("body without --title = %v, want turn alone", body)
	}
}

func TestChatShowHidesRewoundTurns(t *testing.T) {
	s := &rewindCLIStub{}
	stdout, stderr, code := runChatSend(t, s.handler(t), "chat", "show", "7")
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	for _, want := range []string{
		"--- turn 1 (done) ---", "--- turn 4 (done, seeded, not resumed) ---",
		"(2 rewound turns hidden; --rewound shows them)",
		"forked from chat 3, before turn 2", "next turn: seeded, not resumed",
		"rewound state 1: refs/vincent/chats/7/rewound-1",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout lacks %q:\n%s", want, stdout)
		}
	}
	if strings.Contains(stdout, "--- turn 2") || strings.Contains(stdout, "third") {
		t.Errorf("a rewound turn was shown:\n%s", stdout)
	}

	stdout, _, _ = runChatSend(t, s.handler(t), "chat", "show", "7", "--rewound")
	for _, want := range []string{"--- turn 2 (done, rewound r1) ---", "--- turn 3 (done, rewound r1) ---"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("--rewound stdout lacks %q:\n%s", want, stdout)
		}
	}
	if strings.Contains(stdout, "hidden") {
		t.Errorf("--rewound still says turns are hidden:\n%s", stdout)
	}

	for args, want := range map[string]int{"": 2, "--rewound": 4} {
		cmd := []string{"chat", "show", "7", "--json"}
		if args != "" {
			cmd = append(cmd, args)
		}
		stdout, _, _ = runChatSend(t, s.handler(t), cmd...)
		var got struct {
			Turns []json.RawMessage `json:"turns"`
		}
		if err := json.Unmarshal([]byte(stdout), &got); err != nil || len(got.Turns) != want {
			t.Errorf("%v: %d turns (%v), want %d", cmd, len(got.Turns), err, want)
		}
	}
}

func TestChatTranscriptSkipsRewoundTurns(t *testing.T) {
	transcriptOf := func(s *rewindCLIStub) string {
		reqs, _ := s.log()
		for _, r := range reqs {
			if strings.Contains(r, "/turns/") {
				return r
			}
		}
		return ""
	}

	// The default selection is the newest turn no rewind left behind.
	s := &rewindCLIStub{}
	_, stderr, code := runChatSend(t, s.handler(t), "chat", "transcript", "7")
	if code != 0 || !strings.Contains(transcriptOf(s), "/turns/4/") {
		t.Fatalf("exit %d, read %q, stderr %q", code, transcriptOf(s), stderr)
	}
	if !strings.Contains(stderr, "turn 4 (done, seeded, not resumed)") {
		t.Errorf("seeded turn not marked: %q", stderr)
	}

	s = &rewindCLIStub{}
	_, stderr, code = runChatSend(t, s.handler(t), "chat", "transcript", "7", "--turn", "2")
	if code != 1 || !strings.Contains(stderr, "--rewound") || transcriptOf(s) != "" {
		t.Errorf("rewound --turn: exit %d stderr %q read %q", code, stderr, transcriptOf(s))
	}

	s = &rewindCLIStub{}
	_, stderr, code = runChatSend(t, s.handler(t), "chat", "transcript", "7", "--turn", "2", "--rewound")
	if code != 0 || !strings.Contains(transcriptOf(s), "/turns/2/") ||
		!strings.Contains(stderr, "turn 2 (done, rewound r1)") {
		t.Errorf("--rewound: exit %d stderr %q read %q", code, stderr, transcriptOf(s))
	}
}

func TestChatExportRewoundFlag(t *testing.T) {
	for args, want := range map[string]bool{"": false, "--rewound": true} {
		s := &rewindCLIStub{}
		cmd := []string{"chat", "export", "7", "--format", "md"}
		if args != "" {
			cmd = append(cmd, args)
		}
		if _, stderr, code := runChatSend(t, s.handler(t), cmd...); code != 0 {
			t.Fatalf("%v: exit %d stderr %q", cmd, code, stderr)
		}
		reqs, _ := s.log()
		var export string
		for _, r := range reqs {
			if strings.HasPrefix(r, "GET /v1/chats/7/export") {
				export = r
			}
		}
		if export == "" || strings.Contains(export, "include_rewound=true") != want {
			t.Errorf("%v: requests %v, want include_rewound=true %v", cmd, reqs, want)
		}
	}
}
