package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/agent"
	"github.com/lezli01/vincent/internal/agent/agenttest"
	"github.com/lezli01/vincent/internal/agent/claude"
	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/doctor"
	"github.com/lezli01/vincent/internal/store"
)

// infoBody fetches GET /v1/info both decoded and raw. The raw copy is what
// the absence assertions read: a field that decodes to a zero value and a
// field that is not on the wire at all are different answers, and only the
// second one keeps the scans off this endpoint.
func infoBody(t *testing.T, ts *httptest.Server) (map[string]any, string) {
	t.Helper()
	resp, body := doRequest(t, ts, http.MethodGet, "/v1/info", testToken)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /v1/info: %d %s", resp.StatusCode, body)
	}
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("info body: %v (%s)", err, body)
	}
	return out, string(body)
}

// TestInfoCarriesDatabaseBytesOnly is task 029 decision 1 as an assertion.
//
// The byte figures are three os.Stat calls and belong on the endpoint the
// board, the projects view and the daemon view poll. A COUNT(*) over a
// multi-million-row events table does not, and the absence assertions below
// are the regression guard for that — a later change that "just adds the row
// counts here too" fails here rather than in production on a big install.
func TestInfoCarriesDatabaseBytesOnly(t *testing.T) {
	h := newDoctorHarness(t)
	body, raw := infoBody(t, h.ts)

	db, ok := body["database"].(map[string]any)
	if !ok {
		t.Fatalf("info has no database object: %s", raw)
	}
	for _, key := range []string{"path", "size_bytes", "wal_bytes", "shm_bytes", "total_bytes"} {
		if _, ok := db[key]; !ok {
			t.Errorf("database.%s is missing: %s", key, raw)
		}
	}
	size, total := db["size_bytes"].(float64), db["total_bytes"].(float64)
	if size <= 0 {
		t.Errorf("database.size_bytes = %v, want the real file size", size)
	}
	if total < size {
		t.Errorf("database.total_bytes = %v < size_bytes = %v", total, size)
	}
	if db["path"] == "" {
		t.Error("database.path is empty")
	}
	for _, scan := range []string{"table_rows", "oldest_event_at", "workflow_snapshot_bytes"} {
		if strings.Contains(raw, scan) {
			t.Errorf("%q rides /v1/info; scans belong on /v1/doctor (decision 1): %s", scan, raw)
		}
	}
}

// TestHealthStaysStatusAndVersion holds the line the issue drew: nothing about
// the shape of a user's disk goes on the one unauthenticated route (§13.1).
func TestHealthStaysStatusAndVersion(t *testing.T) {
	h := newDoctorHarness(t)
	resp, body := doRequest(t, h.ts, http.MethodGet, "/v1/health", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /v1/health: %d %s", resp.StatusCode, body)
	}
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("health body: %v (%s)", err, body)
	}
	if len(out) != 2 || out["status"] != "ok" || out["version"] == "" {
		t.Errorf("health = %v, want exactly {status, version}", out)
	}
}

func TestDoctorCarriesRowCountsSpanAndFootprint(t *testing.T) {
	h := newDoctorHarness(t)
	// A project and a task carrying a real snapshot, so the counts and the
	// snapshot total describe something rather than an empty schema.
	projectID, _ := h.seedProject(t)
	task := h.seedTask(t, projectID, store.TaskQueued, "measured")
	task.WorkflowSnapshot = "name: measured\nsteps: []\n"
	if err := h.store.UpdateTask(t.Context(), task); err != nil {
		t.Fatalf("set the task's snapshot: %v", err)
	}

	rep := h.report(t)
	db := rep.Database
	if !db.Known {
		t.Fatalf("database = %+v, want known over the endpoint", db)
	}
	if db.SizeBytes <= 0 || db.TotalBytes < db.SizeBytes {
		t.Errorf("footprint = size %d, wal %d, shm %d, total %d",
			db.SizeBytes, db.WALBytes, db.SHMBytes, db.TotalBytes)
	}
	if db.TotalBytes != db.SizeBytes+db.WALBytes+db.SHMBytes {
		t.Errorf("total %d is not the sum of its parts: %+v", db.TotalBytes, db)
	}
	if db.TableRows["tasks"] != 1 || db.TableRows["projects"] != 1 {
		t.Errorf("table_rows = %v, want one project and one task", db.TableRows)
	}
	if db.TableRows["events"] == 0 {
		t.Errorf("table_rows.events = 0 after creating a project and a task: %v", db.TableRows)
	}
	if _, ok := db.TableRows["schema_migrations"]; !ok {
		t.Errorf("table_rows omits schema_migrations; the set is enumerated, not curated: %v",
			db.TableRows)
	}
	if db.OldestEventAt == nil {
		t.Error("oldest_event_at is null after events were written")
	} else if time.Since(*db.OldestEventAt) > time.Hour {
		t.Errorf("oldest_event_at = %v, want a timestamp from this test run", *db.OldestEventAt)
	}
	if db.WorkflowSnapshotBytes <= 0 {
		t.Errorf("workflow_snapshot_bytes = %d, want the seeded task's snapshot",
			db.WorkflowSnapshotBytes)
	}
}

