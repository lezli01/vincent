package tui

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/lezli01/vincent/internal/apiclient"
)

func testDoctorReport() *apiclient.DoctorReport {
	oldest := testNow.Add(-96 * time.Hour)
	return &apiclient.DoctorReport{
		Database: apiclient.DoctorDatabase{
			Path:  "/data/vincent.db",
			Known: true,
			TableRows: map[string]int64{
				"events": 48210, "step_runs": 640, "tasks": 96, "projects": 4,
			},
			OldestEventAt:         &oldest,
			WorkflowSnapshotBytes: 3 << 20,
		},
	}
}

func infoWithDatabase() apiclient.Info {
	i := testInfo()
	i.Database = apiclient.InfoDatabase{
		Path:       "/data/vincent.db",
		SizeBytes:  6 << 20,
		WALBytes:   2 << 20,
		SHMBytes:   32 << 10,
		TotalBytes: 6<<20 + 2<<20 + 32<<10,
	}
	return i
}

func loadedDatabase(d *daemonView) {
	d.update(daemonInfoMsg{info: infoWithDatabase()})
	d.update(daemonConfigMsg{config: testConfig()})
	d.update(daemonDoctorMsg{report: testDoctorReport()})
}

func TestDaemonViewRendersTheDatabaseBlock(t *testing.T) {
	d := newTestDaemonView(nil, nil)
	loadedDatabase(d)
	out := renderDaemon(d)

	for _, want := range []string{
		"database",
		"8.0MB",     // the total, which is the honest headline in WAL mode
		"wal 2.0MB", // named, because a fat WAL is a different story
		"events 48210",
		"projects 4",
		"3.0MB",  // the workflow-snapshot total
		"4 days", // the span, without which a count is not extrapolable
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the database block does not show %q:\n%s", want, out)
		}
	}
	if i, j := strings.Index(out, "events 48210"), strings.Index(out, "projects 4"); i > j {
		t.Errorf("row counts are not ordered biggest-first:\n%s", out)
	}
	// Reporting only: this block offers no button, the way this view offers
	// none for gc or for stopping the daemon (§15 view 6). Scoped to the
	// block, since the config above it legitimately names a delete policy.
	block := strings.ToLower(strings.Join(d.databaseLines(), "\n"))
	for _, forbidden := range []string{"prune", "vacuum", "delete"} {
		if strings.Contains(block, forbidden) {
			t.Errorf("the database block offers %q; this view reports and does not act:\n%s",
				forbidden, block)
		}
	}
}

// TestDaemonViewDatabaseNamedStates is the PR M rule for this block: the three
// ways it can have nothing to show are three different sentences, because only
// one of them is a problem.
func TestDaemonViewDatabaseNamedStates(t *testing.T) {
	t.Run("disconnected", func(t *testing.T) {
		d := newTestDaemonView(nil, nil)
		d.connected = false
		if out := renderDaemon(d); !strings.Contains(out, "unavailable — the daemon is not reachable") {
			t.Errorf("a disconnected view does not say so:\n%s", out)
		}
	})

	t.Run("fetch failed keeps last good", func(t *testing.T) {
		d := newTestDaemonView(nil, nil)
		loadedDatabase(d)
		d.update(daemonDoctorMsg{err: errors.New("doctor boom")})
		out := renderDaemon(d)
		if !strings.Contains(out, "events 48210") {
			t.Errorf("a failed refresh discarded the last-good counts:\n%s", out)
		}
		if !strings.Contains(out, "doctor boom") {
			t.Errorf("a failed refresh is presented as current:\n%s", out)
		}
	})

	t.Run("report says unknown", func(t *testing.T) {
		d := newTestDaemonView(nil, nil)
		d.update(daemonInfoMsg{info: infoWithDatabase()})
		d.update(daemonDoctorMsg{report: &apiclient.DoctorReport{}})
		out := renderDaemon(d)
		if !strings.Contains(out, "unknown — the daemon did not open the database") {
			t.Errorf("known:false rendered as an empty database rather than as unknown:\n%s", out)
		}
		if strings.Contains(out, "events 0") {
			t.Errorf("an unknown database rendered zero rows:\n%s", out)
		}
	})

	t.Run("not fetched yet", func(t *testing.T) {
		d := newTestDaemonView(nil, nil)
		d.update(daemonInfoMsg{info: infoWithDatabase()})
		if out := renderDaemon(d); !strings.Contains(out, "counting rows…") {
			t.Errorf("a pending first fetch is not distinguished from a failure:\n%s", out)
		}
	})
}

// TestDaemonViewFetchesDoctorWithoutProbing is task 029 decision 4 from the
// client side: this panel opens on a keypress, so it must not make the daemon
// spawn a subprocess per adapter every time.
func TestDaemonViewFetchesDoctorWithoutProbing(t *testing.T) {
	queries := make(chan string, 4)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/doctor" {
			http.NotFound(w, r)
			return
		}
		queries <- r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"database":{"known":true,"table_rows":{"events":3}}}`))
	}))
	t.Cleanup(ts.Close)

	d := newTestDaemonView(nil, nil)
	d.client = apiclient.New(ts.URL, "token")
	cmd := d.doctorCmd()
	if cmd == nil {
		t.Fatal("doctorCmd returned nil with a client wired")
	}
	msg, ok := cmd().(daemonDoctorMsg)
	if !ok {
		t.Fatalf("doctorCmd produced %T, want daemonDoctorMsg", msg)
	}
	if msg.err != nil {
		t.Fatalf("doctorCmd: %v", msg.err)
	}
	select {
	case q := <-queries:
		if q != "probe=false" {
			t.Errorf("the daemon view fetched /v1/doctor?%s, want probe=false", q)
		}
	default:
		t.Fatal("the handler was never reached")
	}
	if msg.report.Database.TableRows["events"] != 3 {
		t.Errorf("report = %+v, want the daemon's counts", msg.report.Database)
	}
}

// TestDaemonViewRefreshRefetchesTheCounts: R re-reads every source, so the
// row counts are not frozen at whatever they were when the view first opened.
func TestDaemonViewRefreshRefetchesTheCounts(t *testing.T) {
	var doctorHits int
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/doctor":
			doctorHits++
			_, _ = w.Write([]byte(`{"database":{"known":true,"table_rows":{"events":7}}}`))
		default:
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	t.Cleanup(ts.Close)

	d := newTestDaemonView(nil, nil)
	d.client = apiclient.New(ts.URL, "token")

	_, cmd := d.updateKey(tea.KeyPressMsg{Code: 'R', Text: "R"})
	var got *daemonDoctorMsg
	for _, msg := range flatten(cmd) {
		if m, ok := msg.(daemonDoctorMsg); ok {
			got = &m
		}
	}
	if doctorHits != 1 {
		t.Fatalf("R made %d doctor requests, want 1", doctorHits)
	}
	if got == nil {
		t.Fatal("R did not produce a daemonDoctorMsg")
	}
	if got.err != nil {
		t.Fatalf("doctor fetch: %v", got.err)
	}
	if got.report.Database.TableRows["events"] != 7 {
		t.Errorf("report = %+v, want the daemon's counts", got.report.Database)
	}
}
