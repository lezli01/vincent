package notify

import (
	"context"
	"encoding/json"
	"path/filepath"
	"slices"
	"strconv"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/lezli01/vincent/internal/chatstate"
	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/taskstate"
)

// bodyRecorder keeps every envelope the spawn hook was handed, decoded as a
// raw map so a case can assert a key is present as null rather than absent.
type bodyRecorder struct {
	mu     sync.Mutex
	jobs   []job
	bodies []map[string]any
}

func recordBodies(t *testing.T, h *harness) *bodyRecorder {
	t.Helper()
	r := &bodyRecorder{}
	h.notifier.spawn = func(_ context.Context, j job, body []byte) error {
		var m map[string]any
		if err := json.Unmarshal(body, &m); err != nil {
			t.Errorf("envelope is not JSON: %v (%q)", err, body)
		}
		r.mu.Lock()
		defer r.mu.Unlock()
		r.jobs = append(r.jobs, j)
		r.bodies = append(r.bodies, m)
		return nil
	}
	return r
}

// spawned is what reached the hook, as "kind:id:to" triples.
func (r *bodyRecorder) spawned() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, 0, len(r.jobs))
	for _, j := range r.jobs {
		id := j.taskID
		if j.kind == KindChat {
			id = j.chatID
		}
		out = append(out, j.kind+":"+strconv.FormatInt(id, 10)+":"+j.to)
	}
	return out
}

func (r *bodyRecorder) body(t *testing.T, i int) map[string]any {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	if i >= len(r.bodies) {
		t.Fatalf("envelope %d not spawned; have %d", i, len(r.bodies))
	}
	return r.bodies[i]
}

// newChatHarness is newHarness with a chat in the store and `chat_on` set.
func newChatHarness(t *testing.T, on []taskstate.State, chatOn []chatstate.NotifyState) *harness {
	t.Helper()
	h := newHarness(t, on, []string{"notifier"})
	h.store.chats = map[int64]*store.Chat{
		3: {
			ID: 3, ProjectID: 7, Title: "Why is the gate red?", State: chatstate.AwaitingInput,
			Branch: "vincent/chat-3", WorktreePath: filepath.Join("/tmp", "wt", "chat-3"),
			PendingInput: json.RawMessage(
				`{"Kind":"question","Questions":[{"Text":"Rerun the gate?"},{"Text":"And m2?"}]}`),
		},
	}
	h.setConfig(func(c *config.Config) { c.Notify.ChatOn = chatOn })
	return h
}

// chatStateEvent builds a chat.state_changed the way store.chatEvent does.
// Its chat is always newChatHarness's chat 3.
func chatStateEvent(id int64, st chatstate.State) *store.Event {
	b, _ := json.Marshal(map[string]any{"id": 3, "title": "Why is the gate red?", "state": string(st)})
	pid := int64(7)
	return &store.Event{
		ID: id, TS: time.Date(2026, 11, 19, 8, 0, 0, 0, time.UTC),
		Type: store.EventChatState, ProjectID: &pid, Payload: b,
	}
}

// chatTurnEvent builds a chat.turn_changed the way store.chatEvent does: the
// chat's own state beside the turn's.
func chatTurnEvent(id, chatID int64, st chatstate.State, turn chatstate.TurnState) *store.Event {
	b, _ := json.Marshal(map[string]any{
		"id": chatID, "title": "Why is the gate red?", "state": string(st),
		"turn_id": 11, "turn_seq": 2, "turn_state": string(turn),
	})
	pid := int64(7)
	return &store.Event{
		ID: id, TS: time.Date(2026, 11, 19, 8, 0, 0, 0, time.UTC),
		Type: store.EventChatTurn, ProjectID: &pid, Payload: b,
	}
}