// countingAdapter is the real claude adapter over the real fake binary, with
// a tally of how many times the daemon actually probed it. Wrapping rather
// than stubbing keeps the argv, the parse and the §9.6 cache behaviour
// genuine — the only thing added is the count.
type countingAdapter struct {
	agent.Adapter
	detects *atomic.Int64
}

func (c countingAdapter) Detect(ctx context.Context) (agent.Availability, error) {
	c.detects.Add(1)
	return c.Adapter.Detect(ctx)
}

// TestDoctorProbeFlag pins both halves of task 029 decision 4: the endpoint's
// default still forces the re-probe task 005 decision 2 requires, and
// `?probe=false` is served from the §9.6 cache instead.
func TestDoctorProbeFlag(t *testing.T) {
	dirs := config.Dirs{Config: t.TempDir(), Data: t.TempDir()}
	fake := agenttest.BuildFakeAgent(t)
	var detects atomic.Int64
	reg := agent.NewRegistry(countingAdapter{
		Adapter: claude.New(func() string { return fake }),
		detects: &detects,
	})
	s := New(Deps{
		Token:       testToken,
		Config:      config.Default,
		StartedAt:   time.Now(),
		ListenAddr:  "127.0.0.1:0",
		Dirs:        dirs,
		LogPath:     filepath.Join(dirs.Data, "logs", "daemon.log"),
		RequestStop: func() {},
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		Agents:      reg,
		Catalog:     agent.NewCatalogCache(reg),
	})
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)

	get := func(t *testing.T, path string) doctor.Report {
		t.Helper()
		resp, body := doRequest(t, ts, http.MethodGet, path, testToken)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET %s: %d %s", path, resp.StatusCode, body)
		}
		var rep doctor.Report
		if err := json.Unmarshal(body, &rep); err != nil {
			t.Fatalf("doctor body: %v (%s)", err, body)
		}
		return rep
	}

	// The first request primes a cold cache whatever the flag says, so the
	// comparison starts from a primed one.
	if rep := get(t, "/v1/doctor?probe=false"); len(rep.Agents) != 1 {
		t.Fatalf("agents = %+v, want the one wired adapter", rep.Agents)
	}
	primed := detects.Load()
	if primed == 0 {
		t.Fatal("a cold cache was served without probing at all")
	}

	for _, path := range []string{"/v1/doctor?probe=false", "/v1/doctor?probe=0"} {
		rep := get(t, path)
		if n := detects.Load(); n != primed {
			t.Errorf("%s probed %d extra time(s); it must be served from the cache",
				path, n-primed)
		}
		// Cached is not degraded: the answer is still the adapter's.
		if len(rep.Agents) != 1 || !rep.Agents[0].Available {
			t.Errorf("%s: agents = %+v, want the cached availability", path, rep.Agents)
		}
	}

	for _, path := range []string{"/v1/doctor", "/v1/doctor?probe=true"} {
		before := detects.Load()
		get(t, path)
		if detects.Load() <= before {
			t.Errorf("%s did not re-probe; decision 2 is the default", path)
		}
	}
}
