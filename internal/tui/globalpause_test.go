package tui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
)

// pauseServer is a daemon that answers only the three global pause routes
// (task 149), keeping the state they change.
type pauseServer struct {
	mu    sync.Mutex
	state apiclient.GlobalPause
	posts []string
}

func newPauseRoot(t *testing.T) (*root, *pauseServer) {
	t.Helper()
	ps := &pauseServer{state: apiclient.GlobalPause{State: apiclient.PauseActive}}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ps.mu.Lock()
		defer ps.mu.Unlock()
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/daemon/pause":
		case r.Method == http.MethodPost && r.URL.Path == "/v1/daemon/pause":
			ps.posts = append(ps.posts, "pause")
			at := time.Date(2026, 11, 17, 9, 30, 0, 0, time.UTC)
			ps.state = apiclient.GlobalPause{State: apiclient.PausePausing, PausedAt: &at, Running: 2, Queued: 3}
		case r.Method == http.MethodPost && r.URL.Path == "/v1/daemon/continue":
			ps.posts = append(ps.posts, "continue")
			n := 2
			ps.state = apiclient.GlobalPause{State: apiclient.PauseActive, Resumed: &n}
		default:
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(ps.state)
	}))
	t.Cleanup(ts.Close)
	m := newRoot(testCtx(t), connector{}, ackedDir(t))
	m.phase = phaseConnected
	m.client = apiclient.New(ts.URL, "t")
	m.width, m.height = 140, 30
	return m, ps
}

// TestGlobalPauseKeyAsksThenActs walks `Z` (task 149): nothing is drawn while
// active, the key asks before it acts, n drops the question, y pauses, the
// header and status line then say so on any screen, and a second `Z` y
// continues.
func TestGlobalPauseKeyAsksThenActs(t *testing.T) {
	m, ps := newPauseRoot(t)
	drainPicker(m, fetchGlobalPause(m.client))
	if !m.globalPause.ok {
		t.Fatal("the pause reading never landed")
	}
	if strings.Contains(ansi.Strip(m.headerLine()), "PAUSE") {
		t.Fatalf("active daemon draws a pause badge: %q", ansi.Strip(m.headerLine()))
	}
	if _, ok := m.statusLine(); ok {
		t.Fatal("active daemon draws a status line")
	}

	z := tea.KeyPressMsg{Code: 'Z', Text: "Z"}
	pressRoot(m, z)
	line, ok := m.statusLine()
	if !ok || !strings.Contains(ansi.Strip(line), "Pause all work?") {
		t.Fatalf("Z: status line = %q, want the pause question", ansi.Strip(line))
	}
	pressRoot(m, tea.KeyPressMsg{Code: 'n', Text: "n"})
	if m.globalPause.asking || len(ps.posts) != 0 {
		t.Fatalf("n: asking=%v posts=%v, want the question dropped and nothing sent", m.globalPause.asking, ps.posts)
	}

	pressRoot(m, z)
	pressRoot(m, tea.KeyPressMsg{Code: 'y', Text: "y"})
	if len(ps.posts) != 1 || ps.posts[0] != "pause" {
		t.Fatalf("y: posts = %v, want one pause", ps.posts)
	}
	if head := ansi.Strip(m.headerLine()); !strings.Contains(head, "pausing (2 finishing)") {
		t.Fatalf("header after pause = %q, want the pausing badge", head)
	}
	// The confirmation notice is cleared by the next key; the pause's own
	// line is what stays.
	m.selNotice = ""
	line, ok = m.statusLine()
	if got := ansi.Strip(line); !ok || !strings.Contains(got, "Pausing all work — 2 tasks still finishing their step") ||
		!strings.Contains(got, "3 queued") {
		t.Fatalf("status line while pausing = %q", got)
	}

	// The last task parks: the daemon reports paused.
	drainPicker(m, func() tea.Msg {
		return globalPauseMsg{pause: apiclient.GlobalPause{State: apiclient.PausePaused, Held: 2, Queued: 3}}
	})
	if head := ansi.Strip(m.headerLine()); !strings.Contains(head, "⏸ PAUSED") {
		t.Fatalf("header once paused = %q", head)
	}

	pressRoot(m, z)
	if line, _ := m.statusLine(); !strings.Contains(ansi.Strip(line), "Continue all work?") ||
		!strings.Contains(ansi.Strip(line), "2 tasks parked by the pause resume") {
		t.Fatalf("Z while paused: %q, want the continue question", ansi.Strip(line))
	}
	pressRoot(m, tea.KeyPressMsg{Code: 'y', Text: "y"})
	if len(ps.posts) != 2 || ps.posts[1] != "continue" {
		t.Fatalf("posts = %v, want pause then continue", ps.posts)
	}
	if !strings.Contains(m.selNotice, "resumed 2 tasks") {
		t.Errorf("notice = %q, want the resumed count", m.selNotice)
	}
	if strings.Contains(ansi.Strip(m.headerLine()), "⏸") {
		t.Errorf("header after continue still shows the pause: %q", ansi.Strip(m.headerLine()))
	}
}

// TestGlobalPauseRefetchesOnEvents: the pause event always refetches, and a
// transition refetches only while a pause is in effect — that is what turns
// `pausing` into `paused` on screen.
func TestGlobalPauseRefetchesOnEvents(t *testing.T) {
	var g globalPauseState
	if !g.refetchOn("daemon.pause_changed") {
		t.Error("the pause event must refetch")
	}
	if g.refetchOn("task.state_changed") {
		t.Error("an active daemon refetched on a transition")
	}
	g.ok, g.pause = true, apiclient.GlobalPause{State: apiclient.PausePausing}
	if !g.refetchOn("task.state_changed") {
		t.Error("a pausing daemon must refetch on a transition")
	}
}
