package trigger

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/store"
)

func openStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "vincent.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// fakeAPI stands in for the inner mux: it records every replay and answers
// with the status the test set. A 201 creates a real task row, because the
// ledger's task_id references it.
type fakeAPI struct {
	mu     sync.Mutex
	st     *store.Store
	status int
	body   string
	reqs   []recorded
}

func newFakeAPI(t *testing.T, st *store.Store, status int, body string) *fakeAPI {
	t.Helper()
	p := &store.Project{Name: "p", Path: "/p", DefaultBranch: "main"}
	if err := st.CreateProject(t.Context(), p); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	return &fakeAPI{st: st, status: status, body: body}
}

type recorded struct {
	key  string
	body CreateBody
}

func (f *fakeAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var body CreateBody
	b, _ := io.ReadAll(r.Body)
	_ = json.Unmarshal(b, &body)
	f.reqs = append(f.reqs, recorded{key: r.Header.Get("Idempotency-Key"), body: body})
	status := f.status
	if status == 0 {
		status = http.StatusCreated
	}
	if status == http.StatusCreated {
		task := &store.Task{
			ProjectID: 1, Title: body.Title, WorkflowName: "adhoc", WorkflowSnapshot: "steps: []",
			BaseBranch: "main", BranchName: fmt.Sprintf("vincent/t%d", len(f.reqs)), State: store.TaskPaused,
		}
		if err := f.st.CreateTask(r.Context(), task, nil); err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]int64{"id": task.ID})
		return
	}
	w.WriteHeader(status)
	_, _ = io.WriteString(w, f.body)
}

func fixtureEvents(t *testing.T, name string) []Event {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return parseOutput(b, slog.New(slog.DiscardHandler)).Events
}

func jiraDef(t *testing.T, extra string) *Definition {
	t.Helper()
	src := `id: jira
enabled: true
source:
  type: command
  project: 7
  poll_interval: 1m
  command: [poll]
match:
  fields.status.name: Ready for Dev
action:
  type: create_task
  workflow: feature-pr
  title: '{{ .Event.key }} {{ .Event.fields.summary }}'
  description: '{{ .Event.fields.description }}'
  fields:
    ticket: '{{ .Event.key }}'
` + extra
	d, errs := Parse([]byte(src), "jira")
	if len(errs) > 0 {
		t.Fatalf("Parse: %v", errs)
	}
	return d
}

// TestParseOutputFixture: a captured Jira page parses into its events, the
// line without an id is refused, and the last line is the watermark.
func TestParseOutputFixture(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("testdata", "jira-search-v3.ndjson"))
	if err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	res := parseOutput(b, slog.New(slog.NewTextHandler(&logs, nil)))
	if len(res.Events) != 2 || res.Refused != 1 || res.Cursor == nil || *res.Cursor != "2026-09-10 09:30" {
		t.Fatalf("parse = %d events, %d refused, cursor %v", len(res.Events), res.Refused, res.Cursor)
	}
	if !strings.Contains(logs.String(), "no string id") {
		t.Errorf("refusal not logged: %s", logs.String())
	}
	// A cursor line that is not last is not a watermark.
	res = parseOutput([]byte(`{"cursor":"a"}`+"\n"+`{"id":"e1"}`), slog.New(slog.DiscardHandler))
	if res.Cursor != nil || res.Refused != 1 || len(res.Events) != 1 {
		t.Errorf("mid-stream cursor = %+v", res)
	}
}

