package jira

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/jira/fakejira"
)

// The write half (task 142.13) against the fake's httptest leg, with every
// POST body captured on its way in.

type bodyLog struct {
	mu     sync.Mutex
	bodies map[string][]string // by path
}

func (b *bodyLog) get(path string) []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.bodies[path]...)
}

// newWriteFake is newFake with POST bodies recorded.
func newWriteFake(t *testing.T) (*fakeLeg, *bodyLog) {
	t.Helper()
	f := newFake(t)
	log := &bodyLog{bodies: map[string][]string{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			raw, _ := io.ReadAll(r.Body)
			log.mu.Lock()
			log.bodies[r.URL.Path] = append(log.bodies[r.URL.Path], string(raw))
			log.mu.Unlock()
			r.Body = io.NopCloser(bytes.NewReader(raw))
		}
		f.store.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	f.client = New(srv.URL+"/", srv.Client(), staticCreds(testUser, testToken))
	return f, log
}

func TestTransitionsDecode(t *testing.T) {
	f, _ := newWriteFake(t)
	ts, err := f.client.Transitions(context.Background(), "10001")
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]int{}
	for i, tr := range ts {
		byID[tr.ID] = i
	}
	done, ok := byID["31"]
	if !ok || len(ts) != 2 {
		t.Fatalf("transitions = %+v, want Start progress and Done", ts)
	}
	d := ts[done]
	if d.Name != "Done" || d.To.Name != "Done" || d.To.StatusCategory.Key != "done" || !d.HasScreen {
		t.Errorf("Done transition = %+v", d)
	}
	res, ok := d.Fields["resolution"]
	if !ok || !res.Required || len(res.AllowedValues) != 2 || res.AllowedValues[1].Name != "Won't Do" {
		t.Errorf("Done's resolution field = %+v", d.Fields)
	}
}

func TestDoTransitionBodies(t *testing.T) {
	f, log := newWriteFake(t)
	ctx := context.Background()
	// Company-managed: the screen has a resolution.
	if err := f.client.DoTransition(ctx, "10001", "31", "Won't Do"); err != nil {
		t.Fatal(err)
	}
	c, err := f.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, is := range c.Issues {
		if is.ID == "10001" && (is.Status != "10002" || is.Resolution == nil || is.Resolution.Name != "Won't Do") {
			t.Errorf("after the transition: status %s resolution %+v", is.Status, is.Resolution)
		}
	}
	bodies := log.get("/rest/api/3/issue/10001/transitions")
	if len(bodies) != 1 || bodies[0] != `{"transition":{"id":"31"},"fields":{"resolution":{"name":"Won't Do"}}}` {
		t.Errorf("transition body = %v", bodies)
	}
	// Reopen, and any transition with no resolution: no fields key at all.
	if err := f.client.DoTransition(ctx, "10001", "41", ""); err != nil {
		t.Fatal(err)
	}
	bodies = log.get("/rest/api/3/issue/10001/transitions")
	if len(bodies) != 2 || bodies[1] != `{"transition":{"id":"41"}}` {
		t.Errorf("reopen body = %v", bodies)
	}
	if n := f.count("POST", "rest/api/3/issue/10001/transitions"); n != 2 {
		t.Errorf("POSTs = %d, want 2", n)
	}
}

func TestWriteErrors(t *testing.T) {
	f, _ := newWriteFake(t)
	now := time.Date(2026, 11, 5, 8, 0, 0, 0, time.UTC)
	f.client.now = func() time.Time { return now }
	ctx := context.Background()

	// A 400: the Done screen's required resolution left out.
	err := f.client.DoTransition(ctx, "10001", "31", "")
	if record(err) == nil || ReasonOf(err) != ReasonBadRequest {
		t.Errorf("missing resolution: %v", err)
	}

	f.scenario(fakejira.ScenarioReadOnly)
	err = f.client.DoTransition(ctx, "10001", "31", "Done")
	if record(err) == nil || ReasonOf(err) != ReasonNoWriteScope {
		t.Errorf("read-only transition: %v", err)
	}
	_, err = f.client.AddComment(ctx, "10001", json.RawMessage(`{"type":"doc","version":1,"content":[]}`))
	if record(err) == nil || ReasonOf(err) != ReasonNoWriteScope {
		t.Errorf("read-only comment: %v", err)
	}
	// A read keeps its own classification under the same scenario.
	if _, err := f.client.Transitions(ctx, "10001"); err != nil {
		t.Errorf("read-only blocks a read: %v", err)
	}

	f.scenario(fakejira.ScenarioRateLimited)
	err = f.client.DoTransition(ctx, "10001", "31", "Done")
	var e *Error
	if record(err) == nil || !asError(err, &e) || e.Reason != ReasonRateLimited || !e.RetryAt.Equal(now.Add(2*time.Second)) {
		t.Errorf("rate limited: %v", err)
	}
	if strings.Contains(err.Error(), testToken) {
		t.Errorf("an error carries the token: %v", err)
	}
}

func TestAddCommentSendsADF(t *testing.T) {
	f, log := newWriteFake(t)
	adf := json.RawMessage(`{"type":"doc","version":1,"content":[{"type":"paragraph","content":[{"type":"text","text":"Delivered."}]}]}`)
	id, err := f.client.AddComment(context.Background(), "10001", adf)
	if err != nil {
		t.Fatal(err)
	}
	if id == "" {
		t.Fatal("no comment id")
	}
	bodies := log.get("/rest/api/3/issue/10001/comment")
	if len(bodies) != 1 || bodies[0] != `{"body":`+string(adf)+`}` {
		t.Errorf("comment body = %v", bodies)
	}
	thread, err := f.client.Comments(context.Background(), "10001")
	if err != nil {
		t.Fatal(err)
	}
	last := thread[len(thread)-1]
	if last.ID != id || !strings.Contains(last.RenderedBody, "Delivered.") {
		t.Errorf("posted comment reads back as %+v, want id %s", last, id)
	}
}
