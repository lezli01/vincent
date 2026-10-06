package tui

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/api"
	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/events"
	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/store/storetest"
)

// TestProjectPickerFollowsTheDaemon is task 132.4 against the real handlers:
// with two projects and the picker open, a new open issue in one moves that
// row's figure, by the debounced refetch, and leaves the other row alone.
func TestProjectPickerFollowsTheDaemon(t *testing.T) {
	const token = "pick-token"
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

	ctx := context.Background()
	var ids []int64
	for _, name := range []string{"api", "web"} {
		pr := &store.Project{Name: name, Path: filepath.Join(t.TempDir(), name), DefaultBranch: "main"}
		if err := st.CreateProject(ctx, pr); err != nil {
			t.Fatalf("CreateProject: %v", err)
		}
		ids = append(ids, pr.ID)
	}

	client := apiclient.New(ts.URL, token)
	m := newRoot(testCtx(t), connector{}, ackedDir(t))
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	_, cmd := m.Update(connectedMsg{client: client, dataDir: t.TempDir()})
	p := newPump(t, m, cmd)
	p.until(10*time.Second, "the stream and the selection", func() bool { return m.streamLive && m.sel.id != 0 })

	_, cmd = m.Update(tea.KeyPressMsg{Code: '@', Text: "@"})
	p.push(cmd)
	row := func(id int64) *apiclient.Project {
		if m.projPick == nil {
			return nil
		}
		for i := range m.projPick.projects {
			if m.projPick.projects[i].ID == id && m.projPick.projects[i].Stats != nil {
				return &m.projPick.projects[i]
			}
		}
		return nil
	}
	p.until(10*time.Second, "the picker's stats to land", func() bool { return row(ids[0]) != nil && row(ids[1]) != nil })
	if row(ids[1]).Stats.Issues.Open != 0 {
		t.Fatalf("web starts with %d open issues", row(ids[1]).Stats.Issues.Open)
	}

	if _, err := client.CreateIssue(ctx, apiclient.CreateIssueRequest{ProjectID: ids[1], Title: "new"}, ""); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}
	p.until(10*time.Second, "web's row to count the issue", func() bool {
		r := row(ids[1])
		return r != nil && r.Stats.Issues.Open == 1
	})
	if got := row(ids[0]).Stats.Issues.Open; got != 0 {
		t.Errorf("api's row moved to %d open issues", got)
	}
}

// TestProjectPickerMarksTheConfiguredDefault is task 132.18 against the real
// handlers: with `tui.default_project` set, the picker opened after connect
// marks that project's row `★`, and only that one (decision 56).
func TestProjectPickerMarksTheConfiguredDefault(t *testing.T) {
	const token = "pick-token"
	st, err := storetest.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	broker := events.New()
	t.Cleanup(broker.Close)
	st.SetEventHook(broker.Publish)

	cfg := config.Default()
	web := "web"
	cfg.TUI.DefaultProject = &web
	s := api.New(api.Deps{
		Token:       token,
		Config:      func() config.Config { return cfg },
		StartedAt:   time.Now(),
		ListenAddr:  "127.0.0.1:0",
		RequestStop: func() {},
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		Store:       st,
		Broker:      broker,
	})
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)

	ctx := context.Background()
	for _, name := range []string{"api", "web"} {
		pr := &store.Project{Name: name, Path: filepath.Join(t.TempDir(), name), DefaultBranch: "main"}
		if err := st.CreateProject(ctx, pr); err != nil {
			t.Fatalf("CreateProject: %v", err)
		}
	}

	client := apiclient.New(ts.URL, token)
	m := newRoot(testCtx(t), connector{}, ackedDir(t))
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	_, cmd := m.Update(connectedMsg{client: client, dataDir: t.TempDir()})
	p := newPump(t, m, cmd)
	p.until(10*time.Second, "the selection and the config answer", func() bool {
		return m.sel.id != 0 && m.defaultProject == "web"
	})

	_, cmd = m.Update(tea.KeyPressMsg{Code: '@', Text: "@"})
	p.push(cmd)
	p.until(10*time.Second, "the picker's stats to land", func() bool { return m.projPick != nil && m.projPick.loaded })
	var starred []string
	for _, line := range strings.Split(ansi.Strip(m.projPick.render(80, 12)), "\n") {
		if strings.Contains(line, projectPickerDefaultGlyph) {
			starred = append(starred, line)
		}
	}
	if len(starred) != 1 || !strings.Contains(starred[0], "web") {
		t.Errorf("starred rows %q, want web's alone", starred)
	}
}
