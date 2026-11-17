package cli

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/testrepo"
)

// TestStreamDetectsARestoredEventHistoryE2E is issue #1001, the half of #967
// that PR #995 could not close (§13.3's 2026-11-17 amendment names it): a
// client keeps one event stream open across `vincent daemon restore`, and the
// restored daemon writes past the client's stale Last-Event-ID before the
// client reconnects. The cursor then looks legitimate, the replay starts
// after it, and the restored events between the backup's last id and that
// cursor are never delivered — while the client keeps REST state from the
// replaced database.
//
// Driven through the real binary, the real backup and restore, and the real
// apiclient stream. The client dials through a gate standing in for "the
// daemon is down": closed across the stop, restore and restart, opened only
// once the restored daemon's sequence has climbed past the stale cursor — the
// ordering apiclient's up-to-5 s backoff makes ordinary.
//
// Correct behaviour, whatever shape the fix takes: on reconnecting to the
// restored history the client either delivers every restored event the stale
// cursor would hide, or does not resume from that cursor at all (it resets and
// says so, so its consumer re-snapshots). Silently resuming past it is the bug.
func TestStreamDetectsARestoredEventHistoryE2E(t *testing.T) {
	dataDir, cfgDir := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(cfgDir, config.FileName),
		[]byte("listen: \"127.0.0.1:0\"\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	repo := testrepo.Init(t, "main")

	// The gate: a reverse proxy whose target is swapped per daemon
	// generation, answering 503 while there is none.
	var target atomic.Pointer[url.URL]
	gate := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u := target.Load()
		if u == nil {
			http.Error(w, "daemon down", http.StatusServiceUnavailable)
			return
		}
		p := httputil.NewSingleHostReverseProxy(u)
		p.FlushInterval = -1
		p.ServeHTTP(w, r)
	}))
	t.Cleanup(gate.Close)
	open := func(base string) {
		u, err := url.Parse(base)
		if err != nil {
			t.Fatalf("parse %s: %v", base, err)
		}
		target.Store(u)
	}

	// Generation one: the history the backup captures.
	first := startDaemonProcess(t, dataDir, cfgDir, "success")
	c1 := waitDaemonAPI(t, dataDir, first)
	if out, code := runVincent(t, dataDir, cfgDir, "project", "add", repo); code != 0 {
		t.Fatalf("project add: code %d, out %q", code, out)
	}
	direct1 := apiclient.New(c1.base, c1.token)
	createIssue(t, direct1, "before the backup")
	archive := filepath.Join(t.TempDir(), "vincent-backup.tar.gz")
	if out, code := runVincent(t, dataDir, cfgDir, "daemon", "backup", archive); code != 0 {
		t.Fatalf("daemon backup: code %d, out %q", code, out)
	}

	// The long-lived client, as the TUI holds it.
	open(c1.base)
	client := apiclient.New(gate.URL, c1.token)
	notes := client.StreamEvents(t.Context(), apiclient.StreamOptions{
		InitialBackoff: 50 * time.Millisecond, MaxBackoff: 200 * time.Millisecond,
	})
	if _, ok := nextNote[apiclient.ConnectedNote](t, notes); !ok {
		t.Fatal("the stream never connected to the first daemon")
	}
	// Events the backup does not hold; the client's cursor ends past them.
	const lost = 3
	for i := range lost {
		createIssue(t, direct1, fmt.Sprintf("after the backup %d", i))
	}
	var stale, firstPost int64
	for seen := 0; seen < lost; {
		ev, ok := nextNote[apiclient.EventNote](t, notes)
		if !ok {
			t.Fatalf("saw %d of the %d post-backup issue events", seen, lost)
		}
		if firstPost == 0 {
			firstPost = ev.Event.ID
		}
		stale = max(stale, ev.Event.ID)
		if ev.Event.Type == store.EventIssueCreated {
			seen++
		}
	}

	// Down, restored onto the backup, up again — with the gate shut.
	target.Store(nil)
	c1.post(t, "/v1/daemon/stop", nil, http.StatusAccepted, nil)
	waitExit(t, first)
	// Whatever the stopping daemon still wrote reached the client and moved
	// its cursor; the cursor it reconnects with is the last id before the
	// drop.
	for dropped := false; !dropped; {
		select {
		case n := <-notes:
			switch n := n.(type) {
			case apiclient.EventNote:
				stale = max(stale, n.Event.ID)
			case apiclient.DisconnectedNote:
				dropped = true
			}
		case <-time.After(daemonAPIBudget()):
			t.Fatal("the stream never noticed the first daemon stop")
		}
	}
	if out, code := runVincent(t, dataDir, cfgDir, "daemon", "restore", "--force", archive); code != 0 {
		t.Fatalf("daemon restore: code %d, out %q", code, out)
	}
	third := startDaemonProcess(t, dataDir, cfgDir, "success")
	c3 := waitDaemonAPI(t, dataDir, third)
	direct3 := apiclient.New(c3.base, c3.token)

	// The restored daemon climbs past the stale cursor before the client is
	// let back in. A probe stream replaying from 1 records the restored
	// events the stale cursor would hide: every one numbered from the first
	// id the client saw after the backup up to that cursor, all of which the
	// restored daemon wrote and the backup never held.
	probe := direct3.StreamEvents(t.Context(), apiclient.StreamOptions{LastEventID: 1})
	if _, ok := nextNote[apiclient.ConnectedNote](t, probe); !ok {
		t.Fatal("the probe never connected to the restored daemon")
	}
	hidden := map[int64]bool{}
	for i := 0; ; i++ {
		createIssue(t, direct3, fmt.Sprintf("after the restore %d", i))
		var top int64
		for top == 0 {
			ev, ok := nextNote[apiclient.EventNote](t, probe)
			if !ok {
				t.Fatal("the probe stream stopped delivering")
			}
			if ev.Event.ID >= firstPost && ev.Event.ID <= stale {
				hidden[ev.Event.ID] = true
			}
			if ev.Event.Type == store.EventIssueCreated {
				top = ev.Event.ID
			}
		}
		if top > stale {
			break
		}
	}
	if len(hidden) == 0 {
		t.Fatalf("test setup: no restored event at or below the stale cursor %d", stale)
	}

	// Let the client back in and judge its reconnect.
	open(c3.base)
	conn, ok := nextNote[apiclient.ConnectedNote](t, notes)
	if !ok {
		t.Fatal("the stream never reconnected to the restored daemon")
	}
	if conn.Cursor < firstPost {
		return // the client reset rather than trust the stale cursor
	}
	if conn.Cursor != stale {
		t.Fatalf("test setup: reconnected with cursor %d, want the stale %d or a reset", conn.Cursor, stale)
	}
	for len(hidden) > 0 {
		ev, ok := nextNote[apiclient.EventNote](t, notes)
		if !ok {
			t.Fatalf("the restored daemon's events %v never arrived", keys(hidden))
		}
		if hidden[ev.Event.ID] {
			delete(hidden, ev.Event.ID)
			continue
		}
		if ev.Event.ID > stale {
			t.Fatalf("after `daemon restore` the client resumed from its stale cursor %d and "+
				"received event %d, silently skipping the restored history's events %v: "+
				"nothing told it the event history had changed (§13.3, issue #1001)",
				stale, ev.Event.ID, keys(hidden))
		}
	}
}

// nextNote returns the next note of type N from ch, skipping the rest, or
// false when none arrives in time.
func nextNote[N apiclient.Note](t *testing.T, ch <-chan apiclient.Note) (N, bool) {
	t.Helper()
	var zero N
	deadline := time.After(daemonAPIBudget())
	for {
		select {
		case n, ok := <-ch:
			if !ok {
				return zero, false
			}
			if v, ok := n.(N); ok {
				return v, true
			}
		case <-deadline:
			return zero, false
		}
	}
}

func createIssue(t *testing.T, c *apiclient.Client, title string) {
	t.Helper()
	if _, err := c.CreateIssue(t.Context(), apiclient.CreateIssueRequest{ProjectID: 1, Title: title}, ""); err != nil {
		t.Fatalf("create issue %q: %v", title, err)
	}
}

func keys(m map[int64]bool) []int64 {
	out := make([]int64, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
