package tui

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/lezli01/vincent/internal/api"
	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/events"
	"github.com/lezli01/vincent/internal/issuestate"
	"github.com/lezli01/vincent/internal/store"
)

// TestEventFilterAgainstTheRealAPI is task 132.5 against the real handlers:
// the root's one stream stays unfiltered, and with B selected an issue event
// in project A refetches nothing while one in B refetches the issues list.
func TestEventFilterAgainstTheRealAPI(t *testing.T) {
	const token = "filter-token"
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	broker := events.New()
	t.Cleanup(broker.Close)
	st.SetEventHook(broker.Publish)

	ctx := context.Background()
	a := &store.Project{Name: "alpha", Path: filepath.Join(t.TempDir(), "a"), DefaultBranch: "main"}
	b := &store.Project{Name: "beta", Path: filepath.Join(t.TempDir(), "b"), DefaultBranch: "main"}
	for _, p := range []*store.Project{a, b} {
		if err := st.CreateProject(ctx, p); err != nil {
			t.Fatalf("CreateProject: %v", err)
		}
	}

	s := api.New(api.Deps{
		Token:       token,
		Config:      config.Default,
		StartedAt:   time.Now(),
		ListenAddr:  "127.0.0.1:0",
		RequestStop: func() {},
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		Store:       st,
		Broker:      broker,
	})
	var (
		mu          sync.Mutex
		issueLists  int
		streamQuery []string
	)
	h := s.Handler()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/issues":
			issueLists++
		case r.URL.Path == "/v1/events":
			streamQuery = append(streamQuery, r.URL.RawQuery)
		}
		mu.Unlock()
		h.ServeHTTP(w, r)
	}))
	t.Cleanup(ts.Close)
	lists := func() int {
		mu.Lock()
		defer mu.Unlock()
		return issueLists
	}

	m := newRoot(testCtx(t), connector{}, ackedDir(t))
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	_, cmd := m.Update(connectedMsg{client: apiclient.New(ts.URL, token), dataDir: t.TempDir()})
	p := newPump(t, m, cmd)
	p.until(10*time.Second, "the event stream to go live", func() bool { return m.streamLive })
	p.until(10*time.Second, "alpha to be selected", func() bool { return m.sel.id == a.ID })

	mu.Lock()
	queries := append([]string(nil), streamQuery...)
	mu.Unlock()
	if len(queries) == 0 || queries[0] != "" {
		t.Fatalf("the root's stream opened with %q, want no project filter", queries)
	}

	// Select B: the issues view reloads for it.
	before := lists()
	p.push(m.selectProject(apiclient.Project{ID: b.ID, Name: b.Name}, "test"))
	p.until(10*time.Second, "the switch to reload the issues", func() bool { return lists() > before })
	p.settle(10*time.Second, time.Second, "the switch's loads to settle", func() (bool, int) { return true, lists() })

	newIssue := func(project int64, title string) {
		t.Helper()
		if _, err := st.CreateIssue(ctx, store.NewIssue{
			ProjectID: project, Title: title, Author: "human",
		}, issuestate.Human); err != nil {
			t.Fatalf("CreateIssue: %v", err)
		}
	}

	settled := lists()
	newIssue(a.ID, "in alpha")
	p.settle(10*time.Second, time.Second, "alpha's event to pass by", func() (bool, int) { return true, lists() })
	if got := lists(); got != settled {
		t.Fatalf("an issue event in A, with B selected, listed issues %d more times", got-settled)
	}

	newIssue(b.ID, "in beta")
	p.until(10*time.Second, "B's event to refetch the issues", func() bool { return lists() > settled })
}