// TestChatFiresOnlyWhenListed: a chat moment reaches the command only when
// `notify.chat_on` lists it (task 165). `notify.on` is not widened — a task
// state named awaiting_input there does not make a chat fire — and a turn
// that is still running, or was interrupted by a restart, is no moment at all.
func TestChatFiresOnlyWhenListed(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newChatHarness(t, []taskstate.State{taskstate.AwaitingInput},
			[]chatstate.NotifyState{chatstate.NotifyTurnDone})
		rec := recordBodies(t, h)

		h.notifier.OnEvent(chatStateEvent(1, chatstate.AwaitingInput))
		h.notifier.OnEvent(chatTurnEvent(2, 3, chatstate.Running, chatstate.TurnFailed))
		h.notifier.OnEvent(chatTurnEvent(3, 3, chatstate.Running, chatstate.TurnRunning))
		h.notifier.OnEvent(chatTurnEvent(4, 3, chatstate.Running, chatstate.TurnInterruptedState))
		h.notifier.OnEvent(chatStateEvent(5, chatstate.Idle))
		synctest.Wait()
		if got := rec.spawned(); len(got) != 0 {
			t.Fatalf("unlisted chat moments fired: %q", got)
		}

		h.notifier.OnEvent(chatTurnEvent(6, 3, chatstate.Running, chatstate.TurnDone))
		synctest.Wait()
		if got := rec.spawned(); !slices.Equal(got, []string{"chat:3:turn_done"}) {
			t.Fatalf("spawned %q, want exactly the listed turn_done", got)
		}

		// Hot reload: the next event reads the edited list.
		h.setConfig(func(c *config.Config) {
			c.Notify.ChatOn = []chatstate.NotifyState{chatstate.NotifyAwaitingInput, chatstate.NotifyTurnFailed}
		})
		h.notifier.OnEvent(chatStateEvent(7, chatstate.AwaitingInput))
		synctest.Wait() // four workers: order is only fixed one at a time
		h.notifier.OnEvent(chatTurnEvent(8, 3, chatstate.AwaitingInput, chatstate.TurnFailed))
		synctest.Wait()
		want := []string{"chat:3:turn_done", "chat:3:awaiting_input", "chat:3:turn_failed"}
		if got := rec.spawned(); !slices.Equal(got, want) {
			t.Fatalf("spawned %q after the reload, want %q", got, want)
		}

		// No command: the list alone fires nothing.
		h.setConfig(func(c *config.Config) { c.Notify.Command = nil })
		h.notifier.OnEvent(chatTurnEvent(9, 3, chatstate.Running, chatstate.TurnFailed))
		synctest.Wait()
		if got := rec.spawned(); len(got) != len(want) {
			t.Fatalf("chat_on with no command fired: %q", got)
		}
	})
}

// TestEnvelopeKind: every task envelope says "task" and every chat envelope
// says "chat" (task 165), and a chat's carries its own fields with the
// task-only ones zero or null.
func TestEnvelopeKind(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newChatHarness(t, []taskstate.State{taskstate.Blocked},
			[]chatstate.NotifyState{chatstate.NotifyAwaitingInput, chatstate.NotifyTurnFailed})
		rec := recordBodies(t, h)

		h.notifier.OnEvent(stateEvent(1, 1, taskstate.Running, taskstate.Blocked, nil))
		synctest.Wait()
		h.notifier.OnEvent(chatStateEvent(2, chatstate.AwaitingInput))
		synctest.Wait()
		h.notifier.OnEvent(chatTurnEvent(3, 3, chatstate.AwaitingInput, chatstate.TurnFailed))
		synctest.Wait()

		task := rec.body(t, 0)
		if task["kind"] != "task" || task["task_id"] != float64(1) || task["chat_id"] != float64(0) {
			t.Errorf("task envelope kind/ids = %v/%v/%v, want task/1/0",
				task["kind"], task["task_id"], task["chat_id"])
		}

		chat := rec.body(t, 1)
		want := map[string]any{
			"event_id": float64(2), "ts": "2026-11-19T08:00:00Z", "type": "chat.state_changed",
			"kind": "chat", "chat_id": float64(3), "task_id": float64(0),
			"title": "Why is the gate red?", "from": "running", "to": "awaiting_input",
			"worktree_path": filepath.Join("/tmp", "wt", "chat-3"), "branch": "vincent/chat-3",
			"project_id": float64(7), "project": "vincent",
			"block_reason": "", "queued_reason": "", "admit_not_before": nil,
			"current_step": float64(0), "steps_total": float64(0), "workflow": "",
		}
		for k, v := range want {
			got, ok := chat[k]
			if !ok {
				t.Errorf("chat envelope has no %q", k)
				continue
			}
			if got != v {
				t.Errorf("chat envelope %s = %#v, want %#v", k, got, v)
			}
		}
		input, _ := chat["input"].(map[string]any)
		if input["kind"] != "question" || input["summary"] != "Rerun the gate? (+1 more)" {
			t.Errorf("chat input = %v, want the pending question lifted the §7.4 way", chat["input"])
		}

		failed := rec.body(t, 2)
		if failed["type"] != "chat.turn_changed" || failed["from"] != "awaiting_input" ||
			failed["to"] != "turn_failed" {
			t.Errorf("turn envelope type/from/to = %v/%v/%v, want chat.turn_changed/awaiting_input/turn_failed",
				failed["type"], failed["from"], failed["to"])
		}
		if _, ok := failed["input"]; ok {
			t.Errorf("a turn ending carries an input object: %v", failed["input"])
		}
	})
}