// TestFireOutcomes drives every ledger outcome through fire against the
// captured payloads.
func TestFireOutcomes(t *testing.T) {
	ctx := t.Context()
	now := time.Now()
	events := fixtureEvents(t, "jira-search-v3.ndjson")
	ready, inProgress := events[0], events[1]

	t.Run("fired propose restricted", func(t *testing.T) {
		st := openStore(t)
		api := newFakeAPI(t, st, 0, "")
		del, err := fire(ctx, st, api, jiraDef(t, "limits:\n  max_task_cost_usd: 2.5\n"), ready, now)
		if err != nil || del.Outcome != store.DeliveryFired || del.TaskID == nil || *del.TaskID != 1 {
			t.Fatalf("fire = %+v, %v", del, err)
		}
		got := api.reqs[0]
		want := CreateBody{
			ProjectID: 7, Workflow: "feature-pr", Title: "VIN-12 Add retries to the poller",
			Description: "The poller gives up on the first 502.", Fields: map[string]string{"ticket": "VIN-12"},
			Paused: true, Restricted: true,
		}
		if got.body.MaxTaskCostUSD == nil || *got.body.MaxTaskCostUSD != 2.5 {
			t.Errorf("max_task_cost_usd = %v, want 2.5", got.body.MaxTaskCostUSD)
		}
		got.body.MaxTaskCostUSD = nil
		gb, _ := json.Marshal(got.body)
		wb, _ := json.Marshal(want)
		if !bytes.Equal(gb, wb) {
			t.Errorf("body = %s\nwant   %s", gb, wb)
		}
		if got.key != IdempotencyKey("jira", ready.ID()) {
			t.Errorf("Idempotency-Key = %q", got.key)
		}
		// The same event again is deduped and replays nothing.
		del, err = fire(ctx, st, api, jiraDef(t, ""), ready, now)
		if err != nil || del.Outcome != store.DeliveryDeduped || len(api.reqs) != 1 {
			t.Errorf("second fire = %+v, %v (%d replays)", del, err, len(api.reqs))
		}
	})

	t.Run("create and workflow permission", func(t *testing.T) {
		st := openStore(t)
		api := newFakeAPI(t, st, 0, "")
		d := jiraDef(t, "on_fire: create\npermission: workflow\n")
		if _, err := fire(ctx, st, api, d, ready, now); err != nil {
			t.Fatal(err)
		}
		if b := api.reqs[0].body; b.Paused || b.Restricted || b.MaxTaskCostUSD != nil {
			t.Errorf("body = %+v, want neither paused nor restricted nor a cost cap", b)
		}
	})

	for _, tc := range []struct {
		name   string
		extra  string
		event  Event
		status int
		body   string
		want   string
	}{
		{"match filters", "", inProgress, 0, "", store.DeliveryFiltered},
		{"if filters", "if: '{{ eq .Event.key \"VIN-99\" }}'\n", ready, 0, "", store.DeliveryFiltered},
		{"if renders garbage", "if: '{{ .Event.key }}'\n", ready, 0, "", store.DeliveryError},
		{"template reads a missing key", "dedupe_key: '{{ .Event.nope }}'\n", ready, 0, "", store.DeliveryError},
		{"refused keeps the envelope", "", ready, http.StatusBadRequest, `{"error":{"code":"validation_failed"}}`, store.DeliveryRefused},
		{"server error", "", ready, http.StatusInternalServerError, `{"error":{"code":"internal"}}`, store.DeliveryError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := openStore(t)
			api := newFakeAPI(t, st, tc.status, tc.body)
			del, err := fire(ctx, st, api, jiraDef(t, tc.extra), tc.event, now)
			if err != nil || del.Outcome != tc.want {
				t.Fatalf("fire = %+v, %v; want %s", del, err, tc.want)
			}
			rows, _ := st.ListTriggerDeliveries(ctx, "jira", 10)
			if len(rows) != 1 || rows[0].Outcome != tc.want {
				t.Fatalf("ledger = %+v", rows)
			}
			if tc.want == store.DeliveryRefused && rows[0].Detail != tc.body {
				t.Errorf("refused detail = %q, want the envelope", rows[0].Detail)
			}
		})
	}

	t.Run("rate limited", func(t *testing.T) {
		st := openStore(t)
		api := newFakeAPI(t, st, 0, "")
		d := jiraDef(t, "dedupe_key: '{{ .Event.id }}-{{ .Event.n }}'\nlimits:\n  max_per_hour: 2\n")
		var outcomes []string
		for i := range 3 {
			ev := Event{}
			for k, v := range ready {
				ev[k] = v
			}
			ev["n"] = float64(i)
			del, err := fire(ctx, st, api, d, ev, now)
			if err != nil {
				t.Fatal(err)
			}
			outcomes = append(outcomes, del.Outcome)
		}
		if strings.Join(outcomes, ",") != "fired,fired,rate_limited" || len(api.reqs) != 2 {
			t.Errorf("outcomes %v, %d replays", outcomes, len(api.reqs))
		}
	})
}

// TestRunCommand drives the real process path with the helper child.
func TestRunCommand(t *testing.T) {
	ctx := t.Context()
	var logs bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logs, nil))

	res, err := runCommand(ctx, helperArgv(t, "print", `{"id":"e1","n":1}`, `{"n":2}`, `{"cursor":"c2"}`),
		"c1", nil, 30*time.Second, log)
	if err != nil {
		t.Fatalf("runCommand: %v", err)
	}
	if len(res.Events) != 1 || res.Refused != 1 || res.Cursor == nil || *res.Cursor != "c2" {
		t.Errorf("result = %+v", res)
	}
	if !strings.Contains(logs.String(), "cursor=c1") {
		t.Errorf("the child did not see %s, or its stderr was not logged: %s", CursorEnv, logs.String())
	}

	_, err = runCommand(ctx, helperArgv(t, "fail"), "", nil, 30*time.Second, log)
	var pe *PollError
	if !errors.As(err, &pe) || !strings.Contains(pe.Error(), "exit status 3") || !strings.Contains(pe.Stderr, "deliberate") {
		t.Errorf("failing command = %v, want exit status 3 with its stderr", err)
	}

	start := time.Now()
	_, err = runCommand(ctx, helperArgv(t, "hang"), "", nil, 500*time.Millisecond, log)
	if !errors.As(err, &pe) || !strings.Contains(pe.Reason, "killed after") {
		t.Errorf("hung command = %v, want a timeout", err)
	}
	// Well under WaitDelay: the grandchild holding stdout died with the tree.
	if d := time.Since(start); d > 4*time.Second {
		t.Errorf("timeout took %s: the process tree was not killed", d)
	}
}
