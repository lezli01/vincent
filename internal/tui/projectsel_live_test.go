package tui

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/lezli01/vincent/internal/api"
	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/events"
	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/store/storetest"
)

// TestProjectSelectionFollowsTheDaemon is task 132.2 against the real
// handlers: the root's project list is refreshed on connect, on reconnect
// and on each project.* event, the first project by name is selected only
// while nothing is, and a rename reaches the selection.
func TestProjectSelectionFollowsTheDaemon(t *testing.T) {
	const token = "sel-token"
	st, err := storetest.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	broker := events.New()
	t.Cleanup(broker.Close)
	st.SetEventHook(broker.Publish)

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
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)

	m := newRoot(testCtx(t), connector{}, ackedDir(t))
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	_, cmd := m.Update(connectedMsg{client: apiclient.New(ts.URL, token), dataDir: t.TempDir()})
	p := newPump(t, m, cmd)
	p.until(10*time.Second, "the event stream to go live", func() bool { return m.streamLive })
	if m.sel.id != 0 {
		t.Fatalf("no project registered, yet %+v is selected", m.sel)
	}

	ctx := context.Background()
	create := func(name string) *store.Project {
		t.Helper()
		pr := &store.Project{Name: name, Path: filepath.Join(t.TempDir(), name), DefaultBranch: "main"}
		if err := st.CreateProject(ctx, pr); err != nil {
			t.Fatalf("CreateProject: %v", err)
		}
		return pr
	}

	// project.created with nothing selected: auto-select.
	zeta := create("zeta")
	p.until(10*time.Second, "the first project to be selected", func() bool { return m.sel.id == zeta.ID })
	if v := m.views[viewHome].(*shell); v.project != m.sel {
		t.Errorf("board got %+v, want %+v", v.project, m.sel)
	}

	// project.created with one selected: no change (decision 10), though
	// the list did refresh.
	alpha := create("alpha")
	p.until(10*time.Second, "the list to hold both projects", func() bool { return len(m.projects) == 2 })
	if m.sel.id != zeta.ID {
		t.Fatalf("a new project took over the selection: %+v", m.sel)
	}

	// project.updated: a rename reaches the selection.
	zeta.Name = "omega"
	if err := st.UpdateProject(ctx, zeta); err != nil {
		t.Fatalf("UpdateProject: %v", err)
	}
	p.until(10*time.Second, "the rename to reach the selection", func() bool { return m.sel.name == "omega" })

	// project.deleted: the list refreshes, the selection is 132.7's.
	if err := st.DeleteProjectCascade(ctx, alpha.ID); err != nil {
		t.Fatalf("DeleteProjectCascade: %v", err)
	}
	p.until(10*time.Second, "the deletion to reach the list", func() bool { return len(m.projects) == 1 })

	// A reconnect refetches the list.
	seq := m.projectsSeq
	m.streamLive = false
	_, cmd = m.Update(noteMsg{note: apiclient.ConnectedNote{}})
	p.push(cmd)
	if m.projectsSeq == seq {
		t.Fatalf("a reconnect did not refetch the project list")
	}
	p.until(10*time.Second, "the reconnect's list to land", func() bool { return len(m.projects) == 1 && m.sel.name == "omega" })
}

// TestProjectFlagOpensOnItsProject is `vincent --project api` against the
// real handlers (task 132.3): the option outranks the first project by name,
// the pick is silent, and it becomes the last-used project in tui.json.
func TestProjectFlagOpensOnItsProject(t *testing.T) {
	const token = "flag-token"
	st, err := storetest.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	broker := events.New()
	t.Cleanup(broker.Close)
	st.SetEventHook(broker.Publish)
	ctx := context.Background()
	var apiID int64
	for _, name := range []string{"aaa", "api"} {
		pr := &store.Project{Name: name, Path: filepath.Join(t.TempDir(), name), DefaultBranch: "main"}
		if err := st.CreateProject(ctx, pr); err != nil {
			t.Fatalf("CreateProject: %v", err)
		}
		if name == "api" {
			apiID = pr.ID
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
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)

	dataDir := ackedDir(t)
	m := newRoot(testCtx(t), connector{}, dataDir)
	m.startup.flag = "api"
	m.startup.cwd = t.TempDir() // inside no project, so rule 2 matches nothing
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	_, cmd := m.Update(connectedMsg{client: apiclient.New(ts.URL, token), dataDir: dataDir})
	p := newPump(t, m, cmd)
	p.until(10*time.Second, "the flag's project to be selected", func() bool { return m.sel.id == apiID })
	if m.selWhy != whyFlag || m.selNotice != "" {
		t.Errorf("chosen by %q with notice %q, want a silent --project pick", m.selWhy, m.selNotice)
	}
	p.until(10*time.Second, "the pick to reach tui.json", func() bool {
		sp := readTUIState(dataDir).SelectedProject
		return sp != nil && sp.ID == apiID && sp.Name == "api"
	})
}