// TestReplayedEventIsSilent: an event id already handed to the hook is not a
// second notification, for a task or a chat — the no-replay rule.
func TestReplayedEventIsSilent(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newChatHarness(t, []taskstate.State{taskstate.Blocked},
			[]chatstate.NotifyState{chatstate.NotifyTurnDone})
		rec := recordBodies(t, h)

		task := stateEvent(10, 1, taskstate.Running, taskstate.Blocked, nil)
		chat := chatTurnEvent(11, 3, chatstate.Running, chatstate.TurnDone)
		h.notifier.OnEvent(task)
		h.notifier.OnEvent(chat)
		synctest.Wait()
		h.notifier.OnEvent(task)
		h.notifier.OnEvent(chat)
		synctest.Wait()
		want := []string{"chat:3:turn_done", "task:1:blocked"}
		got := rec.spawned()
		slices.Sort(got) // four workers race; only the count matters here
		if !slices.Equal(got, want) {
			t.Fatalf("spawned %q, want each event once: %q", got, want)
		}
	})
}

// TestUnreadableChatIsSkipped: a chat gone by the time the worker reads it
// loses its notification and nothing else.
func TestUnreadableChatIsSkipped(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newChatHarness(t, nil, []chatstate.NotifyState{chatstate.NotifyTurnDone})
		rec := recordBodies(t, h)

		h.notifier.OnEvent(chatTurnEvent(1, 99, chatstate.Running, chatstate.TurnDone))
		synctest.Wait()
		if got := rec.spawned(); len(got) != 0 {
			t.Fatalf("spawned for a chat that could not be read: %q", got)
		}
		if !h.logs.contains("chat could not be read") {
			t.Error("the skip was not logged")
		}
	})
}

// TestPendingInputSummary holds the chat lift to the task path's summary rule.
func TestPendingInputSummary(t *testing.T) {
	cases := []struct {
		raw  string
		want *Input
	}{
		{``, nil},
		{`not json`, nil},
		{
			`{"Kind":"permission","Permission":{"Tool":"Bash","Summary":"rm -rf bin"}}`,
			&Input{Kind: "permission", Summary: "permission: Bash — rm -rf bin"},
		},
		{
			`{"Kind":"question","Questions":[{"Text":"Which branch?"}]}`,
			&Input{Kind: "question", Summary: "Which branch?"},
		},
		{`{"Kind":"question"}`, &Input{Kind: "question", Summary: "question"}},
	}
	for _, tc := range cases {
		got := pendingInput(json.RawMessage(tc.raw))
		if (got == nil) != (tc.want == nil) || (got != nil && *got != *tc.want) {
			t.Errorf("pendingInput(%s) = %+v, want %+v", tc.raw, got, tc.want)
		}
	}
}
